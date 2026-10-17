package proxy

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/otlp"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
	"github.com/klauspost/compress/zstd"
)

func testToolSpans(h *Handler, exchange Exchange, chat otlp.RawSpan) []otlp.RawSpan {
	request, response := decodeExchange(exchange)
	return h.toolSpans(exchange, chat, request, response)
}

func TestToolExtractionUsesDecodedMessages(t *testing.T) {
	h, err := New(Config{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	issue := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, EndNs: 200, RequestBody: []byte("{}"), ResponseBody: []byte(`{"id":"issue","output":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"}]}`)}
	request, response := decodeExchange(issue)
	chat, err := parseExchange(issue, "", request, response)
	if err != nil {
		t.Fatal(err)
	}
	issue.RequestBody, issue.ResponseBody = []byte("invalid"), []byte("invalid")
	chat.Attrs["gen_ai.output.messages"] = "invalid"
	h.toolSpans(issue, chat, request, response)
	result := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, StartNs: 800, RequestBody: []byte(`{"input":[{"type":"function_call_output","call_id":"a","output":"ok"}]}`)}
	request, response = decodeExchange(result)
	chat, err = parseExchange(result, "", request, response)
	if err != nil {
		t.Fatal(err)
	}
	result.RequestBody = []byte("invalid")
	spans := h.toolSpans(result, chat, request, response)
	if len(spans) != 1 || spans[0].ParentSpanID == "" || spans[0].Name != "execute_tool bash" || spans[0].Attrs["gen_ai.tool.call.result"] != "ok" {
		t.Fatalf("decoded data was not reused: %+v", spans)
	}
}

func BenchmarkParseAndToolsNoTools8MiB(b *testing.B) {
	large := strings.Repeat("x", 8<<20)
	for _, side := range []string{"request", "response"} {
		b.Run(side, func(b *testing.B) {
			exchange := Exchange{Vendor: "openai", Path: "/v1/chat/completions", StatusCode: 200, RequestBody: []byte(`{"messages":[{"role":"user","content":"small"}]}`), ResponseBody: []byte(`{"id":"reply","choices":[{"message":{"role":"assistant","content":"small"}}]}`)}
			if side == "request" {
				exchange.RequestBody = []byte(`{"messages":[{"role":"user","content":"` + large + `"}]}`)
			} else {
				exchange.ResponseBody = []byte(`{"id":"reply","choices":[{"message":{"role":"assistant","content":"` + large + `"}}]}`)
			}
			h, err := New(Config{Logf: b.Logf})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(8 << 20)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				request, response := decodeExchange(exchange)
				chat, err := parseExchange(exchange, "", request, response)
				if err != nil {
					b.Fatal(err)
				}
				h.toolSpans(exchange, chat, request, response)
			}
		})
	}
}

