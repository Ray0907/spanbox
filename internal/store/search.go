package store

import (
	"context"
	"strings"
)

type SearchHit struct {
	TraceID   string
	SpanID    string
	TraceName string
	SpanName  string
	Kind      string
	Model     string
	Snippet   string
	StartNs   int64
}

func (s *Store) Search(ctx context.Context, phrase string, limit int, cursor ...SearchHit) ([]SearchHit, error) {
	if phrase == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	match := `"` + strings.ReplaceAll(phrase, `"`, `""`) + `"`
	// Pick the page before loading bodies or computing snippets. A broad MATCH
	// must not evaluate snippet() on every matching span just to sort its date.
	query := `WITH page AS MATERIALIZED (
		SELECT s.id, s.start_ns, s.span_id FROM spans s INDEXED BY spans_search
		WHERE s.id IN (SELECT rowid FROM spans_fts WHERE spans_fts MATCH ?)`
	args := []any{match}
	if len(cursor) != 0 {
		if cursor[0].TraceID != "" {
			query += ` AND (s.start_ns, s.span_id, s.trace_id) < (?, ?, ?)`
			args = append(args, cursor[0].StartNs, cursor[0].SpanID, cursor[0].TraceID)
		} else {
			// Accept legacy cursors that did not include the trace ID.
			query += ` AND (s.start_ns, s.span_id) < (?, ?)`
			args = append(args, cursor[0].StartNs, cursor[0].SpanID)
		}
	}
	query += ` ORDER BY s.start_ns DESC, s.span_id DESC, s.trace_id DESC LIMIT ?)
		SELECT s.trace_id, s.span_id, t.name, s.name, s.kind,
		COALESCE(NULLIF(s.response_model,''), s.request_model), snippet(spans_fts, -1, '[', ']', '…', 16), s.start_ns
		FROM page p CROSS JOIN spans_fts ON spans_fts.rowid=p.id
		JOIN spans s ON s.id=p.id JOIN traces t ON t.trace_id=s.trace_id
		WHERE spans_fts MATCH ? ORDER BY s.start_ns DESC, s.span_id DESC, s.trace_id DESC`
	args = append(args, limit, match)
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SearchHit
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.TraceID, &hit.SpanID, &hit.TraceName, &hit.SpanName, &hit.Kind, &hit.Model, &hit.Snippet, &hit.StartNs); err != nil {
			return nil, err
		}
		result = append(result, hit)
	}
	return result, rows.Err()
}

// SearchRanked returns the top limit matches by FTS5 bm25 relevance, ranked over
// every match. Scores shift as the corpus changes, so there is no cursor.
func (s *Store) SearchRanked(ctx context.Context, phrase string, limit int) ([]SearchHit, error) {
	if phrase == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	match := `"` + strings.ReplaceAll(phrase, `"`, `""`) + `"`
	// Same shape as Search: pick the page first so snippet() runs only on it.
	// The page carries its rank: ordering by spans_fts.rank outside would make
	// bm25 scan every match a second time. rowid breaks score ties, so the page
	// boundary never depends on scan order.
	rows, err := s.r.QueryContext(ctx, `WITH page AS MATERIALIZED (
		SELECT rowid AS id, rank AS r FROM spans_fts WHERE spans_fts MATCH ?1 ORDER BY rank, rowid LIMIT ?2)
		SELECT s.trace_id, s.span_id, t.name, s.name, s.kind,
		COALESCE(NULLIF(s.response_model,''), s.request_model), snippet(spans_fts, -1, '[', ']', '…', 16), s.start_ns
		FROM page p CROSS JOIN spans_fts ON spans_fts.rowid=p.id
		JOIN spans s ON s.id=p.id JOIN traces t ON t.trace_id=s.trace_id
		WHERE spans_fts MATCH ?1 ORDER BY p.r, s.id`, match, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SearchHit
	for rows.Next() {
		var hit SearchHit
		if err := rows.Scan(&hit.TraceID, &hit.SpanID, &hit.TraceName, &hit.SpanName, &hit.Kind, &hit.Model, &hit.Snippet, &hit.StartNs); err != nil {
			return nil, err
		}
		result = append(result, hit)
	}
	return result, rows.Err()
}
