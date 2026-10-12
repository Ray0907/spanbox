# Update Model Pricing

spanbox prices spans with LiteLLM's `model_prices_and_context_window.json`. The file lives at `internal/pricing/model_prices.json` and is embedded into the binary by `internal/pricing/pricing.go` with `//go:embed`.

`cost_source` on each span records where its cost came from: `explicit` when the span carried a cost, `pricing` when spanbox computed it from the table.

## Update the embedded table

```sh
curl -o internal/pricing/model_prices.json \
  https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
```

Rebuild spanbox after updating the file.

## Replace the table at runtime

Set `PRICING_FILE` to a LiteLLM-format JSON file. It replaces the embedded table.

```sh
PRICING_FILE=/etc/spanbox/prices.json ./spanbox
```

Stored costs are not recomputed when the table changes.
