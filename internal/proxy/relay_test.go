package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
)

func TestRelayPreservesRequestAndStreamsResponse(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotBody, gotKey, gotEncoding, gotConnection, gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotKey, gotEncoding, gotConnection, gotHost = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("x-api-key"), r.Header.Get("Accept-Encoding"), r.Header.Get("Connection"), r.Host
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "data: ok\n\n")
	}))
	defer upstream.Close()

	handler, err := New(Config{AnthropicUpstream: upstream.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/proxy/anthropic/v1/messages?beta=true", strings.NewReader(`{"model":"test"}`))
	req.Header.Set("x-api-key", "secret")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Connection", "keep-alive")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if response.Code != http.StatusCreated || response.Body.String() != "data: ok\n\n" {
		t.Fatalf("response status=%d body=%q", response.Code, response.Body.String())
	}
	if gotMethod != http.MethodPatch || gotPath != "/v1/messages" || gotQuery != "beta=true" || gotBody != `{"model":"test"}` {
		t.Fatalf("request method=%q path=%q query=%q body=%q", gotMethod, gotPath, gotQuery, gotBody)
	}
	if gotKey != "secret" || gotEncoding != "" || gotConnection != "" || gotHost == "" {
		t.Fatalf("headers key=%q encoding=%q connection=%q host=%q", gotKey, gotEncoding, gotConnection, gotHost)
	}
	if response.Header().Get("Connection") != "" || response.Header().Get("Content-Length") != "" {
		t.Fatalf("response headers: %#v", response.Header())
	}
}

func TestChatGPTRelayUsesCodexResponsesRoute(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write(fixture(t, "chatgpt_responses_stream.txt"))
	}))
	defer upstream.Close()
	handler, err := New(Config{ChatGPTUpstream: upstream.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/proxy/chatgpt/codex/responses", bytes.NewReader(fixture(t, "chatgpt_request.json"))))
	if response.Code != http.StatusOK || path != "/codex/responses" || !isInference("chatgpt", http.MethodPost, path) {
		t.Fatalf("status=%d path=%q", response.Code, path)
	}
}

func TestChatGPTCompressedRequestIsCaptured(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "chatgpt_responses_stream.txt"))
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler, err := New(Config{ChatGPTUpstream: upstream.URL, Store: database, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/chatgpt/codex/responses", bytes.NewReader(fixture(t, "chatgpt_request.json.zst")))
	req.Header.Set("Content-Encoding", "zstd")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	handler.Wait()
	var provider, model string
	var input, output int64
	if err := database.Reader().QueryRow("SELECT provider,request_model,input_tokens,output_tokens FROM spans").Scan(&provider, &model, &input, &output); err != nil {
		t.Fatal(err)
	}
	if provider != "chatgpt" || model != "gpt-6-astra" || input != 374 || output != 5 {
		t.Fatalf("provider=%q model=%q input=%d output=%d", provider, model, input, output)
	}
}

func TestNamedOpenAICompatibleRelay(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write(fixture(t, "openai_responses_stream.txt"))
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler, err := New(Config{OpenAICompatUpstreams: "local=" + upstream.URL + "/v1", Store: database, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/proxy/openai-compat/local/responses", strings.NewReader(`{"model":"local-model","stream":true}`)))
	handler.Wait()
	var provider, model, attributes string
	if err := database.Reader().QueryRow("SELECT provider,request_model,attributes FROM spans").Scan(&provider, &model, &attributes); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || path != "/v1/responses" || provider != "openai" || model != "local-model" || !strings.Contains(attributes, `"server.address":"127.0.0.1"`) {
		t.Fatalf("status=%d path=%q provider=%q model=%q attrs=%s", response.Code, path, provider, model, attributes)
	}
}

func TestNamedOpenAICompatibleRelayRejectsInvalidNames(t *testing.T) {
	for _, value := range []string{"UPPER=http://localhost", "has_underscore=http://localhost"} {
		if _, err := New(Config{OpenAICompatUpstreams: value}); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func TestRelayRejectsOversizedBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream must not be called")
	}))
	defer upstream.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/openai/v1/responses", io.LimitReader(strings.NewReader(strings.Repeat("x", 1024)), 33<<20))
	req.Body = io.NopCloser(io.MultiReader(req.Body, strings.NewReader(strings.Repeat("x", 32<<20))))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "payload too large") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestInferenceIsStoredAfterRelay(t *testing.T) {
	responseBody := fixture(t, "anthropic_stream.txt")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{AnthropicUpstream: upstream.URL, Store: database, Pricing: prices, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/anthropic/v1/messages?beta=true", bytes.NewReader(fixture(t, "anthropic_request.json")))
	req.Header.Set("User-Agent", "claude-cli/2.1.273")
	req.Header.Set("Authorization", "Bearer never-store")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	handler.Wait()
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), responseBody) {
		t.Fatalf("status=%d", response.Code)
	}
	var provider, sessionID, attributes string
	var input, output int64
	var cost float64
	if err := database.Reader().QueryRow(`SELECT provider, session_id, input_tokens, output_tokens, cost_usd, attributes FROM spans`).Scan(&provider, &sessionID, &input, &output, &cost, &attributes); err != nil {
		t.Fatal(err)
	}
	if provider != "anthropic" || sessionID != "session-test" || input != 55_794 || output != 81 || cost <= 0 || strings.Contains(attributes, "never-store") {
		t.Fatalf("stored provider=%q session=%q input=%d output=%d cost=%g attrs=%q", provider, sessionID, input, output, cost, attributes)
	}
}

