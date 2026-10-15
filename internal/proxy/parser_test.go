package proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/otlp"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func attrInt(t *testing.T, attrs map[string]any, key string, want int64) {
	t.Helper()
	got, ok := attrs[key].(int64)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %d", key, attrs[key], want)
	}
}

func parseTools(t *testing.T, value any) struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
} {
	t.Helper()
	var got struct {
		Count int      `json:"count"`
		Names []string `json:"names"`
	}
	text, ok := value.(string)
	if !ok || json.Unmarshal([]byte(text), &got) != nil {
		t.Fatalf("invalid tool summary: %#v", value)
	}
	return got
}

func toolResultSpans(exchange Exchange, chat otlp.RawSpan) []otlp.RawSpan {
	var spans []otlp.RawSpan
	for _, result := range toolResults(exchange, decodeObject(exchange.RequestBody)) {
		spans = append(spans, toolSpan(exchange.route(), result, chat))
	}
	return spans
}

func TestToolResultSpans(t *testing.T) {
	tests := []struct {
		name, vendor, path, body string
		want                     []string
	}{
		{"anthropic parallel and error", "anthropic", "/v1/messages", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"bash","input":{"cmd":"ls"}},{"type":"tool_use","id":"b","name":"edit","input":{}}]},{"role":"user","content":[{"type":"text","text":"not a result"},{"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"file.txt"}]},{"type":"tool_result","tool_use_id":"b","is_error":true,"content":"boom"}]}]}`, []string{"a", "b"}},
		{"openai parallel", "openai", "/v1/chat/completions", `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}},{"id":"b","function":{"name":"edit","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"file.txt"},{"role":"tool","tool_call_id":"b","content":"ok"}]}`, []string{"a", "b"}},
		{"responses", "openai", "/v1/responses", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{\"cmd\":\"ls\"}"},{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`, []string{"a"}},
		{"chatgpt responses", "chatgpt", "/codex/responses", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`, []string{"a"}},
		{"old anthropic result", "anthropic", "/v1/messages", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"old"}]},{"role":"assistant","content":"done"},{"role":"user","content":"thanks"}]}`, nil},
		{"old openai result", "openai", "/v1/chat/completions", `{"messages":[{"role":"assistant","tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"old"},{"role":"assistant","content":"done"}]}`, nil},
		{"old responses result", "openai", "/v1/responses", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"old"},{"role":"assistant","content":"done"}]}`, nil},
		{"responses next call", "openai", "/v1/responses", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"old"},{"type":"function_call","call_id":"b","name":"edit","arguments":"{}"},{"type":"function_call_output","call_id":"b","output":"ok"}]}`, []string{"b"}},
		{"missing call", "openai", "/v1/chat/completions", `{"messages":[{"role":"tool","tool_call_id":"missing","content":"ok"}]}`, []string{"missing"}},
		{"missing id", "anthropic", "/v1/messages", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"bash","input":{}}]},{"role":"user","content":[{"type":"tool_result","content":"ok"}]}]}`, nil},
		{"duplicate result", "openai", "/v1/responses", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file.txt"},{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`, []string{"a"}},
		{"gemini skipped", "gemini", "/v1/responses", `{"input":[{"type":"function_call","call_id":"a","name":"bash","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"file.txt"}]}`, nil},
		{"malformed input", "anthropic", "/v1/messages", `{`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exchange := Exchange{Vendor: tt.vendor, Path: tt.path, RequestBody: []byte(tt.body), StartNs: 100, EndNs: 200, StatusCode: 200}
			chat, err := Parse(exchange, "test")
			if err != nil {
				t.Fatal(err)
			}
			spans := toolResultSpans(exchange, chat)
			var ids []string
			for _, span := range spans {
				id := text(span.Attrs["gen_ai.tool.call.id"])
				ids = append(ids, id)
				if span.Name != strings.TrimSpace("execute_tool "+text(span.Attrs["gen_ai.tool.name"])) || span.Attrs["gen_ai.operation.name"] != "execute_tool" {
					t.Fatalf("name/operation: %+v", span)
				}
				if span.SpanID == "" || span.TraceID == "" || span.TraceID == chat.TraceID || span.ParentSpanID != "" || span.StartNs != 100 || span.EndNs != 100 {
					t.Fatalf("orphan identity/timing: %+v", span)
				}
				if _, ok := span.Attrs["gen_ai.tool.call.arguments"]; !ok {
					t.Fatal("missing arguments")
				}
				if _, ok := span.Attrs["gen_ai.tool.call.result"]; !ok {
					t.Fatal("missing result")
				}
				if !reflect.DeepEqual(span.Resource, chat.Resource) || !reflect.DeepEqual(span.Scope, chat.Scope) {
					t.Fatal("tool span lost resource or scope")
				}
				if tt.name == "anthropic parallel and error" {
					if id == "a" && (!reflect.DeepEqual(span.Attrs["gen_ai.tool.call.arguments"], map[string]any{"cmd": "ls"}) || !strings.Contains(jsonString(span.Attrs["gen_ai.tool.call.result"]), "file.txt")) {
						t.Fatalf("arguments/result: %+v", span.Attrs)
					}
					if id == "b" && span.StatusCode != 2 {
						t.Fatalf("error status = %d", span.StatusCode)
					}
				} else if span.StatusCode != 0 {
					t.Fatalf("unexpected error status = %d", span.StatusCode)
				}
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Fatalf("call ids = %v, want %v", ids, tt.want)
			}
			replayed := toolResultSpans(exchange, chat)
			for i := range spans {
				if spans[i].SpanID != replayed[i].SpanID || spans[i].TraceID != replayed[i].TraceID {
					t.Fatal("replay changed tool span identity")
				}
			}
		})
	}
}

