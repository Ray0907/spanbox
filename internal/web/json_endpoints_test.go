package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/store"
)

func jsonEndpointRequest(t *testing.T, handler http.Handler, method, path, accept, mediaType, body, auth string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", mediaType)
	switch auth {
	case "bearer":
		req.Header.Set("Authorization", "Bearer mock-dev-token")
	case "cookie":
		req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("mock-dev-token")})
	case "invalid":
		req.Header.Set("Authorization", "Bearer wrong-token")
		req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: "wrong-cookie"})
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func decodeEndpointJSON(t *testing.T, resp *httptest.ResponseRecorder, status int, value any) {
	t.Helper()
	if resp.Code != status || resp.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status=%d type=%q body=%q; want JSON status %d", resp.Code, resp.Header().Get("Content-Type"), resp.Body.String(), status)
	}
	if err := json.Unmarshal(resp.Body.Bytes(), value); err != nil {
		t.Fatal(err)
	}
}

func TestJSONEndpointsSQLSchema(t *testing.T) {
	handler, database := newTestHandler(t, "mock-dev-token")
	var body struct {
		Tables []struct {
			Name    string   `json:"name"`
			Columns []string `json:"columns"`
		} `json:"tables"`
	}
	resp := jsonEndpointRequest(t, handler, http.MethodGet, "/sql?format=json", "", "", "", "bearer")
	decodeEndpointJSON(t, resp, 200, &body)
	want, err := (Deps{Store: database}).schema(httptest.NewRequest(http.MethodGet, "/sql", nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Tables) != len(want) {
		t.Fatalf("schema tables=%+v want=%+v", body.Tables, want)
	}
	for i, table := range body.Tables {
		if table.Name != want[i].Name || !reflect.DeepEqual(table.Columns, want[i].Columns) {
			t.Fatalf("table %d=%+v want=%+v", i, table, want[i])
		}
	}
}

func TestJSONEndpointsSQLResults(t *testing.T) {
	handler, _ := newTestHandler(t, "mock-dev-token")
	for _, tc := range []struct {
		name, path, accept, query string
		rows                      int
		truncated                 bool
	}{
		{"accept", "/sql", "application/json", "SELECT 42 AS answer, NULL AS missing", 1, false},
		{"format", "/sql?format=json", "", "SELECT 42 AS answer, NULL AS missing", 1, false},
		{"empty", "/sql?format=json", "", "SELECT 42 AS answer, NULL AS missing WHERE 0", 0, false},
		{"truncated", "/sql", "application/json", "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1001) SELECT x AS answer, NULL AS missing FROM n", config.SQLMaxRows, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := jsonEndpointRequest(t, handler, http.MethodPost, tc.path, tc.accept, "application/x-www-form-urlencoded", url.Values{"sql": {tc.query}}.Encode(), "bearer")
			var body struct {
				Columns   []string   `json:"columns"`
				Rows      [][]string `json:"rows"`
				Truncated bool       `json:"truncated"`
				Error     string     `json:"error"`
			}
			decodeEndpointJSON(t, resp, 200, &body)
			if !reflect.DeepEqual(body.Columns, []string{"answer", "missing"}) || body.Rows == nil || len(body.Rows) != tc.rows || body.Truncated != tc.truncated || body.Error != "" {
				t.Fatalf("SQL result=%+v", body)
			}
			if tc.rows > 0 && !tc.truncated && !reflect.DeepEqual(body.Rows[0], []string{"42", "NULL"}) {
				t.Fatalf("SQL cells=%v", body.Rows[0])
			}
			var keys map[string]any
			if err := json.Unmarshal(resp.Body.Bytes(), &keys); err != nil || len(keys) != 4 {
				t.Fatalf("unexpected result keys: %v err=%v", keys, err)
			}
		})
	}
}

