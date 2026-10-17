package web

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	events := openEvents(t, server, "/events?min_cost=0", "", "", 5*time.Second)

	otlpBody, err := json.Marshal(map[string]any{
		"resourceSpans": []any{map[string]any{"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId":           base64.StdEncoding.EncodeToString(bytesOf(0xbb, 16)),
			"spanId":            base64.StdEncoding.EncodeToString(bytesOf(0xcc, 8)),
			"name":              "chat claude-haiku-4-5-20251001",
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
	assertCostEvent(t, events, strings.Repeat("b", 32), 10e-6+55_784*1.25e-6+81*5e-6)
	proxyReq := httptest.NewRequest(http.MethodPost, "/proxy/anthropic/v1/messages", bytes.NewReader(requestBody))
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(proxyResponse, proxyReq)
	capture.Wait()
	if proxyResponse.Code != http.StatusOK {
		t.Fatalf("proxy status=%d", proxyResponse.Code)
	}
	input, output := int64(55_794), int64(81)
	imported := store.Span{
		TraceID: strings.Repeat("d", 32), SpanID: strings.Repeat("e", 16),
		Name: "chat claude-haiku-4-5-20251001", Kind: "llm", ServiceName: "import",
		StartNs: 3_000_000_000, EndNs: 4_000_000_000, DurationMs: 1000,
		Provider: "anthropic", RequestModel: "claude-haiku-4-5-20251001",
		InputTokens: &input, OutputTokens: &output,
		Attributes: `{"gen_ai.usage.cache_creation.input_tokens":55784}`,
		Events:     "[]", Links: "[]", Resource: "{}", Scope: "{}",
	}
	importedLine, err := json.Marshal(imported)
	if err != nil {
		t.Fatal(err)
	}
	if response := request(t, handler, http.MethodPost, "/import", "application/x-ndjson", "", importedLine); response.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", response.Code, response.Body.Bytes())
	}
	pinnedCost := 0.01
	pinned := imported
	pinned.TraceID, pinned.SpanID = strings.Repeat("a", 32), strings.Repeat("1", 16)
	pinned.CostUSD = &pinnedCost
	pinnedLine, err := json.Marshal(pinned)
	if err != nil {
		t.Fatal(err)
	}
	if response := request(t, handler, http.MethodPost, "/import", "application/x-ndjson", "", pinnedLine); response.Code != http.StatusOK {
		t.Fatalf("priced import status=%d body=%s", response.Code, response.Body.Bytes())
	}
	expectNoEvent(t, events)

	rows, err := database.Reader().Query(`SELECT trace_id, cost_usd FROM spans WHERE request_model=? ORDER BY trace_id`, "claude-haiku-4-5-20251001")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]float64{}
	for rows.Next() {
		var traceID string
		var cost float64
		if err := rows.Scan(&traceID, &cost); err != nil {
			t.Fatal(err)
		}
		got[traceID] = cost
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := 10e-6 + 55_784*1.25e-6 + 81*5e-6
	for _, traceID := range []string{strings.Repeat("b", 32), strings.Repeat("d", 32)} {
		cost, ok := got[traceID]
		if !ok || math.Abs(cost-want) > 1e-12 {
			t.Fatalf("trace %s cost=%v want %g all=%v", traceID, cost, want, got)
		}
	}
	var proxyCosts int
	for traceID, cost := range got {
		if traceID == strings.Repeat("b", 32) || traceID == strings.Repeat("d", 32) || traceID == strings.Repeat("a", 32) {
			continue
		}
		proxyCosts++
		if math.Abs(cost-want) > 1e-12 {
			t.Fatalf("proxy trace %s cost=%g want %g", traceID, cost, want)
		}
	}
	if proxyCosts != 1 || math.Abs(got[strings.Repeat("a", 32)]-pinnedCost) > 1e-12 {
		t.Fatalf("proxy spans=%d explicit import=%v all=%v", proxyCosts, got[strings.Repeat("a", 32)], got)
	}
}

func assertCostEvent(t *testing.T, reader *bufio.Reader, traceID string, want float64) {
	t.Helper()
	frame := readEventFrame(t, reader)
	for strings.HasPrefix(frame, ":") {
		frame = readEventFrame(t, reader)
	}
	if !strings.Contains(frame, "event: cost\n") || !strings.Contains(frame, traceID) {
		t.Fatalf("cost event = %q", frame)
	}
	var data struct {
		Cost float64 `json:"cost_usd"`
	}
	line := frame[strings.Index(frame, "data: ")+len("data: "):]
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &data); err != nil {
		t.Fatal(err)
	}
	if math.Abs(data.Cost-want) > 1e-9 {
		t.Fatalf("event cost=%g want %g frame=%q", data.Cost, want, frame)
	}
}

func expectNoEvent(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			got <- ""
			return
		}
		got <- line
	}()
	select {
	case line := <-got:
		if strings.HasPrefix(line, "event:") {
			t.Fatalf("proxy or import published %q", line)
		}
	case <-time.After(300 * time.Millisecond):
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
