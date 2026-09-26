package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/store"
)

func exportRequest(handler http.Handler, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestExportImportRoundTrip(t *testing.T) {
	h, db := newTestHandler(t, "")
	now := time.Now().UTC().Truncate(time.Second)
	span := store.Span{
		TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16), Name: "original", Kind: "llm",
		ServiceName: "demo", StartNs: now.UnixNano(), EndNs: now.Add(time.Second).UnixNano(), DurationMs: 1000,
		InputContent: `{"prompt":"hello"}`, OutputContent: `{"reply":"world"}`, Attributes: `{"key":"value"}`,
		Events: `[{"name":"event"}]`, Links: `[]`, Resource: `{}`, Scope: `{}`,
		RequestModel: "gpt-4o", SessionID: "session-a", UserID: "user-a", StatusCode: 2,
	}
	input := int64(0)
	cost := 0.0
	span.InputTokens, span.CostUSD = &input, &cost
	if err := db.InsertBatch(context.Background(), []store.Span{span}); err != nil {
		t.Fatal(err)
	}
	from, to := now.Format(time.RFC3339), now.Add(time.Second).Format(time.RFC3339)
	query := "range=custom&from=" + from + "&to=" + to + "&model=gpt-4o&service=demo&errors=1&min_duration=1000&session=session-a&user=user-a"
	response := exportRequest(h, "GET", "/export?"+query, "", nil)
	if response.Code != 200 || response.Header().Get("Content-Type") != "application/x-ndjson" || response.Header().Get("Content-Disposition") != `attachment; filename="spanbox-export.ndjson"` {
		t.Fatalf("export: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	var decoded store.Span
	if err := json.Unmarshal(bytes.TrimSpace(response.Body.Bytes()), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.InputContent != span.InputContent || decoded.Events != span.Events || decoded.InputTokens == nil || *decoded.InputTokens != 0 || decoded.CostUSD == nil || *decoded.CostUSD != 0 {
		t.Fatalf("exported span: %+v", decoded)
	}
	if filtered := exportRequest(h, "GET", "/export?range=custom&from="+to+"&to="+now.Add(2*time.Second).Format(time.RFC3339), "", nil); filtered.Body.Len() != 0 {
		t.Fatalf("from filter: %s", filtered.Body.String())
	}
	if filtered := exportRequest(h, "GET", "/export?range=custom&from="+now.Add(-time.Second).Format(time.RFC3339)+"&to="+from, "", nil); filtered.Body.Len() != 0 {
		t.Fatalf("to filter: %s", filtered.Body.String())
	}
	if empty := exportRequest(h, "GET", "/export?range=custom&from="+now.Add(-time.Hour).Format(time.RFC3339)+"&to="+from, "", nil); empty.Code != 200 || empty.Body.Len() != 0 {
		t.Fatalf("empty export: %d %s", empty.Code, empty.Body.String())
	}

	other, target := newTestHandler(t, "")
	for n := 0; n < 2; n++ {
		result := exportRequest(other, "POST", "/import", "application/x-ndjson; charset=utf-8", response.Body.Bytes())
		if result.Code != 200 || strings.TrimSpace(result.Body.String()) != `{"imported":1}` {
			t.Fatalf("import: %d %s", result.Code, result.Body.String())
		}
	}
	var spans, traces, fts int
	for _, query := range []struct {
		sql string
		out *int
	}{{"SELECT count(*) FROM spans", &spans}, {"SELECT count(*) FROM traces", &traces}, {"SELECT count(*) FROM spans_fts", &fts}} {
		if err := target.Reader().QueryRow(query.sql).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if spans != 1 || traces != 1 || fts != 1 {
		t.Fatalf("spans=%d traces=%d fts=%d", spans, traces, fts)
	}
	got := exportRequest(other, "GET", "/export", "", nil)
	if got.Body.String() != response.Body.String() {
		t.Fatalf("roundtrip: %q != %q", got.Body.String(), response.Body.String())
	}
	decoded.OutputContent = `{"reply":"updated"}`
	changed, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	updated := exportRequest(other, "POST", "/import", "application/x-ndjson", changed)
	if updated.Code != 200 || strings.TrimSpace(updated.Body.String()) != `{"imported":1}` {
		t.Fatalf("updated import: %d %s", updated.Code, updated.Body.String())
	}
	persisted, err := target.GetSpan(context.Background(), decoded.TraceID, decoded.SpanID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.OutputContent != decoded.OutputContent {
		t.Fatalf("upsert did not overwrite content: %q", persisted.OutputContent)
	}
	for _, path := range []string{"/?" + query, "/?" + query + "&partial=1"} {
		page := exportRequest(h, "GET", path, "", nil)
		_, after, ok := strings.Cut(html.UnescapeString(page.Body.String()), `id="export-link" href="/export?`)
		if !ok {
			t.Fatalf("missing filtered export link for %s: %s", path, page.Body.String())
		}
		href, _, _ := strings.Cut(after, `"`)
		params, err := url.ParseQuery(href)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := url.ParseQuery(query)
		if params.Encode() != want.Encode() {
			t.Fatalf("export link query = %q, want %q", href, query)
		}
		if strings.Contains(path, "partial=1") && !strings.Contains(page.Body.String(), `hx-swap-oob="outerHTML"`) {
			t.Fatal("filter refresh does not update export link")
		}
	}
	for _, param := range []string{"model=missing", "service=missing", "errors=0&model=missing", "min_duration=1001", "session=missing", "user=missing"} {
		filtered := exportRequest(h, "GET", "/export?"+param, "", nil)
		if filtered.Code != 200 || filtered.Body.Len() != 0 {
			t.Fatalf("filter %q: %d %s", param, filtered.Code, filtered.Body.String())
		}
	}
}

func TestExportImportValidation(t *testing.T) {
	h, db := newTestHandler(t, "")
	for _, path := range []string{"/export?range=invalid", "/export?range=custom&from=bad&to=2026-01-02T00:00:00Z", "/export?range=custom&from=2026-01-01T00:00:00Z&to=bad", "/export?range=custom&from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z", "/export?range=custom&from=2026-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", "/export?min_duration=bad"} {
		if got := exportRequest(h, "GET", path, "", nil); got.Code != 400 {
			t.Errorf("%s status=%d", path, got.Code)
		}
	}
	for _, method := range []string{"POST", "PUT"} {
		if got := exportRequest(h, method, "/export", "", nil); got.Code != 405 {
			t.Errorf("export %s=%d", method, got.Code)
		}
	}
	if got := exportRequest(h, "GET", "/import", "", nil); got.Code != 405 {
		t.Errorf("import GET=%d", got.Code)
	}
	if got := exportRequest(h, "POST", "/import", "text/plain", []byte("{}\n")); got.Code != 415 {
		t.Errorf("type=%d", got.Code)
	}
	if got := exportRequest(h, "POST", "/import", "application/x-ndjson", []byte("\n \n")); got.Code != 200 || strings.TrimSpace(got.Body.String()) != `{"imported":0}` {
		t.Errorf("blank=%d %s", got.Code, got.Body.String())
	}
	valid := store.Span{TraceID: strings.Repeat("c", 32), SpanID: strings.Repeat("d", 16), Name: "ok", Kind: "other", StartNs: 1, EndNs: 2}
	line, _ := json.Marshal(valid)
	cases := []struct {
		name   string
		body   []byte
		status int
	}{
		{"malformed", []byte("{oops\n"), 400},
		{"missing ids", []byte(`{"Name":"x"}`), 400},
		{"zero start", []byte(`{"TraceID":"x","SpanID":"y"}`), 400},
		{"zero end", []byte(`{"TraceID":"x","SpanID":"y","StartNs":1}`), 400},
		{"extra JSON", append(append([]byte{}, line...), []byte(` {}`)...), 400},
		{"oversized line", []byte(`{"Name":"` + strings.Repeat("x", 10<<20) + `"}`), 400},
		{"oversized body", bytes.Repeat([]byte("\n"), config.MaxBodyBytes+1), 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := exportRequest(h, "POST", "/import", "application/x-ndjson", tc.body)
			if got.Code != tc.status {
				t.Fatalf("status=%d body=%s", got.Code, got.Body.String())
			}
		})
	}
	for _, tc := range []struct{ name, invalid string }{{"malformed", "{oops"}, {"extra value", string(line) + " {}"}, {"trailing garbage", string(line) + " garbage"}} {
		t.Run("line number "+tc.name, func(t *testing.T) {
			body := append(append([]byte("\n"), line...), '\n')
			body = append(body, tc.invalid...)
			got := exportRequest(h, "POST", "/import", "application/x-ndjson", body)
			if got.Code != 400 || !strings.Contains(got.Body.String(), "line 3: ") {
				t.Fatalf("status=%d body=%q", got.Code, got.Body.String())
			}
		})
	}
	if got := exportRequest(h, "POST", "/import", "application/x-ndjson", append(append([]byte("\n"), line...), '\n')); got.Code != 200 {
		t.Fatalf("valid: %d %s", got.Code, got.Body.String())
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 1 {
		t.Fatalf("spans=%d err=%v", count, err)
	}
}

func TestImportBatchCommitAndAuth(t *testing.T) {
	h, db := newTestHandler(t, "secret")
	if got := exportRequest(h, "GET", "/export", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("export auth=%d", got.Code)
	}
	if got := exportRequest(h, "POST", "/import", "application/x-ndjson", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("import auth=%d", got.Code)
	}
	var body bytes.Buffer
	for n := 0; n < 501; n++ {
		span := store.Span{TraceID: strings.Repeat("a", 32), SpanID: fmt.Sprintf("%016x", n), Name: "batch", Kind: "other", StartNs: int64(n + 1), EndNs: int64(n + 2)}
		if err := json.NewEncoder(&body).Encode(span); err != nil {
			t.Fatal(err)
		}
	}
	body.WriteString("bad json\n")
	req := httptest.NewRequest("POST", "/import", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("secret")})
	response := httptest.NewRecorder()
	h.ServeHTTP(response, req)
	if response.Code != 400 {
		t.Fatalf("status=%d %s", response.Code, response.Body.String())
	}
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 500 {
		t.Fatalf("committed spans=%d err=%v", count, err)
	}
}
