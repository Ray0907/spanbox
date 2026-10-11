package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/store"
)

const eventsKeepalive = 15 * time.Second

type eventData struct {
	TraceID    string  `json:"trace_id"`
	SpanID     string  `json:"span_id"`
	StatusCode int32   `json:"status_code,omitempty"`
	DurationMs float64 `json:"duration_ms,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

type eventSubscription struct {
	minDuration *float64
	minCost     *float64
	queue       chan []byte
}

type eventBroadcaster struct {
	mu          sync.Mutex
	subscribers map[*eventSubscription]struct{}
}

func (b *eventBroadcaster) subscribe(minDuration, minCost *float64) *eventSubscription {
	sub := &eventSubscription{minDuration: minDuration, minCost: minCost, queue: make(chan []byte, 32)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribers == nil {
		b.subscribers = make(map[*eventSubscription]struct{})
	}
	b.subscribers[sub] = struct{}{}
	return sub
}

func (b *eventBroadcaster) unsubscribe(sub *eventSubscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, sub)
}

func (b *eventBroadcaster) interest() (active, cost bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subscribers {
		active = true
		cost = cost || sub.minCost != nil
	}
	return
}

func (b *eventBroadcaster) publish(kind string, data eventData) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", kind, body))
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subscribers {
		if kind == "slow" && (sub.minDuration == nil || data.DurationMs <= *sub.minDuration) ||
			kind == "cost" && (sub.minCost == nil || data.CostUSD <= *sub.minCost) {
			continue
		}
		select {
		case sub.queue <- frame:
		default: // Best effort: a slow client must never stall ingestion.
		}
	}
}

func (deps Deps) publishIngestEvents(ctx context.Context, spans []store.Span) {
	active, wantCost := deps.events.interest()
	if !active {
		return
	}
	traces := make(map[string]string)
	for _, span := range spans {
		if span.StatusCode == 2 {
			deps.events.publish("error", eventData{TraceID: span.TraceID, SpanID: span.SpanID, StatusCode: span.StatusCode})
		}
		deps.events.publish("slow", eventData{TraceID: span.TraceID, SpanID: span.SpanID, DurationMs: span.DurationMs})
		if wantCost {
			traces[span.TraceID] = span.SpanID
		}
	}
	for traceID, spanID := range traces {
		var cost sql.NullFloat64
		if err := deps.Store.Reader().QueryRowContext(ctx, "SELECT cost_usd FROM traces WHERE trace_id=?", traceID).Scan(&cost); err != nil {
			deps.Logf("events trace cost: %v", err)
			continue
		}
		if cost.Valid {
			deps.events.publish("cost", eventData{TraceID: traceID, SpanID: spanID, CostUSD: cost.Float64})
		}
	}
}

func (deps Deps) eventStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAgentError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := r.URL.Query()
	thresholds := make([]*float64, 2)
	for i, key := range []string{"min_duration", "min_cost"} {
		values, present := query[key]
		if !present {
			continue
		}
		value, err := strconv.ParseFloat(values[0], 64)
		if len(values) != 1 || err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			writeAgentError(w, http.StatusBadRequest, key+" must be a finite non-negative number")
			return
		}
		thresholds[i] = &value
	}
	if _, ok := w.(http.Flusher); !ok {
		writeAgentError(w, http.StatusInternalServerError, "streaming unavailable")
		return
	}
	sub := deps.events.subscribe(thresholds[0], thresholds[1])
	defer deps.events.unsubscribe(sub)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(frame []byte) error {
		// Refresh the ordinary server deadline for this long-lived response.
		if err := controller.SetWriteDeadline(time.Now().Add(config.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		if _, err := w.Write(frame); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write([]byte(": connected\n\n")); err != nil {
		return
	}
	ticker := time.NewTicker(eventsKeepalive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-sub.queue:
			if err := write(frame); err != nil {
				return
			}
		case <-ticker.C:
			if err := write([]byte(": keepalive\n\n")); err != nil {
				return
			}
		}
	}
}