func TestJSONEndpointsSQLRejections(t *testing.T) {
	handler, database := newTestHandler(t, "mock-dev-token")
	seedSession(t, database)
	for _, tc := range []struct {
		name, mediaType, body, message string
		status                         int
	}{
		{"bad-query", "application/x-www-form-urlencoded", url.Values{"sql": {"SELECT no_such_column FROM spans"}}.Encode(), "no such column", 400},
		{"write", "application/x-www-form-urlencoded", url.Values{"sql": {"DELETE FROM spans"}}.Encode(), "sql rejected", 400},
		{"cte-write", "application/x-www-form-urlencoded", url.Values{"sql": {"WITH x AS (SELECT 1) DELETE FROM spans"}}.Encode(), "sql rejected", 400},
		{"statement-size", "application/x-www-form-urlencoded", "sql=SELECT+1" + strings.Repeat("+", config.SQLMaxTextBytes), "16 KiB", 400},
		{"form-size", "application/x-www-form-urlencoded", "sql=SELECT+1&padding=" + strings.Repeat("x", 20<<10), "invalid form", 400},
		{"media-type", "application/json", `{"sql":"SELECT 1"}`, "unsupported media type", 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := jsonEndpointRequest(t, handler, http.MethodPost, "/sql?format=json", "", tc.mediaType, tc.body, "cookie")
			var body map[string]any
			decodeEndpointJSON(t, resp, tc.status, &body)
			message, _ := body["error"].(string)
			if !strings.Contains(message, tc.message) {
				t.Fatalf("error=%q want substring %q", message, tc.message)
			}
			if tc.name == "bad-query" || tc.name == "write" || tc.name == "cte-write" || tc.name == "statement-size" {
				if len(body) != 4 || body["columns"] == nil || body["rows"] == nil || body["truncated"] != false {
					t.Fatalf("missing SQL result envelope: %v", body)
				}
			}
		})
	}
	var count int
	if err := database.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 8 {
		t.Fatalf("write rejection failed: count=%d err=%v", count, err)
	}
}

func TestJSONEndpointsSQLDisabled(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, mode := range []struct{ path, accept string }{{"/sql?format=json", ""}, {"/sql", "application/json"}} {
			t.Run(method+mode.path+mode.accept, func(t *testing.T) {
				resp := jsonEndpointRequest(t, handler, method, mode.path, mode.accept, "application/x-www-form-urlencoded", "sql=SELECT+1", "")
				var body map[string]string
				decodeEndpointJSON(t, resp, 404, &body)
				if !strings.Contains(body["error"], "disabled") {
					t.Fatal("SQL schema or data exposed without configured auth")
				}
			})
		}
	}
}

func TestJSONEndpointsAuthentication(t *testing.T) {
	handler, database := newTestHandler(t, "mock-dev-token")
	seedSession(t, database)
	for _, endpoint := range []struct{ method, path, accept string }{
		{http.MethodGet, "/sessions?id=" + url.QueryEscape(sessionTestID) + "&format=json", ""},
		{http.MethodGet, "/sql?format=json", ""},
		{http.MethodPost, "/sql?format=json", ""},
		{http.MethodPost, "/sql", "application/json"},
		{http.MethodGet, "/spans/" + strings.Repeat("a", 32) + "/" + strings.Repeat("a", 16) + "?format=json", ""},
	} {
		for _, auth := range []string{"", "invalid", "bearer", "cookie"} {
			t.Run(endpoint.method+endpoint.path+endpoint.accept+"/"+auth, func(t *testing.T) {
				resp := jsonEndpointRequest(t, handler, endpoint.method, endpoint.path, endpoint.accept, "application/x-www-form-urlencoded", "sql=SELECT+1", auth)
				status := 200
				if auth == "" || auth == "invalid" {
					status = 401
				}
				var body map[string]any
				decodeEndpointJSON(t, resp, status, &body)
				if status == 401 && (len(body) != 1 || body["error"] != "unauthorized") {
					t.Fatalf("unauthenticated data leaked: %v", body)
				}
			})
		}
	}
	// Bearer-only access must not change ordinary HTML SQL authentication.
	resp := jsonEndpointRequest(t, handler, http.MethodGet, "/sql", "", "", "", "bearer")
	if resp.Code != 302 || resp.Header().Get("Location") != "/login" {
		t.Fatalf("HTML SQL auth changed: status=%d", resp.Code)
	}
}