func TestGeminiToolResults(t *testing.T) {
	exchange := Exchange{Vendor: "gemini", Path: "/v1beta/models/test:generateContent", StatusCode: 200, StartNs: 100, EndNs: 200, RequestBody: []byte(`{"contents":[{"role":"user","parts":[{"text":"run twice"}]},{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"cmd":"one"}}},{"functionCall":{"name":"bash","args":{"cmd":"two"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"bash","response":{"out":"first"}}},{"functionResponse":{"name":"bash","response":{"out":"second"}}}]}]}`)}
	chat, err := Parse(exchange, "test")
	if err != nil {
		t.Fatal(err)
	}
	spans := toolResultSpans(exchange, chat)
	if len(spans) != 2 {
		t.Fatalf("spans=%+v", spans)
	}
	for i, want := range []string{"one", "two"} {
		if !strings.Contains(jsonString(spans[i].Attrs["gen_ai.tool.call.arguments"]), want) || spans[i].Attrs["gen_ai.tool.call.id"] == "" {
			t.Fatalf("call %d = %+v", i, spans[i])
		}
	}
	if spans[0].SpanID == spans[1].SpanID {
		t.Fatal("repeated names collapsed")
	}
	replayed := toolResultSpans(exchange, chat)
	if replayed[0].SpanID != spans[0].SpanID || replayed[1].SpanID != spans[1].SpanID {
		t.Fatal("unstable Gemini identity")
	}
	// The issuing turn is part of identity; old results are skipped.
	request := decodeObject(exchange.RequestBody)
	contents := array(request["contents"])
	contents = append(contents, contents[1], contents[2])
	exchange.RequestBody = []byte(jsonString(map[string]any{"contents": contents}))
	next := toolResultSpans(exchange, chat)
	if len(next) != 2 || next[0].SpanID == spans[0].SpanID || next[1].SpanID == spans[1].SpanID {
		t.Fatalf("later turn identity = %+v", next)
	}
}

