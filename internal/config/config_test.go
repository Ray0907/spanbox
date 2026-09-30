package config

import "testing"

func TestFromEnvDefaults(t *testing.T) {
	cfg, err := FromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 4318 || cfg.DataDir != "./data" || cfg.RetentionDays != 30 || cfg.PurgeBatchSize != DefaultPurgeBatchSize || cfg.FTSMergeEvery != DefaultFTSMergeEvery || cfg.AuthToken != "" || cfg.PricingFile != "" || cfg.AnthropicUpstream != "" || cfg.OpenAIUpstream != "" || cfg.OpenAICompatUpstreams != "" || cfg.ChatGPTUpstream != "" || cfg.GeminiUpstream != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestFromEnvParsesAllValues(t *testing.T) {
	values := map[string]string{
		"PORT":                    "9000",
		"DATA_DIR":                "/tmp/spanbox",
		"RETENTION_DAYS":          "0",
		"PURGE_BATCH_SIZE":        "50",
		"FTS_MERGE_EVERY":         "4",
		"AUTH_TOKEN":              "secret",
		"PRICING_FILE":            "/tmp/prices.json",
		"ANTHROPIC_UPSTREAM":      "http://anthropic.test/base",
		"OPENAI_UPSTREAM":         "http://openai.test/base",
		"OPENAI_COMPAT_UPSTREAMS": "local=http://localhost:18080/v1",
		"CHATGPT_UPSTREAM":        "http://chatgpt.test/base",
		"GEMINI_UPSTREAM":         "http://gemini.test/base",
	}
	cfg, err := FromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Port: 9000, DataDir: "/tmp/spanbox", RetentionDays: 0, PurgeBatchSize: 50, FTSMergeEvery: 4, AuthToken: "secret", PricingFile: "/tmp/prices.json", AnthropicUpstream: "http://anthropic.test/base", OpenAIUpstream: "http://openai.test/base", OpenAICompatUpstreams: "local=http://localhost:18080/v1", ChatGPTUpstream: "http://chatgpt.test/base", GeminiUpstream: "http://gemini.test/base"}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestFromEnvRejectsInvalidRetention(t *testing.T) {
	for _, value := range []string{"-1", "106752"} {
		t.Run(value, func(t *testing.T) {
			_, err := FromEnv(func(key string) string {
				if key == "RETENTION_DAYS" {
					return value
				}
				return ""
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFromEnvRejectsNonPositivePurgeTuning(t *testing.T) {
	for _, key := range []string{"PURGE_BATCH_SIZE", "FTS_MERGE_EVERY"} {
		for _, value := range []string{"0", "-1"} {
			_, err := FromEnv(func(got string) string {
				if got == key {
					return value
				}
				return ""
			})
			if err == nil {
				t.Fatalf("%s=%s accepted", key, value)
			}
		}
	}
}

func TestFromEnvRejectsNonInteger(t *testing.T) {
	for _, key := range []string{"PORT", "RETENTION_DAYS", "PURGE_BATCH_SIZE", "FTS_MERGE_EVERY"} {
		t.Run(key, func(t *testing.T) {
			_, err := FromEnv(func(got string) string {
				if got == key {
					return "abc"
				}
				return ""
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
