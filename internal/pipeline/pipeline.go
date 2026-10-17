package pipeline

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Ray0907/spanbox/internal/normalize"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
)

// Source is one ingest entry point. The zero value is not a source.
type Source int

const (
	OTLP Source = iota + 1
	Proxy
	Import
)

// Publisher is called with the trace costs InsertBatch already read.
type Publisher func([]store.Span, map[string]sql.NullFloat64)

type Pipeline struct {
	Store   *store.Store
	Pricing *pricing.Table
	Publish Publisher
}

func (p Pipeline) Commit(ctx context.Context, source Source, spans []store.Span) error {
	switch source {
	case OTLP, Proxy, Import:
	default:
		return fmt.Errorf("unknown ingest source %d", source)
	}
	if p.Pricing != nil {
		for i := range spans {
			if !price(source, spans[i]) {
				continue
			}
			creation := spans[i].CacheCreationTokens
			if creation == nil && source == Import {
				creation = normalize.ParseCacheCreation(spans[i].Attributes)
			}
			cost, ok := p.Pricing.Cost(spans[i].Provider, spans[i].RequestModel, spans[i].ResponseModel, spans[i].InputTokens, spans[i].OutputTokens, spans[i].CacheReadTokens, creation)
			if !ok {
				continue
			}
			spans[i].CostUSD = &cost
			spans[i].CostSource = "pricing"
		}
	}
	costs, err := p.Store.InsertBatch(ctx, spans)
	if err != nil {
		return err
	}
	if source == OTLP && p.Publish != nil {
		p.Publish(spans, costs)
	}
	return nil
}

func price(source Source, span store.Span) bool {
	if span.CostUSD != nil || span.InputTokens == nil || span.OutputTokens == nil {
		return false
	}
	if source == Proxy {
		return true
	}
	return span.Kind == "llm" || span.Kind == "embedding"
}