func TestGeminiNativeToolResults(t *testing.T) {
	exchange := Exchange{Vendor: "gemini", Path: "/v1beta/models/test:generateContent", StatusCode: 200, RequestBody: []byte(`{"contents":[{"role":"user","parts":[{"text":"run twice"}]},{"role":"model","parts":[{"functionCall":{"id":"call-a","name":"bash","args":{"cmd":"one"}}},{"functionCall":{"id":"call-b","name":"bash","args":{"cmd":"two"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-b","name":"bash","response":{"out":"second"}}},{"functionResponse":{"id":"call-a","name":"bash","response":{"out":"first"}}}]}]}`)}
	chat, err := Parse(exchange, "")
	if err != nil {
		t.Fatal(err)
	}
	spans := toolResultSpans(exchange, chat)
	if len(spans) != 2 {
		t.Fatalf("spans=%+v", spans)
	}
	for i, want := range []struct{ id, cmd, out string }{{"call-b", "two", "second"}, {"call-a", "one", "first"}} {
		attrs := spans[i].Attrs
		if attrs["gen_ai.tool.call.id"] != want.id || !reflect.DeepEqual(attrs["gen_ai.tool.call.arguments"], map[string]any{"cmd": want.cmd}) || !reflect.DeepEqual(attrs["gen_ai.tool.call.result"], map[string]any{"out": want.out}) {
			t.Fatalf("reordered call %d: %+v", i, attrs)
		}
	}
	if spans[0].SpanID == spans[1].SpanID {
		t.Fatal("native ids collapsed")
	}
	// Pruning earlier turns must not change a supplied ID or its span identity.
	request := decodeObject(exchange.RequestBody)
	request["contents"] = array(request["contents"])[1:]
	exchange.RequestBody = []byte(jsonString(request))
	pruned := toolResultSpans(exchange, chat)
	if len(pruned) != len(spans) {
		t.Fatalf("pruned spans=%+v", pruned)
	}
	for i := range spans {
		if pruned[i].SpanID != spans[i].SpanID || pruned[i].TraceID != spans[i].TraceID {
			t.Fatal("native identity changed after pruning history")
		}
	}
}

