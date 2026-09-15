package store

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func traceID(n byte) string  { return string(makeHex(n, 32)) }
func spanID(n byte) string   { return string(makeHex(n, 16)) }
func makeHex(n byte, size int) []byte {
	const digits = "0123456789abcdef"
	result := make([]byte, size)
	for i := range result {
		result[i] = digits[(int(n)+i)%len(digits)]
	}
	return result
}

func testSpan(trace, span string, start, end int64) Span {
	return Span{
		TraceID: trace, SpanID: span, Name: "span", Kind: "other", ServiceName: "demo",
		StartNs: start, EndNs: end, DurationMs: float64(end-start) / 1e6,
		Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}",
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func TestOpenNewAndExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var journal string
	if err := store.Reader().QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q", journal)
	}
	var version int
	if err := store.Reader().QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	fileInfo, err := os.Stat(filepath.Join(dir, "spanbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o", got)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o", got)
	}
}

func TestInsertBatchRecompute(t *testing.T) {
	store := openTestStore(t)
	trace := traceID(1)
	root := testSpan(trace, spanID(1), 100_000_000, 500_000_000)
	root.Name, root.Kind = "root-agent", "agent"
	llm := testSpan(trace, spanID(2), 200_000_000, 400_000_000)
	llm.Kind, llm.RequestModel = "llm", "gpt-4o"
	llm.InputTokens, llm.OutputTokens, llm.CostUSD = i64(100), i64(20), f64(0.001)
	tool := testSpan(trace, spanID(3), 250_000_000, 300_000_000)
	tool.Kind, tool.ParentSpanID = "tool", llm.SpanID
	if err := store.InsertBatch(context.Background(), []Span{tool, llm, root}); err != nil {
		t.Fatal(err)
	}

	var name, service, models string
	var start, end, spanCount, llmCount, input, output, hasError int64
	var duration, cost float64
	err := store.Reader().QueryRow(`SELECT name, service_name, start_ns, end_ns, duration_ms, span_count, llm_count,
		input_tokens, output_tokens, cost_usd, has_error, models FROM traces WHERE trace_id=?`, trace).
		Scan(&name, &service, &start, &end, &duration, &spanCount, &llmCount, &input, &output, &cost, &hasError, &models)
	if err != nil {
		t.Fatal(err)
	}
	if name != "root-agent" || service != "demo" || start != 100_000_000 || end != 500_000_000 || duration != 400 || spanCount != 3 || llmCount != 1 || input != 100 || output != 20 || math.Abs(cost-0.001) > 1e-12 || hasError != 0 || models != "gpt-4o" {
		t.Fatalf("unexpected trace summary: name=%q service=%q start=%d end=%d duration=%g spans=%d llms=%d input=%d output=%d cost=%g error=%d models=%q", name, service, start, end, duration, spanCount, llmCount, input, output, cost, hasError, models)
	}
}

func TestNullPropagation(t *testing.T) {
	store := openTestStore(t)
	trace := traceID(2)
	one := testSpan(trace, spanID(1), 1, 2)
	one.Kind, one.InputTokens, one.OutputTokens, one.CostUSD = "llm", i64(10), i64(2), f64(0.1)
	two := testSpan(trace, spanID(2), 2, 3)
	two.Kind, two.InputTokens, two.OutputTokens = "llm", i64(20), i64(3)
	embedding := testSpan(trace, spanID(3), 3, 4)
	embedding.Kind, embedding.OutputTokens, embedding.CostUSD = "embedding", i64(0), f64(0.2)
	if err := store.InsertBatch(context.Background(), []Span{one, two, embedding}); err != nil {
		t.Fatal(err)
	}
	var input, output sql.NullInt64
	var cost sql.NullFloat64
	if err := store.Reader().QueryRow("SELECT input_tokens, output_tokens, cost_usd FROM traces WHERE trace_id=?", trace).Scan(&input, &output, &cost); err != nil {
		t.Fatal(err)
	}
	if input.Valid || !output.Valid || output.Int64 != 5 || cost.Valid {
		t.Fatalf("got input=%+v output=%+v cost=%+v", input, output, cost)
	}
}

