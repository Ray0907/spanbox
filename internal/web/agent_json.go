package web

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Ray0907/spanbox/internal/store"
)

func writeAgentJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAgentError(w http.ResponseWriter, status int, message string) {
	writeAgentJSON(w, status, map[string]string{"error": message})
}

func agentNumber(text string, fallback, maximum int) (int, error) {
	if text == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid number")
	}
	if maximum > 0 {
		n = min(n, maximum)
	}
	return n, nil
}

func agentLimit(r *http.Request) (int, error) {
	n, err := agentNumber(r.URL.Query().Get("limit"), 20, 50)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	return n, nil
}

func agentCursor(value string, idBytes int) (int64, string, error) {
	start, id, found := strings.Cut(value, ":")
	stamp, err := strconv.ParseInt(start, 10, 64)
	if !found || err != nil || len(id) != idBytes*2 {
		return 0, "", fmt.Errorf("invalid cursor")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return 0, "", fmt.Errorf("invalid cursor")
	}
	return stamp, id, nil
}

func agentPage[T any](rows []T, limit int, cursor func(T) string) map[string]any {
	var next any
	if len(rows) > limit {
		rows = rows[:limit]
		next = cursor(rows[len(rows)-1])
	}
	return map[string]any{"items": rows, "next_cursor": next}
}

func agentTraceSummary(row store.TraceRow) map[string]any {
	return map[string]any{
		"trace_id": row.TraceID, "name": row.Name, "service": row.ServiceName, "models": row.Models,
		"session_id": row.SessionID, "user_id": row.UserID, "start_ns": row.StartNs, "end_ns": row.EndNs,
		"duration_ms": row.DurationMs, "span_count": row.SpanCount, "llm_count": row.LLMCount,
		"input_tokens": row.InputTokens, "output_tokens": row.OutputTokens, "cost_usd": row.CostUSD, "has_error": row.HasError,
	}
}

func (deps Deps) agentTraces(w http.ResponseWriter, r *http.Request) {
	limit, err := agentLimit(r)
	if err != nil {
		writeAgentError(w, 400, err.Error())
		return
	}
	filter, _, err := traceFilter(r)
	if err != nil {
		writeAgentError(w, 400, err.Error())
		return
	}
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		if _, _, err := agentCursor(cursor, 16); err != nil {
			writeAgentError(w, 400, err.Error())
			return
		}
	}
	filter.Limit = limit + 1
	rows, err := deps.Store.ListTraces(r.Context(), filter)
	if err != nil {
		writeAgentError(w, 500, err.Error())
		return
	}
	page := agentPage(rows, limit, func(row store.TraceRow) string { return fmt.Sprintf("%d:%s", row.StartNs, row.TraceID) })
	items := make([]map[string]any, 0, min(len(rows), limit))
	for _, row := range rows[:min(len(rows), limit)] {
		items = append(items, agentTraceSummary(row))
	}
	page["items"] = items
	writeAgentJSON(w, 200, page)
}

func (deps Deps) agentSessions(w http.ResponseWriter, r *http.Request) {
	limit, err := agentLimit(r)
	if err != nil {
		writeAgentError(w, 400, err.Error())
		return
	}
	q := r.URL.Query()
	from, to, err := parseRange(q.Get("range"), q.Get("from"), q.Get("to"))
	if err != nil {
		writeAgentError(w, 400, err.Error())
		return
	}
	filter := store.SessionFilter{FromNs: from, ToNs: to, Limit: limit + 1}
	if cursor := q.Get("cursor"); cursor != "" {
		last, id, ok := strings.Cut(cursor, ":")
		if !ok || id == "" {
			writeAgentError(w, 400, "invalid cursor")
			return
		}
		filter.CursorLastNs, err = strconv.ParseInt(last, 10, 64)
		if err != nil {
			writeAgentError(w, 400, "invalid cursor")
			return
		}
		filter.CursorSessionID = id
	}
	rows, err := deps.Store.ListSessions(r.Context(), filter)
	if err != nil {
		writeAgentError(w, 500, err.Error())
		return
	}
	page := agentPage(rows, limit, func(row store.SessionRow) string { return fmt.Sprintf("%d:%s", row.LastNs, row.SessionID) })
	items := make([]map[string]any, 0, min(len(rows), limit))
	for _, row := range rows[:min(len(rows), limit)] {
		items = append(items, map[string]any{
			"session_id": row.SessionID, "service": row.ServiceName, "first_ns": row.FirstNs, "last_ns": row.LastNs,
			"trace_count": row.TraceCount, "llm_count": row.LLMCount, "input_tokens": row.InputTokens,
			"output_tokens": row.OutputTokens, "cost_usd": row.CostUSD, "models": row.Models,
		})
	}
	page["items"] = items
	writeAgentJSON(w, 200, page)
}

