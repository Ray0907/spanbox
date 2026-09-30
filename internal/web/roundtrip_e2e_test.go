package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/store"
)

// These tests use real HTTP connections and independent on-disk databases.
func TestRoundTripE2E(t *testing.T) {
	const token = "roundtrip-secret"
	sourceHandler, source := newTestHandler(t, token)
	targetHandler, target := newTestHandler(t, token)
	a, b := httptest.NewServer(sourceHandler), httptest.NewServer(targetHandler)
	defer a.Close()
	defer b.Close()
	client := &http.Client{Timeout: 30 * time.Second}
	defer client.CloseIdleConnections()
	request := func(base, method, path, credential string, body io.Reader) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/x-ndjson")
		}
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	get := func(base, path string) []byte {
		t.Helper()
		status, data := request(base, "GET", path, token, nil)
		if status != 200 {
			t.Fatalf("GET %s: %d %s", path, status, data)
		}
		return data
	}

	// The normal seed traverses OTLP decoding, normalization and pricing.
	seed, _ := traceFixture(t)
	req, _ := http.NewRequest("POST", a.URL+"/v1/traces", bytes.NewReader(seed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("OTLP seed: %d", resp.StatusCode)
	}

	// Protobuf rejects invalid UTF-8 strings. Seed legacy/corrupt string bytes
	// through storage to exercise the export boundary without normalizing them.
	now := time.Now().UTC().Truncate(time.Second)
	zero, cost := int64(0), 0.0
	edge := store.Span{
		TraceID: strings.Repeat("e", 32), SpanID: strings.Repeat("f", 16),
		Name: "edge 世界 🦄", Kind: "llm", ServiceName: "roundtrip",
		StartNs: now.UnixNano(), EndNs: now.Add(time.Second).UnixNano(), DurationMs: 1000,
		InputContent: "hello 世界\x00\xff\xfe", OutputContent: "",
		Attributes: `{"huge":"` + strings.Repeat("界", 4<<20) + `"}`,
		Events:     `[{"name":"é"}]`, Links: "[]", Resource: "{}", Scope: "{}",
		StatusMessage: "bad\xff", RequestModel: "edge-model", StatusCode: 2,
		InputTokens: &zero, OutputTokens: &zero, CacheReadTokens: &zero, CostUSD: &cost,
	}
	if err := source.InsertBatch(context.Background(), []store.Span{edge}); err != nil {
		t.Fatal(err)
	}
	data := get(a.URL, "/export")
	for i := 0; i < 2; i++ {
		status, body := request(b.URL, "POST", "/import", token, bytes.NewReader(data))
		if status != 200 {
			t.Fatalf("import %d: %d %s", i, status, body)
		}
		if got := get(b.URL, "/export"); !bytes.Equal(got, data) {
			t.Fatal("export bytes changed on round trip")
		}
	}
	persisted, err := target.GetSpan(context.Background(), edge.TraceID, edge.SpanID)
	if err != nil || !reflect.DeepEqual(persisted, edge) {
		t.Fatalf("stored bytes/nullable values changed: err=%v", err)
	}

	paths := []string{"/?format=json&limit=50"}
	var lines []store.Span
	decoder := json.NewDecoder(bytes.NewReader(data))
	for decoder.More() {
		var span store.Span
		if err := decoder.Decode(&span); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, span)
		paths = append(paths, "/traces/"+span.TraceID+"?format=json")
		for _, field := range []string{"input", "output", "attributes"} {
			for _, offset := range []int{0, 1, 7, 20000, 4 << 20} {
				paths = append(paths, fmt.Sprintf("/spans/%s/%s?format=json&field=%s&offset=%d&len=17", span.TraceID, span.SpanID, field, offset))
			}
		}
	}
	for _, path := range paths {
		if !bytes.Equal(get(a.URL, path), get(b.URL, path)) {
			t.Fatalf("read API changed: %s", path)
		}
	}
	for _, table := range []string{"spans", "traces", "spans_fts"} {
		var x, y int
		if err := source.Reader().QueryRow("SELECT count(*) FROM " + table).Scan(&x); err != nil {
			t.Fatal(err)
		}
		if err := target.Reader().QueryRow("SELECT count(*) FROM " + table).Scan(&y); err != nil {
			t.Fatal(err)
		}
		if x != y {
			t.Fatalf("%s count: %d != %d", table, x, y)
		}
	}

	t.Run("auth", func(t *testing.T) {
		for _, methodPath := range [][2]string{{"GET", "/export"}, {"POST", "/import"}} {
			for _, credential := range []string{"", "wrong"} {
				status, _ := request(b.URL, methodPath[0], methodPath[1], credential, strings.NewReader(""))
				if status != 401 {
					t.Fatalf("%v credential %q: %d", methodPath, credential, status)
				}
			}
		}
		login, _ := http.NewRequest("POST", b.URL+"/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
		login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		noRedirect := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := noRedirect.Do(login)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || len(resp.Cookies()) == 0 {
			t.Fatalf("login did not create session: status=%d cookies=%d", resp.StatusCode, len(resp.Cookies()))
		}
		for _, mp := range [][2]string{{"GET", "/export?model=missing"}, {"POST", "/import"}} {
			req, _ := http.NewRequest(mp[0], b.URL+mp[1], strings.NewReader(""))
			req.AddCookie(resp.Cookies()[0])
			req.Header.Set("Content-Type", "application/x-ndjson")
			result, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			result.Body.Close()
			if result.StatusCode != 200 {
				t.Fatalf("cookie %v: %d", mp, result.StatusCode)
			}
		}
	})

	t.Run("empty", func(t *testing.T) {
		if data := get(a.URL, "/export?model=missing"); len(data) != 0 {
			t.Fatal("empty export not empty")
		}
		status, body := request(b.URL, "POST", "/import", token, strings.NewReader(""))
		if status != 200 || string(body) != "{\"imported\":0}\n" {
			t.Fatalf("empty import: %d %s", status, body)
		}
	})
}

