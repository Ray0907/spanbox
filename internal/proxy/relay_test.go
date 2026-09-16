package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestNonInferenceIsCounted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	var log string
	handler, err := New(Config{GeminiUpstream: upstream.URL, Logf: func(format string, args ...any) { log = format }})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/proxy/gemini/v1beta/models", nil))
	if response.Code != http.StatusOK || !strings.Contains(log, "non-inference proxy request") {
		t.Fatalf("status=%d log=%q", response.Code, log)
	}
}