func (deps Deps) agentSearch(w http.ResponseWriter, r *http.Request) {
	limit, err := agentLimit(r)
	if err != nil {
		writeAgentError(w, 400, err.Error())
		return
	}
	var cursor []store.SearchHit
	if text := r.URL.Query().Get("cursor"); text != "" {
		start, spanID, err := agentCursor(text, 8)
		if err != nil {
			writeAgentError(w, 400, err.Error())
			return
		}
		cursor = []store.SearchHit{{StartNs: start, SpanID: spanID}}
	}
	rows, err := deps.Store.Search(r.Context(), r.URL.Query().Get("q"), limit+1, cursor...)
	if err != nil {
		writeAgentError(w, 500, err.Error())
		return
	}
	page := agentPage(rows, limit, func(row store.SearchHit) string { return fmt.Sprintf("%d:%s", row.StartNs, row.SpanID) })
	items := make([]map[string]any, 0, min(len(rows), limit))
	for _, row := range rows[:min(len(rows), limit)] {
		items = append(items, map[string]any{
			"trace_id": row.TraceID, "span_id": row.SpanID, "trace_name": row.TraceName,
			"span_name": row.SpanName, "kind": row.Kind, "model": row.Model, "snippet": row.Snippet, "start_ns": row.StartNs,
		})
	}
	page["items"] = items
	writeAgentJSON(w, 200, page)
}

func (deps Deps) agentTrace(w http.ResponseWriter, r *http.Request, traceID string) {
	trace, _, err := deps.Store.GetTrace(r.Context(), traceID)
	if err != nil {
		agentStoreError(w, err)
		return
	}
	spans, err := deps.Store.ListTraceAgentSpans(r.Context(), traceID)
	if err != nil {
		agentStoreError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(spans))
	for _, row := range spans {
		span := row.Span
		model := span.ResponseModel
		if model == "" {
			model = span.RequestModel
		}
		items = append(items, map[string]any{
			"span_id": span.SpanID, "parent_span_id": span.ParentSpanID, "name": span.Name, "kind": span.Kind,
			"model": model, "start_offset_ms": float64(span.StartNs-trace.StartNs) / 1e6,
			"duration_ms": span.DurationMs, "input_tokens": span.InputTokens, "output_tokens": span.OutputTokens,
			"cost_usd": span.CostUSD, "status_code": span.StatusCode,
			"input_chars": row.InputChars, "output_chars": row.OutputChars,
		})
	}
	result := agentTraceSummary(trace)
	result["spans"] = items
	writeAgentJSON(w, 200, result)
}

func agentStoreError(w http.ResponseWriter, err error) {
	if err == sql.ErrNoRows {
		writeAgentError(w, 404, "not found")
		return
	}
	writeAgentError(w, 500, err.Error())
}

type agentWindow struct {
	Text        string `json:"text"`
	TotalChars  int    `json:"total_chars"`
	NextOffset  *int   `json:"next_offset"`
	InvalidUTF8 bool   `json:"invalid_utf8,omitempty"`
}

func textWindow(text string, offset, length int) agentWindow {
	runes := []rune(text)
	window := agentWindow{TotalChars: len(runes), InvalidUTF8: !utf8.ValidString(text)}
	if offset >= len(runes) {
		return window
	}
	end := offset + min(length, len(runes)-offset)
	window.Text = string(runes[offset:end])
	if end < len(runes) {
		window.NextOffset = &end
	}
	return window
}

func (deps Deps) agentSpan(w http.ResponseWriter, r *http.Request, traceID, spanID string) {
	q := r.URL.Query()
	field := q.Get("field")
	if field != "" && field != "input" && field != "output" && field != "attributes" {
		writeAgentError(w, 400, "invalid field")
		return
	}
	offset, err := agentNumber(q.Get("offset"), 0, 0)
	if err != nil {
		writeAgentError(w, 400, "invalid offset")
		return
	}
	length, err := agentNumber(q.Get("len"), 2000, 20000)
	if err != nil || length == 0 {
		writeAgentError(w, 400, "invalid len")
		return
	}
	span, err := deps.Store.GetSpan(r.Context(), traceID, spanID)
	if err != nil {
		agentStoreError(w, err)
		return
	}
	trace, _, err := deps.Store.GetTrace(r.Context(), traceID)
	if err != nil {
		agentStoreError(w, err)
		return
	}
	model := span.ResponseModel
	if model == "" {
		model = span.RequestModel
	}
	result := map[string]any{
		"trace_id": traceID, "span_id": span.SpanID, "parent_span_id": span.ParentSpanID,
		"name": span.Name, "kind": span.Kind, "model": model, "provider": span.Provider,
		"start_offset_ms": float64(span.StartNs-trace.StartNs) / 1e6, "duration_ms": span.DurationMs,
		"input_tokens": span.InputTokens, "output_tokens": span.OutputTokens, "cost_usd": span.CostUSD,
		"status_code": span.StatusCode, "status_message": span.StatusMessage,
	}
	for name, text := range map[string]string{"input": span.InputContent, "output": span.OutputContent, "attributes": span.Attributes} {
		if field == "" || field == name {
			result[name] = textWindow(text, offset, length)
		}
	}
	writeAgentJSON(w, 200, result)
}
