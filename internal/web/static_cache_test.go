package web

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"
)

// Hashed build assets and fonts are immutable; the shell and unknown paths are not.
func TestSPAAssetCacheHeaders(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	immutable := 0
	err := fs.WalkDir(webFiles, "static/ui", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.HasSuffix(path, "index.html") {
			return err
		}
		response := request(t, handler, http.MethodGet, "/"+path, "", "", nil)
		if got := response.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("%s cache=%q, want immutable", path, got)
		}
		immutable++
		return nil
	})
	if err != nil || immutable == 0 {
		t.Fatalf("walk: err=%v checked=%d", err, immutable)
	}
	for _, path := range []string{"/static/ui/", "/static/ui/missing.js", "/ui"} {
		response := request(t, handler, http.MethodGet, path, "", "", nil)
		if strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s must not be immutable", path)
		}
	}
}

// The CSP forbids inline styles, so the shell must carry none (replaces the old trace-page style= check).
func TestSPAShellHasNoInlineStyle(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	for _, path := range []string{"/ui", "/login", "/traces"} {
		response := request(t, handler, http.MethodGet, path, "", "", nil)
		body := response.Body.String()
		if response.Code != http.StatusOK || strings.Contains(body, "<style") || strings.Contains(body, ` style="`) {
			t.Errorf("%s status=%d inline style present", path, response.Code)
		}
	}
}
