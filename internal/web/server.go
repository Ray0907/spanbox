package web

import (
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
)

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

type Deps struct {
	Cfg         config.Config
	Store       *store.Store
	Pricing     *pricing.Table
	Version     string
	Logf        func(string, ...any)
	ingestSlots chan struct{}
}

func NewHandler(deps Deps) http.Handler {
	if deps.Logf == nil {
		deps.Logf = log.Printf
	}
	if deps.ingestSlots == nil {
		deps.ingestSlots = make(chan struct{}, config.MaxConcurrentIngest)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v1/traces", deps.limitIngest)
	mux.HandleFunc("/v1/logs", deps.limitIngest)
	mux.HandleFunc("/v1/metrics", deps.limitIngest)
	mux.HandleFunc("/login", deps.login)
	staticFiles, err := fs.Sub(webFiles, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFiles))))
	mux.HandleFunc("/traces/", deps.trace)
	mux.HandleFunc("/spans/", deps.span)
	mux.HandleFunc("/dashboard/data", deps.dashboardData)
	mux.HandleFunc("/dashboard", deps.dashboard)
	mux.HandleFunc("/search", deps.search)
	mux.HandleFunc("/sql", deps.sqlPage)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			deps.limitIngest(w, r)
			return
		}
		deps.traces(w, r)
	})
	return headers(recoverPanics(deps.Logf, authenticate(deps.Cfg.AuthToken, mux)))
}

func NewServer(deps Deps) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", deps.Cfg.Port),
		Handler:           NewHandler(deps),
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaxHeaderBytes,
	}
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		}
		next.ServeHTTP(w, r)
	})
}

func recoverPanics(logf func(string, ...any), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				logf("panic serving %s: %v\n%s", r.URL.Path, value, debug.Stack())
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
