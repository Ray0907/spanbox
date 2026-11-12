package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func agentSeed(t *testing.T, handler http.Handler, n int, input string) (map[string]bool, string, string) {
	t.Helper()
	fixture, _ := traceFixture(t)
	var batch collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(fixture, &batch); err != nil {
		t.Fatal(err)
	}
	scope := batch.ResourceSpans[0].ScopeSpans[0]
	base := scope.Spans[0]
	scope.Spans = nil
	ids := make(map[string]bool, n)
	var firstTrace, firstSpan string
	for i := 0; i < n; i++ {
		span := proto.Clone(base).(*tracepb.Span)
		span.TraceId[14], span.TraceId[15] = byte(i>>8), byte(i)
		span.SpanId[6], span.SpanId[7] = byte(i>>8), byte(i)
		span.StartTimeUnixNano = base.StartTimeUnixNano + uint64(i/4)*1_000_000
		span.EndTimeUnixNano = span.StartTimeUnixNano + 300_000_000
		span.Name = "needle"
		span.Attributes = append([]*commonpb.KeyValue(nil), base.Attributes...)
		span.Attributes = append(span.Attributes, &commonpb.KeyValue{Key: "session.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("session-%02d", i)}}})
		if i%2 == 0 {
			span.Attributes = append(span.Attributes, &commonpb.KeyValue{Key: "gen_ai.input.messages", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: input}}})
		} else {
			span.Status = &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR}
		}
		ids[fmt.Sprintf("%x", span.TraceId)] = true
		if i == 0 {
			firstTrace, firstSpan = fmt.Sprintf("%x", span.TraceId), fmt.Sprintf("%x", span.SpanId)
		}
		scope.Spans = append(scope.Spans, span)
	}
	body, err := protojson.Marshal(&batch)
	if err != nil {
		t.Fatal(err)
	}
	resp := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", body)
	if resp.Code != http.StatusOK {
		t.Fatalf("ingest: %d %s", resp.Code, resp.Body.String())
	}
	return ids, firstTrace, firstSpan
}

