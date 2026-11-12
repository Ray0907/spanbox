package web

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func newTestHandler(t *testing.T, token string) (http.Handler, *store.Store) {
	t.Helper()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Port: 4318, DataDir: t.TempDir(), RetentionDays: 30, AuthToken: token}
	return NewHandler(Deps{Cfg: cfg, Store: database, Pricing: prices, Version: "test", Logf: t.Logf}), database
}

func request(t *testing.T, handler http.Handler, method, path, contentType, encoding string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	return response
}

func TestIngestRejectsExcessiveSpanCost(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	var requestBody collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(jsonBody, &requestBody); err != nil {
		t.Fatal(err)
	}
	span := requestBody.ResourceSpans[0].ScopeSpans[0].Spans[1]
	span.Attributes = append(span.Attributes,
		&commonpb.KeyValue{Key: "llm.cost.total", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1e308}}},
	)
	body, err := protojson.Marshal(&requestBody)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestIngestRejectsExcessiveTraceCost(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	var requestBody collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(jsonBody, &requestBody); err != nil {
		t.Fatal(err)
	}
	spans := requestBody.ResourceSpans[0].ScopeSpans[0].Spans
	spans[0].Attributes = append(spans[0].Attributes,
		&commonpb.KeyValue{Key: "gen_ai.operation.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "chat"}}},
		&commonpb.KeyValue{Key: "llm.cost.total", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 600_000}}},
	)
	spans[1].Attributes = append(spans[1].Attributes,
		&commonpb.KeyValue{Key: "llm.cost.total", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 600_000}}},
	)
	body, err := protojson.Marshal(&requestBody)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestIngestRejectsUnsafeIndexedPath(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	var requestBody collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(jsonBody, &requestBody); err != nil {
		t.Fatal(err)
	}
	span := requestBody.ResourceSpans[0].ScopeSpans[0].Spans[0]
	span.Attributes = append(span.Attributes, &commonpb.KeyValue{
		Key:   "gen_ai.prompt.10001.content",
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "x"}},
	})
	body, err := protojson.Marshal(&requestBody)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", body)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestIngestBackPressure(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	handler := NewHandler(Deps{Cfg: config.Config{}, Store: database, Pricing: prices, Logf: t.Logf, ingestSlots: slots})
	jsonBody, _ := traceFixture(t)
	response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", jsonBody)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" {
		t.Fatalf("status=%d retry-after=%q body=%q", response.Code, response.Header().Get("Retry-After"), response.Body.String())
	}
}

func TestNewServerTimeouts(t *testing.T) {
	handler, database := newTestHandler(t, "")
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(Deps{Cfg: config.Config{Port: 9000}, Store: database, Pricing: prices, Logf: t.Logf})
	if server.Addr != ":9000" || server.ReadHeaderTimeout != config.ReadHeaderTimeout || server.ReadTimeout != config.ReadTimeout || server.WriteTimeout != 60*time.Second || server.IdleTimeout != config.IdleTimeout || server.MaxHeaderBytes != config.MaxHeaderBytes || server.Handler == nil || handler == nil {
		t.Fatalf("unexpected server: %+v", server)
	}
}

func TestHealthIgnoresCanceledContext(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d", response.Code)
	}
}