func TestStreamingRelayFlushesBeforeUpstreamCompletes(t *testing.T) {
	thirdWritten := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			flusher.Flush()
			if i < 3 {
				time.Sleep(200 * time.Millisecond)
			}
		}
		close(thirdWritten)
	}))
	defer upstream.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Post(server.URL+"/proxy/openai/v1/chat/completions", "application/json", strings.NewReader(`{"model":"gpt-4o","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "data: 1\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	select {
	case <-thirdWritten:
		t.Fatal("third chunk arrived before client received the first")
	default:
	}
}

func TestClientCancellationCancelsUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/proxy/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true}`))
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream context was not cancelled")
	}
}

func TestUpstreamErrorsPassThroughAndStoreSpans(t *testing.T) {
	tests := []struct {
		name       string
		upstream   func() (string, func())
		wantStatus int
		wantBody   string
	}{
		{"429", func() (string, func()) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"error":"rate limited"}`)
			}))
			return s.URL, s.Close
		}, http.StatusTooManyRequests, `{"error":"rate limited"}`},
		{"unreachable", func() (string, func()) { return "http://127.0.0.1:1", func() {} }, http.StatusBadGateway, "Bad Gateway"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamURL, closeUpstream := tt.upstream()
			defer closeUpstream()
			database, err := store.Open(t.TempDir() + "/data")
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			handler, err := New(Config{OpenAIUpstream: upstreamURL, Store: database, Version: "test", Logf: t.Logf})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/proxy/openai/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"test"}`)))
			handler.Wait()
			if response.Code != tt.wantStatus || !strings.Contains(response.Body.String(), tt.wantBody) {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			var status int
			var message string
			if err := database.Reader().QueryRow("SELECT status_code,status_message FROM spans").Scan(&status, &message); err != nil {
				t.Fatal(err)
			}
			if status != 2 || !strings.Contains(message, tt.wantBody) {
				t.Fatalf("stored status=%d message=%q", status, message)
			}
		})
	}
}

func TestSpanboxHeadersAreNotRelayed(t *testing.T) {
	var token, session string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, session = r.Header.Get("X-Spanbox-Token"), r.Header.Get("X-Spanbox-Session")
	}))
	defer upstream.Close()
	handler, err := New(Config{AnthropicUpstream: upstream.URL, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/anthropic/v1/messages", strings.NewReader(`{"model":"test"}`))
	req.Header.Set("X-Spanbox-Token", "secret")
	req.Header.Set("X-Spanbox-Session", "pane-7")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if token != "" || session != "" {
		t.Fatalf("spanbox headers relayed: token=%q session=%q", token, session)
	}
}

func TestCredentialsAreRelayedButNeverCaptured(t *testing.T) {
	const secret = "literal-super-secret"
	var gotAuthorization, gotAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization, gotAPIKey = r.Header.Get("Authorization"), r.Header.Get("x-api-key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(fixture(t, "anthropic_stream.txt"))
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var mu sync.Mutex
	var logs strings.Builder
	logf := func(format string, args ...any) { mu.Lock(); defer mu.Unlock(); fmt.Fprintf(&logs, format, args...) }
	handler, err := New(Config{AnthropicUpstream: upstream.URL, Store: database, Version: "test", Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/proxy/anthropic/v1/messages", bytes.NewReader(fixture(t, "anthropic_request.json")))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("x-api-key", secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	handler.Wait()
	if gotAuthorization != "Bearer "+secret || gotAPIKey != secret {
		t.Fatalf("credentials not relayed: auth=%q key=%q", gotAuthorization, gotAPIKey)
	}
	var attributes, resource, events string
	if err := database.Reader().QueryRow("SELECT attributes,resource,events FROM spans").Scan(&attributes, &resource, &events); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	capturedLogs := logs.String()
	mu.Unlock()
	if strings.Contains(attributes+resource+events+capturedLogs, secret) {
		t.Fatal("credential was captured")
	}
}

func TestUpstreamIdleTimeoutBeforeHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer upstream.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, IdleTimeout: 20 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/proxy/openai/v1/responses", strings.NewReader(`{"model":"test"}`)))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "upstream idle timeout") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestCaptureAsyncRecoversPanic(t *testing.T) {
	var logs []string
	handler, err := New(Config{Store: &store.Store{}, Logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.captureAsync(Exchange{Vendor: "openai", Path: "/v1/responses", RequestBody: []byte(`{"model":"test"}`), ResponseBody: []byte(`{"id":"response_1"}`), StatusCode: http.StatusOK, StartNs: 1, EndNs: 2})
	handler.Wait()
	if len(logs) != 1 || !strings.Contains(logs[0], "panic") {
		t.Fatalf("capture logs = %q", logs)
	}
}

func TestStreamNonInferenceDoesNotBuffer(t *testing.T) {
	handler, err := New(Config{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	const body = "file contents"
	response := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
	client := httptest.NewRecorder()
	result := handler.stream(client, response, time.Now(), false)
	if client.Body.String() != body || len(result.body) != 0 {
		t.Fatalf("relayed=%q buffered=%q", client.Body.String(), result.body)
	}
}

func TestNonInferenceIsCounted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var log string
	handler, err := New(Config{GeminiUpstream: upstream.URL, Store: database, Logf: func(format string, args ...any) { log = format }})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/proxy/gemini/v1beta/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(log, "non-inference proxy request") {
		t.Fatalf("status=%d log=%q", response.Code, log)
	}
	var count int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 0 {
		t.Fatalf("span count=%d err=%v", count, err)
	}
}
