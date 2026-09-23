package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ray0907/spanbox/internal/store"
)

var templateFuncs = template.FuncMap{
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
}

// Parsed on first use per file set; webFiles is embedded, so entries never go stale.
var templateCache sync.Map

func (deps Deps) render(w http.ResponseWriter, name string, data any, files ...string) {
	key := strings.Join(files, "\x00")
	cached, ok := templateCache.Load(key)
	if !ok {
		tmpl, err := template.New("page").Funcs(templateFuncs).ParseFS(webFiles, files...)
		if err != nil {
			deps.Logf("parse templates: %v", err)
			http.Error(w, "template error", http.StatusInternalServerError)
			return
		}
		cached, _ = templateCache.LoadOrStore(key, tmpl)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := cached.(*template.Template).ExecuteTemplate(w, name, data); err != nil {
		deps.Logf("render template %s: %v", name, err)
	}
}
