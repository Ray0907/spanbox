package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const traceA = "0123456789abcdef0123456789abcdef"

func TestProxyTurnsMigration(t *testing.T) {
	dir := t.TempDir()
	old, err := sql.Open("sqlite", filepath.Join(dir, "spanbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:3] {
		if _, err := old.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.Exec(`PRAGMA user_version=3; INSERT INTO traces VALUES ('0123456789abcdef0123456789abcdef','legacy','service',1,2,0,0,0,NULL,NULL,NULL,0,'','','')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err := db.r.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	if err := db.r.QueryRow("SELECT count(*) FROM traces WHERE name='legacy'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy traces=%d err=%v", count, err)
	}
	if err := db.r.QueryRow("SELECT count(*) FROM proxy_turns").Scan(&count); err != nil || count != 0 {
		t.Fatalf("turns=%d err=%v", count, err)
	}
}

func TestProxyTurnsAtomicMerge(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	root := testSpan(traceA, "aaaaaaaaaaaaaaaa", 100, 200)
	root.Kind = "agent"
	root.Name = "invoke_agent demo"
	root.InputContent = "prompt"
	turn := ProxyTurn{Key: "turn", Route: "openai", ScopeSession: "session", Root: root, CreatedNs: 10}
	model := testSpan(traceA, "bbbbbbbbbbbbbbbb", 100, 200)
	model.Kind = "llm"
	model.ParentSpanID = root.SpanID
	input, output, cost := int64(10), int64(3), 0.1
	model.InputTokens = &input
	model.OutputTokens = &output
	model.CostUSD = &cost
	call := ProxyToolCall{Key: "call", TraceID: traceA, SpanID: model.SpanID, Arguments: "{}", CreatedNs: 10}
	if _, err := db.w.Exec(`CREATE TRIGGER fail_turn BEFORE INSERT ON proxy_tool_calls BEGIN SELECT RAISE(ABORT,'rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertProxyTurnBatch(ctx, []Span{model}, []ProxyToolCall{call}, &turn); err == nil {
		t.Fatal("wanted rollback")
	}
	for _, table := range []string{"proxy_turns", "proxy_tool_calls", "spans", "traces"} {
		var count int
		if err := db.r.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s leaked %d rows: %v", table, count, err)
		}
	}
	if _, err := db.w.Exec("DROP TRIGGER fail_turn"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertProxyTurnBatch(ctx, []Span{model}, []ProxyToolCall{call}, &turn); err != nil {
		t.Fatal(err)
	}
	// Competing stale snapshots must not shrink the root or clear an error.
	var wg sync.WaitGroup
	for _, window := range []struct {
		start, end int64
		status     int32
		output     string
	}{{50, 150, 2, "old error"}, {110, 300, 0, "latest"}, {90, 250, 0, "middle"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			update := turn
			update.Root.StartNs = window.start
			update.Root.EndNs = window.end
			update.Root.StatusCode = window.status
			update.Root.OutputContent = window.output
			if err := db.InsertProxyTurnBatch(ctx, nil, nil, &update); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, found, err := db.GetProxyTurn(ctx, "turn", "", "")
	if err != nil || !found {
		t.Fatalf("found=%t err=%v", found, err)
	}
	if got.Root.StartNs != 50 || got.Root.EndNs != 300 || got.Root.StatusCode != 2 || got.Root.OutputContent != "latest" || got.Root.InputContent != "prompt" {
		t.Fatalf("bad merged root: %+v", got.Root)
	}
	var tokens int64
	var total float64
	var count int
	if err := db.r.QueryRow("SELECT input_tokens,cost_usd,span_count FROM traces WHERE trace_id=?", traceA).Scan(&tokens, &total, &count); err != nil || tokens != input || total != cost || count != 2 {
		t.Fatalf("trace=%d/%g/%d err=%v", tokens, total, count, err)
	}
}

func TestProxyTurnsWaitForWriter(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "existing"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			turn := ProxyTurn{Key: "turn", Route: "openai", ScopeSession: "session", Root: testSpan(traceA, "aaaaaaaaaaaaaaaa", 100, 200), CreatedNs: 10}
			if existing {
				if err := db.InsertProxyTurnBatch(ctx, nil, nil, &turn); err != nil {
					t.Fatal(err)
				}
			}
			writer, err := sql.Open("sqlite", filepath.Join(dir, "spanbox.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			defer writer.ExecContext(context.Background(), "ROLLBACK")
			done := make(chan error, 1)
			go func() { done <- db.InsertProxyTurnBatch(ctx, nil, nil, &turn) }()
			select {
			case err := <-done:
				t.Fatalf("turn insert returned before writer released: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if _, err := writer.ExecContext(ctx, "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			got, found, err := db.GetProxyTurn(ctx, turn.Key, "", "")
			if err != nil || !found || got.Root.StartNs != 100 || got.Root.EndNs != 200 {
				t.Fatalf("turn=%+v found=%t err=%v", got, found, err)
			}
		})
	}
}

func TestProxyTurnsLatestAndRetention(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	for i, key := range []string{"old", "boundary", "fresh"} {
		root := testSpan(traceA, "aaaaaaaaaaaaaaaa", int64(i+1), int64(i+2))
		root.Kind = "agent"
		turn := ProxyTurn{Key: key, Route: "openai", ScopeSession: "session", Root: root, CreatedNs: int64(9 + i)}
		if err := db.InsertProxyTurnBatch(ctx, nil, nil, &turn); err != nil {
			t.Fatal(err)
		}
	}
	latest, found, err := db.GetProxyTurn(ctx, "", "openai", "session")
	if err != nil || !found || latest.Key != "fresh" {
		t.Fatalf("latest=%+v found=%t err=%v", latest, found, err)
	}
	for _, scope := range [][2]string{{"other", "session"}, {"openai", "other"}} {
		if _, found, err := db.GetProxyTurn(ctx, "", scope[0], scope[1]); err != nil || found {
			t.Fatalf("cross-scope turn: found=%t err=%v", found, err)
		}
	}
	if _, err := db.Purge(ctx, 10); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"old", "boundary", "fresh"} {
		_, found, err := db.GetProxyTurn(ctx, key, "", "")
		if err != nil || found != (key != "old") {
			t.Fatalf("retention key=%s found=%t err=%v", key, found, err)
		}
	}
}
