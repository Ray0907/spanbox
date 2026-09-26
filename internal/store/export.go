package store

import "context"

// ExportSpans streams all spans belonging to traces matching filter, ignoring pagination.
func (s *Store) ExportSpans(ctx context.Context, filter TraceFilter, write func(Span) error) error {
	where, args := buildTraceWhere(filter)
	rows, err := s.r.QueryContext(ctx, spanSelect+` WHERE trace_id IN (SELECT trace_id FROM traces WHERE `+where+`)
		ORDER BY trace_id, start_ns, span_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		span, err := scanSpan(rows)
		if err != nil {
			return err
		}
		if err := write(span); err != nil {
			return err
		}
	}
	return rows.Err()
}
