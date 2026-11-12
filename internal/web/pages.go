package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
)

func (deps Deps) traces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		deps.agentTraces(w, r)
		return
	}
	serveShell(w, r, http.StatusOK)
}

func traceFilter(r *http.Request) (store.TraceFilter, error) {
	query := r.URL.Query()
	from, to, err := parseRange(query.Get("range"), query.Get("from"), query.Get("to"))
	if err != nil {
		return store.TraceFilter{}, err
	}
	filter := store.TraceFilter{
		FromNs: from, ToNs: to, Model: query.Get("model"), Service: query.Get("service"), ErrorsOnly: query.Get("errors") == "1",
		SessionID: query.Get("session"), UserID: query.Get("user"), Limit: 51,
	}
	if duration := query.Get("min_duration"); duration != "" {
		filter.MinDurationMs, err = strconv.ParseFloat(duration, 64)
		if err != nil || filter.MinDurationMs < 0 || math.IsNaN(filter.MinDurationMs) || math.IsInf(filter.MinDurationMs, 0) {
			return store.TraceFilter{}, fmt.Errorf("minimum duration must be a non-negative number")
		}
	}
	if cursor := query.Get("cursor"); cursor != "" {
		start, traceID, ok := strings.Cut(cursor, ":")
		if !ok || len(traceID) != 32 {
			return store.TraceFilter{}, fmt.Errorf("invalid cursor")
		}
		filter.CursorStartNs, err = strconv.ParseInt(start, 10, 64)
		if err != nil {
			return store.TraceFilter{}, fmt.Errorf("invalid cursor")
		}
		filter.CursorTraceID = traceID
	}
	return filter, nil
}

func parseRange(value, fromText, toText string) (int64, int64, error) {
	now := time.Now().UTC()
	switch value {
	case "":
		return 0, 0, nil
	case "15m":
		return now.Add(-15 * time.Minute).UnixNano(), now.UnixNano(), nil
	case "1h":
		return now.Add(-time.Hour).UnixNano(), now.UnixNano(), nil
	case "24h":
		return now.Add(-24 * time.Hour).UnixNano(), now.UnixNano(), nil
	case "7d":
		return now.Add(-7 * 24 * time.Hour).UnixNano(), now.UnixNano(), nil
	case "custom":
		from, err := time.Parse(time.RFC3339, fromText)
		if err != nil {
			return 0, 0, fmt.Errorf("from must be RFC3339")
		}
		to, err := time.Parse(time.RFC3339, toText)
		if err != nil || !to.After(from) {
			return 0, 0, fmt.Errorf("to must be RFC3339 and after from")
		}
		return from.UnixNano(), to.UnixNano(), nil
	default:
		return 0, 0, fmt.Errorf("invalid range")
	}
}

func (deps Deps) trace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	traceID := strings.TrimPrefix(r.URL.Path, "/traces/")
	if len(traceID) != 32 || strings.Contains(traceID, "/") {
		if r.URL.Query().Get("format") == "json" {
			writeAgentError(w, 404, "not found")
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		deps.agentTrace(w, r, traceID)
		return
	}
	serveShell(w, r, http.StatusOK)
}

func (deps Deps) sessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/sessions" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		if id := r.URL.Query().Get("id"); id != "" {
			deps.sessionDetail(w, r, id)
		} else {
			deps.agentSessions(w, r)
		}
		return
	}
	serveShell(w, r, http.StatusOK)
}

func (deps Deps) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	serveShell(w, r, http.StatusOK)
}

func (deps Deps) dashboardData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rangeValue := r.URL.Query().Get("range")
	if rangeValue == "" {
		rangeValue = "24h"
	}
	from, to, err := parseRange(rangeValue, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, err := deps.Store.Dashboard(r.Context(), from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body, err := json.Marshal(data)
	if err != nil {
		deps.Logf("encode dashboard: %v", err)
		http.Error(w, "encoding error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (deps Deps) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		deps.agentSearch(w, r)
		return
	}
	serveShell(w, r, http.StatusOK)
}

type schemaTable struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

func (deps Deps) sqlPage(w http.ResponseWriter, r *http.Request) {
	jsonMode := sqlJSON(r)
	fail := func(status int, message string) {
		if jsonMode {
			writeAgentError(w, status, message)
		} else {
			http.Error(w, message, status)
		}
	}
	if deps.Cfg.AuthToken == "" {
		fail(http.StatusNotFound, "SQL console disabled: set AUTH_TOKEN to enable")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	schema, err := deps.schema(r)
	if err != nil {
		deps.Logf("inspect schema: %v", err)
		fail(http.StatusInternalServerError, "database error")
		return
	}
	status := http.StatusOK
	var result store.SQLResult
	var queryError string
	if r.Method == http.MethodPost {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			fail(http.StatusUnsupportedMediaType, "unsupported media type")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 20<<10)
		if err := r.ParseForm(); err != nil {
			fail(http.StatusBadRequest, "invalid form")
			return
		}
		result, err = deps.Store.RunUserSQL(r.Context(), r.PostForm.Get("sql"))
		if err != nil {
			queryError = err.Error()
			status = http.StatusBadRequest
		}
	}
	if jsonMode {
		if r.Method == http.MethodGet {
			writeAgentJSON(w, status, map[string]any{"tables": schema})
		} else {
			if result.Columns == nil {
				result.Columns = []string{}
			}
			if result.Rows == nil {
				result.Rows = [][]string{}
			}
			writeAgentJSON(w, status, map[string]any{
				"columns": result.Columns, "rows": result.Rows,
				"truncated": result.Truncated, "error": queryError,
			})
		}
		return
	}
	serveShell(w, r, status)
}

func (deps Deps) schema(r *http.Request) ([]schemaTable, error) {
	var result []schemaTable
	for _, table := range []string{"traces", "spans", "spans_fts"} {
		rows, err := deps.Store.Reader().QueryContext(r.Context(), "PRAGMA table_info("+table+")")
		if err != nil {
			return nil, err
		}
		entry := schemaTable{Name: table}
		for rows.Next() {
			var id, notNull, primaryKey int
			var name, kind string
			var defaultValue sql.NullString
			if err := rows.Scan(&id, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				return nil, errors.Join(err, rows.Close())
			}
			entry.Columns = append(entry.Columns, name+" "+kind)
		}
		if err := rows.Err(); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}

func (deps Deps) span(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/spans/"), "/")
	if len(parts) != 2 || len(parts[0]) != 32 || len(parts[1]) != 16 {
		if r.URL.Query().Get("format") == "json" {
			writeAgentError(w, 404, "not found")
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		deps.agentSpan(w, r, parts[0], parts[1])
		return
	}
	serveShell(w, r, http.StatusOK)
}