func TestCaptureToolSpans(t *testing.T) {
	tests := []struct {
		name, vendor, path, response, history string
	}{
		{"anthropic", "anthropic", "/v1/messages", `{"id":"issuing","content":[{"type":"tool_use","id":"a","name":"bash","input":{"cmd":"ls"}}]}`, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"bash","input":{"cmd":"ls"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"file.txt"}]}]}`},
		{"anthropic stream", "anthropic", "/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"id\":\"issuing\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"a\",\"name\":\"bash\",\"input\":{}}}\n\n", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"file.txt"}]}]}`},
		{"openai", "openai", "/v1/chat/completions", `{"id":"issuing","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{}"}}]}}]}`, `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"file.txt"}]}`},
		{"openai stream", "openai", "/v1/chat/completions", "data: {\"id\":\"issuing\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"bash\",\"arguments\":\"{}\"}}]}}]}\n\n", `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"file.txt"}]}`},
		{"responses", "openai", "/v1/responses", `{"id":"issuing","output":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"}]}`, `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`},
		{"responses output only", "openai", "/v1/responses", `{"id":"issuing","output":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"}]}`, `{"previous_response_id":"issuing","input":[{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`},
		{"chatgpt output only", "chatgpt", "/codex/responses", `{"id":"issuing","output":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"}]}`, `{"previous_response_id":"issuing","input":[{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`},
		{"gemini", "gemini", "/v1beta/models/test:generateContent", `{"responseId":"issuing","candidates":[{"content":{"parts":[{"functionCall":{"name":"bash","args":{}}}]}}]}`, `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"bash","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"out":"file.txt"}}}]}]}`},
		{"gemini stream", "gemini", "/v1beta/models/test:streamGenerateContent", "data: {\"responseId\":\"issuing\",\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"bash\",\"args\":{}}}]}}]}\n\n", `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"bash","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"out":"file.txt"}}}]}]}`},
		{"responses stream", "chatgpt", "/codex/responses", "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"issuing\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"a\",\"name\":\"bash\",\"arguments\":\"{}\"}]}}\n\n", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			h, err := New(Config{Store: db, Logf: t.Logf})
			if err != nil {
				t.Fatal(err)
			}
			request := `{}`
			if strings.Contains(tt.name, "stream") {
				request = `{"stream":true}`
			}
			issuing := Exchange{Vendor: tt.vendor, Path: tt.path, RequestBody: []byte(request), ResponseBody: []byte(tt.response), StatusCode: 200, StartNs: 100, EndNs: 200}
			h.capture(issuing)
			chat, err := Parse(issuing, "")
			if err != nil {
				t.Fatal(err)
			}
			result := Exchange{Vendor: tt.vendor, Path: tt.path, RequestBody: []byte(tt.history), ResponseBody: []byte(`{"id":"final"}`), StatusCode: 200, StartNs: 800, EndNs: 900}
			h.capture(result)
			result.StartNs, result.EndNs = 1600, 1700
			h.capture(result) // A replay must not extend the first observed tool duration.
			var count int
			if err := db.Reader().QueryRow(`SELECT count(*) FROM spans WHERE kind='tool'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("tool count=%d err=%v", count, err)
			}
			var traceID, parentID, name, callID, input, output string
			var start, end int64
			if err := db.Reader().QueryRow(`SELECT trace_id, parent_span_id, name, tool_call_id, start_ns, end_ns, input_content, output_content FROM spans WHERE kind='tool'`).Scan(&traceID, &parentID, &name, &callID, &start, &end, &input, &output); err != nil {
				t.Fatal(err)
			}
			wantID := "a"
			if tt.vendor == "gemini" {
				wantID = geminiToolKey("bash", 0, 0)
			}
			if traceID != chat.TraceID || parentID != chat.SpanID || name != "execute_tool bash" || callID != wantID || start != 200 || end != 800 || input == "" || !strings.Contains(output, "file.txt") {
				t.Fatalf("tool: trace=%s parent=%s name=%s call=%s time=%d..%d input=%s output=%s", traceID, parentID, name, callID, start, end, input, output)
			}
		})
	}
}

func TestCaptureScopedTools(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(Config{Store: db, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	for _, vendor := range []string{"openai", "anthropic"} {
		for _, session := range []string{"one", "two"} {
			for _, header := range []bool{false, true} {
				body, response := `{"messages":[]}`, `{"id":"origin","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{}"}}]}}]}`
				history := `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"ok"}]}`
				path := "/v1/chat/completions"
				if vendor == "anthropic" {
					path = "/v1/messages"
					response = `{"id":"origin","content":[{"type":"tool_use","id":"a","name":"bash","input":{}}]}`
					history = `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}]}`
				}
				headers := http.Header{}
				scope := session
				if header {
					scope += "-header"
					headers.Set("X-Spanbox-Session", scope)
				}
				body = strings.TrimSuffix(body, "}") + fmt.Sprintf(`,"session_id":%q}`, session)
				history = strings.TrimSuffix(history, "}") + fmt.Sprintf(`,"session_id":%q}`, session)
				h.capture(Exchange{Vendor: vendor, Path: path, RequestHeaders: headers, RequestBody: []byte(body), ResponseBody: []byte(response), StatusCode: 200, StartNs: 100, EndNs: 200})
				h.capture(Exchange{Vendor: vendor, Path: path, RequestHeaders: headers, RequestBody: []byte(history), ResponseBody: []byte(`{"id":"final"}`), StatusCode: 200, StartNs: 800, EndNs: 900})
				var parent string
				if err := db.Reader().QueryRow("SELECT parent_span_id FROM spans WHERE kind='tool' AND provider=? AND session_id=?", vendor, scope).Scan(&parent); err != nil || parent == "" {
					t.Fatalf("%s/%s parent=%s err=%v", vendor, scope, parent, err)
				}
			}
		}
	}
	var count, identities int
	if err := db.Reader().QueryRow("SELECT count(*),count(DISTINCT span_id) FROM spans WHERE kind='tool'").Scan(&count, &identities); err != nil || count != 8 || identities != 8 {
		t.Fatalf("count=%d identities=%d err=%v", count, identities, err)
	}
}

func TestGeminiIssuingTurns(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(Config{Store: db, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	user := map[string]any{"role": "user", "parts": []any{map[string]any{"text": "run twice"}}}
	model := map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": "bash", "args": map[string]any{"cmd": "ls"}}}}}
	result := map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "bash", "response": map[string]any{"out": "ok"}}}}}
	contents := []any{user}
	exchange := Exchange{Vendor: "gemini", Path: "/v1beta/models/test:generateContent", RequestHeaders: http.Header{"X-Spanbox-Session": {"same"}}, StatusCode: 200, StartNs: 100, EndNs: 200}
	for turn := 0; turn < 2; turn++ {
		exchange.RequestBody = []byte(jsonString(map[string]any{"contents": contents}))
		exchange.ResponseBody = []byte(jsonString(map[string]any{"responseId": fmt.Sprintf("issue_%d", turn), "candidates": []any{map[string]any{"content": model}}}))
		h.capture(exchange)
		contents = append(contents, model, result)
		exchange.StartNs, exchange.EndNs = exchange.EndNs+100, exchange.EndNs+200
		exchange.RequestBody = []byte(jsonString(map[string]any{"contents": contents}))
		exchange.ResponseBody = []byte(fmt.Sprintf(`{"responseId":"result_%d"}`, turn))
		h.capture(exchange)
		exchange.StartNs, exchange.EndNs = exchange.EndNs+100, exchange.EndNs+200
	}
	var count, ids, parents int
	if err := db.Reader().QueryRow("SELECT count(*),count(DISTINCT span_id),count(DISTINCT parent_span_id) FROM spans WHERE kind='tool' AND parent_span_id<>''").Scan(&count, &ids, &parents); err != nil || count != 2 || ids != 2 || parents != 2 {
		t.Fatalf("executions=%d ids=%d parents=%d err=%v", count, ids, parents, err)
	}
}

func TestGeminiNativeIDsSurvivePrunedHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			h, err := New(Config{Logf: t.Logf})
			if err != nil {
				t.Fatal(err)
			}
			issue := Exchange{Vendor: "gemini", Path: "/v1beta/models/test:generateContent", StatusCode: 200, RequestHeaders: http.Header{"X-Spanbox-Session": {"native"}}, EndNs: 200, RequestBody: []byte(`{"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"model","parts":[{"text":"old reply"}]},{"role":"user","parts":[{"text":"run"}]}]}`), ResponseBody: []byte(`{"responseId":"issuing","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-a","name":"bash","args":{"cmd":"one"}}},{"functionCall":{"id":"call-b","name":"bash","args":{"cmd":"two"}}}]}}]}`)}
			if stream {
				issue.Path = "/v1beta/models/test:streamGenerateContent"
				issue.ResponseBody = []byte("data: " + string(issue.ResponseBody) + "\n\n")
			}
			chat, err := Parse(issue, "")
			if err != nil {
				t.Fatal(err)
			}
			testToolSpans(h, issue, chat)
			// All issuing history is gone; only native IDs can recover the right arguments.
			result := Exchange{Vendor: "gemini", Path: "/v1beta/models/test:generateContent", StatusCode: 200, RequestHeaders: issue.RequestHeaders, StartNs: 800, RequestBody: []byte(`{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"call-b","name":"bash","response":{"out":"second"}}},{"functionResponse":{"id":"call-a","name":"bash","response":{"out":"first"}}}]}]}`)}
			raw, err := Parse(result, "")
			if err != nil {
				t.Fatal(err)
			}
			spans := testToolSpans(h, result, raw)
			if len(spans) != 2 {
				t.Fatalf("spans=%+v", spans)
			}
			for i, want := range []struct{ id, cmd string }{{"call-b", "two"}, {"call-a", "one"}} {
				span := spans[i]
				if span.Attrs["gen_ai.tool.call.id"] != want.id || jsonString(span.Attrs["gen_ai.tool.call.arguments"]) != jsonString(map[string]any{"cmd": want.cmd}) || span.TraceID != chat.TraceID || span.ParentSpanID != chat.SpanID || span.StartNs != 200 || span.EndNs != 800 {
					t.Fatalf("native/pruned call %d: %+v", i, span)
				}
			}
			// A later retry with full history must still address those same executions.
			request := decodeObject(issue.RequestBody)
			contents := array(request["contents"])
			contents = append(contents, map[string]any{"role": "model", "parts": array(parseResponse("gemini", stream, issue.ResponseBody).outputMessages)[0].(map[string]any)["parts"]})
			contents = append(contents, array(decodeObject(result.RequestBody)["contents"])...)
			result.RequestBody = []byte(jsonString(map[string]any{"contents": contents}))
			result.StartNs = 1600
			raw, err = Parse(result, "")
			if err != nil {
				t.Fatal(err)
			}
			replayed := testToolSpans(h, result, raw)
			if len(replayed) != len(spans) {
				t.Fatalf("replayed spans=%+v", replayed)
			}
			for i := range spans {
				if replayed[i].SpanID != spans[i].SpanID || replayed[i].ParentSpanID != spans[i].ParentSpanID || replayed[i].StartNs != spans[i].StartNs || replayed[i].EndNs != spans[i].EndNs {
					t.Fatal("native retry changed identity, parent, or first timing")
				}
			}
		})
	}
}

