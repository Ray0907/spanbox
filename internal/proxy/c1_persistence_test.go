package proxy

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

func c1Tool(t *testing.T, db *store.Store) store.Span {
	t.Helper()
	var spans []store.Span
	if err := json.Unmarshal(c1Dump(t, db), &spans); err != nil {
		t.Fatal(err)
	}
	for _, span := range spans {
		if span.Kind == "tool" {
			return span
		}
	}
	t.Fatal("missing tool span")
	return store.Span{}
}

func c1Evict(t *testing.T, h *Handler) {
	t.Helper()
	calls := make([]any, maxToolCalls)
	for i := range calls {
		calls[i] = map[string]any{"id": fmt.Sprintf("evict-%d", i), "type": "function", "function": map[string]any{"name": "read", "arguments": "{}"}}
	}
	body, err := json.Marshal(map[string]any{"id": "eviction", "model": "gpt-test", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": calls}}}})
	if err != nil {
		t.Fatal(err)
	}
	h.capture(Exchange{Vendor: "openai", Path: "/v1/chat/completions", RequestBody: []byte(`{"model":"gpt-test","messages":[]}`), ResponseBody: body, StatusCode: 200, StartNs: 9000000000, EndNs: 9100000000})
}

func TestC1ToolPersistence(t *testing.T) {
	for _, c := range c1Cases(t) {
		if len(c.Exchanges) < 2 {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			reference, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer reference.Close()
			continuous := c1Handler(t, reference)
			continuous.capture(c.Exchanges[0])
			continuous.capture(c.Exchanges[1])
			want := c1Tool(t, reference)
			if want.ParentSpanID == "" {
				t.Skip("HEAD produces an orphan for this non-stream Codex fixture; covered by golden bytes")
			}
			if want.DurationMs <= 0 {
				t.Fatalf("bad reference duration: %+v", want)
			}
			for _, mode := range []string{"restart-before-result", "restart-after-result", "evict-before-result", "evict-after-result"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					db, err := store.Open(dir)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { db.Close() }()
					h := c1Handler(t, db)
					h.capture(c.Exchanges[0])
					result := c.Exchanges[1]
					if strings.HasSuffix(mode, "after-result") {
						h.capture(result)
						result.StartNs += 5000000000
						result.EndNs += 5000000000
					}
					if strings.HasPrefix(mode, "restart") {
						if err := db.Close(); err != nil {
							t.Fatal(err)
						}
						db, err = store.Open(dir)
						if err != nil {
							t.Fatal(err)
						}
						h = c1Handler(t, db)
					} else {
						c1Evict(t, h)
					}
					h.capture(result)
					got := c1Tool(t, db)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("tool changed across %s: got %+v want %+v", mode, got, want)
					}
					var count int
					if err := db.Reader().QueryRow("SELECT count(*) FROM spans WHERE kind='tool'").Scan(&count); err != nil || count != 1 {
						t.Fatalf("orphan added: count=%d err=%v", count, err)
					}
				})
			}
		})
	}
}

func TestC1DroppedOriginPersistence(t *testing.T) {
	c := c1Cases(t)[2]
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	h := c1Handler(t, db)
	h.recordDroppedOrigins(c.Exchanges[0])
	var count int
	if err := db.Reader().QueryRow("SELECT count(*) FROM proxy_tool_calls").Scan(&count); err != nil || count != 1 {
		t.Fatalf("dropped origin not persisted: count=%d err=%v", count, err)
	}
	if err := db.Reader().QueryRow("SELECT count(*) FROM spans").Scan(&count); err != nil || count != 0 {
		t.Fatalf("dropped capture stored spans: count=%d err=%v", count, err)
	}
	// Dropping a result must not record a first-result timestamp without its span.
	h.recordDroppedOrigins(c.Exchanges[1])
	if err := db.Reader().QueryRow("SELECT count(*) FROM proxy_tool_calls WHERE has_result").Scan(&count); err != nil || count != 0 {
		t.Fatalf("dropped result changed correlation: count=%d err=%v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h = c1Handler(t, db)
	// A repeated dropped issue after restart must not replace the original timing.
	retry := c.Exchanges[0]
	retry.EndNs += 500000000
	h.recordDroppedOrigins(retry)
	h.capture(c.Exchanges[1])
	got := c1Tool(t, db)
	wantDuration := float64(c.Exchanges[1].StartNs-c.Exchanges[0].EndNs) / 1e6
	if got.ParentSpanID == "" || got.Name != "execute_tool bash" || got.DurationMs != wantDuration {
		t.Fatalf("lost dropped origin after restart: %+v, want duration=%v", got, wantDuration)
	}
}

func TestC1CaptureRollback(t *testing.T) {
	c := c1Cases(t)[2] // known issuing call and result, with a session
	for _, phase := range []string{"origin", "first-result", "dropped-origin"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			db, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "spanbox.db")))
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			h := c1Handler(t, db)
			capture := h.capture
			if phase == "dropped-origin" {
				capture = h.recordDroppedOrigins
			}
			e := c.Exchanges[0]
			event := "INSERT"
			if phase == "first-result" {
				h.capture(e)
				e = c.Exchanges[1]
				event = "UPDATE"
			}
			before := c1Dump(t, db)
			if _, err := writer.Exec("CREATE TRIGGER fail_c1 BEFORE " + event + " ON proxy_tool_calls BEGIN SELECT RAISE(ABORT, 'c1 rollback'); END"); err != nil {
				t.Fatal(err)
			}
			var logs []string
			h.logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
			capture(e)
			if len(logs) == 0 {
				t.Fatal("capture did not report failed transaction")
			}
			if string(c1Dump(t, db)) != string(before) {
				t.Fatal("span committed without its tool origin/result")
			}
			if _, err := writer.Exec("DROP TRIGGER fail_c1"); err != nil {
				t.Fatal(err)
			}
			var results int
			if err := db.Reader().QueryRow("SELECT count(*) FROM proxy_tool_calls WHERE has_result").Scan(&results); err != nil || results != 0 {
				t.Fatalf("failed result persisted: count=%d err=%v", results, err)
			}
			h.logf = func(f string, a ...any) { t.Errorf(f, a...) }
			if phase == "first-result" {
				e.StartNs += 2000000000
				e.EndNs += 2000000000
			}
			capture(e)
			var count int
			if err := db.Reader().QueryRow("SELECT count(*) FROM proxy_tool_calls").Scan(&count); err != nil || count != 1 {
				t.Fatalf("retry did not commit correlation: count=%d err=%v", count, err)
			}
			if phase != "first-result" {
				h.capture(c.Exchanges[1])
			} else {
				var first int64
				if err := db.Reader().QueryRow("SELECT first_result_ns FROM proxy_tool_calls").Scan(&first); err != nil || first != e.StartNs {
					t.Fatalf("failed result leaked through cache: first=%d want=%d err=%v", first, e.StartNs, err)
				}
			}
			want := c1Tool(t, db)
			if want.ParentSpanID == "" || want.DurationMs <= 0 {
				t.Fatalf("retry lost origin: %+v", want)
			}
			// A fresh handler must agree with the successfully committed cache state.
			h = c1Handler(t, db)
			h.capture(c.Exchanges[1])
			if got := c1Tool(t, db); !reflect.DeepEqual(got, want) {
				t.Fatalf("retry result not persisted: %+v", got)
			}
		})
	}
}
