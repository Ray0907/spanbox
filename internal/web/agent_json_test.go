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

func TestAgentJSONFiltersAndWindow(t *testing.T) {
	handler, database := newTestHandler(t, "")
	text := strings.Repeat("字éa", 4000)
	_, traceID, spanID := agentSeed(t, handler, 9, text)
	if err := database.Reader().QueryRow("SELECT input_content FROM spans WHERE trace_id=? AND span_id=?", traceID, spanID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"service=demo&errors=1", "service=missing"} {
		path := "/?format=json&limit=2&" + filter
		seen := map[string]bool{}
		cursor := ""
		for {
			page := agentGet(t, handler, path+"&cursor="+url.QueryEscape(cursor))
			for _, item := range page["items"].([]any) {
				id := item.(map[string]any)["trace_id"].(string)
				if seen[id] || filter == "service=demo&errors=1" && !item.(map[string]any)["has_error"].(bool) {
					t.Fatalf("filter %s: unexpected row %v", filter, item)
				}
				seen[id] = true
			}
			cursor, _ = page["next_cursor"].(string)
			if cursor == "" {
				break
			}
		}
		want := 4
		if filter == "service=missing" {
			want = 0
		}
		if len(seen) != want {
			t.Fatalf("filter %s: got %d want %d", filter, len(seen), want)
		}
	}
	trace := agentGet(t, handler, "/traces/"+traceID+"?format=json")
	if strings.Contains(fmt.Sprint(trace), text) {
		t.Fatal("trace leaked span body")
	}
	spans := trace["spans"].([]any)
	if len(spans) != 1 || spans[0].(map[string]any)["input_chars"].(float64) != float64(len([]rune(text))) {
		t.Fatalf("trace span metadata: %v", spans)
	}
	base := "/spans/" + traceID + "/" + spanID + "?format=json"
	defaultWindow := agentGet(t, handler, base)
	if defaultWindow["input"] == nil || defaultWindow["output"] == nil || defaultWindow["attributes"] == nil {
		t.Fatalf("missing default windows: %v", defaultWindow)
	}
	var collected strings.Builder
	offset := 0
	for {
		result := agentGet(t, handler, fmt.Sprintf("%s&field=input&offset=%d&len=777", base, offset))
		if result["output"] != nil || result["attributes"] != nil {
			t.Fatalf("field-only response: %v", result)
		}
		window := result["input"].(map[string]any)
		collected.WriteString(window["text"].(string))
		if window["next_offset"] == nil {
			break
		}
		offset = int(window["next_offset"].(float64))
	}
	if collected.String() != text {
		t.Fatalf("window reassembly: %d runes, want %d", len([]rune(collected.String())), len([]rune(text)))
	}
	past := agentGet(t, handler, base+"&field=input&offset=99999")
	if win := past["input"].(map[string]any); win["text"] != "" || win["next_offset"] != nil {
		t.Fatalf("past end: %v", win)
	}
}

func TestAgentJSONWindowsAcrossScripts(t *testing.T) {
	for _, script := range []struct{ name, text string }{
		{"English", "ordinary english words and punctuation! "},
		{"Latin precomposed", "café déjà vu àèîôü "},
		{"Latin combining", "cafe\u0301 de\u0301ja\u0300 e\u0302 "},
		{"Cyrillic", "Привет мир и здравствуйте "},
		{"Arabic", "مرحبا بالعالم أهلاً وسهلاً "},
		{"Thai", "สวัสดีชาวโลกภาษาไทย "},
		{"Devanagari", "नमस्ते दुनिया हिंदी भाषा "},
		{"emoji ZWJ", "👩‍💻 👨‍👩‍👧‍👦 🧑🏽‍🚀 "},
		{"4-byte runes", "𐍈 𠀀 𝄞 🦊 "},
	} {
		t.Run(script.name, func(t *testing.T) {
			handler, database := newTestHandler(t, "")
			text := strings.Repeat(script.text, 1+777/len([]rune(script.text)))
			_, traceID, spanID := agentSeed(t, handler, 1, text)
			original := text
			if err := database.Reader().QueryRow("SELECT input_content FROM spans WHERE trace_id=? AND span_id=?", traceID, spanID).Scan(&text); err != nil {
				t.Fatal(err)
			}
			var stored map[string]string
			if err := json.Unmarshal([]byte(text), &stored); err != nil || stored["messages"] != original {
				t.Fatalf("ingest changed script %q: %v", script.name, err)
			}
			base := "/spans/" + traceID + "/" + spanID + "?format=json&field=input"
			for _, length := range []int{1, 777} {
				var collected strings.Builder
				offset := 0
				for {
					result := agentGet(t, handler, fmt.Sprintf("%s&offset=%d&len=%d", base, offset, length))
					window := result["input"].(map[string]any)
					if got := int(window["total_chars"].(float64)); got != len([]rune(text)) {
						t.Fatalf("len=%d total_chars=%d want %d", length, got, len([]rune(text)))
					}
					chunk := window["text"].(string)
					if count := len([]rune(chunk)); count == 0 || count > length {
						t.Fatalf("len=%d offset=%d: %d runes", length, offset, count)
					}
					collected.WriteString(chunk)
					offset += len([]rune(chunk))
					if window["next_offset"] == nil {
						break
					}
					if next := int(window["next_offset"].(float64)); next != offset {
						t.Fatalf("len=%d next_offset=%d want %d", length, next, offset)
					}
				}
				if collected.String() != text || offset != len([]rune(text)) {
					t.Fatalf("len=%d: reassembled bytes differ, %d/%d runes", length, offset, len([]rune(text)))
				}
			}
			if script.name == "English" {
				path := "/search?q=ordinary+english+words"
				before := request(t, handler, http.MethodGet, path, "", "", nil).Body.String()
				hits := agentGet(t, handler, path+"&format=json")
				items := hits["items"].([]any)
				if len(items) != 1 || items[0].(map[string]any)["span_id"] != spanID || !strings.Contains(before, "ordinary english words") {
					t.Fatalf("English search: HTML=%q JSON=%v", before, items)
				}
				if after := request(t, handler, http.MethodGet, path, "", "", nil).Body.String(); after != before {
					t.Fatal("English HTML search changed after JSON search")
				}
			}
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
		{"/", []string{"<h1>Traces</h1>", "trace-table", traceID}},
		{"/sessions", []string{"<h1>Sessions</h1>", "session-00"}},
		{"/search?q=needle", []string{"<h1>Search</h1>", "Results for “needle”", traceID}},
		{"/traces/" + traceID, []string{"Span tree", "span-detail", spanID}},
		{"/spans/" + traceID + "/" + spanID, []string{"span-detail", "<h3>Input</h3>", "private-body"}},
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

func TestAgentJSONAuth(t *testing.T) {
	handler, _ := newTestHandler(t, "secret")
	for _, path := range []string{"/?format=json", "/sessions?format=json", "/search?format=json", "/traces/" + strings.Repeat("0", 32) + "?format=json", "/spans/" + strings.Repeat("0", 32) + "/" + strings.Repeat("0", 16) + "?format=json"} {
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
}
