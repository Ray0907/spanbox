package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "gemini-3.6-flash") || !strings.Contains(response.Body.String(), "What is two plus two?") {
		t.Fatalf("trace detail status=%d body=%q", response.Code, response.Body.String())
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

func TestTraceFilterRejectsNonFiniteDuration(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?min_duration="+url.QueryEscape(value), nil)
			if _, _, err := traceFilter(req); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestBuildTreeKeepsShortSpansVisible(t *testing.T) {
	spans := []store.Span{
		{SpanID: "root", StartNs: 1_000, EndNs: 2_000},
		{SpanID: "child", ParentSpanID: "root", StartNs: 1_500, EndNs: 1_501},
	}
	roots := buildTree(spans, 1_000, 2_000)
	if len(roots) != 1 || len(roots[0].Children) != 1 {
		t.Fatalf("unexpected tree: %#v", roots)
	}
	child := roots[0].Children[0]
	if child.OffsetPct != 50 || child.WidthPct != 1 {
		t.Fatalf("child geometry = %d/%d", child.OffsetPct, child.WidthPct)
	}
}

func TestTracePages(t *testing.T) {
	handler, database := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	if response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", jsonBody); response.Code != http.StatusOK {
		t.Fatalf("ingest status=%d", response.Code)
	}
	response := request(t, handler, http.MethodGet, "/", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "agent-root") || !strings.Contains(response.Body.String(), "gpt-4o") || !strings.Contains(response.Body.String(), `hx-target="#trace-results"`) {
		t.Fatalf("trace list status=%d body=%q", response.Code, response.Body.String())
	}
	partial := request(t, handler, http.MethodGet, "/?partial=1&errors=1", "", "", nil)
	if partial.Code != http.StatusOK || strings.Contains(partial.Body.String(), "agent-root") || !strings.Contains(partial.Body.String(), `id="trace-results"`) || !strings.Contains(partial.Body.String(), "result-count") {
		t.Fatalf("partial response status=%d body=%q", partial.Code, partial.Body.String())
	}
	traceID := "0102030405060708090a0b0c0d0e0f10"
	response = request(t, handler, http.MethodGet, "/traces/"+traceID, "", "", nil)
	if !strings.Contains(response.Body.String(), `class="kind-row llm"`) {
		t.Fatalf("trace detail missing kind row marker: %q", response.Body.String())
	}
	if strings.Contains(response.Body.String(), `style="`) {
		t.Fatalf("trace detail contains CSP-blocked inline styles: %q", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `<rect class="waterfall-bar" x="0" width="100" height="8">`) || !strings.Contains(response.Body.String(), `<rect class="waterfall-bar" x="17" width="67" height="8">`) {
		t.Fatalf("trace detail missing SVG waterfall geometry: %q", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `class="time-ruler"`) || !strings.Contains(response.Body.String(), "300ms") {
		t.Fatalf("trace detail missing time ruler: %q", response.Body.String())
	}
	for _, name := range []string{"agent-root", "chat gpt-4o", "lookup-weather"} {
		if !strings.Contains(response.Body.String(), name) {
			t.Fatalf("trace detail missing %q: %q", name, response.Body.String())
		}
	}
	response = request(t, handler, http.MethodGet, "/spans/"+traceID+"/2122232425262728", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "What is the weather?") || !strings.Contains(response.Body.String(), "100") || !strings.Contains(response.Body.String(), "Start offset") || !strings.Contains(response.Body.String(), "+50.0 ms") {
		t.Fatalf("span detail status=%d body=%q", response.Code, response.Body.String())
	}

	malicious := store.Span{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb", Name: "<script>x</script>", Kind: "other",
		ServiceName: "demo", StartNs: 2_000_000_000, EndNs: 3_000_000_000, DurationMs: 1000,
		Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}",
	}
	if err := database.InsertBatch(context.Background(), []store.Span{malicious}); err != nil {
		t.Fatal(err)
	}
	response = request(t, handler, http.MethodGet, "/", "", "", nil)
	if !strings.Contains(response.Body.String(), "&lt;script&gt;x&lt;/script&gt;") || strings.Contains(response.Body.String(), "<script>x</script>") {
		t.Fatalf("telemetry was not escaped: %q", response.Body.String())
	}
}

func TestDashboardSearchAndSQLPages(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	if response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", jsonBody); response.Code != http.StatusOK {
		t.Fatalf("ingest status=%d", response.Code)
	}

	response := request(t, handler, http.MethodGet, "/dashboard/data?range=24h", "", "", nil)
	var dashboard store.DashboardData
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &dashboard) != nil {
		t.Fatalf("dashboard data status=%d body=%q", response.Code, response.Body.String())
	}
	if dashboard.TraceCount != 1 || len(dashboard.Models) == 0 || dashboard.Models[0].Model != "gpt-4o" || len(dashboard.Days) == 0 {
		t.Fatalf("unexpected dashboard: %+v", dashboard)
	}
	if dashboard.RangeFrom >= dashboard.RangeTo || dashboard.RangeTo-dashboard.RangeFrom != int64((24*time.Hour)/time.Second) {
		t.Fatalf("dashboard range = %d..%d", dashboard.RangeFrom, dashboard.RangeTo)
	}
	response = request(t, handler, http.MethodGet, "/dashboard", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `data-chart="cost"`) {
		t.Fatalf("dashboard page status=%d body=%q", response.Code, response.Body.String())
	}
	response = request(t, handler, http.MethodGet, "/search?q=weather", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "agent-root") {
		t.Fatalf("search status=%d body=%q", response.Code, response.Body.String())
	}
	response = request(t, handler, http.MethodGet, "/sql", "", "", nil)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "set AUTH_TOKEN to enable") {
		t.Fatalf("disabled SQL status=%d body=%q", response.Code, response.Body.String())
	}

	authed, _ := newTestHandler(t, "secret")
	ingest := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(jsonBody))
	ingest.Header.Set("Content-Type", "application/json")
	ingest.Header.Set("Authorization", "Bearer secret")
	ingestResponse := httptest.NewRecorder()
	authed.ServeHTTP(ingestResponse, ingest)
	if ingestResponse.Code != http.StatusOK {
		t.Fatalf("authenticated ingest status=%d", ingestResponse.Code)
	}
	cookie := &http.Cookie{Name: "spanbox_session", Value: sessionValue("secret")}
	postSQL := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(url.Values{"sql": {query}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		result := httptest.NewRecorder()
		authed.ServeHTTP(result, req)
		return result
	}
	response = postSQL("SELECT count(*) AS n FROM spans")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "3") {
		t.Fatalf("SQL response status=%d body=%q", response.Code, response.Body.String())
	}
	response = postSQL("ATTACH DATABASE '/tmp/x' AS x")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "rejected") {
		t.Fatalf("rejected SQL status=%d body=%q", response.Code, response.Body.String())
	}
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

