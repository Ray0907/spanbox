package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStaticCacheHeaders(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	for _, path := range []string{"htmx.min.js", "uPlot.min.js", "uPlot.min.css", "fonts/bricolage-grotesque-latin.woff2", "fonts/source-sans-3-latin.woff2"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/"+path, nil))
			original, err := webFiles.ReadFile("static/" + path)
			if err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Cache-Control"), "immutable") || response.Body.String() != string(original) {
				t.Fatalf("status=%d cache=%q body matches=%v", response.Code, response.Header().Get("Cache-Control"), response.Body.String() == string(original))
			}
		})
	}
	for _, path := range []string{"app.js", "app.css", "missing.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/"+path, nil))
		if strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("%s cache=%q", path, response.Header().Get("Cache-Control"))
		}
		if path == "missing.js" && response.Code != http.StatusNotFound {
			t.Fatalf("missing file status=%d", response.Code)
		}
	}
}

func TestLoginTemplateIsNotParsedPerRequest(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	allocs := testing.AllocsPerRun(10, func() {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "<html") {
			t.Fatalf("login status=%d body=%q", response.Code, response.Body.String())
		}
	})
	if allocs > 100 {
		t.Fatalf("login allocations per request = %g, want <= 100", allocs)
	}
}
