package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
)

func TestCheckUserSQL(t *testing.T) {
	accepted := []string{
		"SELECT 1",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"EXPLAIN QUERY PLAN SELECT 1",
		"select * from spans -- attach comment",
		"SELECT 'ATTACH' AS s",
		`SELECT "PRAGMA"`,
	}
	for _, query := range accepted {
		t.Run("accept "+query, func(t *testing.T) {
			if err := CheckUserSQL(query); err != nil {
				t.Fatalf("rejected safe SQL: %v", err)
			}
		})
	}
	rejected := []string{
		"ATTACH DATABASE '/etc/x' AS y",
		"PRAGMA query_only=0",
		"SELECT 1; SELECT 2",
		"DELETE FROM spans",
		"SELECT load_extension('x')",
		"/* x */ INSERT INTO spans VALUES (1)",
		"",
		"SELECT " + strings.Repeat("x", config.SQLMaxTextBytes),
	}
	for _, query := range rejected {
		t.Run("reject "+query[:min(len(query), 40)], func(t *testing.T) {
			if err := CheckUserSQL(query); !errors.Is(err, ErrSQLRejected) {
				t.Fatalf("got %v, want ErrSQLRejected", err)
			}
		})
	}
}

func TestRunUserSQLRowsAndLimits(t *testing.T) {
	store := openTestStore(t)
	span := testSpan(traceID(1), spanID(1), 1, 2)
	span.Name = "visible"
	if err := store.InsertBatch(context.Background(), []Span{span}); err != nil {
		t.Fatal(err)
	}
	result, err := store.RunUserSQL(context.Background(), "SELECT name FROM spans")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 1 || result.Columns[0] != "name" || len(result.Rows) != 1 || result.Rows[0][0] != "visible" {
		t.Fatalf("unexpected result: %+v", result)
	}

	result, err = store.RunUserSQL(context.Background(), "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<2000) SELECT x FROM n")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != config.SQLMaxRows || !result.Truncated {
		t.Fatalf("rows=%d truncated=%v", len(result.Rows), result.Truncated)
	}

	result, err = store.RunUserSQL(context.Background(), "SELECT lower(hex(zeroblob(35000))) AS big")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0][0]) > config.SQLMaxCellBytes || !strings.Contains(result.Rows[0][0], "truncated") || !result.Truncated {
		t.Fatalf("cell bytes=%d truncated=%v", len(result.Rows[0][0]), result.Truncated)
	}
}

func TestRunUserSQLInterruptLeavesConnectionUsable(t *testing.T) {
	store := openTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := store.RunUserSQL(ctx, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c")
	if err == nil {
		t.Fatal("expected recursive query to be interrupted")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("interrupt took %v", elapsed)
	}
	result, err := store.RunUserSQL(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("following query failed: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0] != "1" {
		t.Fatalf("unexpected following result: %+v", result)
	}
}

func TestReaderPoolIsReadOnly(t *testing.T) {
	store := openTestStore(t)
	_, err := store.Reader().Exec("DELETE FROM spans")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("got %v, want readonly error", err)
	}
}
