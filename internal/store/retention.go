package store

import "context"

func (s *Store) Purge(ctx context.Context, cutoffNs int64) (int64, error) {
	return s.purge(ctx, cutoffNs, func(ctx context.Context) error {
		_, err := s.w.ExecContext(ctx, "PRAGMA incremental_vacuum(2000)")
		return err
	})
}

func (s *Store) purge(ctx context.Context, cutoffNs int64, vacuum func(context.Context) error) (int64, error) {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE del AS SELECT trace_id FROM traces WHERE start_ns < ?", cutoffNs); err != nil {
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
	return deleted, vacuum(ctx)
}
