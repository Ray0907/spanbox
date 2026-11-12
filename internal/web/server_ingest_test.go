package web

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func traceFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	jsonBody, err := os.ReadFile("../otlp/testdata/trace.json")
	if err != nil {
		t.Fatal(err)
	}
	var request collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(jsonBody, &request); err != nil {
		t.Fatal(err)
	}
	base := uint64(time.Now().UTC().Add(-time.Minute).UnixNano())
	for _, resource := range request.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				span.StartTimeUnixNano = base + span.StartTimeUnixNano - 1_000_000_000
				span.EndTimeUnixNano = base + span.EndTimeUnixNano - 1_000_000_000
			}
		}
	}
	jsonBody, err = protojson.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	protoBody, err := proto.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	return jsonBody, protoBody
}

func logsFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	jsonBody, err := os.ReadFile("../otlp/testdata/gemini_cli_logs.json")
	if err != nil {
		t.Fatal(err)
	}
	var request collectorlogspb.ExportLogsServiceRequest
	if err := protojson.Unmarshal(jsonBody, &request); err != nil {
		t.Fatal(err)
	}
	protoBody, err := proto.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	return jsonBody, protoBody
}

func splitLogsFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	jsonBody, _ := logsFixture(t)
	var logs collectorlogspb.ExportLogsServiceRequest
	if err := protojson.Unmarshal(jsonBody, &logs); err != nil {
		t.Fatal(err)
	}
	requestBatch := proto.Clone(&logs).(*collectorlogspb.ExportLogsServiceRequest)
	requestBatch.ResourceLogs[0].ScopeLogs[0].LogRecords = requestBatch.ResourceLogs[0].ScopeLogs[0].LogRecords[:1]
	responseBatch := proto.Clone(&logs).(*collectorlogspb.ExportLogsServiceRequest)
	responseRecords := responseBatch.ResourceLogs[0].ScopeLogs[0].LogRecords
	responseBatch.ResourceLogs[0].ScopeLogs[0].LogRecords = responseRecords[len(responseRecords)-1:]
	response := responseBatch.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	attrs := response.Attributes[:0]
	for _, attr := range response.Attributes {
		if attr.Key != "gen_ai.input.messages" && attr.Key != "gen_ai.system_instructions" {
			attrs = append(attrs, attr)
		}
	}
	response.Attributes = attrs
	requestBody, err := protojson.Marshal(requestBatch)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, err := protojson.Marshal(responseBatch)
	if err != nil {
		t.Fatal(err)
	}
	return requestBody, responseBody
}

func gzipBody(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestIngestEndpoint(t *testing.T) {
	handler, database := newTestHandler(t, "")
	jsonBody, protoBody := traceFixture(t)

	response := request(t, handler, http.MethodPost, "/v1/traces", "application/json; charset=utf-8", "", jsonBody)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(response.Body.String()) != "{}" {
		t.Fatalf("JSON response: status=%d type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	var spanCount, traceCount int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&spanCount); err != nil {
		t.Fatal(err)
	}
	if err := database.Reader().QueryRow("SELECT count(*) FROM traces").Scan(&traceCount); err != nil {
		t.Fatal(err)
	}
	if spanCount != 3 || traceCount != 1 {
		t.Fatalf("spans=%d traces=%d", spanCount, traceCount)
	}

	response = request(t, handler, http.MethodPost, "/v1/traces", "application/x-protobuf", "", protoBody)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("protobuf response: status=%d type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	var exportResponse collectortracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(response.Body.Bytes(), &exportResponse); err != nil {
		t.Fatal(err)
	}

	response = request(t, handler, http.MethodPost, "/v1/traces", "application/json", "gzip", gzipBody(t, jsonBody))
	if response.Code != http.StatusOK {
		t.Fatalf("gzip status=%d body=%q", response.Code, response.Body.String())
	}

	response = request(t, handler, http.MethodPost, "/v1/traces", "text/plain", "", jsonBody)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text status=%d", response.Code)
	}
	var status statuspb.Status
	if err := protojson.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Message == "" {
		t.Fatalf("invalid status response: message=%q err=%v", status.GetMessage(), err)
	}

	response = request(t, handler, http.MethodPost, "/v1/traces", "application/json", "br", jsonBody)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("br status=%d", response.Code)
	}

	var malformed collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(jsonBody, &malformed); err != nil {
		t.Fatal(err)
	}
	malformed.ResourceSpans[0].ScopeSpans[0].Spans[0].TraceId = make([]byte, 16)
	badBody, err := protojson.Marshal(&malformed)
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", badBody)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d body=%q", response.Code, response.Body.String())
	}
	status.Reset()
	if err := protojson.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Message == "" {
		t.Fatalf("invalid malformed status: message=%q err=%v", status.GetMessage(), err)
	}

	response = request(t, handler, http.MethodPost, "/v1/traces", "application/json", "gzip", gzipBody(t, make([]byte, 33<<20)))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%q", response.Code, response.Body.String())
	}

	response = request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", jsonBody)
	if response.Code != http.StatusOK {
		t.Fatalf("repeat status=%d", response.Code)
	}
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&spanCount); err != nil {
		t.Fatal(err)
	}
	if spanCount != 3 {
		t.Fatalf("repeat produced %d spans", spanCount)
	}

	response = request(t, handler, http.MethodGet, "/healthz", "", "", nil)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "ok" {
		t.Fatalf("health response: %d %q", response.Code, response.Body.String())
	}
}

