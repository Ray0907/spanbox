package normalize

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/otlp"
)

type fixtureWant struct {
	Kind                  string   `json:"kind"`
	Provider              string   `json:"provider"`
	RequestModel          string   `json:"request_model"`
	ResponseModel         string   `json:"response_model"`
	ServiceName           string   `json:"service_name"`
	InputTokens           *int64   `json:"input_tokens"`
	OutputTokens          *int64   `json:"output_tokens"`
	CacheReadTokens       *int64   `json:"cache_read_tokens"`
	CostUSD               *float64 `json:"cost_usd"`
	CostSource            string   `json:"cost_source"`
	InputContentContains  string   `json:"input_content_contains"`
	OutputContentContains string   `json:"output_content_contains"`
	ToolName              string   `json:"tool_name"`
	ToolCallID            string   `json:"tool_call_id"`
	FinishReason          string   `json:"finish_reason"`
	SessionID             string   `json:"session_id"`
	UserID                string   `json:"user_id"`
	AttributesContains    string   `json:"attributes_contains"`
}

type fixture struct {
	Attrs    map[string]any `json:"attrs"`
	Resource map[string]any `json:"resource"`
	Want     fixtureWant    `json:"want"`
}

func TestFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures")
	}
	sort.Strings(paths)
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fixture fixture
			if err := json.Unmarshal(body, &fixture); err != nil {
				t.Fatal(err)
			}
			raw := otlp.RawSpan{
				TraceID: "0102030405060708090a0b0c0d0e0f10", SpanID: "1112131415161718",
				Name: "fixture", StartNs: 1_000_000, EndNs: 3_000_000, Attrs: fixture.Attrs,
				Events: []map[string]any{}, Links: []map[string]any{}, Resource: fixture.Resource,
				Scope: map[string]any{"name": "test", "version": "1", "attributes": map[string]any{}},
			}
			got := Span(raw, t.Logf)
			want := fixture.Want
			if got.Kind != want.Kind || got.Provider != want.Provider || got.RequestModel != want.RequestModel || got.ResponseModel != want.ResponseModel || got.ServiceName != want.ServiceName || !reflect.DeepEqual(got.InputTokens, want.InputTokens) || !reflect.DeepEqual(got.OutputTokens, want.OutputTokens) || !reflect.DeepEqual(got.CacheReadTokens, want.CacheReadTokens) || got.CostSource != want.CostSource || got.ToolName != want.ToolName || got.ToolCallID != want.ToolCallID || got.FinishReason != want.FinishReason || got.SessionID != want.SessionID || got.UserID != want.UserID {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
			if (got.CostUSD == nil) != (want.CostUSD == nil) || got.CostUSD != nil && math.Abs(*got.CostUSD-*want.CostUSD) > 1e-12 {
				t.Fatalf("cost = %v, want %v", got.CostUSD, want.CostUSD)
			}
			contains := []struct{ value, part string }{
				{got.InputContent, want.InputContentContains},
				{got.OutputContent, want.OutputContentContains},
				{got.Attributes, want.AttributesContains},
			}
			for _, check := range contains {
				if check.part != "" && !strings.Contains(check.value, check.part) {
					t.Fatalf("%q does not contain %q", check.value, check.part)
				}
			}
			if got.DurationMs != 2 || got.Attributes == "" || got.Events != "[]" || got.Links != "[]" || got.Resource == "" || got.Scope == "" {
				t.Fatalf("raw fields not mapped: %+v", got)
			}
		})
	}
}

func TestRebuildIndexed(t *testing.T) {
	attrs := map[string]any{
		"llm.input_messages.0.message.contents.1.message_content.text": "hello",
	}
	got, ok := RebuildIndexed(attrs, "llm.input_messages")
	if !ok {
		t.Fatal("expected indexed data")
	}
	want := []any{map[string]any{"message": map[string]any{"contents": []any{nil, map[string]any{"message_content": map[string]any{"text": "hello"}}}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectKindHeuristic(t *testing.T) {
	if got := DetectKind(map[string]any{"gen_ai.request.model": "gpt", "gen_ai.usage.input_tokens": int64(0)}); got != "llm" {
		t.Fatalf("got %q", got)
	}
}
