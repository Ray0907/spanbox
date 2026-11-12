package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

func TestProductionTraceRoutes(t *testing.T) {
	if !spaPaths["/traces"] || !spaPaths["/spans"] {
		t.Fatal("production spaPaths must include /traces and /spans")
	}
	production := spaPaths
	t.Cleanup(func() { spaPaths = production })
	handler, database := newTestHandler(t, "mock-dev-token")
	traceID, spanID := strings.Repeat("1", 32), strings.Repeat("2", 16)
	_, err := database.InsertBatch(context.Background(), []store.Span{{
		TraceID: traceID, SpanID: spanID, Name: "SPA parity", Kind: "llm", ServiceName: "demo", SessionID: "parity-session",
		StartNs: 1_000_000_000, EndNs: 1_300_000_000, DurationMs: 300,
		InputContent: "full input", OutputContent: "full output", Attributes: "{}", Events: "[]", Links: "[]", Resource: "{}", Scope: "{}",
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("mock-dev-token")})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	shell := request("/ui", true)
	for _, path := range []string{"/traces", "/traces/" + traceID, "/spans/" + traceID + "/" + spanID} {
		t.Run(path, func(t *testing.T) {
			denied := request(path, false)
			if denied.Code != 302 || denied.Header().Get("Location") != "/login" {
				t.Fatalf("unauthenticated = %d %q", denied.Code, denied.Header().Get("Location"))
			}
			got := request(path, true)
			if got.Code != 200 || got.Body.String() != shell.Body.String() || !reflect.DeepEqual(got.Header(), shell.Header()) {
				t.Fatal("authenticated route must return the production shell and headers")
			}
		})
	}
	for _, path := range []string{"/traces?format=json", "/?format=json&session=parity-session", "/traces/" + traceID + "?format=json", "/spans/" + traceID + "/" + spanID + "?format=json", "/spans/" + traceID + "/" + spanID + "?format=json&field=input&offset=2&len=3", "/export?session=parity-session"} {
		t.Run(path, func(t *testing.T) {
			spaPaths = map[string]bool{}
			before := request(path, true)
			spaPaths = production
			after := request(path, true)
			if before.Code != after.Code || before.Body.String() != after.Body.String() || !reflect.DeepEqual(before.Header(), after.Header()) {
				t.Fatal("JSON/export status, body or headers changed")
			}
		})
	}
}
