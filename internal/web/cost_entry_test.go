package web

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/proxy"
	"github.com/Ray0907/spanbox/internal/store"
)

func TestSameUsageCostsTheSame(t *testing.T) {
	responseBody, err := os.ReadFile("../proxy/testdata/anthropic_stream.txt")
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := os.ReadFile("../proxy/testdata/anthropic_request.json")
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	capture, err := proxy.New(proxy.Config{AnthropicUpstream: upstream.URL, Store: database, Pricing: prices, Version: "test", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Deps{Cfg: config.Config{Port: 4318, RetentionDays: 30}, Store: database, Pricing: prices, Version: "test", Logf: t.Logf, Proxy: capture})

	otlpBody, err := json.Marshal(map[string]any{
		"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId": base64.StdEncoding.EncodeToString(bytesOf(0xbb, 16)),
			"spanId":  base64.StdEncoding.EncodeToString(bytesOf(0xcc, 8)),
			"name":    "chat claude-haiku-4-5-20251001",
			"startTimeUnixNano": "1000000000", "endTimeUnixNano": "2000000000",
			"attributes": []any{
				attr("stringValue", "gen_ai.operation.name", "chat"),
				attr("stringValue", "gen_ai.provider.name", "anthropic"),
				attr("stringValue", "gen_ai.request.model", "claude-haiku-4-5-20251001"),
				attr("intValue", "gen_ai.usage.input_tokens", "55794"),
				attr("intValue", "gen_ai.usage.output_tokens", "81"),
				attr("intValue", "gen_ai.usage.cache_creation.input_tokens", "55784"),
			},
		}}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", otlpBody); response.Code != http.StatusOK {
		t.Fatalf("otlp status=%d body=%s", response.Code, response.Body.Bytes())
	}
	proxyReq := httptest.NewRequest(http.MethodPost, "/proxy/anthropic/v1/messages", bytes.NewReader(requestBody))
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyReq)
	capture.Wait()
	if proxyResponse.Code != http.StatusOK {
		t.Fatalf("proxy status=%d", proxyResponse.Code)
	}

	rows, err := database.Reader().Query(`SELECT cost_usd FROM spans WHERE kind='llm' AND request_model=? ORDER BY trace_id`, "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var costs []float64
	for rows.Next() {
		var cost float64
		if err := rows.Scan(&cost); err != nil {
			t.Fatal(err)
		}
		costs = append(costs, cost)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := 10e-6 + 55_784*1.25e-6 + 81*5e-6
	if len(costs) != 2 || math.Abs(costs[0]-want) > 1e-12 || math.Abs(costs[1]-want) > 1e-12 {
		t.Fatalf("costs=%v want %g from both entry points", costs, want)
	}
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func attr(kind, key, value string) map[string]any {
	return map[string]any{"key": key, "value": map[string]any{kind: value}}
}
