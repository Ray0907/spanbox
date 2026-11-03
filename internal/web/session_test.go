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
	response := request(t, handler, http.MethodGet, "/sessions?id="+url.QueryEscape(sessionTestID), "", "", nil)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", response.Code, body)
	}
	// Turns appear in start order, numbered only when the trace has an agent root.
	positions := []int{
		strings.Index(body, "execute_tool lost"),
		strings.Index(body, "first prompt"),
		strings.Index(body, "second &lt;script&gt;"),
		strings.Index(body, "third prompt"),
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] < 0 || positions[i] <= positions[i-1] {
			t.Fatalf("cards missing or out of order: %v", positions)
		}
	}
	for _, want := range []string{"Turn 1", "Turn 2", "Turn 3", "answer to first prompt", `href="/traces/` + strings.Repeat("a", 32) + `"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %q", want, body)
		}
	}
	// Totals are plain text, not button-like chips; durations are compact.
	if !strings.Contains(body, `class="session-totals"`) || strings.Contains(body, "trace-summary") || !strings.Contains(body, "<span>1ms</span>") {
		t.Fatalf("totals/duration markup not as designed: %q", body)
	}
	if strings.Contains(body, "Turn 4") || strings.Contains(body, "<script>x</script>") || strings.Contains(body, "chat other") {
		t.Fatalf("unexpected content (extra turn, unescaped prompt or foreign session): %q", body)
	}
}

func TestSessionDetailUnknownSession(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	response := request(t, handler, http.MethodGet, "/sessions?id=nope", "", "", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestSessionsListLinksToDetail(t *testing.T) {
	handler, database := newTestHandler(t, "")
	seedSession(t, database)
	body := request(t, handler, http.MethodGet, "/sessions", "", "", nil).Body.String()
	if !strings.Contains(body, `href="/sessions?id=sess%2fwith%20slash"`) && !strings.Contains(body, `href="/sessions?id=sess%2Fwith+slash"`) {
		t.Fatalf("session id does not link to the detail page: %q", body)
	}
	if !strings.Contains(body, `href="/?session=`) {
		t.Fatalf("trace filter link was removed: %q", body)
	}
	// The count link must say what it counts when read out of its table cell.
	if !strings.Contains(body, `aria-label="4 traces in sess/with slash"`) {
		t.Fatalf("trace count link has no descriptive name: %q", body)
	}
}

func TestSessionDetailMarksClippedTextAndNamesLinks(t *testing.T) {
	handler, database := newTestHandler(t, "")
	trace_id := strings.Repeat("c", 32)
	root := sessionSpan(trace_id, strings.Repeat("c", 16), "", "agent", "invoke_agent agent-svc", 1_000_000_000)
	root.InputContent = strings.Repeat("x", 599) + "yz" + strings.Repeat("w", 100)
	if _, err := database.InsertBatch(context.Background(), []store.Span{root}); err != nil {
		t.Fatal(err)
	}
	body := request(t, handler, http.MethodGet, "/sessions?id="+url.QueryEscape(sessionTestID), "", "", nil).Body.String()
	// Cut at 600 characters, and the cut must be visible.
	if !strings.Contains(body, strings.Repeat("x", 599)+"y…") || strings.Contains(body, "yz") {
		t.Fatalf("clipped prompt is not marked: %q", body)
	}
	if !strings.Contains(body, "1 turn · 1 trace ·") || !strings.Contains(body, "<span>1 span</span>") {
		t.Fatalf("singular counts not handled: %q", body)
	}
	// A repeated link text needs a name that tells the cards apart.
	if !strings.Contains(body, `aria-label="Open trace for Turn 1"`) {
		t.Fatalf("trace link has no distinct accessible name: %q", body)
	}
	// An unknown cost is left out instead of shown as a bare dash.
	if strings.Contains(body, "$") || strings.Contains(body, "<span>—</span>") {
		t.Fatalf("unknown cost rendered: %q", body)
	}
}
