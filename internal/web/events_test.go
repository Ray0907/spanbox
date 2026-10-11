package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const eventTraceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func eventSpan(id string, duration int64, cost float64, status int) map[string]any {
	return map[string]any{
		"traceId": eventTraceID, "spanId": id, "name": "test span",
		"startTimeUnixNano": "1000000000", "endTimeUnixNano": strconv.FormatInt(1000000000+duration*1000000, 10),
		"status": map[string]any{"code": status, "message": "test status"},
		"attributes": []any{
			map[string]any{"key": "llm.cost.total", "value": map[string]any{"doubleValue": cost}},
			map[string]any{"key": "gen_ai.operation.name", "value": map[string]any{"stringValue": "chat"}},
		},
	}
}

func eventBatch(spans ...map[string]any) []byte {
	body, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": spans}}}}})
	return body
}

func openEvents(t *testing.T, server *httptest.Server, path, bearer, cookie string, timeout time.Duration) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: cookie})
	}
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("events: %d %v %s", response.StatusCode, response.Header, body)
	}
	if response.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("cache-control: %v", response.Header)
	}
	return bufio.NewReader(response.Body)
}

func readEventFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			return frame.String()
		}
		frame.WriteString(line)
	}
}

func checkEvent(t *testing.T, reader *bufio.Reader, kind, spanID string, field string, value float64) {
	t.Helper()
	frame := readEventFrame(t, reader)
	for strings.HasPrefix(frame, ":") {
		frame = readEventFrame(t, reader)
	}
	lines := strings.Split(strings.TrimSpace(frame), "\n")
	if len(lines) != 2 || lines[0] != "event: "+kind || !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("unexpected event: %q", frame)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &data); err != nil {
		t.Fatal(err)
	}
	if data["trace_id"] != eventTraceID || data["span_id"] != spanID || data[field] != value {
		t.Fatalf("event %s: %v", kind, data)
	}
}