func agentGet(t *testing.T, handler http.Handler, path string) map[string]any {
	t.Helper()
	resp := request(t, handler, http.MethodGet, path, "", "", nil)
	if resp.Code != http.StatusOK || !strings.HasPrefix(resp.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET %s: %d %s", path, resp.Code, resp.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAgentJSONInvalidUTF8Window(t *testing.T) {
	handler, database := newTestHandler(t, "")
	_, traceID, spanID := agentSeed(t, handler, 1, "valid")
	var seq int
	var name, path string
	if err := database.Reader().QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("UPDATE spans SET input_content=CAST(X'61FF62' AS TEXT) WHERE trace_id=? AND span_id=?", traceID, spanID); err != nil {
		t.Fatal(err)
	}
	base := "/spans/" + traceID + "/" + spanID + "?format=json"
	for offset, expected := range []string{"a", "�", "b"} {
		result := agentGet(t, handler, fmt.Sprintf("%s&field=input&offset=%d&len=1", base, offset))
		window := result["input"].(map[string]any)
		if window["invalid_utf8"] != true || window["text"] != expected || window["total_chars"] != float64(3) {
			t.Fatalf("offset %d: %v", offset, window)
		}
	}
	defaultWindows := agentGet(t, handler, base)
	if defaultWindows["input"].(map[string]any)["invalid_utf8"] != true {
		t.Fatalf("default input window not marked invalid: %v", defaultWindows["input"])
	}
	for _, field := range []string{"output", "attributes"} {
		if valid := defaultWindows[field].(map[string]any); valid["invalid_utf8"] != nil {
			t.Fatalf("valid %s field marked invalid: %v", field, valid)
		}
	}
}

func TestAgentJSONPaging(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	traceIDs, _, _ := agentSeed(t, handler, 13, "needle")
	sessionIDs := make(map[string]bool, 13)
	spanIDs := make(map[string]bool, 13)
	for i := 0; i < 13; i++ {
		sessionIDs[fmt.Sprintf("session-%02d", i)] = true
		spanIDs[fmt.Sprintf("111213141516%04x", i)] = true
	}
	for _, route := range []struct {
		path, id string
		want     map[string]bool
	}{{"/", "trace_id", traceIDs}, {"/sessions", "session_id", sessionIDs}, {"/search?q=needle", "span_id", spanIDs}} {
		t.Run(route.path, func(t *testing.T) {
			seen := map[string]bool{}
			cursor := ""
			for page := 0; page < 10; page++ {
				q, _ := url.Parse(route.path)
				v := q.Query()
				v.Set("format", "json")
				v.Set("limit", "3")
				if cursor != "" {
					v.Set("cursor", cursor)
				}
				q.RawQuery = v.Encode()
				result := agentGet(t, handler, q.String())
				items := result["items"].([]any)
				if len(items) == 0 || len(items) > 3 {
					t.Fatalf("page %d: %v", page, result)
				}
				for _, item := range items {
					id := item.(map[string]any)[route.id].(string)
					if !route.want[id] || seen[id] {
						t.Fatalf("unexpected or duplicate %s", id)
					}
					seen[id] = true
				}
				if result["next_cursor"] == nil {
					for id := range route.want {
						if !seen[id] {
							t.Fatalf("missing %s", id)
						}
					}
					return
				}
				cursor = result["next_cursor"].(string)
			}
			t.Fatal("cursor never ended")
		})
	}
}

func TestAgentJSONCaps(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	_, traceID, spanID := agentSeed(t, handler, 52, strings.Repeat("x", 20001))
	first := agentGet(t, handler, "/?format=json&limit=500")
	if got := len(first["items"].([]any)); got != 50 || first["next_cursor"] == nil {
		t.Fatalf("limit clamp: got %d rows, cursor %v", got, first["next_cursor"])
	}
	second := agentGet(t, handler, "/?format=json&limit=500&cursor="+url.QueryEscape(first["next_cursor"].(string)))
	if got := len(second["items"].([]any)); got != 2 || second["next_cursor"] != nil {
		t.Fatalf("limit clamp final: got %d rows, cursor %v", got, second["next_cursor"])
	}
	window := agentGet(t, handler, "/spans/"+traceID+"/"+spanID+"?format=json&field=input&len=50000")["input"].(map[string]any)
	if got := len([]rune(window["text"].(string))); got != 20000 || window["total_chars"].(float64) <= 20000 || window["next_offset"] != float64(20000) {
		t.Fatalf("len clamp: got %d runes, total %v, next %v", got, window["total_chars"], window["next_offset"])
	}
}

func TestAgentJSONErrorsAndHTML(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	_, traceID, spanID := agentSeed(t, handler, 1, "private-body")
	for _, path := range []string{
		"/?format=json&cursor=bad", "/sessions?format=json&cursor=bad", "/search?format=json&q=needle&cursor=bad",
		"/?format=json&limit=-1", "/?format=json&limit=abc", "/?format=json&limit=99999999999999999999999999999",
		"/?format=json&cursor=123:" + strings.Repeat("z", 32),
		"/search?format=json&q=needle&cursor=123:" + strings.Repeat("z", 16),
		"/spans/" + traceID + "/" + spanID + "?format=json&offset=-1",
		"/spans/" + traceID + "/" + spanID + "?format=json&len=-1",
		"/spans/" + traceID + "/" + spanID + "?format=json&len=oops",
		"/spans/" + traceID + "/" + spanID + "?format=json&len=99999999999999999999999999999",
		"/spans/" + traceID + "/" + spanID + "?format=json&offset=99999999999999999999999999999",
		"/spans/" + traceID + "/" + spanID + "?format=json&field=unknown",
	} {
		resp := request(t, handler, http.MethodGet, path, "", "", nil)
		var body map[string]string
		if resp.Code != 400 || json.Unmarshal(resp.Body.Bytes(), &body) != nil || body["error"] == "" {
			t.Errorf("%s: %d %s", path, resp.Code, resp.Body.String())
		}
	}
	for _, path := range []string{"/traces/" + strings.Repeat("0", 32) + "?format=json", "/spans/" + traceID + "/" + strings.Repeat("0", 16) + "?format=json", "/traces/bad?format=json", "/spans/bad/id?format=json"} {
		resp := request(t, handler, http.MethodGet, path, "", "", nil)
		var body map[string]string
		if resp.Code != 404 || json.Unmarshal(resp.Body.Bytes(), &body) != nil || body["error"] == "" {
			t.Errorf("%s: %d %s", path, resp.Code, resp.Body.String())
		}
	}
	for _, page := range []struct {
		path     string
		contains []string
	}{
		{"/", []string{`id="root"`}}, // the trace list is covered by the JSON list assertions above and e2e-mobile
		{"/sessions", []string{`id="root"`}},
		{"/search?q=needle", []string{`id="root"`}},
		{"/traces/" + traceID, []string{`id="root"`}},
		{"/spans/" + traceID + "/" + spanID, []string{`id="root"`}},
	} {
		resp := request(t, handler, http.MethodGet, page.path, "", "", nil)
		if resp.Code != 200 || !strings.HasPrefix(resp.Header().Get("Content-Type"), "text/html") {
			t.Errorf("HTML %s: %d %q", page.path, resp.Code, resp.Header().Get("Content-Type"))
		}
		for _, expected := range page.contains {
			if !strings.Contains(resp.Body.String(), expected) {
				t.Errorf("HTML %s missing %q", page.path, expected)
			}
		}
	}
}

func TestMigratedSpanContentJSON(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	_, traceID, spanID := agentSeed(t, handler, 1, "private-body")
	trace := agentGet(t, handler, "/traces/"+traceID+"?format=json")
	if !strings.Contains(fmt.Sprint(trace["spans"]), spanID) {
		t.Fatal("trace must include span identity")
	}
	span := agentGet(t, handler, "/spans/"+traceID+"/"+spanID+"?format=json")
	if !strings.Contains(span["input"].(map[string]any)["text"].(string), "private-body") {
		t.Fatal("span must include its input")
	}
}

func TestDashboardDataAuth(t *testing.T) {
	handler, _ := newTestHandler(t, "secret")
	check := func(name, path, bearer string, wantStatus int, wantType, wantBody string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != wantStatus || out.Header().Get("Content-Type") != wantType || wantBody != "" && strings.TrimSpace(out.Body.String()) != wantBody {
				t.Fatalf("status=%d type=%q body=%q", out.Code, out.Header().Get("Content-Type"), out.Body.String())
			}
			if wantStatus == 200 {
				var data struct{ TraceCount int64 }
				if err := json.Unmarshal(out.Body.Bytes(), &data); err != nil || data.TraceCount != 0 {
					t.Fatalf("dashboard JSON: %v %q", err, out.Body.String())
				}
			}
		})
	}
	check("valid bearer", "/dashboard/data?range=24h", "secret", 200, "application/json", "")
	for _, auth := range []struct{ name, token string }{{"missing", ""}, {"wrong", "wrong"}} {
		check(auth.name+" bearer", "/dashboard/data?range=24h", auth.token, 401, "application/json", `{"error":"unauthorized"}`)
	}
	for _, path := range []string{"/dashboard", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != http.StatusFound || out.Header().Get("Location") != "/login" {
			t.Errorf("bearer on HTML %s: status=%d location=%q", path, out.Code, out.Header().Get("Location"))
		}
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("token=secret"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, login)
	if len(out.Result().Cookies()) != 1 {
		t.Fatalf("login cookies: %v", out.Result().Cookies())
	}
	cookie := out.Result().Cookies()[0]
	req := httptest.NewRequest(http.MethodGet, "/dashboard/data?range=24h", nil)
	req.AddCookie(cookie)
	out = httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 200 || out.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("cookie dashboard: %d %q", out.Code, out.Body.String())
	}
	var data struct{ TraceCount int64 }
	if err := json.Unmarshal(out.Body.Bytes(), &data); err != nil || data.TraceCount != 0 {
		t.Fatalf("cookie dashboard JSON: %v %q", err, out.Body.String())
	}
	open, _ := newTestHandler(t, "")
	out = request(t, open, http.MethodGet, "/dashboard/data?range=24h", "", "", nil)
	if out.Code != 200 || out.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("open dashboard: %d %q", out.Code, out.Body.String())
	}
}

func TestAgentJSONAuth(t *testing.T) {
	handler, _ := newTestHandler(t, "secret")
	for _, path := range []string{"/?format=json", "/sessions?format=json", "/search?format=json", "/traces/" + strings.Repeat("0", 32) + "?format=json", "/spans/" + strings.Repeat("0", 32) + "/" + strings.Repeat("0", 16) + "?format=json", "/dashboard/data?range=24h"} {
		resp := request(t, handler, http.MethodGet, path, "", "", nil)
		if resp.Code == 200 || resp.Code == 404 {
			t.Errorf("unauthenticated %s: %d", path, resp.Code)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code == http.StatusFound || out.Code == http.StatusUnauthorized {
			t.Errorf("bearer %s: %d", path, out.Code)
		}
	}
	for _, path := range []string{"/dashboard/data?range=24h", "/?format=json"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer wrong")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != http.StatusUnauthorized || !strings.Contains(out.Body.String(), `"error":"unauthorized"`) {
			t.Errorf("wrong bearer %s: %d %q", path, out.Code, out.Body.String())
		}
	}
	for _, path := range []string{"/dashboard", "/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != http.StatusFound {
			t.Errorf("bearer on HTML %s must still redirect to login: %d", path, out.Code)
		}
	}
}
