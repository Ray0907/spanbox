package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

func c2Spans(t *testing.T, db *store.Store) []store.Span {
	t.Helper()
	var spans []store.Span
	if err := json.Unmarshal(c1Dump(t, db), &spans); err != nil {
		t.Fatal(err)
	}
	return spans
}

func TestC2DroppedOriginUsesTurnTrace(t *testing.T) {
	c := c1Cases(t)[2] // Anthropic issuing call; restore its anchor in the continuation.
	for _, session := range []string{"dropped-turn", ""} {
		name := session
		if name == "" {
			name = "sessionless"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			first, next := c.Exchanges[0], c.Exchanges[1]
			continuation := decodeObject(next.RequestBody)
			continuation["messages"] = append(array(decodeObject(first.RequestBody)["messages"]), array(continuation["messages"])...)
			next.RequestBody, _ = json.Marshal(continuation)
			first.RequestHeaders = http.Header{}
			if session != "" {
				first.RequestHeaders.Set("X-Spanbox-Session", session)
			}
			next.RequestHeaders = first.RequestHeaders
			request, response := decodeExchange(first)
			issuing, err := parseExchange(first, "c1-golden", request, response)
			if err != nil {
				t.Fatal(err)
			}
			h := c1Handler(t, db)
			h.recordDroppedOrigins(first)
			for _, table := range []string{"proxy_turns", "spans", "traces"} {
				var count int
				if err := db.Reader().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("dropped capture stored %s: count=%d err=%v", table, count, err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			h = c1Handler(t, db)
			h.capture(next)
			tool := c1Tool(t, db)
			var root store.Span
			for _, span := range c2Spans(t, db) {
				if span.Kind == "agent" {
					root = span
				}
			}
			if session != "" {
				if root.SpanID == "" || tool.TraceID != root.TraceID {
					t.Fatalf("dropped-origin tool escaped agent turn: tool=%s root=%s", tool.TraceID, root.TraceID)
				}
			} else if root.SpanID != "" || tool.TraceID != issuing.TraceID {
				t.Fatalf("sessionless identity changed: tool=%s want=%s root=%s", tool.TraceID, issuing.TraceID, root.SpanID)
			}
			if tool.ParentSpanID != issuing.SpanID || tool.Name != "execute_tool bash" || tool.DurationMs != float64(next.StartNs-first.EndNs)/1e6 {
				t.Fatalf("dropped-origin correlation changed: %+v", tool)
			}
		})
	}
}

func TestC2Turns(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range c1Cases(t) {
		first := c.Exchanges[0]
		family := first.Vendor + first.Path
		if seen[family] || len(c.Exchanges) < 2 {
			continue
		}
		seen[family] = true
		t.Run(family, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			h := c1Handler(t, db)
			if first.Vendor == "chatgpt" {
				request := decodeObject(first.RequestBody)
				request["stream"] = true
				first.RequestBody, _ = json.Marshal(request)
			}
			first.RequestHeaders = http.Header{"X-Spanbox-Session": {"turn\x00|雪/\""}}
			h.capture(first)
			var root, model store.Span
			for _, s := range c2Spans(t, db) {
				if s.Kind == "agent" {
					root = s
				}
				if s.Kind == "llm" {
					model = s
				}
			}
			if root.SpanID == "" || model.ParentSpanID != root.SpanID || model.TraceID != root.TraceID {
				t.Fatalf("missing turn root or model parent: root=%+v model=%+v", root, model)
			}
			if root.InputTokens != nil || root.OutputTokens != nil || root.CostUSD != nil || root.InputContent != "run" || !strings.HasPrefix(root.Name, "invoke_agent ") {
				t.Fatalf("invalid root: %+v", root)
			}
			// Reopen both database and handler: no memory cache can supply the turn.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			h = c1Handler(t, db)
			next := c.Exchanges[1]
			next.RequestHeaders = first.RequestHeaders
			incremental := decodeObject(next.RequestBody)
			for _, key := range []string{"messages", "input", "contents"} {
				items := array(incremental[key])
				if len(items) > 0 && text(objectOf(items[0])["role"]) == "user" && first.Vendor != "anthropic" {
					incremental[key] = items[1:]
				}
			}
			if first.Vendor == "chatgpt" {
				incremental["stream"] = true
			}
			next.RequestBody, _ = json.Marshal(incremental)
			h.capture(next) // Incremental tool-result-only history must use the persisted latest turn.
			roots, models, tools := 0, 0, 0
			for _, s := range c2Spans(t, db) {
				switch s.Kind {
				case "agent":
					roots++
					if s.TraceID != root.TraceID || s.StartNs != first.StartNs || s.EndNs != next.EndNs || s.OutputContent != "done" {
						t.Fatalf("root not merged: %+v", s)
					}
				case "llm":
					models++
					if s.TraceID != root.TraceID || s.ParentSpanID != root.SpanID {
						t.Fatalf("continuation escaped turn: %+v", s)
					}
				case "tool":
					tools++
					if s.TraceID != root.TraceID || s.ParentSpanID != model.SpanID {
						t.Fatalf("lost tool parent: %+v", s)
					}
				}
			}
			if roots != 1 || models != 2 || tools < 1 {
				t.Fatalf("roots/models/tools=%d/%d/%d", roots, models, tools)
			}
			// A complete history must recover the same deterministic anchor after restart.
			req := decodeObject(next.RequestBody)
			orig := decodeObject(first.RequestBody)
			key := "messages"
			if first.Vendor == "gemini" {
				key = "contents"
			}
			if strings.HasSuffix(first.Path, "/responses") {
				key = "input"
			}
			req[key] = append(append([]any{}, array(orig[key])...), array(req[key])...)
			next.RequestBody, _ = json.Marshal(req)
			h.capture(next)
			count := 0
			for _, s := range c2Spans(t, db) {
				if s.Kind == "agent" {
					count++
					if s.SpanID != root.SpanID {
						t.Fatal("history anchor changed across restart")
					}
				}
			}
			if count != 1 {
				t.Fatalf("roots=%d", count)
			}
			// A repeated prompt at a different history index starts a separate trace.
			req[key] = append(array(req[key]), array(orig[key])...)
			first.RequestBody, _ = json.Marshal(req)
			first.ResponseHeaders = first.ResponseHeaders.Clone()
			first.ResponseHeaders.Set("Request-Id", "second-turn")
			first.ResponseBody = []byte(strings.ReplaceAll(string(first.ResponseBody), "\"id\": \"", "\"id\": \"new-"))
			first.StartNs = next.EndNs + 100
			first.EndNs = first.StartNs + 100
			h.capture(first)
			count = 0
			for _, s := range c2Spans(t, db) {
				if s.Kind == "agent" {
					count++
				}
			}
			if count != 2 {
				t.Fatalf("new prompt did not start turn: roots=%d", count)
			}
		})
	}
	if len(seen) < 4 {
		t.Fatalf("only %d families covered", len(seen))
	}
}
