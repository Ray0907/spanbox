package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIPage(t *testing.T) {
	handler, _ := newTestHandler(t, "secret")

	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/ui", nil))
	if denied.Code != http.StatusFound || denied.Header().Get("Location") != "/login" {
		t.Fatalf("unauthenticated /ui = %d %q, want redirect to /login", denied.Code, denied.Header().Get("Location"))
	}

	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("secret")})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	_, err := fs.Stat(webFiles, "static/ui/index.html")
	if err != nil {
		if rec.Code != http.StatusNotFound {
			t.Fatalf("/ui without a built UI = %d, want 404", rec.Code)
		}
		return
	}
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("/ui = %d %q, want the built UI shell", rec.Code, rec.Header().Get("Content-Type"))
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP = %q, the UI must not need unsafe-inline", csp)
	}
}
