package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
)

const testPrices = `{"m": {"input_cost_per_token": 1, "output_cost_per_token": 2, "cache_read_input_token_cost": 0.5, "cache_creation_input_token_cost": 3}}`

func ptr[T any](v T) *T { return &v }

func TestCommitPricing(t *testing.T) {
	const creationAttrs = `{"gen_ai.usage.cache_creation.input_tokens":4}`
	tests := []struct {
		name       string
		source     Source
		kind       string
		input      *int64
		output     *int64
		creation   *int64
		cost       *float64
		attributes string
		want       *float64
	}{
		{name: "otlp llm", source: OTLP, kind: "llm", want: ptr(20.0)},
		{name: "otlp embedding", source: OTLP, kind: "embedding", want: ptr(20.0)},
		{name: "otlp tool", source: OTLP, kind: "tool"},
		{name: "import llm", source: Import, kind: "llm", want: ptr(20.0)},
		{name: "import embedding", source: Import, kind: "embedding", want: ptr(20.0)},
		{name: "import tool", source: Import, kind: "tool"},
		{name: "proxy llm", source: Proxy, kind: "llm", want: ptr(20.0)},
		{name: "proxy tool", source: Proxy, kind: "tool", want: ptr(20.0)},
		{name: "already costed", source: Proxy, kind: "llm", cost: ptr(9.0), want: ptr(9.0)},
		{name: "missing input tokens", source: Proxy, kind: "llm", input: ptr[int64](-1)},
		{name: "missing output tokens", source: Proxy, kind: "llm", output: ptr[int64](-1)},
		{name: "import cache creation from attributes", source: Import, kind: "llm", attributes: creationAttrs, want: ptr(28.0)},
		{name: "import column wins over attributes", source: Import, kind: "llm", creation: ptr[int64](0), attributes: creationAttrs, want: ptr(20.0)},
		{name: "proxy ignores attributes fallback", source: Proxy, kind: "llm", attributes: creationAttrs, want: ptr(20.0)},
		{name: "otlp ignores attributes fallback", source: OTLP, kind: "llm", attributes: creationAttrs, want: ptr(20.0)},
	}
	pricesPath := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(pricesPath, []byte(testPrices), 0o600); err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.Load(pricesPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := store.Span{
				TraceID: strings.Repeat("a", 32), SpanID: fmt.Sprintf("%016x", i+1), Name: tt.name, Kind: tt.kind, Provider: "openai", RequestModel: "m",
				InputTokens: ptr[int64](10), OutputTokens: ptr[int64](5), CacheCreationTokens: tt.creation, CostUSD: tt.cost,
				Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}",
			}
			if tt.cost != nil {
				span.CostSource = "reported"
			}
			if tt.attributes != "" {
				span.Attributes = tt.attributes
			}
			if tt.input != nil {
				span.InputTokens = nil
			}
			if tt.output != nil {
				span.OutputTokens = nil
			}
			published := 0
			p := Pipeline{Store: db, Pricing: prices, Publish: func([]store.Span, map[string]sql.NullFloat64) { published++ }}
			if err := p.Commit(context.Background(), tt.source, []store.Span{span}); err != nil {
				t.Fatal(err)
			}
			var cost sql.NullFloat64
			if err := db.Reader().QueryRow(`SELECT cost_usd FROM spans WHERE span_id = ?`, span.SpanID).Scan(&cost); err != nil {
				t.Fatal(err)
			}
			if (tt.want == nil) == cost.Valid || tt.want != nil && cost.Float64 != *tt.want {
				t.Fatalf("cost=%v, want %v", cost, tt.want)
			}
			if wantPublished := tt.source == OTLP; (published == 1) != wantPublished || published > 1 {
				t.Fatalf("published=%d for source %d", published, tt.source)
			}
		})
	}
}

func TestCommitRejectsUnknownSource(t *testing.T) {
	if err := (Pipeline{}).Commit(context.Background(), 0, nil); err == nil {
		t.Fatal("zero source accepted")
	}
}