func TestGeminiToolErrors(t *testing.T) {
	for _, payload := range []string{`{"error":{"message":"boom"}}`, `{"error":"boom"}`, `{"error":null}`, `{"output":"ok"}`} {
		exchange := Exchange{Vendor: "gemini", Path: "/v1beta/models/test:generateContent", StatusCode: 200, RequestBody: []byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"bash","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"bash","response":` + payload + `}}]}]}`)}
		chat, err := Parse(exchange, "")
		if err != nil {
			t.Fatal(err)
		}
		spans := toolResultSpans(exchange, chat)
		if len(spans) != 1 || !reflect.DeepEqual(spans[0].Attrs["gen_ai.tool.call.result"], decodeObject([]byte(payload))) {
			t.Fatalf("result lost: %+v", spans)
		}
		want := int32(0)
		if strings.Contains(payload, "error") {
			want = 2
		}
		if spans[0].StatusCode != want {
			t.Fatalf("payload=%s status=%d want=%d", payload, spans[0].StatusCode, want)
		}
	}
}

func TestProxySessionScope(t *testing.T) {
	for _, body := range []string{`{"session_id":"native"}`, `{"conversation":"native"}`, `{"conversation":{"id":"native"}}`, `{"metadata":{"session_id":"native"}}`, `{"metadata":{"user_id":"{\"session_id\":\"native\"}"}}`} {
		for _, header := range []bool{false, true} {
			exchange := Exchange{Vendor: "anthropic", RequestBody: []byte(body), RequestHeaders: http.Header{}, StatusCode: 200}
			if header {
				exchange.RequestHeaders.Set("X-Spanbox-Session", "header")
			}
			span, err := Parse(exchange, "test")
			if err != nil || span.Attrs["gen_ai.conversation.id"] != "native" || header && span.Attrs["spanbox.session"] != "header" {
				t.Fatalf("body=%s header=%v session=%v err=%v", body, header, span.Attrs["gen_ai.conversation.id"], err)
			}
		}
	}
}

func TestParseVendorFixtures(t *testing.T) {
	anthropicRequest := fixture(t, "anthropic_request.json")
	geminiRequest := fixture(t, "gemini_request.json")
	tests := []struct {
		name     string
		exchange Exchange
		provider string
		model    string
		input    int64
		output   int64
		cache    int64
		reason   int64
		finish   string
		tools    int
		service  string
	}{
		{
			name: "Anthropic stream", provider: "anthropic", model: "claude-haiku-4-5-20251001", input: 55794, output: 81, cache: 0, reason: 73, finish: "end_turn", tools: 51, service: "claude-cli",
			exchange: Exchange{Vendor: "anthropic", Path: "/v1/messages", RequestBody: anthropicRequest, ResponseBody: fixture(t, "anthropic_stream.txt"), RequestHeaders: http.Header{"User-Agent": {"claude-cli/2.1.273 (external)"}, "Anthropic-Version": {"2023-06-01"}, "Authorization": {"Bearer never-store"}}, ResponseHeaders: http.Header{"Request-Id": {"req_test"}}, StatusCode: 200, StartNs: 1, EndNs: 2, TTFBMs: 12.5},
		},
		{
			name: "Gemini stream", provider: "gcp.gen_ai", model: "gemini-3.6-flash", input: 9386, output: 42, finish: "STOP", tools: 9, service: "GeminiCLI",
			exchange: Exchange{Vendor: "gemini", Path: "/v1beta/models/gemini-3.6-flash:streamGenerateContent", RequestBody: geminiRequest, ResponseBody: fixture(t, "gemini_stream.txt"), RequestHeaders: http.Header{"User-Agent": {"GeminiCLI/0.26.0/gemini-3.6-flash"}, "X-Gemini-Api-Privileged-User-Id": {"user-redacted"}, "X-Goog-Api-Key": {"never-store"}, "X-Goog-Api-Client": {"google-genai-sdk/1.30.0"}}, StatusCode: 200, StartNs: 3, EndNs: 4, TTFBMs: 20},
		},
		{
			name: "OpenAI chat stream", provider: "openai", model: "gpt-5-mini", input: 12, output: 8, cache: 3, reason: 2, finish: "tool_calls", tools: 1, service: "OpenAI",
			exchange: Exchange{Vendor: "openai", Path: "/v1/chat/completions", RequestBody: []byte(`{"model":"gpt-5-mini","messages":[{"role":"user","content":"test"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"user":"user-test","max_tokens":100,"temperature":0.2,"stream":true}`), ResponseBody: fixture(t, "openai_chat_stream.txt"), RequestHeaders: http.Header{"User-Agent": {"OpenAI/Python 2.0"}, "Authorization": {"Bearer never-store"}}, StatusCode: 200, StartNs: 5, EndNs: 6, TTFBMs: 30},
		},
		{
			name: "OpenAI responses stream", provider: "openai", model: "gpt-5-mini", input: 20, output: 9, cache: 4, reason: 3, finish: "completed", service: "codex_cli_rs",
			exchange: Exchange{Vendor: "openai", Path: "/v1/responses", RequestBody: []byte(`{"model":"gpt-5-mini","instructions":"Be concise.","input":[{"role":"user","content":"test"}],"stream":true}`), ResponseBody: fixture(t, "openai_responses_stream.txt"), RequestHeaders: http.Header{"User-Agent": {"codex_cli_rs/1.0"}}, StatusCode: 200, StartNs: 7, EndNs: 8, TTFBMs: 40},
		},
		{
			name: "ChatGPT responses stream", provider: "chatgpt", model: "gpt-6-astra", input: 374, output: 5, finish: "completed", service: "pi",
			exchange: Exchange{Vendor: "chatgpt", Path: "/codex/responses", ServerAddress: "chatgpt.com", RequestBody: fixture(t, "chatgpt_request.json"), ResponseBody: fixture(t, "chatgpt_responses_stream.txt"), RequestHeaders: http.Header{"User-Agent": {"pi/0.52.9"}}, StatusCode: 200, StartNs: 9, EndNs: 10, TTFBMs: 50},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Parse(tt.exchange, "v0.3.0")
			if err != nil {
				t.Fatal(err)
			}
			attrs := raw.Attrs
			if attrs["gen_ai.provider.name"] != tt.provider || attrs["gen_ai.request.model"] != tt.model || attrs["gen_ai.response.model"] != tt.model {
				t.Fatalf("provider/models: %#v", attrs)
			}
			attrInt(t, attrs, "gen_ai.usage.input_tokens", tt.input)
			attrInt(t, attrs, "gen_ai.usage.output_tokens", tt.output)
			if tt.cache > 0 {
				attrInt(t, attrs, "gen_ai.usage.cache_read.input_tokens", tt.cache)
			}
			if tt.reason > 0 {
				attrInt(t, attrs, "gen_ai.usage.reasoning.output_tokens", tt.reason)
			}
			if !strings.Contains(raw.Resource["service.name"].(string), tt.service) || raw.Scope["name"] != "spanbox/proxy" || raw.Scope["version"] != "v0.3.0" {
				t.Fatalf("resource=%#v scope=%#v", raw.Resource, raw.Scope)
			}
			finishes, ok := attrs["gen_ai.response.finish_reasons"].([]any)
			if !ok || len(finishes) != 1 || finishes[0] != tt.finish {
				t.Fatalf("finish reasons = %#v", attrs["gen_ai.response.finish_reasons"])
			}
			if tt.tools > 0 && parseTools(t, attrs["gen_ai.tool.definitions"]).Count != tt.tools {
				t.Fatalf("tool summary = %#v", attrs["gen_ai.tool.definitions"])
			}
			if attrs["spanbox.source"] != "proxy" || attrs["spanbox.proxy.stream"] != true || attrs["http.response.status_code"] != int64(200) || attrs["url.path"] != tt.exchange.Path || attrs["spanbox.proxy.ttfb_ms"] != tt.exchange.TTFBMs {
				t.Fatalf("proxy attrs = %#v", attrs)
			}
			encoded, _ := json.Marshal(raw)
			if strings.Contains(string(encoded), "never-store") {
				t.Fatal("credential leaked into raw span")
			}
			if attrs["gen_ai.input.messages"] == "" || attrs["gen_ai.output.messages"] == "" {
				t.Fatalf("missing content attrs: %#v", attrs)
			}
			if raw.Resource["service.version"] == "" || attrs["server.address"] == "" {
				t.Fatalf("missing service metadata: resource=%#v attrs=%#v", raw.Resource, attrs)
			}
			switch tt.name {
			case "Anthropic stream":
				attrInt(t, attrs, "gen_ai.usage.cache_creation.input_tokens", 55_784)
				if attrs["gen_ai.conversation.id"] != "session-test" || attrs["user.id"] != "account-redacted" || attrs["service_tier"] != "standard" || attrs["http.request.header.anthropic-version"] != "2023-06-01" || attrs["gen_ai.system_instructions"] == "" {
					t.Fatalf("Anthropic attrs=%#v", attrs)
				}
			case "Gemini stream":
				if attrs["user.id"] != "user-redacted" || attrs["gen_ai.response.id"] != "MLWqaoHcGLfZ1e8P-biEeQ" || !strings.Contains(attrs["gen_ai.output.messages"].(string), "proxy ok") {
					t.Fatalf("Gemini attrs=%#v", attrs)
				}
			case "OpenAI chat stream":
				if attrs["user.id"] != "user-test" || attrs["gen_ai.request.max_tokens"] != int64(100) || attrs["gen_ai.request.temperature"] != 0.2 || !strings.Contains(attrs["gen_ai.output.messages"].(string), "lookup") {
					t.Fatalf("OpenAI attrs=%#v", attrs)
				}
			case "OpenAI responses stream":
				if !strings.Contains(attrs["gen_ai.system_instructions"].(string), "Be concise") || attrs["gen_ai.response.id"] != "resp_test" {
					t.Fatalf("Responses attrs=%#v", attrs)
				}
			}
		})
	}
}

