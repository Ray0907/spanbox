package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUTCDayBounds(t *testing.T) {
	day := int64(24 * time.Hour)
	for _, test := range []struct{ ns, want int64 }{{0, 0}, {1, 0}, {-1, -1}, {day - 1, 0}, {day, 1}, {-day - 1, -2}} {
		if got := utcDay(test.ns); got != test.want {
			t.Fatalf("utcDay(%d)=%d want %d", test.ns, got, test.want)
		}
	}
}

func TestDashboardPercentileDaysMatchTraceDaysAtMidnight(t *testing.T) {
	db := openTestStore(t)
	midnight := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC).UnixNano()
	spans := []Span{testSpan(traceID(1), spanID(1), midnight-1, midnight+1000000), testSpan(traceID(2), spanID(2), midnight, midnight+2000000)}
	for i := range spans {
		spans[i].Kind, spans[i].RequestModel = "llm", "model"
		spans[i].DurationMs = float64(i + 1)
	}
	if err := db.InsertBatch(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	data, err := db.Dashboard(context.Background(), midnight-int64(24*time.Hour), midnight+int64(24*time.Hour))
	if err != nil || len(data.Days) != 2 || data.Days[0] != "2026-09-29" || data.Days[1] != "2026-09-30" {
		t.Fatalf("UTC days disagree: %+v err=%v", data, err)
	}
	for i := range data.Days {
		if data.DailyP50[i] == nil || *data.DailyP50[i] != float64(i+1) {
			t.Fatalf("daily percentile lost at UTC midnight: %+v", data)
		}
	}
}

