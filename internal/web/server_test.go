package web

import (
	"bytes"
	"compress/gzip"
	"context"
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
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
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

func TestTracePages(t *testing.T) {
	handler, database := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	if response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", jsonBody); response.Code != http.StatusOK {
		t.Fatalf("ingest status=%d", response.Code)
	}
	response := request(t, handler, http.MethodGet, "/", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "agent-root") || !strings.Contains(response.Body.String(), "gpt-4o") {
		t.Fatalf("trace list status=%d body=%q", response.Code, response.Body.String())
	}
	partial := request(t, handler, http.MethodGet, "/?partial=1&errors=1", "", "", nil)
	if partial.Code != http.StatusOK || strings.Contains(partial.Body.String(), "agent-root") || !strings.Contains(partial.Body.String(), "<tbody") {
		t.Fatalf("partial response status=%d body=%q", partial.Code, partial.Body.String())
	}
	traceID := "0102030405060708090a0b0c0d0e0f10"
	response = request(t, handler, http.MethodGet, "/traces/"+traceID, "", "", nil)
	for _, name := range []string{"agent-root", "chat gpt-4o", "lookup-weather"} {
		if !strings.Contains(response.Body.String(), name) {
			t.Fatalf("trace detail missing %q: %q", name, response.Body.String())
		}
	}
	response = request(t, handler, http.MethodGet, "/spans/"+traceID+"/2122232425262728", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "What is the weather?") || !strings.Contains(response.Body.String(), "100") {
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

	response := request(t, handler, http.MethodGet, "/dashboard/data?range=7d", "", "", nil)
	var dashboard store.DashboardData
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &dashboard) != nil {
		t.Fatalf("dashboard data status=%d body=%q", response.Code, response.Body.String())
	}
	if dashboard.TraceCount != 1 || len(dashboard.Models) == 0 || dashboard.Models[0].Model != "gpt-4o" || len(dashboard.Days) == 0 {
		t.Fatalf("unexpected dashboard: %+v", dashboard)
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
	if server.Addr != ":9000" || server.ReadHeaderTimeout != config.ReadHeaderTimeout || server.ReadTimeout != config.ReadTimeout || server.IdleTimeout != config.IdleTimeout || server.MaxHeaderBytes != config.MaxHeaderBytes || server.Handler == nil || handler == nil {
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
