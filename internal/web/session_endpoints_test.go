package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

type jsonSessionTrace struct {
	TraceID      string   `json:"trace_id"`
	HasRoot      bool     `json:"has_root"`
	Number       int      `json:"number"`
	Prompt       string   `json:"prompt"`
	Output       string   `json:"output"`
	InputTokens  *int64   `json:"input_tokens"`
	OutputTokens *int64   `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd"`
}

type jsonSessionDetail struct {
	SessionID string             `json:"session_id"`
	Traces    []jsonSessionTrace `json:"traces"`
	Totals    struct {
		Turns        int      `json:"turns"`
		Traces       int      `json:"traces"`
		InputTokens  *int64   `json:"input_tokens"`
		OutputTokens *int64   `json:"output_tokens"`
		CostUSD      *float64 `json:"cost_usd"`
	} `json:"totals"`
	Truncated bool `json:"truncated"`
}

func TestJSONEndpointsSessionDetail(t *testing.T) {
	handler, database := newTestHandler(t, "")
	seedSession(t, database)
	path := "/sessions?id=" + url.QueryEscape(sessionTestID)
	resp := request(t, handler, http.MethodGet, path+"&format=json", "", "", nil)
	var detail jsonSessionDetail
	decodeEndpointJSON(t, resp, 200, &detail)
	if detail.SessionID != sessionTestID || len(detail.Traces) != 4 || detail.Truncated || detail.Totals.Turns != 3 || detail.Totals.Traces != 4 {
		t.Fatalf("session detail=%+v", detail)
	}
	if detail.Totals.InputTokens != nil || detail.Totals.OutputTokens != nil || detail.Totals.CostUSD != nil {
		t.Fatalf("unknown totals must stay null: %+v", detail.Totals)
	}
	for i, trace := range detail.Traces {
		wantID := strings.Repeat("f", 32)
		if i > 0 {
			wantID = strings.Repeat(string(rune('a'+i-1)), 32)
		}
		if trace.TraceID != wantID || trace.Number != i || trace.HasRoot != (i > 0) {
			t.Fatalf("trace %d=%+v", i, trace)
		}
	}
	if detail.Traces[1].Prompt != "first prompt" || detail.Traces[1].Output != "answer to first prompt" || detail.Traces[2].Prompt != "second <script>x</script>" {
		t.Fatalf("missing unescaped trace text: %+v", detail.Traces)
	}
	// The existing list shape remains unchanged; rendered turns are checked in e2e-mobile.
	var list map[string]json.RawMessage
	decodeEndpointJSON(t, request(t, handler, http.MethodGet, "/sessions?format=json", "", "", nil), 200, &list)
	if len(list) != 2 || list["items"] == nil || list["next_cursor"] == nil {
		t.Fatalf("session list changed: %v", list)
	}
	html := request(t, handler, http.MethodGet, path, "", "", nil)
	if html.Code != 200 || !strings.Contains(html.Body.String(), `id="root"`) {
		t.Fatalf("HTML detail changed: status=%d", html.Code)
	}
}

func TestJSONEndpointsSessionMissing(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	var body map[string]string
	decodeEndpointJSON(t, request(t, handler, http.MethodGet, "/sessions?id=missing&format=json", "", "", nil), 404, &body)
	if body["error"] == "" {
		t.Fatal("missing JSON error")
	}
}

func TestJSONEndpointsSessionTruncation(t *testing.T) {
	for _, count := range []int{sessionTurnLimit, sessionTurnLimit + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			handler, database := newTestHandler(t, "")
			var spans []store.Span
			for i := 0; i < count; i++ {
				span := sessionSpan(fmt.Sprintf("%032x", i+1), fmt.Sprintf("%016x", 2*i+1), "", "agent", "turn", int64(i+1)*1_000_000)
				span.InputContent = strings.Repeat("界", sessionTextChars+1)
				span.OutputContent = "answer"
				model := sessionSpan(span.TraceID, fmt.Sprintf("%016x", 2*i+2), span.SpanID, "llm", "chat", span.StartNs)
				model.InputTokens, model.OutputTokens = int64ptr(10), int64ptr(2)
				cost := 0.25
				model.CostUSD = &cost
				spans = append(spans, span, model)
			}
			if _, err := database.InsertBatch(context.Background(), spans); err != nil {
				t.Fatal(err)
			}
			path := "/sessions?id=" + url.QueryEscape(sessionTestID)
			var detail jsonSessionDetail
			decodeEndpointJSON(t, request(t, handler, http.MethodGet, path+"&format=json", "", "", nil), 200, &detail)
			if len(detail.Traces) != sessionTurnLimit || detail.Totals.Traces != sessionTurnLimit || detail.Totals.Turns != sessionTurnLimit || detail.Truncated != (count > sessionTurnLimit) {
				t.Fatalf("count=%d detail traces=%d totals=%+v truncated=%v", count, len(detail.Traces), detail.Totals, detail.Truncated)
			}
			if detail.Totals.InputTokens == nil || *detail.Totals.InputTokens != 2000 || detail.Totals.OutputTokens == nil || *detail.Totals.OutputTokens != 400 || detail.Totals.CostUSD == nil || *detail.Totals.CostUSD != 50 {
				t.Fatalf("totals must cover displayed traces only: %+v", detail.Totals)
			}
			if detail.Traces[0].Prompt != strings.Repeat("界", sessionTextChars)+"…" || detail.Traces[0].Output != "answer" {
				t.Fatal("text clipping differs from HTML")
			}
			html := request(t, handler, http.MethodGet, path, "", "", nil)
			if html.Code != 200 || !strings.Contains(html.Body.String(), `id="root"`) {
				t.Fatalf("HTML/JSON truncation differs: status=%d truncated=%v", html.Code, detail.Truncated)
			}
		})
	}
}
