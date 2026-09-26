package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUserSQLDoesNotStarveNormalReaders(t *testing.T) {
	store := openTestStore(t)
	span := testSpan(traceID(1), spanID(1), 1, 2)
	if err := store.InsertBatch(context.Background(), []Span{span}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	started := make(chan struct{}, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			_, _ = store.RunUserSQL(ctx, "WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c")
		}()
	}
	defer func() { cancel(); wg.Wait() }()
	for i := 0; i < 4; i++ {
		<-started
	}
	time.Sleep(100 * time.Millisecond)
	pageCtx, pageCancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer pageCancel()
	startedAt := time.Now()
	rows, err := store.ListTraces(pageCtx, TraceFilter{Limit: 10})
	if err != nil || len(rows) != 1 || time.Since(startedAt) > 300*time.Millisecond {
		t.Fatalf("normal page query blocked behind user SQL: rows=%d err=%v elapsed=%v", len(rows), err, time.Since(startedAt))
	}
}

func TestReaderKeepsFourIdleConnections(t *testing.T) {
	store := openTestStore(t)
	before := store.Reader().Stats().MaxIdleClosed
	var connections [4]interface{ Close() error }
	for i := range connections {
		conn, err := store.Reader().Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections[i] = conn
	}
	for _, conn := range connections {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if after := store.Reader().Stats().MaxIdleClosed; after != before {
		t.Fatalf("reader closed %d idle connections", after-before)
	}
}

func TestCloseStopsUserSQL(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunUserSQL(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("user SQL pool remained open after Store.Close")
	}
}