func TestJSONEndpointsSpanFields(t *testing.T) {
	handler, database := newTestHandler(t, "")
	span := sessionSpan(strings.Repeat("a", 32), strings.Repeat("b", 16), "", "agent", "invoke_agent", 1_000_000)
	span.InputContent, span.OutputContent = "input", "output"
	span.Attributes = `{"custom":"` + strings.Repeat("界", 2100) + `"}`
	span.Events = `[{"name":"event","attributes":{"text":"` + strings.Repeat("界", 2100) + `"}}]`
	span.Links = `[{"traceId":"` + strings.Repeat("c", 32) + `","spanId":"` + strings.Repeat("d", 16) + `"}]`
	span.Resource, span.Scope = `{"service.name":"agent-svc"}`, `{"name":"sdk","version":"1.0"}`
	if _, err := database.InsertBatch(context.Background(), []store.Span{span}); err != nil {
		t.Fatal(err)
	}
	path := "/spans/" + span.TraceID + "/" + span.SpanID + "?format=json"
	var body map[string]json.RawMessage
	decodeEndpointJSON(t, request(t, handler, http.MethodGet, path, "", "", nil), 200, &body)
	fields := map[string]string{"input": span.InputContent, "output": span.OutputContent, "attributes": span.Attributes, "events": span.Events, "links": span.Links, "resource": span.Resource, "scope": span.Scope}
	for name, text := range fields {
		t.Run(name, func(t *testing.T) {
			var window agentWindow
			if err := json.Unmarshal(body[name], &window); err != nil || !reflect.DeepEqual(window, textWindow(text, 0, 2000)) {
				t.Fatalf("field %s window=%+v err=%v", name, window, err)
			}
			var rebuilt strings.Builder
			offset := 0
			for {
				var result map[string]json.RawMessage
				decodeEndpointJSON(t, request(t, handler, http.MethodGet, path+"&field="+name+fmt.Sprintf("&offset=%d&len=997", offset), "", "", nil), 200, &result)
				if err := json.Unmarshal(result[name], &window); err != nil {
					t.Fatal(err)
				}
				for other := range fields {
					if other != name && result[other] != nil {
						t.Fatalf("field=%s also returned %s", name, other)
					}
				}
				rebuilt.WriteString(window.Text)
				if window.NextOffset == nil {
					break
				}
				offset = *window.NextOffset
			}
			if rebuilt.String() != text {
				t.Fatalf("full %s did not reassemble", name)
			}
		})
	}
	for key, want := range map[string]string{"trace_id": span.TraceID, "span_id": span.SpanID, "name": span.Name, "kind": span.Kind, "parent_span_id": "", "model": "", "provider": "", "status_message": ""} {
		var got string
		if err := json.Unmarshal(body[key], &got); err != nil || got != want {
			t.Fatalf("existing %s changed: got=%q want=%q err=%v", key, got, want, err)
		}
	}
	for _, key := range []string{"start_offset_ms", "duration_ms", "input_tokens", "output_tokens", "cost_usd", "status_code"} {
		if body[key] == nil {
			t.Fatalf("existing field %s removed", key)
		}
	}
}

func TestSQLJSONHonorsAcceptQ0(t *testing.T) {
	for accept, want := range map[string]bool{
		"application/json":                  true,
		"text/html, application/json;q=0":   false,
		"text/html, application/json;q=0.0": false,
		"text/html, application/json;q=0.5": true,
		"text/html":                         false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/sql", nil)
		req.Header.Set("Accept", accept)
		if got := sqlJSON(req); got != want {
			t.Errorf("Accept %q: sqlJSON=%v, want %v", accept, got, want)
		}
	}
}