func TestCompatToolScopes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("function_call_output")) {
			fmt.Fprint(w, `{"id":"final"}`)
			return
		}
		name := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
		fmt.Fprintf(w, `{"id":"same_reply","output":[{"type":"function_call","call_id":"a","name":%q,"arguments":"{}"}]}`, name)
	}))
	defer upstream.Close()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := New(Config{Store: db, Logf: t.Logf, OpenAICompatUpstreams: "ua=" + upstream.URL + "/ua,ub=" + upstream.URL + "/ub"})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"ua", "ub"} {
		for _, body := range []string{`{"model":"test","input":"run"}`, `{"model":"test","input":[{"type":"function_call_output","call_id":"a","output":"ok"}]}`} {
			r := httptest.NewRequest(http.MethodPost, "/proxy/openai-compat/"+route+"/responses", strings.NewReader(body))
			r.Header.Set("X-Spanbox-Session", "same")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			h.Wait()
			if w.Code != 200 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		}
	}
	var count, ids, parents, names int
	if err := db.Reader().QueryRow("SELECT count(*),count(DISTINCT span_id),count(DISTINCT parent_span_id),count(DISTINCT tool_name) FROM spans WHERE kind='tool' AND parent_span_id<>''").Scan(&count, &ids, &parents, &names); err != nil || count != 2 || ids != 2 || parents != 2 || names != 2 {
		t.Fatalf("tools=%d ids=%d parents=%d names=%d err=%v", count, ids, parents, names, err)
	}
}

