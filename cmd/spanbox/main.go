package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/proxy"
	"github.com/Ray0907/spanbox/internal/store"
	"github.com/Ray0907/spanbox/internal/web"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	prices, err := pricing.Load(cfg.PricingFile)
	if err != nil {
		log.Fatal(err)
	}
	database, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	if cfg.AuthToken == "" {
		log.Print("warning: AUTH_TOKEN is not set; ingest and UI are unauthenticated")
	}
	if cfg.RetentionDays != 0 {
		go runRetention(database, cfg.RetentionDays)
	}
	proxyHandler, err := proxy.New(proxy.Config{
		AnthropicUpstream:     cfg.AnthropicUpstream,
		OpenAIUpstream:        cfg.OpenAIUpstream,
		OpenAICompatUpstreams: cfg.OpenAICompatUpstreams,
		ChatGPTUpstream:       cfg.ChatGPTUpstream,
		GeminiUpstream:        cfg.GeminiUpstream,
		Logf:                  log.Printf,
		Store:                 database,
		Pricing:               prices,
		Version:               version,
	})
	if err != nil {
		log.Fatal(err)
	}
	server := web.NewServer(web.Deps{Cfg: cfg, Store: database, Pricing: prices, Version: version, Logf: log.Printf, Proxy: proxyHandler})
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("spanbox %s listening on %s", version, server.Addr)
	if err := server.ListenAndServe(); err != nil {
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
		<-done
	}
}

func runRetention(database *store.Store, days int) {
	purge := func() {
		cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).UnixNano()
		deleted, err := database.Purge(context.Background(), cutoff)
		if err != nil {
			if deleted > 0 {
				log.Printf("retention: deleted %d traces; cleanup failed: %v", deleted, err)
			} else {
				log.Printf("retention: %v", err)
			}
		} else if deleted > 0 {
			log.Printf("retention: deleted %d traces", deleted)
		}
	}
	purge()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		purge()
	}
}
