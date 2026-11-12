package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

const sessionTestID = "sess/with slash"

func sessionSpan(trace_id, span_id, parent, kind, name string, start_ns int64) store.Span {
	return store.Span{
		TraceID: trace_id, SpanID: span_id, ParentSpanID: parent, Name: name, Kind: kind,
		ServiceName: "agent-svc", StartNs: start_ns, EndNs: start_ns + 1_000_000, DurationMs: 1,
		SessionID: sessionTestID, Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}",
	}
}

// seedSession writes three agent turns and one orphan tool trace, inserted out of time order.
func seedSession(t *testing.T, database *store.Store) {
	t.Helper()
	var spans []store.Span
	prompts := []string{"first prompt", "second <script>x</script>", "third prompt"}
	for i, prompt := range prompts {
		trace_id := strings.Repeat(string(rune('a'+i)), 32)
		start_ns := int64(1_000_000_000 * (i + 1))
		root := sessionSpan(trace_id, strings.Repeat(string(rune('a'+i)), 16), "", "agent", "invoke_agent agent-svc", start_ns)
		root.InputContent, root.OutputContent = prompt, "answer to "+prompt
		model := sessionSpan(trace_id, strings.Repeat(string(rune('k'+i)), 16), root.SpanID, "llm", "chat m", start_ns)
		model.RequestModel = "m"
		model.InputTokens, model.OutputTokens = int64ptr(10), int64ptr(2)
		spans = append(spans, root, model)
	}
	orphan := sessionSpan(strings.Repeat("f", 32), strings.Repeat("f", 16), "", "tool", "execute_tool lost", 500_000_000)
	spans = append(spans, orphan)
	other := sessionSpan(strings.Repeat("9", 32), strings.Repeat("9", 16), "", "llm", "chat other", 2_000_000_000)
	other.SessionID = "other-session"
	spans = append(spans, other)
	if _, err := database.InsertBatch(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
}

func TestSessionDetailPage(t *testing.T) {
	handler, database := newTestHandler(t, "")
	seedSession(t, database)
	response := request(t, handler, http.MethodGet, "/sessions?id="+url.QueryEscape(sessionTestID)+"&format=json", "", "", nil)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, body)
	}
	// Turns appear in start order, numbered only when the trace has an agent root.
	positions := []int{
		strings.Index(body, "execute_tool lost"),
		strings.Index(body, "first prompt"),
		strings.Index(body, `second \u003cscript\u003e`),
		strings.Index(body, "third prompt"),
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] < 0 || positions[i] <= positions[i-1] {
			t.Fatalf("cards missing or out of order: %v", positions)
		}
	}
	for _, want := range []string{`"number":1`, `"number":2`, `"number":3`, "answer to first prompt", `"trace_id":"` + strings.Repeat("a", 32) + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	// Markup, compact durations and accessible labels are checked in e2e-mobile/sessions-search.cjs.
	if !strings.Contains(body, `"totals":{"cost_usd":null,"input_tokens":null,"output_tokens":null,"traces":4,"turns":3}`) || !strings.Contains(body, `"duration_ms":1`) {
		t.Fatalf("totals/duration JSON not as designed: %q", body)
	}
	if strings.Contains(body, `"number":4`) || strings.Contains(body, "<script>x</script>") || strings.Contains(body, "chat other") {
		t.Fatalf("unexpected content (extra turn, unescaped prompt or foreign session): %q", body)
	}
}

func TestSessionDetailUnknownSession(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	response := request(t, handler, http.MethodGet, "/sessions?id=nope&format=json", "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestSessionsListLinksToDetail(t *testing.T) {
	handler, database := newTestHandler(t, "")
	seedSession(t, database)
	page := agentGet(t, handler, "/sessions?format=json")
	for _, item := range page["items"].([]any) {
		row := item.(map[string]any)
		if row["session_id"] == sessionTestID && row["trace_count"] == float64(4) {
			return // Detail/filter links and their accessible names are checked in e2e-mobile.
		}
	}
	t.Fatalf("missing session ID/count: %v", page)
}

func TestSessionDetailMarksClippedTextAndNamesLinks(t *testing.T) {
	handler, database := newTestHandler(t, "")
	trace_id := strings.Repeat("c", 32)
	root := sessionSpan(trace_id, strings.Repeat("c", 16), "", "agent", "invoke_agent agent-svc", 1_000_000_000)
	root.InputContent = strings.Repeat("x", 599) + "yz" + strings.Repeat("w", 100)
	if _, err := database.InsertBatch(context.Background(), []store.Span{root}); err != nil {
		t.Fatal(err)
	}
	body := request(t, handler, http.MethodGet, "/sessions?id="+url.QueryEscape(sessionTestID)+"&format=json", "", "", nil).Body.String()
	// Cut at 600 characters, and the cut must be visible.
	if !strings.Contains(body, strings.Repeat("x", 599)+"y…") || strings.Contains(body, "yz") {
		t.Fatalf("clipped prompt is not marked: %q", body)
	}
	if !strings.Contains(body, `"traces":1,"turns":1`) || !strings.Contains(body, `"span_count":1`) || !strings.Contains(body, `"number":1`) || !strings.Contains(body, `"cost_usd":null`) {
		t.Fatalf("counts/number/unknown cost lost: %q", body)
	}
	// Singular labels, named links and omitted unknown costs are checked in e2e-mobile.
}
