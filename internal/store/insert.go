package store

import (
	"context"
	"database/sql"
	"sort"
)

const insertSpanSQL = `
INSERT INTO spans (
  trace_id, span_id, parent_span_id, name, kind, service_name, start_ns, end_ns, duration_ms,
  status_code, status_message, provider, request_model, response_model, input_tokens, output_tokens,
  cache_read_tokens, cost_usd, cost_source, input_content, output_content, tool_name, tool_call_id,
  finish_reason, session_id, user_id, trace_state, attributes, events, links, resource, scope
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(trace_id, span_id) DO UPDATE SET
  trace_id=excluded.trace_id, span_id=excluded.span_id, parent_span_id=excluded.parent_span_id,
  name=excluded.name, kind=excluded.kind, service_name=excluded.service_name, start_ns=excluded.start_ns,
  end_ns=excluded.end_ns, duration_ms=excluded.duration_ms, status_code=excluded.status_code,
  status_message=excluded.status_message, provider=excluded.provider, request_model=excluded.request_model,
  response_model=excluded.response_model, input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
  cache_read_tokens=excluded.cache_read_tokens, cost_usd=excluded.cost_usd, cost_source=excluded.cost_source,
  input_content=excluded.input_content, output_content=excluded.output_content, tool_name=excluded.tool_name,
  tool_call_id=excluded.tool_call_id, finish_reason=excluded.finish_reason, session_id=excluded.session_id,
  user_id=excluded.user_id, trace_state=excluded.trace_state, attributes=excluded.attributes,
  events=excluded.events, links=excluded.links, resource=excluded.resource, scope=excluded.scope`

const recomputeTraceSQL = `
INSERT INTO traces (trace_id, name, service_name, start_ns, end_ns, duration_ms, span_count, llm_count,
                    input_tokens, output_tokens, cost_usd, has_error, session_id, user_id, models)
SELECT :tid,
  (SELECT name FROM spans WHERE trace_id=:tid ORDER BY (parent_span_id<>''), start_ns, span_id LIMIT 1),
  (SELECT service_name FROM spans WHERE trace_id=:tid ORDER BY (parent_span_id<>''), start_ns, span_id LIMIT 1),
  MIN(start_ns), MAX(end_ns), (MAX(end_ns)-MIN(start_ns))/1000000.0, COUNT(*), SUM(kind='llm'),
  CASE WHEN SUM(kind IN ('llm','embedding') AND input_tokens IS NULL)>0 OR SUM(kind IN ('llm','embedding'))=0 THEN NULL
       ELSE SUM(CASE WHEN kind IN ('llm','embedding') THEN input_tokens END) END,
  CASE WHEN SUM(kind IN ('llm','embedding') AND output_tokens IS NULL)>0 OR SUM(kind IN ('llm','embedding'))=0 THEN NULL
       ELSE SUM(CASE WHEN kind IN ('llm','embedding') THEN output_tokens END) END,
  CASE WHEN SUM(kind IN ('llm','embedding') AND cost_usd IS NULL)>0 OR SUM(kind IN ('llm','embedding'))=0 THEN NULL
       ELSE SUM(CASE WHEN kind IN ('llm','embedding') THEN cost_usd END) END,
  MAX(status_code=2),
  COALESCE((SELECT session_id FROM spans WHERE trace_id=:tid AND session_id<>'' ORDER BY start_ns, span_id LIMIT 1),''),
  COALESCE((SELECT user_id FROM spans WHERE trace_id=:tid AND user_id<>'' ORDER BY start_ns, span_id LIMIT 1),''),
  COALESCE((SELECT group_concat(m, ',') FROM (
     SELECT m FROM (
       SELECT COALESCE(NULLIF(response_model,''), request_model) AS m, start_ns, span_id,
              ROW_NUMBER() OVER (PARTITION BY COALESCE(NULLIF(response_model,''), request_model) ORDER BY start_ns, span_id) AS rn
       FROM spans WHERE trace_id=:tid AND COALESCE(NULLIF(response_model,''), request_model) <> '')
     WHERE rn=1 ORDER BY start_ns, span_id)),'')
FROM spans WHERE trace_id=:tid
ON CONFLICT(trace_id) DO UPDATE SET
  name=excluded.name, service_name=excluded.service_name, start_ns=excluded.start_ns, end_ns=excluded.end_ns,
  duration_ms=excluded.duration_ms, span_count=excluded.span_count, llm_count=excluded.llm_count,
  input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens, cost_usd=excluded.cost_usd,
  has_error=excluded.has_error, session_id=excluded.session_id, user_id=excluded.user_id, models=excluded.models`

func (s *Store) InsertBatch(ctx context.Context, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, insertSpanSQL)
	if err != nil {
		return err
	}
	defer statement.Close()
	traceSet := make(map[string]struct{})
	for _, span := range spans {
		if _, err := statement.ExecContext(ctx,
			span.TraceID, span.SpanID, span.ParentSpanID, span.Name, span.Kind, span.ServiceName,
			span.StartNs, span.EndNs, span.DurationMs, span.StatusCode, span.StatusMessage, span.Provider,
			span.RequestModel, span.ResponseModel, span.InputTokens, span.OutputTokens, span.CacheReadTokens,
			span.CostUSD, span.CostSource, span.InputContent, span.OutputContent, span.ToolName, span.ToolCallID,
			span.FinishReason, span.SessionID, span.UserID, span.TraceState, span.Attributes, span.Events,
			span.Links, span.Resource, span.Scope,
		); err != nil {
			return err
		}
		traceSet[span.TraceID] = struct{}{}
	}
	traceIDs := make([]string, 0, len(traceSet))
	for traceID := range traceSet {
		traceIDs = append(traceIDs, traceID)
	}
	sort.Strings(traceIDs)
	for _, traceID := range traceIDs {
		if _, err := tx.ExecContext(ctx, recomputeTraceSQL, sql.Named("tid", traceID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
