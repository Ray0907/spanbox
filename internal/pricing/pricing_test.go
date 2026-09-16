package pricing

import (
	"math"
	"strings"
	"testing"
)

const testPrices = `{
  "gpt-4o": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002, "cache_read_input_token_cost": 0.0000001, "cache_creation_input_token_cost": 0.00000125},
  "request-model": {"input_cost_per_token": 0.000003, "output_cost_per_token": 0.000004},
  "response-model": {"input_cost_per_token": 0.000005, "output_cost_per_token": 0.000006},
  "bedrock/anthropic.claude-3-5-sonnet-20240620-v1:0": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
  "vertex_ai/gemini-1.5-pro": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
  "gemini/gemini-3.6-flash": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
  "azure/gpt-4o-deployment": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
  "mistral/mistral-large": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
  "no-cache-price": {"input_cost_per_token": 0.000001, "output_cost_per_token": 0.000002},
  "missing-output": {"input_cost_per_token": 0.000001}
}`

func testTable(t *testing.T) *Table {
	t.Helper()
	table, err := loadFrom(strings.NewReader(testPrices))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func int64ptr(v int64) *int64 { return &v }

func requireCost(t *testing.T, got float64, ok bool, want float64) {
	t.Helper()
	if !ok || math.Abs(got-want) > 1e-12 {
		t.Fatalf("got (%g, %v), want (%g, true)", got, ok, want)
	}
}

func TestLoadRejectsTrailingData(t *testing.T) {
	if _, err := loadFrom(strings.NewReader(testPrices + `{}`)); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
}

func TestCostLookup(t *testing.T) {
	table := testTable(t)
	tests := []struct {
		name, provider, request, response string
	}{
		{name: "exact", request: "gpt-4o"},
		{name: "strip prefix", request: "openai/gpt-4o"},
		{name: "Bedrock alias", provider: "aws.bedrock", request: "anthropic.claude-3-5-sonnet-20240620-v1:0"},
		{name: "Vertex alias", provider: "gcp.vertex_ai", request: "gemini-1.5-pro"},
		{name: "Gemini alias", provider: "gcp.gen_ai", request: "gemini-3.6-flash"},
		{name: "Azure alias", provider: "azure.ai.openai", request: "gpt-4o-deployment"},
		{name: "Mistral alias", provider: "mistral_ai", request: "mistral-large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost, ok := table.Cost(tt.provider, tt.request, tt.response, int64ptr(1), int64ptr(1), nil, nil)
			requireCost(t, cost, ok, 0.000003)
		})
	}
}

func TestCostPrefersResponseModel(t *testing.T) {
	cost, ok := testTable(t).Cost("", "request-model", "response-model", int64ptr(1), int64ptr(1), nil, nil)
	requireCost(t, cost, ok, 0.000011)
}

func TestCostCacheRead(t *testing.T) {
	cost, ok := testTable(t).Cost("", "gpt-4o", "", int64ptr(1000), int64ptr(20), int64ptr(400), nil)
	requireCost(t, cost, ok, 600e-6+400*0.1e-6+20*2e-6)
}

func TestCostCacheReadFallsBackToInputPrice(t *testing.T) {
	cost, ok := testTable(t).Cost("", "no-cache-price", "", int64ptr(1000), int64ptr(20), int64ptr(400), nil)
	requireCost(t, cost, ok, 1000e-6+20*2e-6)
}

func TestCostUnknownOrIncompleteModel(t *testing.T) {
	for _, model := range []string{"unknown", "missing-output"} {
		if _, ok := testTable(t).Cost("", model, "", int64ptr(1), int64ptr(1), nil, nil); ok {
			t.Fatalf("expected %q not to resolve", model)
		}
	}
}

func TestCostRejectsNonFiniteOrExcessiveResult(t *testing.T) {
	table, err := loadFrom(strings.NewReader(`{"huge":{"input_cost_per_token":1e308,"output_cost_per_token":1e308}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := table.Cost("", "huge", "", int64ptr(1_000_000_000_000), int64ptr(1), nil, nil); ok {
		t.Fatal("expected excessive cost not to resolve")
	}
}

func TestCostRequiresInputAndOutputTokens(t *testing.T) {
	table := testTable(t)
	if _, ok := table.Cost("", "gpt-4o", "", nil, int64ptr(1), int64ptr(1), nil); ok {
		t.Fatal("expected missing input tokens not to price")
	}
	if _, ok := table.Cost("", "gpt-4o", "", int64ptr(1), nil, nil, nil); ok {
		t.Fatal("expected missing output tokens not to price")
	}
}

func TestCostCacheCreationFromCapture(t *testing.T) {
	table, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	cost, ok := table.Cost("anthropic", "claude-haiku-4-5-20251001", "", int64ptr(55_794), int64ptr(81), nil, int64ptr(55_784))
	requireCost(t, cost, ok, 10e-6+55_784*1.25e-6+81*5e-6)
}
