package web

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
)

//go:embed templates/*.html static/*
var webFiles embed.FS

type layoutData struct {
	Title      string
	Section    string
	SQLEnabled bool
	Charts     bool
	Version    string
}

type traceFilterView struct {
	Range, From, To, Model, Service, MinDuration, Session, User string
	Errors                                                      bool
}

type tracesPage struct {
	layoutData
	Rows    []store.TraceRow
	Filter  traceFilterView
	NextURL string
	Error   string
}

func (deps Deps) traces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	filter, view, err := traceFilter(r)
	page := tracesPage{layoutData: layoutData{Title: "Traces", Section: "traces", SQLEnabled: deps.Cfg.AuthToken != "", Version: deps.Version}, Filter: view}
	if err == nil {
		page.Rows, err = deps.Store.ListTraces(r.Context(), filter)
		if len(page.Rows) > 50 {
			last := page.Rows[49]
			page.Rows = page.Rows[:50]
			query := cloneValues(r.URL.Query())
			query.Del("partial")
			query.Set("cursor", strconv.FormatInt(last.StartNs, 10)+":"+last.TraceID)
			page.NextURL = "/?" + query.Encode()
		}
	}
	if err != nil {
		page.Error = err.Error()
	}
	if r.URL.Query().Get("partial") == "1" {
		deps.render(w, "results", page, "templates/traces_rows.html")
		return
	}
	deps.render(w, "base", page, "templates/base.html", "templates/traces.html", "templates/traces_rows.html")
}

