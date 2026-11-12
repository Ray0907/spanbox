package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBlock5ShellContracts(t *testing.T) {
	const token = "block5-test-only"
	handler, _ := newTestHandler(t, token)
	get := func(path string, authenticated bool, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue(token)})
		}
		req.Header.Set("Accept", accept)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/", "/dashboard", "/sql", "/login"} {
		t.Run(path, func(t *testing.T) {
			for _, authenticated := range []bool{false, true} {
				rec := get(path, authenticated, "text/html")
				if !authenticated && path != "/login" {
					if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
						t.Fatalf("protected %s: status=%d location=%q", path, rec.Code, rec.Header().Get("Location"))
					}
					continue
				}
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) {
					t.Fatalf("%s authenticated=%t: status=%d, want shell", path, authenticated, rec.Code)
				}
				if rec.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
					t.Fatal("shell must retain strict CSP")
				}
			}
		})
	}
	for _, tc := range []struct{ path, accept string }{
		{"/dashboard/data?range=24h", ""},
		{"/sql?format=json", ""},
		{"/sql", "application/json"},
		{"/?format=json", ""},
	} {
		rec := get(tc.path, true, tc.accept)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("JSON passthrough %s Accept=%s: %d %s", tc.path, tc.accept, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	for _, path := range []string{"/dashboard/child", "/sql/child", "/login/child", "/not-a-route"} {
		rec := get(path, true, "")
		if strings.Contains(rec.Body.String(), `id="root"`) {
			t.Errorf("exact-only route captured %s", path)
		}
	}
	open, _ := newTestHandler(t, "")
	for _, path := range []string{"/sql", "/sql?format=json"} {
		rec := httptest.NewRecorder()
		open.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "set AUTH_TOKEN to enable") {
			t.Errorf("disabled SQL %s: %d", path, rec.Code)
		}
	}
}

func TestBlock5LoginFormUnchanged(t *testing.T) {
	const token = "block5-test-only"
	handler, _ := newTestHandler(t, token)
	for _, tc := range []struct {
		name, media, body string
		status            int
	}{
		{"success", "application/x-www-form-urlencoded", url.Values{"token": {token}}.Encode(), http.StatusFound},
		{"bad token", "application/x-www-form-urlencoded", "token=wrong", http.StatusUnauthorized},
		{"JSON rejected", "application/json", `{}`, http.StatusUnsupportedMediaType},
		{"oversize", "application/x-www-form-urlencoded", "token=" + strings.Repeat("x", 4096), http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login?next=https://example.invalid/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.media)
			req.Header.Set("X-Forwarded-Proto", "https")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want=%d", rec.Code, tc.status)
			}
			if tc.status != http.StatusFound {
				if len(rec.Result().Cookies()) != 0 {
					t.Fatal("failed login set a cookie")
				}
				return
			}
			if rec.Header().Get("Location") != "/" {
				t.Fatal("login target changed or became an open redirect")
			}
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != "spanbox_session" || cookies[0].Value != sessionValue(token) || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" {
				t.Fatal("cookie contract changed")
			}
		})
	}
}

func TestBlock5JSONByteParity(t *testing.T) {
	handler, _ := newTestHandler(t, "block5-test-only")
	for _, path := range []string{"/?format=json", "/sql?format=json", "/dashboard?format=json", "/dashboard/data?range=custom&from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z"} {
		previous := spaPaths
		responses := make([]string, 0, 2)
		for _, routes := range []map[string]bool{previous, {}} {
			spaPaths = routes
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("block5-test-only")})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			responses = append(responses, rec.Result().Status+"\n"+rec.Header().Get("Content-Type")+"\n"+rec.Body.String())
		}
		spaPaths = previous
		if responses[0] != responses[1] {
			t.Errorf("response bytes changed for %s", path)
		}
	}
}

func TestBlock5SQLSchemaFailureShellContract(t *testing.T) {
	handler, database := newTestHandler(t, "block5-test-only")
	if err := database.Reader().Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/sql", http.StatusOK},
		{"/sql?format=json", http.StatusInternalServerError},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("block5-test-only")})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("schema failure %s: status=%d, want %d", tc.path, rec.Code, tc.status)
		}
		if tc.status == http.StatusOK && !strings.Contains(rec.Body.String(), `id="root"`) {
			t.Error("HTML must serve the shell despite schema failure")
		}
		if tc.status == http.StatusInternalServerError && !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Error("schema failure must remain a JSON error")
		}
	}
}
