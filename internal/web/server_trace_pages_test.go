package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
)

func TestTraceFilterRejectsNonFiniteDuration(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/?min_duration="+url.QueryEscape(value), nil)
			if _, err := traceFilter(req); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestTracePages(t *testing.T) {
	handler, database := newTestHandler(t, "")
	jsonBody, _ := traceFixture(t)
	if response := request(t, handler, http.MethodPost, "/v1/traces", "application/json", "", jsonBody); response.Code != http.StatusOK {
		t.Fatalf("ingest status=%d", response.Code)
	}
	response := request(t, handler, http.MethodGet, "/", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="root"`) {
		t.Fatalf("trace list shell status=%d", response.Code)
	}
	listing := agentGet(t, handler, "/?format=json")
	row := listing["items"].([]any)[0].(map[string]any)
	if row["name"] != "agent-root" || row["models"] != "gpt-4o" {
		t.Fatalf("trace list JSON=%v", listing)
	}
	partial := request(t, handler, http.MethodGet, "/?partial=1&errors=1", "", "", nil)
	if partial.Code != http.StatusOK || !strings.Contains(partial.Body.String(), `id="root"`) {
		t.Fatalf("filtered shell status=%d", partial.Code)
	}
	filtered := agentGet(t, handler, "/?format=json&errors=1")
	if len(filtered["items"].([]any)) != 0 {
		t.Fatalf("error filter JSON=%v", filtered)
	}
	traceID := "0102030405060708090a0b0c0d0e0f10"
	trace := agentGet(t, handler, "/traces/"+traceID+"?format=json")
	spans := trace["spans"].([]any)
	if len(spans) != 3 || trace["duration_ms"] != float64(300) {
		t.Fatalf("trace geometry = %v", trace)
	}
	names := map[string]bool{}
	for _, item := range spans {
		names[item.(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"agent-root", "chat gpt-4o", "lookup-weather"} {
		if !names[name] {
			t.Fatalf("trace missing %q", name)
		}
	}
	span := agentGet(t, handler, "/spans/"+traceID+"/2122232425262728?format=json")
	if span["kind"] != "llm" || span["start_offset_ms"] != float64(50) || span["input_tokens"] != float64(100) || !strings.Contains(span["input"].(map[string]any)["text"].(string), "What is the weather?") {
		t.Fatalf("span detail = %v", span)
	}
	for _, path := range []string{"/traces/" + traceID, "/spans/" + traceID + "/2122232425262728"} {
		response = request(t, handler, http.MethodGet, path, "", "", nil)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="root"`) || strings.Contains(response.Body.String(), `style="`) {
			t.Fatalf("expected CSP-safe shell: %d", response.Code)
		}
	}

	malicious := store.Span{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb", Name: "<script>x</script>", Kind: "other",
		ServiceName: "demo", StartNs: 2_000_000_000, EndNs: 3_000_000_000, DurationMs: 1000,
		Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}",
	}
	if _, err := database.InsertBatch(context.Background(), []store.Span{malicious}); err != nil {
		t.Fatal(err)
	}
	response = request(t, handler, http.MethodGet, "/?format=json", "", "", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `\u003cscript\u003ex\u003c/script\u003e`) || strings.Contains(response.Body.String(), "<script>x</script>") {
		t.Fatalf("telemetry JSON was not escaped: %q", response.Body.String())
	}
	// Root-table rendering and script escaping are exercised in dashboard-sql-login.cjs.
}

func TestSessionsPage(t *testing.T) {
	handler, database := newTestHandler(t, "")
	span := store.Span{TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb", Name: "chat", Kind: "llm", ServiceName: "claude-cli", StartNs: time.Now().Add(-time.Minute).UnixNano(), EndNs: time.Now().UnixNano(), DurationMs: 60_000, RequestModel: "claude-haiku", InputTokens: int64ptr(10), OutputTokens: int64ptr(2), CostUSD: float64ptr(0.001), SessionID: "session-page", Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}"}
	if _, err := database.InsertBatch(context.Background(), []store.Span{span}); err != nil {
		t.Fatal(err)
	}
	page := agentGet(t, handler, "/sessions?format=json")
	row := page["items"].([]any)[0].(map[string]any)
	if row["session_id"] != "session-page" || row["service"] != "claude-cli" || row["models"] != "claude-haiku" || row["trace_count"] != float64(1) || row["llm_count"] != float64(1) || row["input_tokens"] != float64(10) || row["output_tokens"] != float64(2) || row["cost_usd"] != 0.001 {
		t.Fatalf("sessions JSON=%v", page)
	}
	// Current nav and session/filter links are checked in e2e-mobile/sessions-search.cjs.
}

func int64ptr(value int64) *int64       { return &value }
func float64ptr(value float64) *float64 { return &value }

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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="root"`) {
		t.Fatalf("dashboard page status=%d body=%q", response.Code, response.Body.String())
	}
	search := agentGet(t, handler, "/search?q=weather&format=json")
	if !strings.Contains(search["items"].([]any)[0].(map[string]any)["trace_name"].(string), "agent-root") {
		t.Fatalf("search JSON=%v", search)
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
		req := httptest.NewRequest(http.MethodPost, "/sql?format=json", strings.NewReader(url.Values{"sql": {query}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		result := httptest.NewRecorder()
		authed.ServeHTTP(result, req)
		return result
	}
	response = postSQL("SELECT count(*) AS n FROM spans")
	var result struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
		Error   string     `json:"error"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Columns) != 1 || result.Columns[0] != "n" || len(result.Rows) != 1 || len(result.Rows[0]) != 1 || result.Rows[0][0] != "3" || result.Error != "" {
		t.Fatalf("SQL response status=%d body=%q", response.Code, response.Body.String())
	}
	response = postSQL("ATTACH DATABASE '/tmp/x' AS x")
	if response.Code != http.StatusBadRequest || json.Unmarshal(response.Body.Bytes(), &result) != nil || !strings.Contains(result.Error, "rejected") {
		t.Fatalf("rejected SQL status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestSQLSchemaFailureReturnsServerError(t *testing.T) {
	handler, database := newTestHandler(t, "secret")
	if err := database.Reader().Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/sql?format=json", nil)
	req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("secret")})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}