func TestCodexOutputItemDone(t *testing.T) {
	body := string(fixture(t, "chatgpt_responses_stream.txt"))
	call := map[string]any{"id": "fc_real", "type": "function_call", "call_id": "call_real", "name": "read", "arguments": `{"path":"a"}`, "status": "completed"}
	done := "\nevent: response.output_item.done\ndata: " + jsonString(map[string]any{"type": "response.output_item.done", "output_index": 1, "item": call}) + "\n\n"
	// If completed.output also contains the items, it is authoritative, not duplicated.
	message := map[string]any{"id": "msg_redacted_2", "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "final text"}}}
	at := strings.LastIndex(body, `"output":[]`)
	if at < 0 {
		t.Fatal("real fixture has no empty completed.output")
	}
	withOutput := body[:at] + `"output":` + jsonString([]any{message, call}) + body[at+len(`"output":[]`):]
	for _, tt := range []struct {
		name, body, text string
		want             int
	}{
		{"real fixture", body, "ok", 1},
		{"appended call and repeated done", body + done + done, "ok", 2},
		{"completed and done share ids", withOutput + done + done, "final text", 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			exchange := Exchange{Vendor: "chatgpt", Path: "/codex/responses", RequestBody: fixture(t, "chatgpt_request.json"), ResponseBody: []byte(tt.body), StatusCode: 200, EndNs: 200}
			raw, err := Parse(exchange, "")
			if err != nil {
				t.Fatal(err)
			}
			var messages []any
			if err := json.Unmarshal([]byte(text(raw.Attrs["gen_ai.output.messages"])), &messages); err != nil || len(messages) != tt.want {
				t.Fatalf("messages=%v want=%d err=%v", messages, tt.want, err)
			}
			seen := map[string]bool{}
			for _, value := range messages {
				id := text(objectOf(value)["id"])
				if id == "" || seen[id] {
					t.Fatalf("missing or duplicate item id: %v", messages)
				}
				seen[id] = true
			}
			if !strings.Contains(text(raw.Attrs["gen_ai.output.messages"]), tt.text) || raw.Attrs["gen_ai.response.id"] != "resp_redacted_1" {
				t.Fatalf("output or response identity lost: %v", raw.Attrs)
			}
			attrInt(t, raw.Attrs, "gen_ai.usage.input_tokens", 374)
			attrInt(t, raw.Attrs, "gen_ai.usage.output_tokens", 5)
			if tt.want == 1 {
				return
			}
			h, err := New(Config{Logf: t.Logf})
			if err != nil {
				t.Fatal(err)
			}
			testToolSpans(h, exchange, raw)
			result := Exchange{Vendor: "chatgpt", Path: "/codex/responses", StatusCode: 200, StartNs: 800, RequestBody: []byte(`{"input":[{"type":"function_call_output","call_id":"call_real","output":"contents of a"}]}`)}
			chat, err := Parse(result, "")
			if err != nil {
				t.Fatal(err)
			}
			spans := testToolSpans(h, result, chat)
			if len(spans) != 1 || spans[0].ParentSpanID != raw.SpanID || spans[0].TraceID != raw.TraceID || spans[0].StartNs != 200 || spans[0].EndNs != 800 || spans[0].Name != "execute_tool read" || spans[0].Attrs["gen_ai.tool.call.arguments"] != `{"path":"a"}` {
				t.Fatalf("Codex call was not registered: %+v", spans)
			}
		})
	}
}

