package store

import "context"

// ExportSpans streams spans matching filter, ignoring pagination.
func (s *Store) ExportSpans(ctx context.Context, filter TraceFilter, write func(Span) error) error {
	where, args := buildFilterWhere(filter, true)
	rows, err := s.r.QueryContext(ctx, spanSelect+` WHERE `+where+`
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
