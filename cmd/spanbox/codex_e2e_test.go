package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
	"github.com/klauspost/compress/zstd"
)

// TestCodexProxyE2E uses a fresh, race-enabled spanbox process, not a recorder
// or a proxy-only handler. All requests and persisted-span reads use HTTP.
func TestCodexProxyE2E(t *testing.T) {
	const token = "spanbox-e2e-secret"
	const authorization = "Bearer chatgpt-e2e-secret"
	const route = "/proxy/chatgpt/codex/responses"
	input := []byte(`{"model":"codex-request-model","stream":true,"instructions":"Be precise","input":[{"role":"user","content":[{"type":"input_text","text":"hello 世界"}]}]}`)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(input, nil)
	encoder.Close()
	var decodedInput map[string]any
	if err := json.Unmarshal(input, &decodedInput); err != nil {
		t.Fatal(err)
	}
	inputMessages, err := json.Marshal(decodedInput["input"])
	if err != nil {
		t.Fatal(err)
	}

	streams := map[string]string{}
	gates := map[string]chan struct{}{}
	for _, mode := range []string{"plain", "zstd", "large", "path-auth"} {
		output := "reply 世界"
		if mode == "large" {
			output = strings.Repeat("large 世界 ", 220_000) // One SSE line over 2 MiB.
		}
		body, err := json.Marshal(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id": "resp_" + mode, "model": "codex-response-model", "status": "completed",
				"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": output}}}},
				"usage":  map[string]any{"input_tokens": 123, "output_tokens": 45, "input_tokens_details": map[string]any{"cached_tokens": 7}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		streams[mode] = "event: response.completed\r\ndata: " + string(body) + "\r\n\r\ndata: [DONE]\n\n"
		if mode != "path-auth" {
			gates[mode] = make(chan struct{})
		}
	}
	partial := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_partial\",\"model\":\"codex-response-model\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial reply 世界\"}\n\n"
	type upstreamRequest struct {
		method, path, query string
		headers             http.Header
		body                []byte
		err                 error
	}
	seen := make(chan upstreamRequest, 32)
	cancelled := make(chan bool, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		seen <- upstreamRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), body, err}
		mode := r.URL.Query().Get("case")
		if mode == "429" || mode == "500" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3")
			status := http.StatusTooManyRequests
			if mode == "500" {
				status = http.StatusInternalServerError
			}
			w.WriteHeader(status)
			fmt.Fprintf(w, "{\"error\":\"upstream %s 世界\"}\n", mode)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Upstream", "codex-e2e")
		if mode == "disconnect" {
			fmt.Fprint(w, partial)
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				cancelled <- true
			case <-time.After(5 * time.Second):
				cancelled <- false
			}
			return
		}
		stream := streams[mode]
		if gate := gates[mode]; gate != nil {
			// Stop in the middle of the SSE data line. The client must receive
			// this prefix before we permit the rest, proving incremental flush.
			fmt.Fprint(w, stream[:47])
			w.(http.Flusher).Flush()
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
			stream = stream[47:]
		}
		// Deliberately split JSON and UTF-8 across writes as well as SSE lines.
		for len(stream) > 0 {
			n := min(4093, len(stream))
			if _, err := io.WriteString(w, stream[:n]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			stream = stream[n:]
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	binary := filepath.Join(dir, "spanbox")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-race", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build spanbox: %v\n%s", err, output)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(binary)
	// Do not inherit host spanbox configuration or custom upstreams.
	settings := map[string]string{
		"PORT": fmt.Sprint(port), "DATA_DIR": filepath.Join(dir, "data"), "RETENTION_DAYS": "0",
		"AUTH_TOKEN": token, "CHATGPT_UPSTREAM": upstream.URL + "/backend-api",
		"ANTHROPIC_UPSTREAM": "", "OPENAI_UPSTREAM": "", "OPENAI_COMPAT_UPSTREAMS": "", "GEMINI_UPSTREAM": "", "PRICING_FILE": "",
		"GORACE": "halt_on_error=1 atexit_sleep_ms=0",
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := settings[key]; !overridden {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range settings {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		if runtime.GOOS == "windows" {
			// Go cannot send os.Interrupt on Windows. Keep every proxy/data
			// assertion, but do not claim a graceful shutdown test here.
			t.Log("Windows cleanup uses forced termination; graceful interrupt assertion is Unix-only")
			if err := cmd.Process.Kill(); err != nil {
				t.Errorf("terminate spanbox: %v", err)
			}
		} else {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case err := <-exited:
			if err != nil && runtime.GOOS != "windows" {
				t.Errorf("spanbox exit: %v", err)
			}
		case <-time.After(12 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Error("spanbox did not shut down: handler/capture leak")
		}
		logs, err := os.ReadFile(logPath)
		if err != nil {
			t.Error(err)
		}
		if strings.Contains(string(logs), "panic") || strings.Contains(string(logs), "DATA RACE") || strings.Contains(string(logs), "proxy capture") {
			t.Errorf("spanbox diagnostics:\n%s", logs)
		}
		if t.Failed() {
			t.Logf("spanbox logs:\n%s", logs)
		}
	}()
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(base + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("spanbox never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	request := func(t *testing.T, path, mode string, body []byte, encoding, provided string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+path+"?case="+mode+"&keep=a%2Fb", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authorization)
		req.Header.Set("X-Spanbox-Token", provided)
		req.Header.Set("X-Spanbox-Session", "codex-e2e-"+mode)
		if encoding != "" {
			req.Header.Set("Content-Encoding", encoding)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		return response
	}
	checkUpstream := func(t *testing.T, mode string, body []byte, encoding string) {
		t.Helper()
		select {
		case got := <-seen:
			if got.err != nil || got.method != "POST" || got.path != "/backend-api/codex/responses" || got.query != "case="+mode+"&keep=a%2Fb" || !bytes.Equal(got.body, body) {
				t.Fatalf("upstream request mismatch: method=%s path=%s query=%s bytes=%d err=%v", got.method, got.path, got.query, len(got.body), got.err)
			}
			if got.headers.Get("Authorization") != authorization || got.headers.Get("Content-Encoding") != encoding || got.headers.Get("Content-Type") != "application/json" || got.headers.Get("X-Spanbox-Token") != "" || got.headers.Get("X-Spanbox-Session") != "" {
				t.Fatalf("upstream headers not faithfully relayed/stripped: %v", got.headers)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("upstream request missing")
		}
	}
	readSpans := func(t *testing.T) []store.Span {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/export", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("export status=%d", response.StatusCode)
		}
		decoder := json.NewDecoder(response.Body)
		var spans []store.Span
		for {
			var span store.Span
			if err := decoder.Decode(&span); err == io.EOF {
				return spans
			} else if err != nil {
				t.Fatal(err)
			}
			spans = append(spans, span)
		}
	}
	waitSpan := func(t *testing.T, mode string) store.Span {
		t.Helper()
		// Async span capture is slow on shared macOS Intel runners under -race.
		deadline := time.Now().Add(30 * time.Second)
		for {
			for _, span := range readSpans(t) {
				var attrs map[string]any
				if err := json.Unmarshal([]byte(span.Attributes), &attrs); err != nil {
					t.Fatal(err)
				}
				if attrs["spanbox.session"] == "codex-e2e-"+mode {
					if attrs["http.request.header.x-spanbox-session"] != "codex-e2e-"+mode {
						t.Fatal("session header tag missing")
					}
					if attrs["gen_ai.input.messages"] != string(inputMessages) || attrs["gen_ai.system_instructions"] != `"Be precise"` {
						t.Fatal("decoded request input/instructions changed during capture")
					}
					if span.Provider != "chatgpt" || span.RequestModel != "codex-request-model" || !strings.Contains(span.InputContent, "hello 世界") || span.StartNs <= 0 || span.EndNs <= span.StartNs {
						t.Fatalf("span metadata/input mismatch: %+v", span)
					}
					if strings.Contains(span.Attributes+span.Resource, token) || strings.Contains(span.Attributes+span.Resource, authorization) {
						t.Fatal("credentials stored in span")
					}
					return span
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("span missing for %s", mode)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	for _, mode := range []string{"plain", "zstd", "large", "path-auth"} {
		t.Run(mode, func(t *testing.T) {
			body, encoding, path, provided := input, "", route, token
			if mode == "zstd" {
				body, encoding = compressed, "zstd"
			}
			if mode == "path-auth" {
				path, provided = "/proxy/t/"+token+"/chatgpt/codex/responses", ""
			}
			response := request(t, path, mode, body, encoding, provided)
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("X-Upstream") != "codex-e2e" {
				t.Fatalf("response status=%d headers=%v", response.StatusCode, response.Header)
			}
			var received []byte
			if gate := gates[mode]; gate != nil {
				prefix := make([]byte, 47)
				if _, err := io.ReadFull(response.Body, prefix); err != nil {
					t.Fatalf("stream did not flush mid-line prefix: %v", err)
				}
				close(gate)
				received = append(received, prefix...)
			}
			tail, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			received = append(received, tail...)
			if !bytes.Equal(received, []byte(streams[mode])) {
				t.Fatalf("stream changed: got %d bytes, want %d", len(received), len(streams[mode]))
			}
			checkUpstream(t, mode, body, encoding)
			span := waitSpan(t, mode)
			if span.ResponseModel != "codex-response-model" || span.InputTokens == nil || *span.InputTokens != 123 || span.OutputTokens == nil || *span.OutputTokens != 45 || span.CacheReadTokens == nil || *span.CacheReadTokens != 7 || span.StatusCode == 2 || !strings.Contains(span.OutputContent, "reply 世界") && mode != "large" {
				t.Fatalf("response model/tokens/output/status mismatch: %+v", span)
			}
			if mode == "large" && !strings.Contains(span.OutputContent, strings.Repeat("large 世界 ", 220_000)) {
				t.Fatal("large event output was truncated or not parsed")
			}
		})
	}
	for _, mode := range []string{"429", "500"} {
		t.Run(mode, func(t *testing.T) {
			response := request(t, route, mode, input, "", token)
			body, err := io.ReadAll(response.Body)
			want := fmt.Sprintf("{\"error\":\"upstream %s 世界\"}\n", mode)
			if err != nil || fmt.Sprint(response.StatusCode) != mode || string(body) != want || response.Header.Get("Retry-After") != "3" {
				t.Fatalf("error relay: status=%d body=%q err=%v", response.StatusCode, body, err)
			}
			checkUpstream(t, mode, input, "")
			span := waitSpan(t, mode)
			if span.StatusCode != 2 || span.StatusMessage != want {
				t.Fatalf("upstream error not captured: status=%d message=%q", span.StatusCode, span.StatusMessage)
			}
		})
	}
	t.Run("disconnect", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+route+"?case=disconnect&keep=a%2Fb", bytes.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authorization)
		req.Header.Set("X-Spanbox-Token", token)
		req.Header.Set("X-Spanbox-Session", "codex-e2e-disconnect")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		prefix := make([]byte, len(partial))
		if _, err := io.ReadFull(response.Body, prefix); err != nil || string(prefix) != partial {
			t.Fatalf("partial stream=%q err=%v", prefix, err)
		}
		cancel()
		response.Body.Close()
		select {
		case ok := <-cancelled:
			if !ok {
				t.Fatal("upstream handler leaked after client disconnect")
			}
		case <-time.After(6 * time.Second):
			t.Fatal("upstream cancellation never completed")
		}
		checkUpstream(t, "disconnect", input, "")
		span := waitSpan(t, "disconnect")
		if span.ResponseModel != "codex-response-model" || !strings.Contains(span.OutputContent, "partial reply 世界") || span.InputTokens != nil || span.OutputTokens != nil || span.FinishReason != "" {
			t.Fatalf("partial response not faithfully captured: %+v", span)
		}
	})
	t.Run("authentication", func(t *testing.T) {
		for _, provided := range []string{"", "wrong-token"} {
			response := request(t, route, "unauthorized", input, "", provided)
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("spanbox token=%q: status=%d", provided, response.StatusCode)
			}
		}
		// Supplying spanbox's token in Authorization instead is not proxy auth.
		req, _ := http.NewRequest(http.MethodPost, base+route, bytes.NewReader(input))
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bearer incorrectly accepted for proxy auth: %d", response.StatusCode)
		}
	})
	t.Run("reject-other-chatgpt-paths", func(t *testing.T) {
		for _, path := range []string{"/proxy/chatgpt/", "/proxy/chatgpt/models", "/proxy/chatgpt/v1/responses", "/proxy/chatgpt/codex/responses/extra", "/proxy/chatgpt/codex/responses/"} {
			response := request(t, path, "rejected", input, "", token)
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Errorf("%s: status=%d, want 404", path, response.StatusCode)
			}
		}
	})
	select {
	case got := <-seen:
		t.Errorf("rejected request reached upstream: %s?%s", got.path, got.query)
	default:
	}
	if spans := readSpans(t); len(spans) != 7 {
		t.Errorf("span count=%d, want exactly 7 accepted requests", len(spans))
	}
}
