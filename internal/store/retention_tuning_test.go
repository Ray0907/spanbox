package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPurgeErrorCleanupAndCancellation(t *testing.T) {
	for _, phase := range []string{"batch", "checkpoint", "deadline", "canceled", "cancel-during-cleanup"} {
		t.Run(phase, func(t *testing.T) {
			db := openTestStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for i := byte(1); i <= 3; i++ {
				if err := db.InsertBatch(ctx, []Span{testSpan(traceID(i), spanID(1), 1, 2)}); err != nil {
					t.Fatal(err)
				}
			}
			wantDeleted := int64(0)
			switch phase {
			case "batch", "cancel-during-cleanup":
				_, err := db.w.Exec(`CREATE TRIGGER purge_fault BEFORE DELETE ON spans
					WHEN (SELECT count(*) FROM traces)=1 BEGIN SELECT RAISE(ABORT, 'batch fault'); END`)
				if err != nil {
					t.Fatal(err)
				}
				wantDeleted = 2
			case "checkpoint":
				if err := db.c.Close(); err != nil {
					t.Fatal(err)
				}
				wantDeleted = 1
			case "deadline":
				var stop context.CancelFunc
				ctx, stop = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer stop()
			case "canceled":
				cancel()
			}
			cleanupErr := errors.New("cleanup fault")
			calls := 0
			deleted, err := db.purge(ctx, 3, func(cleanupCtx context.Context) error {
				calls++
				deadline, ok := cleanupCtx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 30*time.Second || cleanupCtx.Err() != nil {
					t.Fatalf("cleanup needs a fresh <=30s deadline: %v, err=%v", deadline, cleanupCtx.Err())
				}
				if phase == "cancel-during-cleanup" {
					cancel()
					select {
					case <-cleanupCtx.Done():
						if !errors.Is(cleanupCtx.Err(), context.Canceled) {
							t.Fatal(cleanupCtx.Err())
						}
					case <-time.After(time.Second):
						t.Fatal("shutdown cancellation did not interrupt cleanup")
					}
				}
				return cleanupErr
			}, PurgeOptions{BatchSize: 1, MergeEvery: 256})
			if deleted != wantDeleted || err == nil {
				t.Fatalf("deleted=%d want=%d err=%v", deleted, wantDeleted, err)
			}
			if phase == "canceled" {
				if calls != 0 || !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation ran cleanup: calls=%d err=%v", calls, err)
				}
			} else {
				if calls != 1 || !errors.Is(err, cleanupErr) {
					t.Fatalf("missing/joinless cleanup: calls=%d err=%v", calls, err)
				}
				if (phase == "batch" || phase == "cancel-during-cleanup") && !strings.Contains(err.Error(), "batch fault") {
					t.Fatalf("original batch error lost: %v", err)
				}
				if phase == "checkpoint" && !strings.Contains(err.Error(), "database is closed") {
					t.Fatalf("original checkpoint error lost: %v", err)
				}
				if phase == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("original deadline lost: %v", err)
				}
			}
		})
	}
}

func TestPurgeRejectsInvalidOptionsBeforeDeleting(t *testing.T) {
	for _, options := range [][]PurgeOptions{
		{{BatchSize: 0, MergeEvery: 1}},
		{{BatchSize: -1, MergeEvery: 1}},
		{{BatchSize: 2, MergeEvery: 0}},
		{{BatchSize: 2, MergeEvery: -1}},
		{{BatchSize: 2, MergeEvery: 1}, {BatchSize: 2, MergeEvery: 1}},
	} {
		db := openTestStore(t)
		if err := db.InsertBatch(context.Background(), []Span{testSpan(traceID(1), spanID(1), 1, 2)}); err != nil {
			t.Fatal(err)
		}
		deleted, err := db.Purge(context.Background(), 3, options...)
		if err == nil || deleted != 0 {
			t.Fatalf("invalid options %v: deleted=%d err=%v", options, deleted, err)
		}
		if _, spans, err := db.GetTrace(context.Background(), traceID(1)); err != nil || len(spans) != 1 {
			t.Fatalf("invalid options deleted data: %v", err)
		}
	}
}

func TestPurgeMergesPendingDeletesBeforeFinalReclaim(t *testing.T) {
	db := openTestStore(t)
	spans := make([]Span, 1000)
	for i := range spans {
		spans[i] = testSpan(fmt.Sprintf("%032x", i+1), spanID(1), 1, 2)
		spans[i].InputContent = strings.Repeat("needle searchable prompt content ", 200)
		if i >= 900 {
			spans[i].StartNs, spans[i].EndNs = 20, 21
		}
	}
	if err := db.InsertBatch(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.Reader().QueryRow("SELECT sum(length(block)) FROM spans_fts_data").Scan(&before); err != nil {
		t.Fatal(err)
	}
	deleted, err := db.purge(context.Background(), 3, func(context.Context) error {
		return db.Reader().QueryRow("SELECT sum(length(block)) FROM spans_fts_data").Scan(&after)
	}, PurgeOptions{BatchSize: 10, MergeEvery: 1})
	if err != nil || deleted != 900 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	// A bounded segment count alone misses the large stale posting lists.
	// Do not wait for final reclamation to remove most of their bytes.
	if after >= before*3/4 {
		t.Fatalf("FTS postings not reclaimed during deletion: %d -> %d bytes", before, after)
	}
}

func TestPurgeBatchUsesPerOperationLimit(t *testing.T) {
	db := openTestStore(t)
	var spans []Span
	for i := byte(1); i <= 5; i++ {
		span := testSpan(traceID(i), spanID(1), 1, 2)
		span.InputContent = "needle"
		spans = append(spans, span)
	}
	if err := db.InsertBatch(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	if deleted, err := db.purgeBatch(context.Background(), 3, 2); err != nil || deleted != 2 {
		t.Fatalf("batch limit: deleted=%d err=%v", deleted, err)
	}
	// Finish a partial cadence (fewer than 4 batches). Final reclamation must
	// still run, and real MATCH must not return any deleted postings.
	if deleted, err := db.Purge(context.Background(), 3, PurgeOptions{BatchSize: 2, MergeEvery: 4}); err != nil || deleted != 3 {
		t.Fatalf("partial cadence: deleted=%d err=%v", deleted, err)
	}
	var matches int
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans_fts WHERE spans_fts MATCH 'needle'").Scan(&matches); err != nil || matches != 0 {
		t.Fatalf("stale FTS: matches=%d err=%v", matches, err)
	}
}