func TestIngestGenAILogsAndMetrics(t *testing.T) {
	handler, database := newTestHandler(t, "")
	jsonBody, protoBody := logsFixture(t)

	for _, input := range []struct {
		path, contentType string
		body              []byte
	}{{"/", "application/json", jsonBody}, {"/v1/logs", "application/json", jsonBody}, {"/v1/logs", "application/x-protobuf", protoBody}} {
		response := request(t, handler, http.MethodPost, input.path, input.contentType, "", input.body)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != input.contentType {
			t.Fatalf("POST %s (%s): status=%d type=%q body=%q", input.path, input.contentType, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}

	var traceID, kind, provider, requestModel, sessionID string
	var inputTokens, outputTokens int64
	var cost sql.NullFloat64
	if err := database.Reader().QueryRow(`SELECT trace_id, kind, provider, request_model, input_tokens, output_tokens, cost_usd, session_id FROM spans`).Scan(&traceID, &kind, &provider, &requestModel, &inputTokens, &outputTokens, &cost, &sessionID); err != nil {
		t.Fatal(err)
	}
	if kind != "llm" || provider != "gcp.gen_ai" || requestModel != "gemini-3.6-flash" || inputTokens != 9097 || outputTokens != 1 || !cost.Valid || sessionID != "f5c6f2f9-b6a7-4761-a388-73b99a7da85f" {
		t.Fatalf("unexpected stored span: kind=%q provider=%q model=%q input=%d output=%d cost=%v session=%q", kind, provider, requestModel, inputTokens, outputTokens, cost, sessionID)
	}
	response := request(t, handler, http.MethodGet, "/traces/"+traceID, "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="root"`) {
		t.Fatalf("trace shell status=%d", response.Code)
	}
	trace := agentGet(t, handler, "/traces/"+traceID+"?format=json")
	spanID := trace["spans"].([]any)[0].(map[string]any)["span_id"].(string)
	span := agentGet(t, handler, "/spans/"+traceID+"/"+spanID+"?format=json")
	if span["model"] != "gemini-3.6-flash" || !strings.Contains(span["input"].(map[string]any)["text"].(string), "What is two plus two?") {
		t.Fatalf("trace span = %v", span)
	}

	metrics := []byte(`{"resourceMetrics":[]}`)
	metricsBefore := metricsReceived.Load()
	for _, path := range []string{"/", "/v1/metrics"} {
		if response := request(t, handler, http.MethodPost, path, "application/json", "", metrics); response.Code != http.StatusOK {
			t.Fatalf("metrics POST %s: status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	if got := metricsReceived.Load(); got != metricsBefore+2 {
		t.Fatalf("metrics counter = %d, want %d", got, metricsBefore+2)
	}
	if response := request(t, handler, http.MethodPost, "/", "application/json", "", []byte(`{"unknown":[]}`)); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown root JSON status=%d body=%q", response.Code, response.Body.String())
	}

	authed, _ := newTestHandler(t, "secret")
	for _, path := range []string{"/v1/logs", "/"} {
		if response := request(t, authed, http.MethodPost, path, "application/json", "", jsonBody); response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated POST %s status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	authed.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated root ingest status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestIngestPairsGenAILogsAcrossBatches(t *testing.T) {
	handler, database := newTestHandler(t, "")
	requestBody, responseBody := splitLogsFixture(t)
	if response := request(t, handler, http.MethodPost, "/v1/logs", "application/json", "", requestBody); response.Code != http.StatusOK {
		t.Fatalf("request batch status=%d body=%q", response.Code, response.Body.String())
	}
	var count int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 0 {
		t.Fatalf("request batch stored %d spans: %v", count, err)
	}
	if response := request(t, handler, http.MethodPost, "/v1/logs", "application/json", "", responseBody); response.Code != http.StatusOK {
		t.Fatalf("response batch status=%d body=%q", response.Code, response.Body.String())
	}
	var startNs, endNs int64
	var durationMs float64
	var input string
	if err := database.Reader().QueryRow("SELECT start_ns, end_ns, duration_ms, input_content FROM spans").Scan(&startNs, &endNs, &durationMs, &input); err != nil {
		t.Fatal(err)
	}
	if startNs != 1789539115130000000 || endNs != 1789539117346000000 || durationMs <= 0 || !strings.Contains(input, "What is two plus two?") {
		t.Fatalf("stored cross-batch span: start=%d end=%d duration=%g input=%q", startNs, endNs, durationMs, input)
	}
}

func TestLangfuseNativeAlias(t *testing.T) {
	handler, database := newTestHandler(t, "secret")
	jsonBody, _ := traceFixture(t)
	post := func(auth string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/public/otel/v1/traces", bytes.NewReader(jsonBody))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for _, input := range []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("pk:wrong")), http.StatusUnauthorized},
		{"Basic " + base64.StdEncoding.EncodeToString([]byte("anything:secret")), http.StatusOK},
		{"Bearer secret", http.StatusOK},
	} {
		if response := post(input.auth); response.Code != input.want {
			t.Fatalf("auth=%q status=%d body=%q", input.auth, response.Code, response.Body.String())
		}
	}
	var count int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 3 {
		t.Fatalf("span count=%d err=%v", count, err)
	}
	health := request(t, handler, http.MethodGet, "/api/public/health", "", "", nil)
	if health.Code != http.StatusOK || health.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(health.Body.String()) != `{"status":"OK"}` {
		t.Fatalf("health status=%d type=%q body=%q", health.Code, health.Header().Get("Content-Type"), health.Body.String())
	}
}

func TestWireBodyLimit(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	body := io.LimitReader(strings.NewReader(strings.Repeat("x", 1024)), 1024)
	req := httptest.NewRequest(http.MethodPost, "/v1/traces", io.MultiReader(body, bytes.NewReader(make([]byte, config.MaxBodyBytes))))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("wire limit status=%d", response.Code)
	}
}
