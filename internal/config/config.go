package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	maxRetentionDays = int64((1<<63 - 1) / (24 * time.Hour))

	MaxBodyBytes        = 32 << 20
	MaxConcurrentIngest = 4
	ReadHeaderTimeout   = 10 * time.Second
	ReadTimeout         = 60 * time.Second
	WriteTimeout        = 60 * time.Second
	IdleTimeout         = 120 * time.Second
	MaxHeaderBytes      = 64 << 10
	SQLTimeout          = 5 * time.Second
	SQLMaxRows          = 1000
	SQLMaxCols          = 64
	SQLMaxCellBytes     = 64 << 10
	SQLMaxRespBytes     = 4 << 20
	SQLMaxTextBytes     = 16 << 10
	MaxTokens           = int64(1e12)
	MaxCostUSD          = 1_000_000.0
)

type Config struct {
	Port          int
	DataDir       string
	RetentionDays int
	AuthToken     string
	PricingFile   string
}

func FromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:          4318,
		DataDir:       "./data",
		RetentionDays: 30,
		AuthToken:     getenv("AUTH_TOKEN"),
		PricingFile:   getenv("PRICING_FILE"),
	}
	if value := getenv("DATA_DIR"); value != "" {
		cfg.DataDir = value
	}
	if value := getenv("PORT"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("PORT: %w", err)
		}
		cfg.Port = port
	}
	if value := getenv("RETENTION_DAYS"); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("RETENTION_DAYS: %w", err)
		}
		if days < 0 || int64(days) > maxRetentionDays {
			return Config{}, fmt.Errorf("RETENTION_DAYS must be between 0 and %d", maxRetentionDays)
		}
		cfg.RetentionDays = days
	}
	return cfg, nil
}
