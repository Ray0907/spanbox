package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ProxyTurn persists both deterministic identity and the aggregate root span.
type ProxyTurn struct {
	Key, Route, ScopeSession string
	Root                     Span
	CreatedNs                int64
}

func (s *Store) GetProxyTurn(ctx context.Context, key, route, session string) (ProxyTurn, bool, error) {
	query := `SELECT key,route,scope_session,root,created_ns FROM proxy_turns WHERE key=?`
	args := []any{key}
	if key == "" {
		query = `SELECT key,route,scope_session,root,created_ns FROM proxy_turns WHERE route=? AND scope_session=? ORDER BY start_ns DESC,key DESC LIMIT 1`
		args = []any{route, session}
	}
	return scanProxyTurn(s.r.QueryRowContext(ctx, query, args...))
}

func scanProxyTurn(row *sql.Row) (ProxyTurn, bool, error) {
	var turn ProxyTurn
	var root string
	err := row.Scan(&turn.Key, &turn.Route, &turn.ScopeSession, &root, &turn.CreatedNs)
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyTurn{}, false, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(root), &turn.Root)
	}
	return turn, err == nil, err
}

// Merge under the writer transaction, not a handler's possibly stale read.
func upsertProxyTurn(ctx context.Context, tx *sql.Tx, turn ProxyTurn) (Span, error) {
	// Acquire the writer lock even for a new turn, before a read-to-write upgrade can fail.
	if _, err := tx.ExecContext(ctx, "UPDATE proxy_turns SET start_ns=start_ns WHERE key=?", turn.Key); err != nil {
		return Span{}, err
	}
	previous, found, err := scanProxyTurn(tx.QueryRowContext(ctx, `SELECT key,route,scope_session,root,created_ns FROM proxy_turns WHERE key=?`, turn.Key))
	if err != nil {
		return Span{}, err
	}
	root := turn.Root
	if found {
		root = previous.Root
		root.StartNs = min(root.StartNs, turn.Root.StartNs)
		if turn.Root.EndNs >= root.EndNs {
			root.OutputContent = turn.Root.OutputContent
		}
		root.EndNs = max(root.EndNs, turn.Root.EndNs)
		if turn.Root.StatusCode == 2 {
			root.StatusCode = 2
			root.StatusMessage = turn.Root.StatusMessage
		}
	}
	root.DurationMs = float64(root.EndNs-root.StartNs) / 1e6
	data, err := json.Marshal(root)
	if err != nil {
		return Span{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO proxy_turns (key,route,scope_session,start_ns,end_ns,root,created_ns) VALUES (?,?,?,?,?,?,?)
 ON CONFLICT(key) DO UPDATE SET start_ns=excluded.start_ns,end_ns=excluded.end_ns,root=excluded.root`, turn.Key, turn.Route, turn.ScopeSession, root.StartNs, root.EndNs, string(data), turn.CreatedNs)
	return root, err
}

// InsertProxyTurnBatch commits the root, models, tools and correlation atomically.
func (s *Store) InsertProxyTurnBatch(ctx context.Context, spans []Span, calls []ProxyToolCall, turn *ProxyTurn) error {
	return s.insertProxyBatch(ctx, spans, calls, turn)
}