func TestEventsAuthAndValidation(t *testing.T) {
	h, _ := newTestHandler(t, "secret")
	for _, token := range []string{"", "wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		// Even protobuf/HTML clients must get JSON, never a login redirect.
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Content-Type", "application/x-protobuf")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != 401 || rr.Header().Get("Content-Type") != "application/json" || !strings.Contains(rr.Body.String(), `"error":"unauthorized"`) {
			t.Fatalf("auth: %d %v %s", rr.Code, rr.Header(), rr.Body.String())
		}
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	openEvents(t, server, "/events", "secret", "", 5*time.Second)
	openEvents(t, server, "/events", "", sessionValue("secret"), 5*time.Second)

	h, db := newTestHandler(t, "")
	for _, key := range []string{"min_duration", "min_cost"} {
		for _, value := range []string{"bad", "-1", "NaN", "Inf", "-Inf", "1e999", ""} {
			rr := request(t, h, http.MethodGet, "/events?"+key+"="+url.QueryEscape(value), "", "", nil)
			if rr.Code != 400 || rr.Header().Get("Content-Type") != "application/json" {
				t.Errorf("%s=%q: %d %s", key, value, rr.Code, rr.Body.String())
			}
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
		rr := request(t, h, method, "/events", "application/json", "", eventBatch(eventSpan("1111111111111111", 100, 1, 2)))
		if rr.Code != 405 || rr.Header().Get("Allow") != "GET" {
			t.Errorf("%s: %d %v", method, rr.Code, rr.Header())
		}
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 0 {
		t.Fatalf("events mutated store: count=%d err=%v", count, err)
	}
}

func TestEventsIngestFanoutAndThresholds(t *testing.T) {
	h, _ := newTestHandler(t, "")
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	low := openEvents(t, server, "/events?min_duration=100&min_cost=1", "", "", 10*time.Second)
	high := openEvents(t, server, "/events?min_duration=200&min_cost=10", "", "", 10*time.Second)
	errorsOnly := openEvents(t, server, "/events", "", "", 10*time.Second)
	zero := openEvents(t, server, "/events?min_duration=0&min_cost=0", "", "", 10*time.Second)

	// Failed ingestion must not publish, and equality must not trigger.
	if rr := request(t, h, "POST", "/v1/traces", "application/json", "", eventBatch(eventSpan("9999999999999999", 999, -1, 2))); rr.Code != 400 {
		t.Fatalf("invalid cost: %d %s", rr.Code, rr.Body.String())
	}
	first := "1111111111111111"
	if rr := request(t, h, "POST", "/v1/traces", "application/json", "", eventBatch(eventSpan(first, 100, 1, 1))); rr.Code != 200 {
		t.Fatalf("ingest: %d %s", rr.Code, rr.Body.String())
	}
	checkEvent(t, zero, "slow", first, "duration_ms", 100)
	checkEvent(t, zero, "cost", first, "cost_usd", 1)

	second := "2222222222222222"
	if rr := request(t, h, "POST", "/api/public/otel/v1/traces", "application/json", "", eventBatch(eventSpan(second, 150, 0.5, 2))); rr.Code != 200 {
		t.Fatalf("ingest: %d %s", rr.Code, rr.Body.String())
	}
	checkEvent(t, low, "error", second, "status_code", 2)
	checkEvent(t, low, "slow", second, "duration_ms", 150)
	checkEvent(t, low, "cost", second, "cost_usd", 1.5) // Across batches, not just this span.
	checkEvent(t, high, "error", second, "status_code", 2)
	checkEvent(t, errorsOnly, "error", second, "status_code", 2)
	checkEvent(t, zero, "error", second, "status_code", 2)
	checkEvent(t, zero, "slow", second, "duration_ms", 150)
	checkEvent(t, zero, "cost", second, "cost_usd", 1.5)

	// Upserts must not add cost twice; no queued slow/cost on high/default streams.
	if rr := request(t, h, "POST", "/", "application/json", "", eventBatch(eventSpan(second, 150, 0.5, 2))); rr.Code != 200 {
		t.Fatalf("ingest: %d %s", rr.Code, rr.Body.String())
	}
	checkEvent(t, low, "error", second, "status_code", 2)
	checkEvent(t, low, "slow", second, "duration_ms", 150)
	checkEvent(t, low, "cost", second, "cost_usd", 1.5)
	checkEvent(t, high, "error", second, "status_code", 2)
	checkEvent(t, errorsOnly, "error", second, "status_code", 2)
}

func TestEventsKeepalive(t *testing.T) {
	h, _ := newTestHandler(t, "")
	server := httptest.NewUnstartedServer(h)
	// A stream must override/refresh the server's ordinary response deadline.
	server.Config.WriteTimeout = time.Second
	server.Start()
	t.Cleanup(server.Close)
	reader := openEvents(t, server, "/events", "", "", 20*time.Second)
	start := time.Now()
	frame := readEventFrame(t, reader)
	if time.Since(start) < time.Second { // Allow an initial connection comment.
		frame = readEventFrame(t, reader)
	}
	if !strings.HasPrefix(frame, ":") || time.Since(start) < 14*time.Second {
		t.Fatalf("keepalive too early/not a comment: %q after %v", frame, time.Since(start))
	}
}

type blockedEventWriter struct {
	header  http.Header
	ready   chan struct{}
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedEventWriter) Header() http.Header { return w.header }
func (w *blockedEventWriter) WriteHeader(int)     {}
func (w *blockedEventWriter) Flush()              { w.once.Do(func() { close(w.ready) }) }
func (w *blockedEventWriter) Write(p []byte) (int, error) {
	if bytes.HasPrefix(p, []byte("event:")) {
		select {
		case <-w.blocked:
		default:
			close(w.blocked)
		}
		<-w.release
	}
	return len(p), nil
}

func TestEventsSlowClientDoesNotBlockIngest(t *testing.T) {
	h, _ := newTestHandler(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	writer := &blockedEventWriter{header: make(http.Header), ready: make(chan struct{}), blocked: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(writer, httptest.NewRequest("GET", "/events", nil).WithContext(ctx))
	}()
	t.Cleanup(func() {
		cancel()
		close(writer.release)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream did not stop after cancellation")
		}
	})
	select {
	case <-writer.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not open")
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	fast := openEvents(t, server, "/events", "", "", 10*time.Second)
	if rr := request(t, h, "POST", "/v1/traces", "application/json", "", eventBatch(eventSpan("1111111111111111", 1, 0, 2))); rr.Code != 200 {
		t.Fatalf("ingest: %d", rr.Code)
	}
	checkEvent(t, fast, "error", "1111111111111111", "status_code", 2)
	select {
	case <-writer.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("slow client did not block")
	}
	var spans []map[string]any
	for i := 0; i < 256; i++ {
		spans = append(spans, eventSpan(fmt.Sprintf("%016x", i+2), 1, 0, 2))
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- request(t, h, "POST", "/v1/traces", "application/json", "", eventBatch(spans...)) }()
	select {
	case rr := <-result:
		if rr.Code != 200 {
			t.Fatalf("ingest: %d %s", rr.Code, rr.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow client blocked ingest")
	}
	checkEvent(t, fast, "error", "0000000000000002", "status_code", 2)
}
