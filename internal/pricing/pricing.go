package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/Ray0907/spanbox/internal/config"
)

//go:embed model_prices.json
var embeddedPrices []byte

type price struct {
	Input         *float64 `json:"input_cost_per_token"`
	Output        *float64 `json:"output_cost_per_token"`
	CacheRead     *float64 `json:"cache_read_input_token_cost"`
	CacheCreation *float64 `json:"cache_creation_input_token_cost"`
}

type Table struct {
	prices map[string]price
}

var providerAliases = map[string]string{
	"aws.bedrock":        "bedrock",
	"gcp.vertex_ai":      "vertex_ai",
	"gcp.gemini":         "gemini",
	"gcp.gen_ai":         "gemini",
	"azure.ai.openai":    "azure",
	"azure.ai.inference": "azure_ai",
	"mistral_ai":         "mistral",
	"anthropic":          "anthropic",
	"openai":             "openai",
	"cohere":             "cohere",
	"groq":               "groq",
}

func Load(overridePath string) (*Table, error) {
	if overridePath == "" {
		return loadFrom(strings.NewReader(string(embeddedPrices)))
	}
	file, err := os.Open(overridePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return loadFrom(file)
}

func loadFrom(r io.Reader) (*Table, error) {
	decoder := json.NewDecoder(r)
	var all map[string]price
	if err := decoder.Decode(&all); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected trailing JSON value")
		}
		return nil, err
	}
	prices := make(map[string]price, len(all))
	for model, p := range all {
		if p.Input != nil && p.Output != nil {
			prices[model] = p
		}
	}
	return &Table{prices: prices}, nil
}

func (t *Table) Cost(provider, requestModel, responseModel string, input, output, cacheRead, cacheCreation *int64) (float64, bool) {
	if input == nil || output == nil {
		return 0, false
	}
	p, ok := t.lookup(provider, requestModel, responseModel)
	if !ok {
		return 0, false
	}
	cached := int64(0)
	if cacheRead != nil {
		cached = *cacheRead
	}
	created := int64(0)
	if cacheCreation != nil {
		created = *cacheCreation
	}
	uncached := max(*input-cached-created, 0)
	cachePrice := *p.Input
	if p.CacheRead != nil {
		cachePrice = *p.CacheRead
	}
	creationPrice := *p.Input
	if p.CacheCreation != nil {
		creationPrice = *p.CacheCreation
	}
	cost := float64(uncached)**p.Input + float64(cached)*cachePrice + float64(created)*creationPrice + float64(*output)**p.Output
	if cost < 0 || cost > config.MaxCostUSD || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return 0, false
	}
	return cost, true
}

func (t *Table) lookup(provider, requestModel, responseModel string) (price, bool) {
	models := []string{responseModel, requestModel}
	for _, model := range models {
		if p, ok := t.prices[model]; ok && model != "" {
			return p, true
		}
	}
	for _, model := range models {
		if stripped := stripPrefix(model); stripped != model {
			if p, ok := t.prices[stripped]; ok {
				return p, true
			}
		}
	}
	alias := providerAliases[provider]
	if alias == "" {
		alias = provider
	}
	if alias != "" {
		for _, model := range models {
			if model != "" {
				if p, ok := t.prices[alias+"/"+stripPrefix(model)]; ok {
					return p, true
				}
			}
		}
	}
	return price{}, false
}

func stripPrefix(model string) string {
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		return model[i+1:]
	}
	return model
}
