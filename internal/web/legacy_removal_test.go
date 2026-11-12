package web

import (
	"context"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

func assertSPAShell(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	shell, err := webFiles.ReadFile("static/ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != status || response.Body.String() != string(shell) || response.Header().Get("Content-Type") != "text/html; charset=utf-8" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("shell: status=%d want=%d type=%q cache=%q matches=%v", response.Code, status, response.Header().Get("Content-Type"), response.Header().Get("Cache-Control"), response.Body.String() == string(shell))
	}
}

func TestSPAOnlyPages(t *testing.T) {
	const token = "legacy-removal-test-only"
	handler, _ := newTestHandler(t, token)
	for _, path := range []string{
		"/", "/?partial=1&errors=1", "/traces", "/traces/not-yet-loaded", "/spans/not-yet-loaded/selection",
		"/sessions", "/sessions?id=missing", "/sessions/deep-link", "/search?q=weather", "/search/deep-link",
		"/dashboard", "/dashboard?format=json", "/sql", "/login", "/login?format=json", "/ui",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("HX-Request", "true")
			req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue(token)})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			assertSPAShell(t, response, http.StatusOK)
		})
	}
}

func TestSPAStaticAssets(t *testing.T) {
	handler, _ := newTestHandler(t, "asset-test-only")
	counts := map[string]int{}
	err := fs.WalkDir(webFiles, "static/ui", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		t.Run(path, func(t *testing.T) {
			original, err := webFiles.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			requestPath := "/" + strings.TrimSuffix(path, "index.html")
			response := request(t, handler, http.MethodGet, requestPath, "", "", nil)
			ext := filepath.Ext(path)
			counts[ext]++
			if response.Code != http.StatusOK || response.Body.String() != string(original) || response.Header().Get("Content-Type") != mime.TypeByExtension(ext) {
				t.Fatalf("asset: status=%d type=%q matches=%v", response.Code, response.Header().Get("Content-Type"), response.Body.String() == string(original))
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".html", ".js", ".css", ".woff2"} {
		if counts[ext] == 0 {
			t.Errorf("no %s SPA assets checked", ext)
		}
	}
}

func TestLegacyStaticAssetsRemoved(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	for _, path := range []string{"app.js", "app.css", "htmx.min.js", "uPlot.min.js", "uPlot.min.css", "fonts/bricolage-grotesque-latin.woff2", "fonts/source-sans-3-latin.woff2"} {
		t.Run(path, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, "/static/"+path, "", "", nil)
			if response.Code != http.StatusNotFound {
				t.Fatalf("retired asset status=%d", response.Code)
			}
		})
	}
}

func TestLoginFailureShell(t *testing.T) {
	handler, _ := newTestHandler(t, "login-shell-test-only")
	for _, body := range []string{"token=wrong", "token=", "token=%zz"} {
		for attempt := 0; attempt < 2; attempt++ {
			response := request(t, handler, http.MethodPost, "/login", "application/x-www-form-urlencoded", "", []byte(body))
			assertSPAShell(t, response, http.StatusUnauthorized)
			if len(response.Result().Cookies()) != 0 {
				t.Fatal("failed login set a cookie")
			}
		}
	}
}

func TestSQLFormShell(t *testing.T) {
	const token = "sql-shell-test-only"
	handler, _ := newTestHandler(t, token)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"SELECT 7 AS answer", http.StatusOK},
		{"ATTACH DATABASE '/tmp/x' AS x", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPost, "/sql", strings.NewReader(url.Values{"sql": {tc.query}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "spanbox_session", Value: sessionValue(token)})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		assertSPAShell(t, response, tc.status)
	}
}

func TestShortSpanJSONGeometry(t *testing.T) {
	handler, database := newTestHandler(t, "")
	const traceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const rootID = "bbbbbbbbbbbbbbbb"
	const childID = "cccccccccccccccc"
	spans := []store.Span{
		{TraceID: traceID, SpanID: rootID, Name: "root", Kind: "other", StartNs: 1000, EndNs: 2000, DurationMs: .001},
		{TraceID: traceID, SpanID: childID, ParentSpanID: rootID, Name: "child", Kind: "other", StartNs: 1500, EndNs: 1501, DurationMs: .000001},
	}
	if _, err := database.InsertBatch(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	trace := agentGet(t, handler, "/traces/"+traceID+"?format=json")
	rows := trace["spans"].([]any)
	if len(rows) != 2 {
		t.Fatalf("span count=%d", len(rows))
	}
	child := rows[1].(map[string]any)
	if child["span_id"] != childID || child["parent_span_id"] != rootID || child["start_offset_ms"] != .0005 || child["duration_ms"] != .000001 {
		t.Fatalf("short span geometry=%v", child)
	}
}