func TestToolCacheOverlappingStream(t *testing.T) {
	h, err := New(Config{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	issue := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, EndNs: 1000, ResponseBody: []byte(`{"id":"issuing","output":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"}]}`)}
	chat, err := Parse(issue, "")
	if err != nil {
		t.Fatal(err)
	}
	testToolSpans(h, issue, chat)
	result := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, StartNs: 800, RequestBody: []byte(`{"input":[{"type":"function_call_output","call_id":"a","output":"ok"}]}`)}
	for _, start := range []int64{800, 1600} {
		result.StartNs = start
		raw, err := Parse(result, "")
		if err != nil {
			t.Fatal(err)
		}
		spans := testToolSpans(h, result, raw)
		if len(spans) != 1 || spans[0].ParentSpanID != chat.SpanID || spans[0].StartNs != 800 || spans[0].EndNs != 800 {
			t.Fatalf("overlap/retry=%+v", spans)
		}
	}
}

func TestToolCacheFIFOAndFirstResult(t *testing.T) {
	h, err := New(Config{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxToolCalls; i++ {
		exchange := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, EndNs: int64(i + 1), ResponseBody: []byte(fmt.Sprintf(`{"id":"reply_%d","output":[{"type":"function_call","call_id":"call_%d","name":"bash","arguments":"{}"}]}`, i, i))}
		chat, err := Parse(exchange, "")
		if err != nil {
			t.Fatal(err)
		}
		testToolSpans(h, exchange, chat)
	}
	if len(h.toolCalls) != maxToolCalls || len(h.toolOrder) != maxToolCalls {
		t.Fatalf("cache=%d order=%d", len(h.toolCalls), len(h.toolOrder))
	}
	result := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, StartNs: 8000, EndNs: 8100, RequestBody: []byte(`{"input":[{"type":"function_call","call_id":"call_0","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"call_0","output":"ok"}]}`)}
	chat, err := Parse(result, "")
	if err != nil {
		t.Fatal(err)
	}
	orphan := testToolSpans(h, result, chat)
	if len(orphan) != 1 || orphan[0].ParentSpanID != "" || orphan[0].StartNs != orphan[0].EndNs {
		t.Fatalf("oldest call was not evicted: %+v", orphan)
	}
	result.RequestBody = []byte(`{"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	chat, err = Parse(result, "")
	if err != nil {
		t.Fatal(err)
	}
	first := testToolSpans(h, result, chat)
	if len(first) != 1 || first[0].ParentSpanID == "" || first[0].Name != "execute_tool bash" || first[0].StartNs != 2 || first[0].EndNs != 8000 || first[0].Attrs["gen_ai.tool.call.arguments"] != "{}" {
		t.Fatalf("enriched span=%+v", first)
	}
	// Replaying the issuing response must retain its parent, time, and FIFO position.
	issue := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, EndNs: 9999, ResponseBody: []byte(`{"id":"replayed","output":[{"type":"function_call","call_id":"call_1","name":"changed","arguments":"changed"}]}`)}
	replayed, err := Parse(issue, "")
	if err != nil {
		t.Fatal(err)
	}
	testToolSpans(h, issue, replayed)
	result.StartNs = 10000
	chat.StartNs = result.StartNs
	again := testToolSpans(h, result, chat)
	if len(again) != 1 || again[0].TraceID != first[0].TraceID || again[0].ParentSpanID != first[0].ParentSpanID || again[0].StartNs != first[0].StartNs || again[0].EndNs != first[0].EndNs || again[0].SpanID != first[0].SpanID || again[0].Name != first[0].Name {
		t.Fatalf("replay changed first observation: %+v -> %+v", first, again)
	}
	// Zero is a valid first result timestamp, not an unset sentinel.
	zero, _ := New(Config{Logf: t.Logf})
	issue.EndNs = 0
	testToolSpans(zero, issue, replayed)
	result.StartNs, chat.StartNs = 0, 0
	first = testToolSpans(zero, result, chat)
	chat.StartNs = 10
	again = testToolSpans(zero, result, chat)
	if first[0].EndNs != 0 || again[0].EndNs != 0 {
		t.Fatal("zero first result time was overwritten")
	}
}

func TestToolCacheConcurrent(t *testing.T) {
	h, err := New(Config{Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			exchange := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, EndNs: 200, ResponseBody: []byte(fmt.Sprintf(`{"id":"reply_%d","output":[{"type":"function_call","call_id":"call_%d","name":"bash","arguments":"{}"}]}`, i, i))}
			chat, err := Parse(exchange, "")
			if err != nil {
				t.Error(err)
				return
			}
			testToolSpans(h, exchange, chat)
			exchange.StartNs, chat.StartNs = 800, 800
			exchange.RequestBody = []byte(fmt.Sprintf(`{"input":[{"type":"function_call_output","call_id":"call_%d","output":"ok"}]}`, i))
			first := testToolSpans(h, exchange, chat)
			exchange.StartNs, chat.StartNs = 1600, 1600
			again := testToolSpans(h, exchange, chat)
			if len(first) != 1 || len(again) != 1 || first[0].ParentSpanID != chat.SpanID || first[0].EndNs != 800 || again[0].EndNs != 800 {
				t.Errorf("first=%+v replay=%+v", first, again)
			}
		}(i)
	}
	wg.Wait()
}

func TestToolCacheMissIsStateless(t *testing.T) {
	exchange := Exchange{Vendor: "openai", Path: "/v1/responses", StatusCode: 200, StartNs: 800, RequestBody: []byte(`{"input":[{"type":"function_call_output","call_id":"missing","output":"ok"}]}`)}
	chat, err := Parse(exchange, "")
	if err != nil {
		t.Fatal(err)
	}
	var previousTrace, previousID string
	for i := 0; i < 2; i++ {
		h, err := New(Config{Logf: t.Logf})
		if err != nil {
			t.Fatal(err)
		}
		spans := testToolSpans(h, exchange, chat)
		if len(spans) != 1 || spans[0].Name != "execute_tool" || spans[0].ParentSpanID != "" || spans[0].TraceID == chat.TraceID || spans[0].StartNs != 800 || spans[0].EndNs != 800 {
			t.Fatalf("orphan=%+v", spans)
		}
		if i == 1 && (spans[0].SpanID != previousID || spans[0].TraceID != previousTrace) {
			t.Fatal("identity changed after cache loss")
		}
		previousTrace, previousID = spans[0].TraceID, spans[0].SpanID
	}
}

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

func TestRelayCapturesLargeResponsesCompleted(t *testing.T) {
	line := `data: {"type":"response.completed","response":{"id":"large-response","model":"gpt-5-mini","status":"completed","usage":{"input_tokens":123,"output_tokens":45},"padding":"` + strings.Repeat("x", 1<<20) + "\"}}\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, line)
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, Store: database, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/proxy/openai/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","stream":true}`)))
	handler.Wait()
	var input, output *int64
	if err := database.Reader().QueryRow("SELECT input_tokens, output_tokens FROM spans").Scan(&input, &output); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || input == nil || *input != 123 || output == nil || *output != 45 {
		t.Fatalf("status=%d usage=%v/%v", response.Code, input, output)
	}
}

func TestZstdDecodeAllocations(t *testing.T) {
	body := fixture(t, "chatgpt_request.json.zst")
	header := http.Header{"Content-Encoding": {"zstd"}}
	allocs := testing.AllocsPerRun(10, func() {
		if _, err := decodeRequestBody(header, body); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 10 {
		t.Fatalf("zstd decode allocations = %g, want <= 10", allocs)
	}
}

func TestZstdDecodeRejectsOversizedAndInvalid(t *testing.T) {
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	header := http.Header{"Content-Encoding": {"zstd"}}
	oversized := encoder.EncodeAll(bytes.Repeat([]byte("x"), maxBodyBytes+1), nil)
	if _, err := decodeRequestBody(header, oversized); err == nil {
		t.Fatal("oversized decoded body accepted")
	}
	if _, err := decodeRequestBody(header, []byte("not zstd")); err == nil {
		t.Fatal("invalid zstd body accepted")
	}
}

func TestConcurrentZstdRequests(t *testing.T) {
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
	body := fixture(t, "chatgpt_request.json.zst")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/proxy/chatgpt/codex/responses", bytes.NewReader(body))
			req.Header.Set("Content-Encoding", "zstd")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Errorf("status=%d", response.Code)
			}
		}()
	}
	wg.Wait()
	handler.Wait()
	var input int64
	if err := database.Reader().QueryRow("SELECT input_tokens FROM spans").Scan(&input); err != nil || input != 374 {
		t.Fatalf("stored input=%d err=%v", input, err)
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

func TestClientResponseCompletesWhenCaptureSlotsAreFull(t *testing.T) {
	const body = `{"id":"response_1","output":[]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var mu sync.Mutex
	var logs []string
	handler, err := New(Config{OpenAIUpstream: upstream.URL, Store: database, Logf: func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range cap(handler.captureSlots) {
		handler.captureSlots <- struct{}{}
	}
	releaseSlots := func() {
		for range cap(handler.captureSlots) {
			select {
			case <-handler.captureSlots:
			default:
			}
		}
	}
	proxy := httptest.NewServer(handler)
	defer func() {
		releaseSlots()
		proxy.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, proxy.URL+"/proxy/openai/v1/responses", strings.NewReader(`{"model":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := proxy.Client().Do(req)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("client still waiting after %s: %v", elapsed, err)
	}
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("client still waiting after %s: %v", time.Since(started), err)
	}
	if response.StatusCode != http.StatusOK || string(got) != body || elapsed > 500*time.Millisecond {
		t.Fatalf("status=%d elapsed=%s body=%q", response.StatusCode, elapsed, got)
	}
	handler.Wait()
	var count int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != 0 || !strings.Contains(strings.Join(logs, "\n"), "proxy captures dropped=1") {
		t.Fatalf("spans=%d logs=%q", count, logs)
	}
}

func TestWaitDrainsInFlightCapture(t *testing.T) {
	dir := t.TempDir() + "/data"
	database, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	lock, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "spanbox.db"))+"?_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{Store: database, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	handler.captureAsync(Exchange{
		Vendor: "anthropic", Path: "/v1/messages", StatusCode: http.StatusOK,
		RequestHeaders: make(http.Header), RequestBody: fixture(t, "anthropic_request.json"),
		ResponseBody: fixture(t, "anthropic_stream.txt"), StartNs: 1, EndNs: 2,
	})
	drained := make(chan struct{})
	go func() {
		handler.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("Wait returned while the capture insert was blocked")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := lock.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after the capture could finish")
	}
	var count int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count == 0 {
		t.Fatalf("in-flight capture not stored: count=%d err=%v", count, err)
	}
}

