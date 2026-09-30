package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
)

func TestRetentionRetriesAfterImport(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := time.Now().Add(-70 * 24 * time.Hour).UnixNano()
	span := store.Span{TraceID: "11111111111111111111111111111111", SpanID: "2222222222222222",
		Name: "old", Kind: "other", StartNs: old, EndNs: old + 1000000, DurationMs: 1}
	if err := db.InsertBatch(context.Background(), []store.Span{span}); err != nil {
		t.Fatal(err)
	}
	release := db.PauseRetention()
	delay := retentionSweep(db, 30)
	release()
	if delay != time.Minute {
		t.Fatalf("skipped sweep retries in %s, want 1m", delay)
	}
	if _, _, err := db.GetTrace(context.Background(), span.TraceID); err != nil {
		t.Fatalf("skipped sweep deleted imported trace: %v", err)
	}
	if delay := retentionSweep(db, 30); delay != time.Hour {
		t.Fatalf("successful retry returns to hourly sweeps, got %s", delay)
	}
	var remaining int
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("retry did not purge old spans: %d err=%v", remaining, err)
	}
}