func traceFilter(r *http.Request) (store.TraceFilter, traceFilterView, error) {
	query := r.URL.Query()
	view := traceFilterView{
		Range: query.Get("range"), From: query.Get("from"), To: query.Get("to"), Model: query.Get("model"),
		Service: query.Get("service"), MinDuration: query.Get("min_duration"), Session: query.Get("session"),
		User: query.Get("user"), Errors: query.Get("errors") == "1",
	}
	from, to, err := parseRange(view.Range, view.From, view.To)
	if err != nil {
		return store.TraceFilter{}, view, err
	}
	filter := store.TraceFilter{
		FromNs: from, ToNs: to, Model: view.Model, Service: view.Service, ErrorsOnly: view.Errors,
		SessionID: view.Session, UserID: view.User, Limit: 51,
	}
	if view.MinDuration != "" {
		filter.MinDurationMs, err = strconv.ParseFloat(view.MinDuration, 64)
		if err != nil || filter.MinDurationMs < 0 || math.IsNaN(filter.MinDurationMs) || math.IsInf(filter.MinDurationMs, 0) {
			return store.TraceFilter{}, view, fmt.Errorf("minimum duration must be a non-negative number")
		}
	}
	if cursor := query.Get("cursor"); cursor != "" {
		start, traceID, ok := strings.Cut(cursor, ":")
		if !ok || len(traceID) != 32 {
			return store.TraceFilter{}, view, fmt.Errorf("invalid cursor")
		}
		filter.CursorStartNs, err = strconv.ParseInt(start, 10, 64)
		if err != nil {
			return store.TraceFilter{}, view, fmt.Errorf("invalid cursor")
		}
		filter.CursorTraceID = traceID
	}
	return filter, view, nil
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

func cloneValues(values url.Values) url.Values {
	copy := make(url.Values, len(values))
	for key, value := range values {
		copy[key] = append([]string(nil), value...)
	}
	return copy
}

type spanNode struct {
	Span      store.Span
	OffsetPct int
	WidthPct  int
	Children  []*spanNode
}

type tracePage struct {
	layoutData
	Trace      store.TraceRow
	Roots      []*spanNode
	TimeLabels []string
	Selected   *spanView
}

func (deps Deps) trace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	traceID := strings.TrimPrefix(r.URL.Path, "/traces/")
	if len(traceID) != 32 || strings.Contains(traceID, "/") {
		http.NotFound(w, r)
		return
	}
	trace, spans, err := deps.Store.GetTrace(r.Context(), traceID)
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	roots := buildTree(spans, trace.StartNs, trace.EndNs)
	page := tracePage{
		layoutData: layoutData{Title: trace.Name, Section: "traces", SQLEnabled: deps.Cfg.AuthToken != "", Version: deps.Version},
		Trace:      trace, Roots: roots, TimeLabels: timeLabels(trace.StartNs, trace.EndNs),
	}
	if len(spans) > 0 {
		selectedSpan, err := deps.Store.GetSpan(r.Context(), traceID, spans[0].SpanID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		selected := newSpanView(selectedSpan, trace.StartNs)
		page.Selected = &selected
	}
	deps.render(w, "base", page, "templates/base.html", "templates/trace.html", "templates/span.html")
}

func buildTree(spans []store.Span, traceStart, traceEnd int64) []*spanNode {
	nodes := make(map[string]*spanNode, len(spans))
	window := traceEnd - traceStart
	for _, span := range spans {
		node := &spanNode{Span: span, WidthPct: 100}
		if window > 0 {
			node.OffsetPct = min(max(int(math.Round(float64(span.StartNs-traceStart)*100/float64(window))), 0), 99)
			node.WidthPct = max(int(math.Round(float64(span.EndNs-span.StartNs)*100/float64(window))), 1)
			node.WidthPct = min(node.WidthPct, 100-node.OffsetPct)
		}
		nodes[span.SpanID] = node
	}
	var roots []*spanNode
	for _, span := range spans {
		node := nodes[span.SpanID]
		if parent := nodes[span.ParentSpanID]; span.ParentSpanID != "" && parent != nil {
			parent.Children = append(parent.Children, node)
		} else {
			roots = append(roots, node)
		}
	}
	return roots
}

func timeLabels(start, end int64) []string {
	durationMs := max(float64(end-start)/1e6, 0)
	return []string{"0 ms", compactDuration(durationMs * .25), compactDuration(durationMs * .5), compactDuration(durationMs * .75), compactDuration(durationMs)}
}

func compactDuration(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

type dashboardPage struct {
	layoutData
	Range string
	Data  store.DashboardData
	Error string
}

func (deps Deps) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	rangeValue := r.URL.Query().Get("range")
	if rangeValue == "" {
		rangeValue = "24h"
	}
	from, to, err := parseRange(rangeValue, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	page := dashboardPage{layoutData: layoutData{Title: "Dashboard", Section: "dashboard", SQLEnabled: deps.Cfg.AuthToken != "", Charts: true, Version: deps.Version}, Range: rangeValue}
	if err == nil {
		page.Data, err = deps.Store.Dashboard(r.Context(), from, to)
	}
	if err != nil {
		page.Error = err.Error()
	}
	deps.render(w, "base", page, "templates/base.html", "templates/dashboard.html")
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

type searchPage struct {
	layoutData
	Query string
	Hits  []store.SearchHit
	Error string
}

func (deps Deps) search(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	page := searchPage{layoutData: layoutData{Title: "Search", Section: "search", SQLEnabled: deps.Cfg.AuthToken != "", Version: deps.Version}, Query: r.URL.Query().Get("q")}
	var err error
	if page.Query != "" {
		page.Hits, err = deps.Store.Search(r.Context(), page.Query, 100)
	}
	if err != nil {
		page.Error = err.Error()
	}
	deps.render(w, "base", page, "templates/base.html", "templates/search.html")
}

type schemaTable struct {
	Name    string
	Columns []string
}

type sqlPage struct {
	layoutData
	Query     string
	Result    store.SQLResult
	HasResult bool
	Error     string
	Schema    []schemaTable
}

func (deps Deps) sqlPage(w http.ResponseWriter, r *http.Request) {
	if deps.Cfg.AuthToken == "" {
		http.Error(w, "SQL console disabled: set AUTH_TOKEN to enable", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	page := sqlPage{layoutData: layoutData{Title: "SQL", Section: "sql", SQLEnabled: true, Version: deps.Version}}
	var err error
	page.Schema, err = deps.schema(r)
	if err != nil {
		deps.Logf("inspect schema: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 20<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		page.Query = r.PostForm.Get("sql")
		page.Result, err = deps.Store.RunUserSQL(r.Context(), page.Query)
		if err != nil {
			page.Error = err.Error()
			status = http.StatusBadRequest
		} else {
			page.HasResult = true
		}
	}
	if status != http.StatusOK {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
	}
	deps.render(w, "base", page, "templates/base.html", "templates/sql.html")
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
		http.NotFound(w, r)
		return
	}
	span, err := deps.Store.GetSpan(r.Context(), parts[0], parts[1])
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	trace, _, err := deps.Store.GetTrace(r.Context(), parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	deps.render(w, "span", newSpanView(span, trace.StartNs), "templates/span.html")
}

type messageView struct {
	Role, Content string
}

type contentView struct {
	Label, Text string
	Present     bool
	Messages    []messageView
}

type spanView struct {
	Span                                       store.Span
	Status                                     string
	StartOffsetMs                              float64
	Input, Output                              contentView
	Attributes, Events, Links, Resource, Scope string
}

func newSpanView(span store.Span, traceStart int64) spanView {
	status := map[int32]string{0: "Unset", 1: "OK", 2: "Error"}[span.StatusCode]
	return spanView{
		Span: span, Status: status, StartOffsetMs: max(float64(span.StartNs-traceStart)/1e6, 0),
		Input: formatContent("Input", span.InputContent), Output: formatContent("Output", span.OutputContent),
		Attributes: prettyJSON(span.Attributes), Events: prettyJSON(span.Events), Links: prettyJSON(span.Links),
		Resource: prettyJSON(span.Resource), Scope: prettyJSON(span.Scope),
	}
}

func formatContent(label, raw string) contentView {
	view := contentView{Label: label, Text: raw, Present: raw != ""}
	if raw == "" {
		return view
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		view.Text = prettyJSON(raw)
		view.Messages = messages(value)
	}
	return view
}

func messages(value any) []messageView {
	if object, ok := value.(map[string]any); ok {
		value = object["messages"]
		if text, ok := value.(string); ok {
			_ = json.Unmarshal([]byte(text), &value)
		}
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]messageView, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil
		}
		role, _ := object["role"].(string)
		content, exists := object["content"]
		if !exists {
			content = object["parts"]
		}
		if role == "" {
			role = "message"
		}
		result = append(result, messageView{Role: role, Content: partsText(content)})
	}
	return result
}

// partsText renders OTel GenAI message parts as plain text when every part is
// a text part; anything else falls back to indented JSON.
func partsText(content any) string {
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return textValue(content)
	}
	var texts []string
	for _, part := range parts {
		object, ok := part.(map[string]any)
		if !ok {
			return textValue(content)
		}
		kind, _ := object["type"].(string)
		text, isText := object["content"].(string)
		if !isText {
			text, isText = object["text"].(string)
		}
		if (kind != "" && kind != "text") || !isText {
			return textValue(content)
		}
		texts = append(texts, text)
	}
	return strings.Join(texts, "\n\n")
}

func textValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	body, _ := json.MarshalIndent(value, "", "  ")
	return string(body)
}

func prettyJSON(value string) string {
	var output bytes.Buffer
	if json.Indent(&output, []byte(value), "", "  ") == nil {
		return output.String()
	}
	return value
}

func (deps Deps) render(w http.ResponseWriter, name string, data any, files ...string) {
	tmpl, err := template.New("page").Funcs(template.FuncMap{
		"timeUTC": func(ns int64) string { return time.Unix(0, ns).UTC().Format(time.RFC3339Nano) },
		"tokens": func(value *int64) string {
			if value == nil {
				return "—"
			}
			return strconv.FormatInt(*value, 10)
		},
		"money": func(value *float64) string {
			if value == nil {
				return "—"
			}
			return fmt.Sprintf("$%.6f", *value)
		},
		"duration": func(ms float64) string { return fmt.Sprintf("%.1f ms", ms) },
		"percent":  func(value float64) string { return fmt.Sprintf("%.1f%%", value*100) },
		"elapsed":  func(value time.Duration) string { return value.Round(time.Millisecond).String() },
		"join":     strings.Join,
		"spanModel": func(span store.Span) string {
			if span.ResponseModel != "" {
				return span.ResponseModel
			}
			return span.RequestModel
		},
	}).ParseFS(webFiles, files...)
	if err != nil {
		deps.Logf("parse templates: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		deps.Logf("render template %s: %v", name, err)
	}
}
