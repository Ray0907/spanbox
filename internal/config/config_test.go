package config

import "testing"

func TestFromEnvDefaults(t *testing.T) {
	cfg, err := FromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 4318 || cfg.DataDir != "./data" || cfg.RetentionDays != 30 || cfg.AuthToken != "" || cfg.PricingFile != "" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestFromEnvParsesAllValues(t *testing.T) {
	values := map[string]string{
		"PORT":           "9000",
		"DATA_DIR":       "/tmp/spanbox",
		"RETENTION_DAYS": "0",
		"AUTH_TOKEN":     "secret",
		"PRICING_FILE":   "/tmp/prices.json",
	}
	cfg, err := FromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Port: 9000, DataDir: "/tmp/spanbox", RetentionDays: 0, AuthToken: "secret", PricingFile: "/tmp/prices.json"}
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

func TestFromEnvRejectsNonInteger(t *testing.T) {
	for _, key := range []string{"PORT", "RETENTION_DAYS"} {
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
