package proxy

import (
	"net/http"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

func TestTurnAnchorBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, path, request, want string
		index                             int
	}{
		{"anthropic mixed", "anthropic", "/v1/messages", `{"messages":[{"role":"user","content":"old"},{"role":"user","content":[{"type":"tool_result","content":"result"},{"type":"text","text":"new雪\u0000|\""}]}]}`, "new雪\x00|\"", 1},
		{"anthropic tool only", "anthropic", "/v1/messages", `{"messages":[{"role":"user","content":"old"},{"role":"user","content":[{"type":"tool_result","content":"result"}]}]}`, "old", 0},
		{"anthropic incremental", "anthropic", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result"}]}]}`, "", -1},
		{"chat tool only", "openai", "/v1/chat/completions", `{"messages":[{"role":"system","content":"system"},{"role":"user","content":"old"},{"role":"assistant","content":"answer"},{"role":"tool","content":"result"}]}`, "old", 1},
		{"chat repeated text", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":"same"},{"role":"assistant","content":"answer"},{"role":"user","content":"same"}]}`, "same", 2},
		{"responses output", "openai", "/v1/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"function_call_output","output":"result"}]}`, "old", 0},
		{"responses string", "openai", "/v1/responses", `{"input":"prompt"}`, "prompt", 0},
		{"codex incremental", "chatgpt", "/codex/responses", `{"input":[{"type":"function_call_output","call_id":"x","output":"result"}]}`, "", -1},
		{"gemini continuation", "gemini", "/models/g:generateContent", `{"contents":[{"role":"user","parts":[{"text":"old"}]},{"role":"user","parts":[{"functionResponse":{"name":"tool"}}]}]}`, "old", 0},
		{"gemini mixed", "gemini", "/models/g:generateContent", `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"tool"}},{"text":"new"}]}]}`, "new", 0},
		{"gemini incremental", "gemini", "/models/g:generateContent", `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"tool"}}]}]}`, "", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anchor, index := turnAnchor(Exchange{Vendor: tc.vendor, Path: tc.path}, decodeObject([]byte(tc.request)))
			if index != tc.index || turnText(anchor) != tc.want {
				t.Fatalf("anchor index=%d text=%q; want %d/%q", index, turnText(anchor), tc.index, tc.want)
			}
		})
	}
}

func TestC2AnchorlessStartsTurn(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	exchange := Exchange{Vendor: "openai", Path: "/v1/responses", RequestHeaders: http.Header{"X-Spanbox-Session": {"incremental"}}, RequestBody: []byte(`{"input":[{"type":"function_call","call_id":"unknown","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"unknown","output":"ok"}]}`), ResponseHeaders: http.Header{"Request-Id": {"first"}}, StatusCode: 200, StartNs: 100, EndNs: 200, ResponseBody: []byte(`{"output":[]}`)}
	c1Handler(t, db).capture(exchange)
	rootID := ""
	for _, span := range c2Spans(t, db) {
		if span.Kind == "agent" {
			rootID = span.SpanID
		}
	}
	if rootID == "" {
		t.Fatal("anchorless first call did not create root")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	exchange.ResponseHeaders.Set("Request-Id", "second")
	exchange.StartNs, exchange.EndNs = 300, 400
	c1Handler(t, db).capture(exchange)
	count := 0
	for _, span := range c2Spans(t, db) {
		if span.Kind == "agent" {
			count++
			if span.SpanID != rootID || span.EndNs != 400 {
				t.Fatalf("fallback turn changed: %+v", span)
			}
		}
	}
	if count != 1 {
		t.Fatalf("roots=%d", count)
	}
}

func TestC2IdentityAndError(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := c1Handler(t, db)
	request := `{"conversation_id":"vendor-conversation","user":"user-id","messages":[{"role":"user","content":"prompt"}]}`
	exchange := Exchange{Vendor: "openai", Route: "compat|雪", Path: "/v1/chat/completions", RequestBody: []byte(request), ResponseHeaders: http.Header{"Request-Id": {"first"}}, StatusCode: 500, StartNs: 100, EndNs: 200, ResponseBody: []byte(`{"error":"failed"}`)}
	h.capture(exchange)
	anchor := array(decodeObject([]byte(request))["messages"])[0]
	key := jsonString([]any{exchange.route(), "vendor-conversation", 0, hash(jsonString(anchor), 16)})
	trace, rootID := hash("spanbox-proxy-turn-trace"+key, 16), hash("spanbox-proxy-turn-root"+key, 8)
	for _, s := range c2Spans(t, db) {
		if s.TraceID != trace || s.SessionID != "vendor-conversation" || s.UserID != "user-id" {
			t.Fatalf("wrong identity: %+v", s)
		}
		if s.Kind == "agent" && (s.SpanID != rootID || s.StatusCode != 2) {
			t.Fatalf("wrong root: %+v", s)
		}
	}
	exchange.StatusCode = 200
	exchange.StartNs = 300
	exchange.EndNs = 400
	exchange.ResponseHeaders.Set("Request-Id", "second")
	exchange.ResponseBody = []byte(`{"choices":[{"message":{"role":"assistant","content":"recovered"}}]}`)
	h.capture(exchange)
	for _, s := range c2Spans(t, db) {
		if s.Kind == "agent" && (s.StatusCode != 2 || s.StartNs != 100 || s.EndNs != 400 || s.OutputContent != "recovered") {
			t.Fatalf("root lost error or output: %+v", s)
		}
	}
	// Header identity takes precedence without discarding the vendor conversation attribute.
	exchange.RequestHeaders = http.Header{"X-Spanbox-Session": {"header-session"}}
	exchange.ResponseHeaders.Set("Request-Id", "third")
	h.capture(exchange)
	count := 0
	for _, s := range c2Spans(t, db) {
		if s.Kind == "agent" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("session scope collision: %d roots", count)
	}
}