func TestLoadQueryMigrationAndSearchPage(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var spans []Span
	for i := 1; i <= 4; i++ {
		span := testSpan(traceID(byte(i)), spanID(byte(i)), 100, 200)
		span.InputContent = "a needle in the haystack"
		span.Kind, span.RequestModel = "llm", "model"
		spans = append(spans, span)
	}
	if err := db.InsertBatch(ctx, spans); err != nil {
		t.Fatal(err)
	}
	// Reopen an existing version-1 DB, not just the fresh-schema path.
	if _, err := db.w.Exec(`DROP INDEX spans_kind; DROP INDEX spans_search;
		CREATE INDEX spans_kind ON spans(kind, start_ns); PRAGMA user_version=1;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err := db.Search(ctx, "needle", 2)
	if err != nil || len(page) != 2 || page[0].SpanID != spanID(4) || page[1].SpanID != spanID(3) {
		t.Fatalf("first page: %+v err=%v", page, err)
	}
	if !strings.Contains(page[0].Snippet, "[needle]") {
		t.Fatalf("snippet lost highlighting: %q", page[0].Snippet)
	}
	page, err = db.Search(ctx, "needle", 2, page[1])
	if err != nil || len(page) != 2 || page[0].SpanID != spanID(2) || page[1].SpanID != spanID(1) {
		t.Fatalf("next page: %+v err=%v", page, err)
	}
	data, err := db.Dashboard(ctx, 101, 0)
	if err != nil || len(data.Models) != 0 || len(data.DailyP50) != 0 {
		t.Fatalf("day index ignored exact start bound: %+v err=%v", data, err)
	}
	data, err = db.Dashboard(ctx, 1, 200)
	if err != nil || data.TraceCount != 4 || len(data.Models) != 1 || len(data.DailyP50) != 1 {
		t.Fatalf("migrated dashboard: %+v err=%v", data, err)
	}
}

func TestPurgeKeepsWholeRecentOrIncompleteTraces(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	// All traces start before 100. Only the first is completely old.
	spans := []Span{testSpan(traceID(1), spanID(1), 1, 2)}
	for i, times := range [][2]int64{{101, 102}, {1, 101}, {1, 0}, {1, 100}, {100, 101}} {
		tid := traceID(byte(i + 2))
		spans = append(spans, testSpan(tid, spanID(1), 1, 2), testSpan(tid, spanID(2), times[0], times[1]))
	}
	if err := db.InsertBatch(ctx, spans); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.Purge(ctx, 100)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v; want only the complete old trace deleted", deleted, err)
	}
	for i := 2; i <= 6; i++ {
		trace, children, err := db.GetTrace(ctx, traceID(byte(i)))
		if err != nil || trace.SpanCount != 2 || len(children) != 2 {
			t.Fatalf("protected trace %d: %+v children=%d err=%v", i, trace, len(children), err)
		}
	}
}

func TestPurgeRestoresCheckpointPolicyAfterCancellation(t *testing.T) {
	db := openTestStore(t)
	if _, err := db.w.Exec("PRAGMA wal_autocheckpoint=17"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertBatch(context.Background(), []Span{testSpan(traceID(1), spanID(1), 1, 2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Purge(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	// Hold the checkpoint connection to force a cancellable maintenance wait,
	// not a timing-dependent huge database or slow query.
	conn, err := db.c.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := db.Purge(ctx, 100)
		done <- err
	}()
	var auto int
	deadline := time.Now().Add(time.Second)
	for {
		if err := db.w.QueryRow("PRAGMA wal_autocheckpoint").Scan(&auto); err != nil {
			t.Fatal(err)
		}
		if auto == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("purge cancellation: %v", err)
	}
	if auto != 0 {
		t.Fatal("purge did not enter manual checkpoint mode")
	}
	if err := db.w.QueryRow("PRAGMA wal_autocheckpoint").Scan(&auto); err != nil || auto != 17 {
		t.Fatalf("checkpoint policy was not restored: %d err=%v", auto, err)
	}
}

func TestPurgeLogsAndSignalsPause(t *testing.T) {
	db := openTestStore(t)
	release := db.PauseRetention()
	defer release()
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	deleted, err := db.Purge(context.Background(), 100)
	if deleted != 0 || !errors.Is(err, ErrRetentionPaused) || !strings.Contains(logs.String(), "purge skipped") {
		t.Fatalf("pause was silent: deleted=%d err=%v logs=%q", deleted, err, logs.String())
	}
}

func TestPurgeDoesNotLoopOnLegacyNonIncrementalDatabase(t *testing.T) {
	db := openTestStore(t)
	if _, err := db.w.Exec("PRAGMA auto_vacuum=NONE; VACUUM;"); err != nil {
		t.Fatal(err)
	}
	span := testSpan(traceID(1), spanID(1), 1, 2)
	span.InputContent = strings.Repeat("body ", 2000)
	if err := db.InsertBatch(context.Background(), []Span{span}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if err := db.InsertBatch(ctx, []Span{span}); err != nil {
			t.Fatal(err)
		}
		deleted, err := db.Purge(ctx, 100)
		if deleted != 1 || err != nil {
			t.Fatalf("legacy vacuum must not fail committed deletes: deleted=%d err=%v", deleted, err)
		}
		var remaining int
		if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("legacy purge did not delete spans: %d err=%v", remaining, err)
		}
	}
	if strings.Count(logs.String(), "vacuum skipped") != 1 || !strings.Contains(logs.String(), "VACUUM offline") {
		t.Fatalf("expected one actionable vacuum warning, got %q", logs.String())
	}
}

func TestPurgeShrinksMainFileWithoutExternalCheckpoint(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	spans := make([]Span, 1000)
	for i := range spans {
		spans[i] = testSpan(fmt.Sprintf("%032x", i+1), spanID(1), 1, 2)
		spans[i].InputContent = strings.Repeat("old searchable body ", 200)
	}
	if err := db.InsertBatch(ctx, spans); err != nil {
		t.Fatal(err)
	}
	if _, err := db.w.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "spanbox.db")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if deleted, err := db.Purge(ctx, 100); err != nil || deleted != 1000 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("main DB did not shrink: %d -> %d", before.Size(), after.Size())
	}
	var free int
	if err := db.Reader().QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free != 0 {
		t.Fatalf("purge left %d reclaimable pages", free)
	}
}
