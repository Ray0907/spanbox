package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type ProxyToolCall struct {
	Key, TraceID, SpanID string
	EndNs, FirstResultNs int64
	HasResult            bool
	Name, Arguments      string
	CreatedNs            int64
}

func (s *Store) GetProxyToolCall(ctx context.Context, key string) (ProxyToolCall, bool, error) {
	call := ProxyToolCall{Key: key}
	err := s.r.QueryRowContext(ctx, `SELECT trace_id,span_id,end_ns,first_result_ns,has_result,name,arguments,created_ns FROM proxy_tool_calls WHERE key=?`, key).Scan(&call.TraceID, &call.SpanID, &call.EndNs, &call.FirstResultNs, &call.HasResult, &call.Name, &call.Arguments, &call.CreatedNs)
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyToolCall{}, false, nil
	}
	return call, err == nil, err
}

// InsertProxyBatch commits correlation and spans together, including the first result timestamp.
func (s *Store) InsertProxyBatch(ctx context.Context, spans []Span, calls []ProxyToolCall) error {
	return s.insertProxyBatch(ctx, spans, calls, nil)
}

func (s *Store) insertProxyBatch(ctx context.Context, spans []Span, calls []ProxyToolCall, turn *ProxyTurn) error {
	if len(calls) == 0 && turn == nil {
		_, err := s.InsertBatch(ctx, spans)
		return err
	}
	for _, span := range spans {
		if span.CostUSD != nil && !costInRange(*span.CostUSD) {
			return fmt.Errorf("%w: span %s", ErrCostOutOfRange, span.SpanID)
		}
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if turn != nil {
		root, err := upsertProxyTurn(ctx, tx, *turn)
		if err != nil {
			return err
		}
		spans = append(spans, root)
	}
	if _, err := insertBatch(ctx, tx, spans); err != nil {
		return err
	}
	for _, call := range calls {
		if _, err := tx.ExecContext(ctx, `INSERT INTO proxy_tool_calls (key,trace_id,span_id,end_ns,first_result_ns,has_result,name,arguments,created_ns)
   VALUES (?,?,?,?,?,?,?,?,?)
   ON CONFLICT(key) DO UPDATE SET first_result_ns=CASE WHEN proxy_tool_calls.has_result THEN proxy_tool_calls.first_result_ns ELSE excluded.first_result_ns END,
     has_result=proxy_tool_calls.has_result OR excluded.has_result`, call.Key, call.TraceID, call.SpanID, call.EndNs, call.FirstResultNs, call.HasResult, call.Name, call.Arguments, call.CreatedNs); err != nil {
			return err
		}
	}
	return tx.Commit()
}
