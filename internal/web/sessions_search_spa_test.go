package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// Replaces the session-00 and needle trace HTML assertions in TestAgentJSONErrorsAndHTML.
func TestMigratedSessionsSearchContentJSON(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	_, traceID, _ := agentSeed(t, handler, 1, "needle")
	sessions := agentGet(t, handler, "/sessions?format=json")["items"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["session_id"] != "session-00" {
		t.Fatalf("session content missing: %v", sessions)
	}
	hits := agentGet(t, handler, "/search?q=needle&format=json")["items"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["trace_id"] != traceID || !strings.Contains(hits[0].(map[string]any)["snippet"].(string), "needle") {
		t.Fatalf("search content missing: %v", hits)
	}
}

func TestProductionSessionsSearchRoutes(t *testing.T) {
	handler, database := newTestHandler(t, "sessions-search-throwaway")
	seedSession(t, database)
	production := spaPaths
	t.Cleanup(func() { spaPaths = production })
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("sessions-search-throwaway")})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	shell := get("/ui", true)
	for _, path := range []string{"/sessions", "/sessions?id=" + url.QueryEscape(sessionTestID), "/sessions?id=missing", "/search", "/search?q=prompt"} {
		t.Run(path, func(t *testing.T) {
			denied := get(path, false)
			if denied.Code != 302 || denied.Header().Get("Location") != "/login" {
				t.Fatalf("unauthenticated = %d %q", denied.Code, denied.Header().Get("Location"))
			}
			got := get(path, true)
			if got.Code != 200 || !strings.Contains(got.Body.String(), `id="root"`) || got.Body.String() != shell.Body.String() || !reflect.DeepEqual(got.Header(), shell.Header()) {
				t.Fatalf("authenticated %s must return production shell; status=%d root=%v", path, got.Code, strings.Contains(got.Body.String(), `id="root"`))
			}
		})
	}
	for _, path := range []string{
		"/sessions?format=json", "/sessions?format=json&limit=1", "/sessions?format=json&cursor=bad", "/sessions?format=json&range=custom&from=bad",
		"/sessions?id=" + url.QueryEscape(sessionTestID) + "&format=json", "/sessions?id=missing&format=json",
		"/search?format=json", "/search?q=prompt&format=json", "/search?q=prompt&format=json&limit=1", "/search?q=prompt&format=json&sort=relevance", "/search?format=json&cursor=bad",
	} {
		t.Run(path, func(t *testing.T) {
			spaPaths = map[string]bool{}
			before := get(path, true)
			spaPaths = production
			after := get(path, true)
			if before.Code != after.Code || before.Body.String() != after.Body.String() || !reflect.DeepEqual(before.Header(), after.Header()) {
				t.Fatal("JSON status, body or headers changed")
			}
			denied := get(path, false)
			if denied.Code != 401 || !strings.Contains(denied.Body.String(), `"error"`) {
				t.Fatalf("unauthenticated JSON=%d %q", denied.Code, denied.Body.String())
			}
		})
	}
}