func TestRootFallbackAndOrdering(t *testing.T) {
	store := openTestStore(t)
	trace := traceID(3)
	early := testSpan(trace, spanID(2), 10, 20)
	early.ParentSpanID, early.Name, early.RequestModel = spanID(9), "earliest", "a"
	early.InputTokens, early.OutputTokens, early.CostUSD = i64(1), i64(1), f64(1)
	early.Kind = "llm"
	earlyB := testSpan(trace, spanID(3), 20, 30)
	earlyB.ParentSpanID, earlyB.RequestModel, earlyB.SessionID = spanID(9), "b", "first-session"
	earlyB.InputTokens, earlyB.OutputTokens, earlyB.CostUSD = i64(1), i64(1), f64(1)
	earlyB.Kind = "llm"
	lateB := testSpan(trace, spanID(1), 30, 40)
	lateB.ParentSpanID, lateB.RequestModel, lateB.SessionID = spanID(9), "b", "late-session"
	lateB.InputTokens, lateB.OutputTokens, lateB.CostUSD = i64(1), i64(1), f64(1)
	lateB.Kind = "llm"
	if err := store.InsertBatch(context.Background(), []Span{lateB, early, earlyB}); err != nil {
		t.Fatal(err)
	}
	var name, session, models string
	if err := store.Reader().QueryRow("SELECT name, session_id, models FROM traces WHERE trace_id=?", trace).Scan(&name, &session, &models); err != nil {
		t.Fatal(err)
	}
	if name != "earliest" || session != "first-session" || models != "a,b" {
		t.Fatalf("got name=%q session=%q models=%q", name, session, models)
	}
}

func TestUpsertKeepsRowid(t *testing.T) {
	store := openTestStore(t)
	span := testSpan(traceID(4), spanID(4), 1, 2)
	span.Name, span.InputContent = "oldname", "oldcontent"
	if err := store.InsertBatch(context.Background(), []Span{span}); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := store.Reader().QueryRow("SELECT id FROM spans WHERE trace_id=? AND span_id=?", span.TraceID, span.SpanID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	span.Name, span.InputContent = "newname", "newcontent"
	if err := store.InsertBatch(context.Background(), []Span{span}); err != nil {
		t.Fatal(err)
	}
	var after, count int64
	var name string
	if err := store.Reader().QueryRow("SELECT id, name FROM spans WHERE trace_id=? AND span_id=?", span.TraceID, span.SpanID).Scan(&after, &name); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader().QueryRow("SELECT count(*) FROM spans WHERE trace_id=? AND span_id=?", span.TraceID, span.SpanID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if before != after || count != 1 || name != "newname" {
		t.Fatalf("before=%d after=%d count=%d name=%q", before, after, count, name)
	}
	for query, want := range map[string]int64{"newname": 1, "oldname": 0, "newcontent": 1, "oldcontent": 0} {
		var hits int64
		if err := store.Reader().QueryRow("SELECT count(*) FROM spans_fts WHERE spans_fts MATCH ?", query).Scan(&hits); err != nil {
			t.Fatal(err)
		}
		if hits != want {
			t.Fatalf("MATCH %q = %d, want %d", query, hits, want)
		}
	}
}

func TestPurge(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	old := testSpan(traceID(5), spanID(5), now.Add(-40*24*time.Hour).UnixNano(), now.Add(-40*24*time.Hour).Add(time.Second).UnixNano())
	old.Name = "oldspan"
	current := testSpan(traceID(6), spanID(6), now.Add(-24*time.Hour).UnixNano(), now.Add(-24*time.Hour).Add(time.Second).UnixNano())
	current.Name = "currentspan"
	if err := store.InsertBatch(context.Background(), []Span{old, current}); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.Purge(context.Background(), now.Add(-30*24*time.Hour).UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted %d traces", deleted)
	}
	var traces, spans, fts int64
	if err := store.Reader().QueryRow("SELECT count(*) FROM traces").Scan(&traces); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&spans); err != nil {
		t.Fatal(err)
	}
	if err := store.Reader().QueryRow("SELECT count(*) FROM spans_fts").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if traces != 1 || spans != 1 || fts != spans {
		t.Fatalf("traces=%d spans=%d fts=%d", traces, spans, fts)
	}
}