func TestParseStoresSpanboxSessionWithoutReplacingConversation(t *testing.T) {
	exchange := Exchange{Vendor: "anthropic", Path: "/v1/messages", RequestBody: fixture(t, "anthropic_request.json"), ResponseBody: fixture(t, "anthropic_stream.txt"), RequestHeaders: http.Header{"X-Spanbox-Session": {"pane-7"}}, StatusCode: 200}
	raw, err := Parse(exchange, "test")
	if err != nil {
		t.Fatal(err)
	}
	if raw.Attrs["spanbox.session"] != "pane-7" || raw.Attrs["http.request.header.x-spanbox-session"] != "pane-7" || raw.Attrs["gen_ai.conversation.id"] != "session-test" {
		t.Fatalf("session attrs: %#v", raw.Attrs)
	}
}

func TestParseAnthropicIdentityUsageAndContent(t *testing.T) {
	exchange := Exchange{Vendor: "anthropic", Path: "/v1/messages", RequestBody: fixture(t, "anthropic_request.json"), ResponseBody: fixture(t, "anthropic_stream.txt"), RequestHeaders: http.Header{"User-Agent": {"claude-cli/2.1.273"}}, StatusCode: 200, StartNs: 10, EndNs: 20}
	raw, err := Parse(exchange, "test")
	if err != nil {
		t.Fatal(err)
	}
	if raw.Attrs["gen_ai.conversation.id"] != "session-test" || raw.Attrs["user.id"] != "account-redacted" || raw.Attrs["gen_ai.response.id"] != "msg_011Cf7MNXtvb1W15qQS8wEMG" {
		t.Fatalf("identity attrs: %#v", raw.Attrs)
	}
	attrInt(t, raw.Attrs, "gen_ai.usage.cache_creation.input_tokens", 55784)
	if raw.Attrs["service_tier"] != "standard" || raw.Attrs["gen_ai.request.max_tokens"] != int64(32000) {
		t.Fatalf("request attrs: %#v", raw.Attrs)
	}
	output := raw.Attrs["gen_ai.output.messages"].(string)
	if !strings.Contains(output, `"type":"thinking"`) || !strings.Contains(output, `"chars":0`) || !strings.Contains(output, "proxy ok") || strings.Contains(output, "signature") {
		t.Fatalf("output = %s", output)
	}
	if raw.TraceID == "" || raw.SpanID == "" || raw.ParentSpanID != "" || raw.StartNs != 10 || raw.EndNs != 20 {
		t.Fatalf("ids/timing: %#v", raw)
	}
}

