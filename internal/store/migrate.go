package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var migrations = []string{`
CREATE TABLE spans (
  id              INTEGER PRIMARY KEY,
  trace_id        TEXT NOT NULL CHECK(length(trace_id) = 32),
  span_id         TEXT NOT NULL CHECK(length(span_id) = 16),
  parent_span_id  TEXT NOT NULL DEFAULT '',
  name            TEXT NOT NULL,
  kind            TEXT NOT NULL CHECK(kind IN ('llm','embedding','tool','retrieval','agent','other')),
  service_name    TEXT NOT NULL DEFAULT 'unknown',
  start_ns        INTEGER NOT NULL,
  end_ns          INTEGER NOT NULL,
  duration_ms     REAL NOT NULL,
  status_code     INTEGER NOT NULL DEFAULT 0,
  status_message  TEXT NOT NULL DEFAULT '',
  provider        TEXT NOT NULL DEFAULT '',
  request_model   TEXT NOT NULL DEFAULT '',
  response_model  TEXT NOT NULL DEFAULT '',
  input_tokens    INTEGER,
  output_tokens   INTEGER,
  cache_read_tokens INTEGER,
  cost_usd        REAL,
  cost_source     TEXT NOT NULL DEFAULT '',
  input_content   TEXT NOT NULL DEFAULT '',
  output_content  TEXT NOT NULL DEFAULT '',
  tool_name       TEXT NOT NULL DEFAULT '',
  tool_call_id    TEXT NOT NULL DEFAULT '',
  finish_reason   TEXT NOT NULL DEFAULT '',
  session_id      TEXT NOT NULL DEFAULT '',
  user_id         TEXT NOT NULL DEFAULT '',
  trace_state     TEXT NOT NULL DEFAULT '',
  attributes      TEXT NOT NULL DEFAULT '{}',
  events          TEXT NOT NULL DEFAULT '[]',
  links           TEXT NOT NULL DEFAULT '[]',
  resource        TEXT NOT NULL DEFAULT '{}',
  scope           TEXT NOT NULL DEFAULT '{}',
  UNIQUE (trace_id, span_id)
);
CREATE INDEX spans_start   ON spans(start_ns);
CREATE INDEX spans_kind    ON spans(kind, start_ns);
CREATE INDEX spans_model   ON spans(request_model, start_ns);
CREATE INDEX spans_session ON spans(session_id);
CREATE INDEX spans_user    ON spans(user_id);

CREATE TABLE traces (
  trace_id      TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  service_name  TEXT NOT NULL,
  start_ns      INTEGER NOT NULL,
  end_ns        INTEGER NOT NULL,
  duration_ms   REAL NOT NULL,
  span_count    INTEGER NOT NULL,
  llm_count     INTEGER NOT NULL,
  input_tokens  INTEGER,
  output_tokens INTEGER,
  cost_usd      REAL,
  has_error     INTEGER NOT NULL,
  session_id    TEXT NOT NULL,
  user_id       TEXT NOT NULL,
  models        TEXT NOT NULL
);
CREATE INDEX traces_start   ON traces(start_ns DESC, trace_id DESC);
CREATE INDEX traces_error   ON traces(has_error, start_ns DESC);
CREATE INDEX traces_session ON traces(session_id);
CREATE INDEX traces_user    ON traces(user_id);

CREATE VIRTUAL TABLE spans_fts USING fts5(
  input_content, output_content, name,
  content='spans', content_rowid='id'
);
CREATE TRIGGER spans_ai AFTER INSERT ON spans BEGIN
  INSERT INTO spans_fts(rowid, input_content, output_content, name)
  VALUES (new.id, new.input_content, new.output_content, new.name);
END;
CREATE TRIGGER spans_ad AFTER DELETE ON spans BEGIN
  INSERT INTO spans_fts(spans_fts, rowid, input_content, output_content, name)
  VALUES ('delete', old.id, old.input_content, old.output_content, old.name);
END;
CREATE TRIGGER spans_au AFTER UPDATE OF input_content, output_content, name ON spans BEGIN
  INSERT INTO spans_fts(spans_fts, rowid, input_content, output_content, name)
  VALUES ('delete', old.id, old.input_content, old.output_content, old.name);
  INSERT INTO spans_fts(rowid, input_content, output_content, name)
  VALUES (new.id, new.input_content, new.output_content, new.name);
END;
INSERT INTO spans_fts(spans_fts) VALUES('rebuild');
`}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		log.Printf("warning: data directory %s permissions are %o; expected 0700", dataDir, info.Mode().Perm())
	}

	path := filepath.Join(dataDir, "spanbox.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}

	writerDSN := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	writer, err := sql.Open("sqlite", writerDSN)
	if err != nil {
		return nil, err
	}
	writer.SetMaxOpenConns(1)
	if err := writer.Ping(); err != nil {
		writer.Close()
		return nil, err
	}
	closeWriter := func(err error) (*Store, error) {
		writer.Close()
		return nil, err
	}

	var version, objectCount int
	if err := writer.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return closeWriter(err)
	}
	if version == 0 {
		if err := writer.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&objectCount); err != nil {
			return closeWriter(err)
		}
		if objectCount == 0 {
			if _, err := writer.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
				return closeWriter(err)
			}
		}
	}
	if _, err := writer.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return closeWriter(err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := writer.Begin()
		if err != nil {
			return closeWriter(err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return closeWriter(fmt.Errorf("migration %d: %w", i+1, err))
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return closeWriter(fmt.Errorf("migration %d version: %w", i+1, err))
		}
		if err := tx.Commit(); err != nil {
			return closeWriter(fmt.Errorf("migration %d commit: %w", i+1, err))
		}
	}

	readerDSN := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(5000)"
	reader, err := sql.Open("sqlite", readerDSN)
	if err != nil {
		return closeWriter(err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	if err := reader.Ping(); err != nil {
		reader.Close()
		return closeWriter(err)
	}
	userReader, err := sql.Open("sqlite", readerDSN)
	if err != nil {
		reader.Close()
		return closeWriter(err)
	}
	userReader.SetMaxOpenConns(1)
	userReader.SetMaxIdleConns(1)
	if err := userReader.Ping(); err != nil {
		userReader.Close()
		reader.Close()
		return closeWriter(err)
	}
	return &Store{w: writer, r: reader, u: userReader}, nil
}
