package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
)

type TraceFilter struct {
	FromNs        int64
	ToNs          int64
	Model         string
	Service       string
	ErrorsOnly    bool
	MinDurationMs float64
	SessionID     string
	UserID        string
	CursorStartNs int64
	CursorTraceID string
	Limit         int
}

type TraceRow struct {
	TraceID      string
	Name         string
	ServiceName  string
	Models       string
	SessionID    string
	UserID       string
	StartNs      int64
	EndNs        int64
	DurationMs   float64
	SpanCount    int
	LLMCount     int
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
	HasError     bool
}

func (s *Store) ListTraces(ctx context.Context, filter TraceFilter) ([]TraceRow, error) {
	query := `SELECT trace_id, name, service_name, models, session_id, user_id, start_ns, end_ns, duration_ms,
		span_count, llm_count, input_tokens, output_tokens, cost_usd, has_error FROM traces WHERE 1=1`
	var args []any
	if filter.FromNs != 0 {
		query += " AND start_ns >= ?"
		args = append(args, filter.FromNs)
	}
	if filter.ToNs != 0 {
		query += " AND start_ns < ?"
		args = append(args, filter.ToNs)
	}
	if filter.Model != "" {
		query += " AND models LIKE ?"
		args = append(args, "%"+filter.Model+"%")
	}
	if filter.Service != "" {
		query += " AND service_name LIKE ?"
		args = append(args, "%"+filter.Service+"%")
	}
	if filter.ErrorsOnly {
		query += " AND has_error = 1"
	}
	if filter.MinDurationMs > 0 {
		query += " AND duration_ms >= ?"
		args = append(args, filter.MinDurationMs)
	}
	if filter.SessionID != "" {
		query += " AND session_id = ?"
		args = append(args, filter.SessionID)
	}
	if filter.UserID != "" {
		query += " AND user_id = ?"
		args = append(args, filter.UserID)
	}
	if filter.CursorStartNs != 0 {
		query += " AND (start_ns, trace_id) < (?, ?)"
		args = append(args, filter.CursorStartNs, filter.CursorTraceID)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " ORDER BY start_ns DESC, trace_id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TraceRow
	for rows.Next() {
		var row TraceRow
		if err := rows.Scan(&row.TraceID, &row.Name, &row.ServiceName, &row.Models, &row.SessionID, &row.UserID,
			&row.StartNs, &row.EndNs, &row.DurationMs, &row.SpanCount, &row.LLMCount, &row.InputTokens, &row.OutputTokens,
			&row.CostUSD, &row.HasError); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) GetTrace(ctx context.Context, traceID string) (TraceRow, []Span, error) {
	var trace TraceRow
	err := s.r.QueryRowContext(ctx, `SELECT trace_id, name, service_name, models, session_id, user_id, start_ns, end_ns, duration_ms,
		span_count, llm_count, input_tokens, output_tokens, cost_usd, has_error FROM traces WHERE trace_id=?`, traceID).
		Scan(&trace.TraceID, &trace.Name, &trace.ServiceName, &trace.Models, &trace.SessionID, &trace.UserID,
			&trace.StartNs, &trace.EndNs, &trace.DurationMs, &trace.SpanCount, &trace.LLMCount, &trace.InputTokens, &trace.OutputTokens,
			&trace.CostUSD, &trace.HasError)
	if err != nil {
		return TraceRow{}, nil, err
	}
	rows, err := s.r.QueryContext(ctx, spanTreeSelect+" WHERE trace_id=? ORDER BY start_ns, span_id", traceID)
	if err != nil {
		return TraceRow{}, nil, err
	}
	defer rows.Close()
	var spans []Span
	for rows.Next() {
		span, err := scanTreeSpan(rows)
		if err != nil {
			return TraceRow{}, nil, err
		}
		spans = append(spans, span)
	}
	if err := rows.Err(); err != nil {
		return TraceRow{}, nil, err
	}
	return trace, spans, nil
}

func (s *Store) GetSpan(ctx context.Context, traceID, spanID string) (Span, error) {
	return scanSpan(s.r.QueryRowContext(ctx, spanSelect+" WHERE trace_id=? AND span_id=?", traceID, spanID))
}

const spanTreeSelect = `SELECT trace_id, span_id, parent_span_id, name, kind, start_ns, end_ns, duration_ms,
	request_model, response_model, input_tokens, output_tokens FROM spans`

func scanTreeSpan(row scanner) (Span, error) {
	var span Span
	err := row.Scan(&span.TraceID, &span.SpanID, &span.ParentSpanID, &span.Name, &span.Kind, &span.StartNs, &span.EndNs, &span.DurationMs,
		&span.RequestModel, &span.ResponseModel, &span.InputTokens, &span.OutputTokens)
	return span, err
}

const spanSelect = `SELECT trace_id, span_id, parent_span_id, name, kind, service_name, start_ns, end_ns,
	duration_ms, status_code, status_message, provider, request_model, response_model, input_tokens,
	output_tokens, cache_read_tokens, cost_usd, cost_source, input_content, output_content, tool_name,
	tool_call_id, finish_reason, session_id, user_id, trace_state, attributes, events, links, resource, scope FROM spans`

type scanner interface {
	Scan(dest ...any) error
}

func scanSpan(row scanner) (Span, error) {
	var span Span
	err := row.Scan(&span.TraceID, &span.SpanID, &span.ParentSpanID, &span.Name, &span.Kind, &span.ServiceName,
		&span.StartNs, &span.EndNs, &span.DurationMs, &span.StatusCode, &span.StatusMessage, &span.Provider,
		&span.RequestModel, &span.ResponseModel, &span.InputTokens, &span.OutputTokens, &span.CacheReadTokens,
		&span.CostUSD, &span.CostSource, &span.InputContent, &span.OutputContent, &span.ToolName, &span.ToolCallID,
		&span.FinishReason, &span.SessionID, &span.UserID, &span.TraceState, &span.Attributes, &span.Events,
		&span.Links, &span.Resource, &span.Scope)
	return span, err
}

type DashboardData struct {
	RangeFrom      int64
	RangeTo        int64
	TotalCost      *float64
	TotalInput     *int64
	TotalOutput    *int64
	TraceCount     int64
	ErrorRate      float64
	Days           []string
	DailyCost      []*float64
	DailyInput     []*int64
	DailyOutput    []*int64
	DailyErrorRate []float64
	DailyP50       []*float64
	DailyP95       []*float64
	Models         []ModelRow
}

type ModelRow struct {
	Model  string
	Calls  int64
	Input  *int64
	Output *int64
	Cost   *float64
	AvgMs  float64
}

func (s *Store) Dashboard(ctx context.Context, fromNs, toNs int64) (DashboardData, error) {
	data := DashboardData{RangeFrom: fromNs / 1e9, RangeTo: toNs / 1e9}
	err := s.r.QueryRowContext(ctx, `SELECT count(*), SUM(cost_usd), SUM(input_tokens), SUM(output_tokens),
		COALESCE(AVG(has_error), 0) FROM traces WHERE start_ns >= ? AND (? = 0 OR start_ns < ?)`, fromNs, toNs, toNs).
		Scan(&data.TraceCount, &data.TotalCost, &data.TotalInput, &data.TotalOutput, &data.ErrorRate)
	if err != nil {
		return DashboardData{}, err
	}
	rows, err := s.r.QueryContext(ctx, `SELECT strftime('%Y-%m-%d', start_ns/1e9, 'unixepoch') AS day,
		SUM(cost_usd), SUM(input_tokens), SUM(output_tokens), AVG(has_error)
		FROM traces WHERE start_ns >= ? AND (? = 0 OR start_ns < ?) GROUP BY day ORDER BY day`, fromNs, toNs, toNs)
	if err != nil {
		return DashboardData{}, err
	}
	for rows.Next() {
		var day string
		var cost sql.NullFloat64
		var input, output sql.NullInt64
		var errorRate float64
		if err := rows.Scan(&day, &cost, &input, &output, &errorRate); err != nil {
			rows.Close()
			return DashboardData{}, err
		}
		data.Days = append(data.Days, day)
		data.DailyCost = append(data.DailyCost, nullableFloat(cost))
		data.DailyInput = append(data.DailyInput, nullableInt(input))
		data.DailyOutput = append(data.DailyOutput, nullableInt(output))
		data.DailyErrorRate = append(data.DailyErrorRate, errorRate)
	}
	if err := rows.Close(); err != nil {
		return DashboardData{}, err
	}
	if err := rows.Err(); err != nil {
		return DashboardData{}, err
	}

	percentiles, err := s.dashboardPercentiles(ctx, fromNs, toNs)
	if err != nil {
		return DashboardData{}, err
	}
	for _, day := range data.Days {
		values := percentiles[day]
		data.DailyP50 = append(data.DailyP50, nullableFloat(values[0]))
		data.DailyP95 = append(data.DailyP95, nullableFloat(values[1]))
	}

	modelRows, err := s.r.QueryContext(ctx, `SELECT COALESCE(NULLIF(response_model,''), request_model) AS model,
		COUNT(*), SUM(input_tokens), SUM(output_tokens), SUM(cost_usd), AVG(duration_ms)
		FROM spans WHERE kind IN ('llm','embedding') AND start_ns >= ? AND (? = 0 OR start_ns < ?)
		AND COALESCE(NULLIF(response_model,''), request_model) <> '' GROUP BY model ORDER BY SUM(cost_usd) DESC, model`, fromNs, toNs, toNs)
	if err != nil {
		return DashboardData{}, err
	}
	defer modelRows.Close()
	for modelRows.Next() {
		var model ModelRow
		if err := modelRows.Scan(&model.Model, &model.Calls, &model.Input, &model.Output, &model.Cost, &model.AvgMs); err != nil {
			return DashboardData{}, err
		}
		data.Models = append(data.Models, model)
	}
	if err := modelRows.Err(); err != nil {
		return DashboardData{}, err
	}
	if err := validateDashboardCosts(data); err != nil {
		return DashboardData{}, err
	}
	return data, nil
}

func validateDashboardCosts(data DashboardData) error {
	costs := []*float64{data.TotalCost}
	costs = append(costs, data.DailyCost...)
	for _, model := range data.Models {
		costs = append(costs, model.Cost)
		if !finite(model.AvgMs) {
			return fmt.Errorf("non-finite dashboard duration")
		}
	}
	for _, cost := range costs {
		if cost != nil && !costInRange(*cost) {
			return fmt.Errorf("%w: dashboard aggregate", ErrCostOutOfRange)
		}
	}
	if !finite(data.ErrorRate) {
		return fmt.Errorf("non-finite dashboard error rate")
	}
	for _, value := range data.DailyErrorRate {
		if !finite(value) {
			return fmt.Errorf("non-finite dashboard aggregate")
		}
	}
	for _, values := range [][]*float64{data.DailyP50, data.DailyP95} {
		for _, value := range values {
			if value != nil && !finite(*value) {
				return fmt.Errorf("non-finite dashboard duration")
			}
		}
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func (s *Store) dashboardPercentiles(ctx context.Context, fromNs, toNs int64) (map[string][2]sql.NullFloat64, error) {
	rows, err := s.r.QueryContext(ctx, `WITH r AS (
		SELECT strftime('%Y-%m-%d', start_ns/1e9, 'unixepoch') AS day, duration_ms,
		ROW_NUMBER() OVER (PARTITION BY strftime('%Y-%m-%d', start_ns/1e9, 'unixepoch') ORDER BY duration_ms) AS rn,
		COUNT(*) OVER (PARTITION BY strftime('%Y-%m-%d', start_ns/1e9, 'unixepoch')) AS n
		FROM spans WHERE kind='llm' AND start_ns >= ? AND (? = 0 OR start_ns < ?)
	) SELECT day,
		MAX(CASE WHEN rn = (50*n+99)/100 THEN duration_ms END),
		MAX(CASE WHEN rn = (95*n+99)/100 THEN duration_ms END)
		FROM r GROUP BY day`, fromNs, toNs, toNs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][2]sql.NullFloat64)
	for rows.Next() {
		var day string
		var p50, p95 sql.NullFloat64
		if err := rows.Scan(&day, &p50, &p95); err != nil {
			return nil, err
		}
		result[day] = [2]sql.NullFloat64{p50, p95}
	}
	return result, rows.Err()
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

func nullableInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

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

func (s *Store) Search(ctx context.Context, phrase string, limit int) ([]SearchHit, error) {
	if phrase == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	match := `"` + strings.ReplaceAll(phrase, `"`, `""`) + `"`
	rows, err := s.r.QueryContext(ctx, `SELECT s.trace_id, s.span_id, t.name, s.name, s.kind,
		COALESCE(NULLIF(s.response_model,''), s.request_model), snippet(spans_fts, -1, '[', ']', '…', 16), s.start_ns
		FROM spans_fts JOIN spans s ON s.id=spans_fts.rowid JOIN traces t ON t.trace_id=s.trace_id
		WHERE spans_fts MATCH ? ORDER BY s.start_ns DESC, s.span_id DESC LIMIT ?`, match, limit)
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