func TestUpstreamIdleTimeoutAfterHeaders(t *testing.T) {
	const partial = "partial-body"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(partial))
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
	}))
	defer upstream.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, IdleTimeout: 30 * time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/proxy/openai/v1/responses", strings.NewReader(`{"model":"test"}`)))
	elapsed := time.Since(started)
	if response.Code != http.StatusOK || response.Body.String() != partial || strings.Contains(response.Body.String(), "upstream idle timeout") || elapsed >= 300*time.Millisecond {
		t.Fatalf("status=%d elapsed=%s body=%q", response.Code, elapsed, response.Body.String())
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

func TestDroppedCaptureStillRecordsToolOrigin(t *testing.T) {
	const issuing = `{"id":"issuing","output":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "function_call_output") {
			_, _ = w.Write([]byte(`{"id":"final","output":[]}`))
			return
		}
		_, _ = w.Write([]byte(issuing))
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler, err := New(Config{OpenAIUpstream: upstream.URL, Store: database, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	send := func(body string) {
		t.Helper()
		response, err := proxy.Client().Post(proxy.URL+"/proxy/openai/v1/responses", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	for range cap(handler.captureSlots) {
		handler.captureSlots <- struct{}{}
	}
	send(`{"model":"test"}`)
	for range cap(handler.captureSlots) {
		<-handler.captureSlots
	}
	if handler.DroppedCaptures() != 1 {
		t.Fatalf("dropped=%d", handler.DroppedCaptures())
	}
	send(`{"previous_response_id":"issuing","input":[{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`)
	handler.Wait()
	chat, err := Parse(Exchange{Vendor: "openai", Path: "/v1/responses", ServerAddress: handler.endpoints["openai"].base.Hostname(), RequestBody: []byte(`{"model":"test"}`), ResponseBody: []byte(issuing), StatusCode: 200}, "")
	if err != nil {
		t.Fatal(err)
	}
	var traceID, parentID, name string
	if err := database.Reader().QueryRow(`SELECT trace_id, parent_span_id, name FROM spans WHERE kind='tool'`).Scan(&traceID, &parentID, &name); err != nil {
		t.Fatal(err)
	}
	if traceID != chat.TraceID || parentID != chat.SpanID || name != "execute_tool bash" {
		t.Fatalf("tool: trace=%s parent=%s name=%s, want trace=%s parent=%s", traceID, parentID, name, chat.TraceID, chat.SpanID)
	}
}
