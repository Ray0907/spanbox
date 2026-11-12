package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
)

func TestProxyAuth(t *testing.T) {
	var upstreamPath, spanboxHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath, spanboxHeader = r.URL.Path, r.Header.Get("X-Spanbox-Token")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	database, err := store.Open(t.TempDir() + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	prices, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Deps{Cfg: config.Config{AuthToken: "secret", AnthropicUpstream: upstream.URL}, Store: database, Pricing: prices, Version: "test", Logf: t.Logf})

	call := func(path, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("X-Spanbox-Token", token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	for _, input := range []struct {
		name, path, token string
		want              int
	}{
		{"missing", "/proxy/anthropic/v1/models", "", http.StatusUnauthorized},
		{"wrong", "/proxy/anthropic/v1/models", "wrong", http.StatusUnauthorized},
		{"header", "/proxy/anthropic/v1/models", "secret", http.StatusOK},
		{"path", "/proxy/t/secret/anthropic/v1/models", "", http.StatusOK},
	} {
		t.Run(input.name, func(t *testing.T) {
			response := call(input.path, input.token)
			if response.Code != input.want {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
			if input.want == http.StatusUnauthorized && response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("content type=%q", response.Header().Get("Content-Type"))
			}
		})
	}
	if upstreamPath != "/v1/models" || spanboxHeader != "" {
		t.Fatalf("upstream path=%q spanbox header=%q", upstreamPath, spanboxHeader)
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
	if loginPage.Code != http.StatusOK || !strings.Contains(loginPage.Body.String(), `id="root"`) {
		t.Fatalf("login page status=%d body=%q", loginPage.Code, loginPage.Body.String())
	}
	if got := loginPage.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Fatalf("login CSP = %q", got)
	}
	font := request(t, handler, http.MethodGet, "/static/ui/bricolage-grotesque-latin.woff2", "", "", nil)
	if font.Code != http.StatusOK || font.Header().Get("Content-Type") != "font/woff2" {
		t.Fatalf("public font status=%d type=%q", font.Code, font.Header().Get("Content-Type"))
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
