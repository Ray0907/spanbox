package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestProxyToolCallsMigration(t *testing.T) {
	dir := t.TempDir()
	old, err := sql.Open("sqlite", filepath.Join(dir, "spanbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:2] {
		if _, err := old.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.Exec("PRAGMA user_version=2; INSERT INTO traces VALUES ('0123456789abcdef0123456789abcdef','legacy','service',1,2,0,0,0,NULL,NULL,NULL,0,'','','')"); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err := db.r.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 3 {
		t.Fatalf("schema=%d err=%v", version, err)
	}
	if err := db.r.QueryRow("SELECT count(*) FROM traces").Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy traces=%d err=%v", count, err)
	}
	if err := db.r.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='index' AND tbl_name='proxy_tool_calls' AND sql LIKE '%created_ns%'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("created index=%d err=%v", count, err)
	}
}

func TestProxyToolCallsRetention(t *testing.T) {
	db := openTestStore(t)
	for _, row := range []struct {
		key     string
		created int64
	}{{"old", 9}, {"boundary", 10}, {"fresh", 11}} {
		if _, err := db.w.Exec(`INSERT INTO proxy_tool_calls (key,trace_id,span_id,end_ns,first_result_ns,has_result,name,arguments,created_ns) VALUES (?, '0123456789abcdef0123456789abcdef','0123456789abcdef',1,0,0,'read','{}',?)`, row.key, row.created); err != nil {
			t.Fatal(err)
		}
	}
	// A sweep with no trace batches must still expire correlation rows.
	if n, err := db.Purge(context.Background(), 10); err != nil || n != 0 {
		t.Fatalf("purge=%d err=%v", n, err)
	}
	var count int
	if err := db.r.QueryRow("SELECT count(*) FROM proxy_tool_calls").Scan(&count); err != nil || count != 2 {
		t.Fatalf("retained=%d err=%v", count, err)
	}
	if err := db.r.QueryRow("SELECT count(*) FROM proxy_tool_calls WHERE key='boundary'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("boundary=%d err=%v", count, err)
	}
}