func TestExportE2ESpanFilters(t *testing.T) {
	h, db := newTestHandler(t, "")
	s := httptest.NewServer(h)
	defer s.Close()
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	root := store.Span{TraceID: strings.Repeat("1", 32), SpanID: strings.Repeat("1", 16), Name: "root", Kind: "other", StartNs: start.UnixNano(), EndNs: start.Add(time.Second).UnixNano()}
	child := root
	child.SpanID, child.ParentSpanID, child.Kind = strings.Repeat("2", 16), root.SpanID, "llm"
	child.StartNs, child.EndNs = start.Add(2*time.Second).UnixNano(), start.Add(3*time.Second).UnixNano()
	child.RequestModel, child.ResponseModel, child.StatusCode = "request-model", "response-model", 2
	if err := db.InsertBatch(context.Background(), []store.Span{root, child}); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{
		"model=request-model", "model=response-model", "errors=1",
		"range=custom&from=" + start.Add(time.Second).Format(time.RFC3339) + "&to=" + start.Add(3*time.Second).Format(time.RFC3339),
		"model=response-model&errors=1&range=custom&from=" + start.Add(time.Second).Format(time.RFC3339) + "&to=" + start.Add(3*time.Second).Format(time.RFC3339),
	} {
		resp, err := s.Client().Get(s.URL + "/export?" + filter)
		if err != nil {
			t.Fatal(err)
		}
		var spans []store.Span
		dec := json.NewDecoder(resp.Body)
		for {
			var span store.Span
			err := dec.Decode(&span)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			spans = append(spans, span)
		}
		resp.Body.Close()
		if len(spans) != 1 || spans[0].SpanID != child.SpanID {
			t.Errorf("filter %s: %+v", filter, spans)
		}
	}
}

func TestImportE2ELargeFile(t *testing.T) {
	h, db := newTestHandler(t, "")
	s := httptest.NewServer(h)
	defer s.Close()
	// Individually valid lines; total upload is larger than the OTLP body limit.
	span := store.Span{TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16), Kind: "other", Name: "large", StartNs: 1, EndNs: 2, Attributes: strings.Repeat("x", 1<<20)}
	line, err := json.Marshal(span)
	if err != nil {
		t.Fatal(err)
	}
	line = append(line, '\n')
	readers := make([]io.Reader, 33)
	for i := range readers {
		readers[i] = bytes.NewReader(line)
	}
	resp, err := s.Client().Post(s.URL+"/import", "application/x-ndjson", io.MultiReader(readers...))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "{\"imported\":33}\n" {
		t.Fatalf("large upload: %d %s", resp.StatusCode, body)
	}
	got, err := db.GetSpan(context.Background(), span.TraceID, span.SpanID)
	if err != nil || !reflect.DeepEqual(got, span) {
		t.Fatalf("large span changed: %v", err)
	}
}