func TestSQLSchemaFailureReturnsServerError(t *testing.T) {
	handler, database := newTestHandler(t, "secret")
	if err := database.Reader().Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sql", nil)
	req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("secret")})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestIngestBearerAuth(t *testing.T) {
	handler, _ := newTestHandler(t, "secret")
	jsonBody, _ := traceFixture(t)
	for _, auth := range []string{"", "Bearer wrong", "Bearer secret"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(jsonBody))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		want := http.StatusUnauthorized
		if auth == "Bearer secret" {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Fatalf("auth %q: got %d, want %d", auth, response.Code, want)
		}
	}
	response := request(t, handler, http.MethodGet, "/healthz", "", "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("health status=%d", response.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	handler, _ := newTestHandler(t, "secret")

	loginPage := request(t, handler, http.MethodGet, "/login", "", "", nil)
	if loginPage.Code != http.StatusOK || !strings.Contains(loginPage.Body.String(), `href="/static/app.css"`) || !strings.Contains(loginPage.Body.String(), `name="token"`) {
		t.Fatalf("login page status=%d body=%q", loginPage.Code, loginPage.Body.String())
	}
	if got := loginPage.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Fatalf("login CSP = %q", got)
	}
	stylesheet := request(t, handler, http.MethodGet, "/static/app.css", "", "", nil)
	if stylesheet.Code != http.StatusOK || !strings.Contains(stylesheet.Body.String(), "@font-face") {
		t.Fatalf("stylesheet status=%d body=%q", stylesheet.Code, stylesheet.Body.String())
	}

	withoutCookie := request(t, handler, http.MethodGet, "/", "", "", nil)
	if withoutCookie.Code != http.StatusFound || withoutCookie.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated response: %d location=%q", withoutCookie.Code, withoutCookie.Header().Get("Location"))
	}

	form := url.Values{"token": {"secret"}}.Encode()
	login := request(t, handler, http.MethodPost, "/login", "application/x-www-form-urlencoded", "", []byte(form))
	if login.Code != http.StatusFound || login.Header().Get("Location") != "/" {
		t.Fatalf("login response: %d body=%q", login.Code, login.Body.String())
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "spanbox_session" || cookies[0].Value == "secret" || cookies[0].Value != sessionValue("secret") || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
		t.Fatalf("unexpected cookies: %+v", cookies)
	}
	withCookie := httptest.NewRequest(http.MethodGet, "/", nil)
	withCookie.AddCookie(cookies[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, withCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated page status=%d", response.Code)
	}

	wrong := request(t, handler, http.MethodPost, "/login", "application/x-www-form-urlencoded", "", []byte(url.Values{"token": {"wrong"}}.Encode()))
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong login status=%d", wrong.Code)
	}
}

func TestTokenEqual(t *testing.T) {
	if !tokenEqual("same", "same") || tokenEqual("same", "different") {
		t.Fatal("constant-time token comparison returned wrong result")
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
