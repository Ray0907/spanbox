package web

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAgentSearchCursorDistinguishesTraces(t *testing.T) {
	handler, database := newTestHandler(t, "")
	agentSeed(t, handler, 60, "collision")
	var seq int
	var name, path string
	if err := database.Reader().QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("UPDATE spans SET start_ns=123, span_id=?", strings.Repeat("ab", 8)); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page := agentGet(t, handler, "/search?format=json&q=needle&limit=50&cursor="+url.QueryEscape(cursor))
		for _, item := range page["items"].([]any) {
			hit := item.(map[string]any)
			trace := hit["trace_id"].(string)
			if seen[trace] {
				t.Fatalf("duplicate trace %s", trace)
			}
			seen[trace] = true
		}
		cursor, _ = page["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	if len(seen) != 60 {
		t.Fatalf("got %d of 60 hits sharing start_ns and span_id", len(seen))
	}
	legacy := "123:" + strings.Repeat("ab", 8)
	agentGet(t, handler, "/search?format=json&q=needle&cursor="+url.QueryEscape(legacy))
	for _, cursor := range []string{legacy + ":", legacy + ":bad", legacy + ":" + strings.Repeat("zz", 16), legacy + ":" + strings.Repeat("ab", 16) + ":extra"} {
		response := request(t, handler, http.MethodGet, "/search?format=json&q=needle&cursor="+url.QueryEscape(cursor), "", "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid cursor %q: status %d", cursor, response.Code)
		}
	}
}
