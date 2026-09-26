package store

import (
	"context"
	"errors"
	"testing"
)

func TestExportSpansOrderAndBounds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	spans := []Span{
		testSpan(traceID(2), spanID(2), 20, 21),
		testSpan(traceID(1), spanID(3), 10, 11),
		testSpan(traceID(1), spanID(1), 10, 11),
		testSpan(traceID(1), spanID(2), 20, 21),
	}
	spans[1].InputContent = `{"messages":["keep me"]}`
	spans[1].InputTokens, spans[1].CostUSD = i64(0), f64(0)
	if err := s.InsertBatch(ctx, spans); err != nil {
		t.Fatal(err)
	}
	var got []Span
	if err := s.ExportSpans(ctx, TraceFilter{FromNs: 10, ToNs: 21}, func(span Span) error { got = append(got, span); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].SpanID != spans[2].SpanID || got[1].SpanID != spans[1].SpanID || got[2].SpanID != spans[3].SpanID || got[3].TraceID != spans[0].TraceID {
		t.Fatalf("order: %+v", got)
	}
	if got[1].InputContent != spans[1].InputContent || got[1].InputTokens == nil || *got[1].InputTokens != 0 || got[1].CostUSD == nil || *got[1].CostUSD != 0 || got[0].InputTokens != nil {
		t.Fatalf("payload/null fields: %+v", got)
	}
	got = nil
	if err := s.ExportSpans(ctx, TraceFilter{FromNs: 10, ToNs: 20}, func(span Span) error { got = append(got, span); return nil }); err != nil || len(got) != 3 {
		t.Fatalf("bounded export: %+v err=%v", got, err)
	}
	stop := errors.New("stop")
	calls := 0
	if err := s.ExportSpans(ctx, TraceFilter{}, func(Span) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("callback err=%v calls=%d", err, calls)
	}
}

func TestExportSpansMatchesTraceFiltersWithoutPageLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := testSpan(traceID(1), spanID(1), 1_000_000_000, 1_800_000_000)
	first.Kind, first.RequestModel, first.ServiceName = "llm", "gpt-4o", "api"
	first.SessionID, first.UserID, first.StatusCode = "session-a", "user-a", 2
	// This child starts outside the filtered trace time range, but belongs in the export.
	child := testSpan(first.TraceID, spanID(2), 2_000_000_000, 2_100_000_000)
	child.ParentSpanID = first.SpanID
	other := testSpan(traceID(2), spanID(3), 3_000_000_000, 3_200_000_000)
	other.Kind, other.RequestModel, other.ServiceName = "llm", "other", "worker"
	also := first
	also.TraceID, also.SpanID = traceID(3), spanID(4)
	also.StartNs, also.EndNs = 1_100_000_000, 2_200_000_000
	if err := s.InsertBatch(ctx, []Span{first, child, other, also}); err != nil {
		t.Fatal(err)
	}
	base := TraceFilter{FromNs: 1_000_000_000, ToNs: 2_000_000_000, Model: "gpt-4o", Service: "api", ErrorsOnly: true, MinDurationMs: 1000, SessionID: "session-a", UserID: "user-a", Limit: 1, CursorStartNs: 1, CursorTraceID: first.TraceID}
	var got []Span
	emit := func(span Span) error { got = append(got, span); return nil }
	if err := s.ExportSpans(ctx, base, emit); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].SpanID != first.SpanID || got[1].SpanID != child.SpanID || got[2].SpanID != also.SpanID {
		t.Fatalf("filtered trace spans: %+v", got)
	}
	for _, filter := range []TraceFilter{
		{FromNs: 2_000_000_000, Model: "gpt-4o"}, {ToNs: 1_000_000_000}, {Model: "missing"}, {Service: "worker", ErrorsOnly: true},
		{ErrorsOnly: true, MinDurationMs: 2000}, {SessionID: "session-b"}, {UserID: "user-b"},
	} {
		got = nil
		if err := s.ExportSpans(ctx, filter, emit); err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("filter %+v: %+v", filter, got)
		}
	}
}
