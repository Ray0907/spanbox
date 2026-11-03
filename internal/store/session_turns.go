package store

import (
	"context"
	"unicode/utf8"
)

// SessionTurn is one trace of a session with the text of its agent root, if it has one.
type SessionTurn struct {
	TraceRow
	HasRoot bool
	Prompt  string
	Output  string
}

// ListSessionTurns returns a session's traces oldest first. Prompt and Output come from the
// trace's earliest parentless agent span and are cut to textChars characters; "…" marks a cut.
// A trace without such a span (orphan tool trace, pre-v4 session trace) keeps HasRoot false.
func (s *Store) ListSessionTurns(ctx context.Context, sessionID string, textChars, limit int) ([]SessionTurn, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT t.trace_id, t.name, t.service_name, t.models, t.session_id, t.user_id,
		t.start_ns, t.end_ns, t.duration_ms, t.span_count, t.llm_count, t.input_tokens, t.output_tokens, t.cost_usd, t.has_error,
		r.span_id IS NOT NULL, COALESCE(substr(r.input_content,1,?),''), COALESCE(substr(r.output_content,1,?),'')
		FROM traces t LEFT JOIN spans r ON r.span_id=(
			SELECT span_id FROM spans WHERE trace_id=t.trace_id AND kind='agent' AND parent_span_id=''
			ORDER BY start_ns, span_id LIMIT 1) AND r.trace_id=t.trace_id
		WHERE t.session_id=? ORDER BY t.start_ns, t.trace_id LIMIT ?`, textChars+1, textChars+1, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SessionTurn
	for rows.Next() {
		var turn SessionTurn
		if err := rows.Scan(&turn.TraceID, &turn.Name, &turn.ServiceName, &turn.Models, &turn.SessionID, &turn.UserID,
			&turn.StartNs, &turn.EndNs, &turn.DurationMs, &turn.SpanCount, &turn.LLMCount, &turn.InputTokens, &turn.OutputTokens,
			&turn.CostUSD, &turn.HasError, &turn.HasRoot, &turn.Prompt, &turn.Output); err != nil {
			return nil, err
		}
		turn.Prompt, turn.Output = clip(turn.Prompt, textChars), clip(turn.Output, textChars)
		result = append(result, turn)
	}
	return result, rows.Err()
}

// clip keeps the first n characters and marks the cut; the query reads n+1 so a cut is detectable.
func clip(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n]) + "…"
}
