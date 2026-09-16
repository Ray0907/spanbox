package proxy

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
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
}