func TestImportE2EMalformedAndRetention(t *testing.T) {
	h, db := newTestHandler(t, "")
	s := httptest.NewServer(h)
	defer s.Close()
	ctx := context.Background()
	original := store.Span{TraceID: strings.Repeat("f", 32), SpanID: strings.Repeat("f", 16), Name: "existing", Kind: "other", StartNs: time.Now().UnixNano(), EndNs: time.Now().UnixNano() + 1}
	if err := db.InsertBatch(ctx, []store.Span{original}); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	for n := 0; n < 501; n++ {
		span := store.Span{TraceID: fmt.Sprintf("%032x", n+1), SpanID: fmt.Sprintf("%016x", n+1), Name: "old import", Kind: "other", StartNs: 1, EndNs: 2}
		if err := json.NewEncoder(&body).Encode(span); err != nil {
			t.Fatal(err)
		}
	}
	reader, writer := io.Pipe()
	t.Cleanup(func() { reader.Close(); writer.Close() })
	done := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := s.Client().Post(s.URL+"/import", "application/x-ndjson", reader)
		if err != nil {
			errCh <- err
			return
		}
		done <- resp
	}()
	if _, err := writer.Write(body.Bytes()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var count int
		if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 501 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first batch not committed: count=%d", count)
		}
		time.Sleep(5 * time.Millisecond)
	}
	deleted, err := db.Purge(ctx, time.Now().Add(-24*time.Hour).UnixNano())
	if !errors.Is(err, store.ErrRetentionPaused) || deleted != 0 {
		t.Errorf("concurrent purge did not signal protected import: %d, %v", deleted, err)
	}
	if _, err := io.WriteString(writer, "{oops\n"); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	var resp *http.Response
	select {
	case resp = <-done:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("import did not complete")
	}
	defer resp.Body.Close()
	message, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(message), "line 502:") {
		t.Fatalf("malformed: %d %s", resp.StatusCode, message)
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 501 {
		t.Fatalf("completed batches not retained: count=%d err=%v", count, err)
	}
	got, err := db.GetSpan(ctx, original.TraceID, original.SpanID)
	if err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("existing data corrupted: %v", err)
	}
}

func TestExportE2EStreamsTenThousandSpans(t *testing.T) {
	h, db := newTestHandler(t, "")
	spans := make([]store.Span, 10001)
	for n := range spans {
		spans[n] = store.Span{TraceID: fmt.Sprintf("%032x", n+1), SpanID: fmt.Sprintf("%016x", n+1), Name: "stream", Kind: "other", StartNs: int64(n + 1), EndNs: int64(n + 2)}
	}
	for i := 0; i < len(spans); i += 500 {
		if err := db.InsertBatch(context.Background(), spans[i:min(i+500, len(spans))]); err != nil {
			t.Fatal(err)
		}
	}
	gate := make(chan struct{})
	first := make(chan int, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&gatedExportWriter{ResponseWriter: w, gate: gate, first: first}, r)
	}))
	defer s.Close()
	defer close(gate)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(s.URL + "/export")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadBytes('\n')
	if err != nil || !json.Valid(bytes.TrimSpace(line)) {
		t.Fatalf("first streamed line: %v", err)
	}
	select {
	case writes := <-first:
		if writes != 1 {
			t.Fatalf("buffered %d writes before flush", writes)
		}
	case <-time.After(time.Second):
		t.Fatal("first row did not flush")
	}
	if resp.ContentLength != -1 {
		t.Fatalf("export has buffered Content-Length %d", resp.ContentLength)
	}
	// Release the remaining rows and verify none were lost. Cancellation is
	// covered by the store callback test and the server's request context.
	gate <- struct{}{}
	count := 1
	scan := bufio.NewScanner(reader)
	for scan.Scan() {
		if !json.Valid(scan.Bytes()) {
			t.Fatal("invalid streamed row")
		}
		count++
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(spans) {
		t.Fatalf("streamed %d spans, want %d", count, len(spans))
	}
}

type gatedExportWriter struct {
	http.ResponseWriter
	gate   chan struct{}
	first  chan int
	writes int
}

func (w *gatedExportWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.ResponseWriter.Write(p)
}
func (w *gatedExportWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	if w.writes == 1 {
		w.first <- w.writes
		<-w.gate
	}
}

// Keep the OTLP trust-boundary limit unchanged while imports stream separately.
var _ = config.MaxBodyBytes
