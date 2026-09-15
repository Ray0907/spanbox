package store

import (
	"database/sql"
	"errors"
)

type Span struct {
	TraceID         string
	SpanID          string
	ParentSpanID    string
	Name            string
	Kind            string
	ServiceName     string
	StartNs         int64
	EndNs           int64
	DurationMs      float64
	StatusCode      int32
	StatusMessage   string
	Provider        string
	RequestModel    string
	ResponseModel   string
	InputTokens     *int64
	OutputTokens    *int64
	CacheReadTokens *int64
	CostUSD         *float64
	CostSource      string
	InputContent    string
	OutputContent   string
	ToolName        string
	ToolCallID      string
	FinishReason    string
	SessionID       string
	UserID          string
	TraceState      string
	Attributes      string
	Events          string
	Links           string
	Resource        string
	Scope           string
}

type Store struct {
	w *sql.DB
	r *sql.DB
}

func (s *Store) Reader() *sql.DB { return s.r }

func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}
