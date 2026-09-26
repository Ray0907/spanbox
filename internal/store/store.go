package store

import (
	"database/sql"
	"errors"
	"math"

	"github.com/Ray0907/spanbox/internal/config"
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

var ErrCostOutOfRange = errors.New("cost out of range")

func costInRange(cost float64) bool {
	return cost >= 0 && cost <= config.MaxCostUSD && !math.IsNaN(cost) && !math.IsInf(cost, 0)
}

type Store struct {
	w *sql.DB
	r *sql.DB
	u *sql.DB
}

func (s *Store) Reader() *sql.DB { return s.r }

func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.u.Close(), s.w.Close())
}