func TestParseAnthropicDeltaWithoutStart(t *testing.T) {
	result := parseStream("anthropic", []byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"))
	messages, ok := result.outputMessages.([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("output messages = %#v", result.outputMessages)
	}
	message := messages[0].(map[string]any)
	content := message["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("content = %#v", content)
	}
}

func TestParseNonStreamingFixtures(t *testing.T) {
	request := []byte(strings.Replace(string(fixture(t, "anthropic_request.json")), `"stream": true`, `"stream": false`, 1))
	tests := []struct {
		name           string
		exchange       Exchange
		finish, output string
	}{
		{"Anthropic", Exchange{Vendor: "anthropic", Path: "/v1/messages", RequestBody: request, ResponseBody: fixture(t, "anthropic_json.json"), StatusCode: 200}, "tool_use", "read_file"},
		{"OpenAI", Exchange{Vendor: "openai", Path: "/v1/chat/completions", RequestBody: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"test"}]}`), ResponseBody: fixture(t, "openai_chat_json.json"), StatusCode: 200}, "stop", "proxy ok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := Parse(tt.exchange, "test")
			if err != nil {
				t.Fatal(err)
			}
			if raw.Attrs["spanbox.proxy.stream"] != false || !strings.Contains(raw.Attrs["gen_ai.output.messages"].(string), tt.output) {
				t.Fatalf("attrs=%#v", raw.Attrs)
			}
			finishes := raw.Attrs["gen_ai.response.finish_reasons"].([]any)
			if len(finishes) != 1 || finishes[0] != tt.finish {
				t.Fatalf("finish=%#v", finishes)
			}
		})
	}
}

func TestParseErrorStatus(t *testing.T) {
	raw, err := Parse(Exchange{Vendor: "openai", Path: "/v1/responses", RequestBody: []byte(`{"model":"gpt-5-mini","input":"test"}`), ResponseBody: []byte(`{"error":"rate limited","extra":"` + strings.Repeat("x", 3000) + `"}`), StatusCode: 429}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if raw.StatusCode != 2 || len(raw.StatusMessage) != 2048 || !strings.Contains(raw.StatusMessage, "rate limited") {
		t.Fatalf("status=%d message=%q", raw.StatusCode, raw.StatusMessage)
	}
	if _, exists := raw.Attrs["gen_ai.usage.input_tokens"]; exists {
		t.Fatalf("error response has fabricated usage: %#v", raw.Attrs)
	}
}
