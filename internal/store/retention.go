package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
)

// PurgeOptions sets trace batch size and the cadence of explicit FTS merge
// steps. SQLite's automatic merges and final reclamation are always enabled.
// BatchSize counts traces, not spans; one trace is never split across deletes.
type PurgeOptions struct {
	BatchSize  int
	MergeEvery int
}

// ErrRetentionPaused lets the scheduler retry a skipped purge after an import.
var ErrRetentionPaused = errors.New("retention paused by import or another purge")

// PauseRetention waits for an already-running purge, then prevents purges during
// the complete import, including gaps between its committed batches.
func (s *Store) PauseRetention() func() {
	// ponytail: imports serialize per store; per-trace protection if throughput matters.
	s.retentionMu.Lock()
	return s.retentionMu.Unlock
}

func (s *Store) Purge(ctx context.Context, cutoffNs int64, tuning ...PurgeOptions) (deleted int64, err error) {
	options := PurgeOptions{BatchSize: config.DefaultPurgeBatchSize, MergeEvery: config.DefaultFTSMergeEvery}
	if len(tuning) > 1 {
		return 0, errors.New("purge accepts at most one set of options")
	}
	if len(tuning) == 1 {
		options = tuning[0]
	}
	if options.BatchSize < 1 || options.MergeEvery < 1 {
		return 0, errors.New("purge batch size and merge interval must be positive")
	}
	// Do not queue a purge behind an import and immediately delete old arrivals.
	// The next scheduled purge still applies the normal retention policy.
	if !s.retentionMu.TryLock() {
		log.Print("retention: purge skipped while import or another purge is active")
		return 0, ErrRetentionPaused
	}
	defer s.retentionMu.Unlock()
	// During maintenance, checkpoint I/O belongs to the dedicated connection,
	// not SQLite's automatic checkpoint inside each ingest/delete commit.
	var auto int
	if err := s.w.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&auto); err != nil {
		return 0, err
	}
	defer func() {
		_, restoreErr := s.w.ExecContext(context.Background(), fmt.Sprintf("PRAGMA wal_autocheckpoint=%d", auto))
		err = errors.Join(err, restoreErr)
	}()
	if _, err := s.w.ExecContext(ctx, "PRAGMA wal_autocheckpoint=0"); err != nil {
		return 0, err
	}
	return s.purge(ctx, cutoffNs, s.reclaim, options)
}

func (s *Store) reclaim(ctx context.Context) error {
	// Start one optimization, then continue it. Repeated negative commands
	// restart merges when new ingest trees arrive and can copy the whole index.
	for pages := -1; ; pages = 1 {
		worked, err := s.mergeFTS(ctx, pages)
		if err != nil {
			return err
		}
		if !worked {
			break
		}
		if err := s.checkpointRetention(ctx, 8192); err != nil {
			return err
		}
		if err := retentionYield(ctx); err != nil {
			return err
		}
	}
	var mode int
	if err := s.w.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	for {
		var free int
		if err := s.r.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
			return err
		}
		if free == 0 {
			return s.checkpointRetention(ctx, 0)
		}
		if mode != 2 {
			s.vacuumWarn.Do(func() {
				log.Print("retention: vacuum skipped for non-incremental database; to enable shrinkage, run PRAGMA auto_vacuum=INCREMENTAL; VACUUM offline")
			})
			return s.checkpointRetention(ctx, 0)
		}
		// SQLite yields a row per reclaimed page. Exec only steps once; drain
		// the pragma, in small writer turns, to actually reclaim the freelist.
		rows, err := s.w.QueryContext(ctx, "PRAGMA incremental_vacuum(16)")
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err := s.checkpointRetention(ctx, 8192); err != nil {
			return err
		}
		if err := retentionYield(ctx); err != nil {
			return err
		}
	}
}

func (s *Store) mergeFTS(ctx context.Context, pages int) (bool, error) {
	conn, err := s.w.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var before, after int64
	if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO spans_fts(spans_fts, rank) VALUES('merge', ?)", pages); err != nil {
		return false, err
	}
	if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return false, err
	}
	return after-before >= 2, nil
}

func retentionYield(ctx context.Context) error {
	timer := time.NewTimer(2 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// PASSIVE checkpoint I/O uses its own connection. At ~32MiB, a TRUNCATE
// checkpoint holds SQLite's writer lock for at most 250ms to drain snapshots;
// opportunistic-only resets can grow WAL forever under overlapping readers.
func (s *Store) checkpointRetention(ctx context.Context, maxFrames int) error {
	for {
		var busy, frames, done int
		err := s.c.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &frames, &done)
		if err != nil {
			return err
		}
		if busy == 0 && frames < maxFrames {
			return nil
		}
		if frames >= maxFrames {
			var remaining, copied int
			err = s.c.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &remaining, &copied)
			if err != nil || busy == 0 {
				return err
			}
			// A failed checkpoint may report -1 counts; retain PASSIVE's
			// known frame count rather than mistaking failure for an empty WAL.
		}
		if busy == 0 || (frames >= 0 && frames < maxFrames) {
			return nil
		}
		if err := retentionYield(ctx); err != nil {
			return err
		}
	}
}

func (s *Store) purge(ctx context.Context, cutoffNs int64, vacuum func(context.Context) error, options PurgeOptions) (total int64, err error) {
	finalizing := false
	defer func() {
		if err == nil || finalizing || errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled {
			return
		}
		// Batch/merge/checkpoint errors get one best-effort finalization on
		// an independent 30s deadline, preserving committed deletes/errors.
		// Shutdown cancellation skips cleanup and interrupts recovery already
		// running; it must not wait out this maintenance deadline.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, func() {
			if ctx.Err() == context.Canceled {
				cancel()
			}
		})
		defer stop()
		err = errors.Join(err, vacuum(cleanupCtx))
	}()
	batches := 0
	// A single early optimization cannot retire delete trees created later.
	// Finish each generation before starting the next; never restart a merge
	// still in progress. This is a nominal page budget, not a time bound:
	// a common term's large doclist may require more work in one SQLite step.
	const mergeBudget = 32
	mergePages := -mergeBudget
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		deleted, err := s.purgeBatch(ctx, cutoffNs, options.BatchSize)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted == 0 {
			finalizing = true
			return total, vacuum(ctx)
		}
		// FTS maintenance gets its own writer turn, not a longer
		// delete transaction. Positive steps finish the current generation.
		batches++
		if batches%options.MergeEvery == 0 {
			worked, err := s.mergeFTS(ctx, mergePages)
			if err != nil {
				return total, err
			}
			mergePages = mergeBudget
			if !worked {
				mergePages = -mergeBudget
			}
		}
		// Checkpoint every batch even when an explicit merge is skipped.
		if err := s.checkpointRetention(ctx, 8192); err != nil {
			return total, err
		}
		if err := retentionYield(ctx); err != nil {
			return total, err
		}
	}
}

func (s *Store) purgeBatch(ctx context.Context, cutoffNs int64, batchSize int) (int64, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE del AS SELECT trace_id FROM traces
		WHERE start_ns < ? AND end_ns < ?
		AND NOT EXISTS (SELECT 1 FROM spans WHERE spans.trace_id=traces.trace_id
			AND (end_ns <= 0 OR end_ns < start_ns)) LIMIT ?`, cutoffNs, cutoffNs, batchSize); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM spans WHERE trace_id IN (SELECT trace_id FROM del)"); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM traces WHERE trace_id IN (SELECT trace_id FROM del)")
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE del"); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}
