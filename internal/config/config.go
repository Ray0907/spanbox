package config

import (
	"fmt"
	"strconv"
	"time"
)

const (
	maxRetentionDays = int64((1<<63 - 1) / (24 * time.Hour))

	// Defaults are selected by scripts/load-e2e.sh's 50k/500k sweep.
	DefaultPurgeBatchSize = 10
	DefaultFTSMergeEvery  = 256
	maxPurgeBatchSize     = 200
	maxFTSMergeEvery      = 1024

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
	Port                  int
	DataDir               string
	RetentionDays         int
	PurgeBatchSize        int
	FTSMergeEvery         int
	AuthToken             string
	PricingFile           string
	AnthropicUpstream     string
	OpenAIUpstream        string
	OpenAICompatUpstreams string
	ChatGPTUpstream       string
	GeminiUpstream        string
}

func FromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		Port:                  4318,
		DataDir:               "./data",
		RetentionDays:         30,
		PurgeBatchSize:        DefaultPurgeBatchSize,
		FTSMergeEvery:         DefaultFTSMergeEvery,
		AuthToken:             getenv("AUTH_TOKEN"),
		PricingFile:           getenv("PRICING_FILE"),
		AnthropicUpstream:     getenv("ANTHROPIC_UPSTREAM"),
		OpenAIUpstream:        getenv("OPENAI_UPSTREAM"),
		OpenAICompatUpstreams: getenv("OPENAI_COMPAT_UPSTREAMS"),
		ChatGPTUpstream:       getenv("CHATGPT_UPSTREAM"),
		GeminiUpstream:        getenv("GEMINI_UPSTREAM"),
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
	for _, tuning := range []struct {
		key  string
		dest *int
		max  int
	}{
		{"PURGE_BATCH_SIZE", &cfg.PurgeBatchSize, maxPurgeBatchSize},
		{"FTS_MERGE_EVERY", &cfg.FTSMergeEvery, maxFTSMergeEvery},
	} {
		if value := getenv(tuning.key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil {
				return Config{}, fmt.Errorf("%s: %w", tuning.key, err)
			}
			if n < 1 || n > tuning.max {
				return Config{}, fmt.Errorf("%s must be between 1 and %d", tuning.key, tuning.max)
			}
			*tuning.dest = n
		}
	}
	return cfg, nil
}
