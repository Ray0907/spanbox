package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestSPAShell(t *testing.T) {
	handler, _ := newTestHandler(t, "mock-dev-token")
	request := func(path string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("mock-dev-token")})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	shell := request("/ui", true)
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), `id="root"`) {
		t.Fatal("built /ui shell is required")
	}
	previous := spaPaths
	spaPaths = map[string]bool{"/sessions": true, "/traces": true, "/spans": true}
	t.Cleanup(func() { spaPaths = previous })
	for _, path := range []string{"/sessions", "/sessions/example", "/traces", "/traces/11111111111111111111111111111111", "/spans/11111111111111111111111111111111/2222222222222222"} {
		t.Run(path, func(t *testing.T) {
			denied := request(path, false)
			if denied.Code != http.StatusFound || denied.Header().Get("Location") != "/login" || strings.Contains(denied.Body.String(), `id="root"`) {
				t.Fatalf("unauthenticated = %d %q, want redirect only", denied.Code, denied.Header().Get("Location"))
			}
			got := request(path, true)
			if got.Code != shell.Code || got.Body.String() != shell.Body.String() || !reflect.DeepEqual(got.Header(), shell.Header()) {
				t.Fatalf("authenticated %s = %d, want /ui body and headers", path, got.Code)
			}
		})
	}
}

func TestSPAPassthrough(t *testing.T) {
	handler, _ := newTestHandler(t, "mock-dev-token")
	previous := spaPaths
	t.Cleanup(func() { spaPaths = previous })
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/sessions?format=json"},
		{http.MethodGet, "/traces/11111111111111111111111111111111?format=json"},
		{http.MethodPost, "/sessions"},
		{http.MethodPut, "/sessions/example"},
		{http.MethodHead, "/sessions"},
		{http.MethodGet, "/"},
		{http.MethodGet, "/search"},
		{http.MethodGet, "/dashboard"},
		{http.MethodGet, "/sessions-other"},
		{http.MethodGet, "/ui"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			request := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(tc.method, tc.path, nil)
				req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("mock-dev-token")})
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}
			spaPaths = map[string]bool{}
			want := request()
			spaPaths = map[string]bool{"/sessions": true, "/traces": true}
			got := request()
			if got.Code != want.Code || got.Body.String() != want.Body.String() || !reflect.DeepEqual(got.Header(), want.Header()) {
				t.Fatalf("response changed: status %d -> %d", want.Code, got.Code)
			}
		})
	}
}

func TestUILayerStylesheet(t *testing.T) {
	handler, _ := newTestHandler(t, "mock-dev-token")
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue("mock-dev-token")})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	shell := get("/ui")
	if shell.Code != http.StatusOK {
		t.Fatalf("shell status = %d", shell.Code)
	}
	csp := shell.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP must remain strict: %q", csp)
	}
	var link string
	for _, tag := range regexp.MustCompile(`<link\b[^>]*>`).FindAllString(shell.Body.String(), -1) {
		if strings.Contains(tag, `id="astryx-layer-slide-rules"`) {
			if link != "" {
				t.Fatal("duplicate layer stylesheet link")
			}
			link = tag
		}
	}
	if link == "" || !strings.Contains(link, `rel="stylesheet"`) {
		t.Fatal("shell must preload a stylesheet link with id astryx-layer-slide-rules")
	}
	href := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(link)
	if len(href) != 2 || !strings.HasPrefix(href[1], "/static/ui/") || !strings.HasSuffix(href[1], ".min.css") {
		t.Fatalf("layer stylesheet must be external under /static/ui/: %q", link)
	}
	css := get(href[1])
	if css.Code != http.StatusOK || !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") || !strings.Contains(css.Body.String(), "@position-try") {
		t.Fatalf("stylesheet = %d %q, want CSS with @position-try", css.Code, css.Header().Get("Content-Type"))
	}
}

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
