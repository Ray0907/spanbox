package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

type c1GoldenCase struct {
	Name      string
	Exchanges []Exchange
}

func c1Cases(t *testing.T) []c1GoldenCase {
	t.Helper()
	var fixtures []struct {
		Name      string
		Exchanges []struct {
			Vendor, Route, Path, Request, Response, Session string
			Headers                                         http.Header
		}
	}
	if err := json.Unmarshal(fixture(t, "c1_e2e.json"), &fixtures); err != nil {
		t.Fatal(err)
	}
	var cases []c1GoldenCase
	for _, f := range fixtures {
		c := c1GoldenCase{Name: f.Name}
		for i, e := range f.Exchanges {
			// HEAD uses random IDs when a response has no ID; fix the input header, not the span.
			e.Headers.Set("Request-Id", fmt.Sprintf("c1-fallback-%d", i))
			headers := http.Header{}
			if e.Session != "" {
				headers.Set("X-Spanbox-Session", e.Session)
			}
			c.Exchanges = append(c.Exchanges, Exchange{Vendor: e.Vendor, Route: e.Route, Path: e.Path, RequestHeaders: headers, ResponseHeaders: e.Headers, RequestBody: []byte(e.Request), ResponseBody: []byte(strings.ReplaceAll(e.Response, "C1_BIG_CONTENT", strings.Repeat("x", 12<<20))), StatusCode: 200, StartNs: int64(i*1000+100) * 1000000, EndNs: int64(i*1000+200) * 1000000, TTFBMs: 12.5})
		}
		cases = append(cases, c)
	}
	for _, f := range []struct{ vendor, path, request, response string }{
		{"anthropic", "/v1/messages", "anthropic_request.json", "anthropic_json.json"},
		{"anthropic", "/v1/messages", "anthropic_request.json", "anthropic_stream.txt"},
		{"gemini", "/v1beta/models/test:streamGenerateContent", "gemini_request.json", "gemini_stream.txt"},
		{"openai", "/v1/chat/completions", "", "openai_chat_json.json"},
		{"openai", "/v1/chat/completions", "", "openai_chat_stream.txt"},
		{"openai", "/v1/responses", "", "openai_responses_stream.txt"},
		{"chatgpt", "/codex/responses", "chatgpt_request.json", "chatgpt_responses_stream.txt"},
		{"chatgpt", "/codex/responses", "chatgpt_request.json.zst", "chatgpt_responses_stream.txt"},
	} {
		request := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"run"}],"input":"run"}`)
		if f.request != "" {
			request = fixture(t, f.request)
		}
		if strings.HasSuffix(f.request, ".zst") {
			var err error
			request, err = decodeRequestBody(http.Header{"Content-Encoding": {"zstd"}}, request)
			if err != nil {
				t.Fatal(err)
			}
		}
		cases = append(cases, c1GoldenCase{Name: f.response + "/" + f.request, Exchanges: []Exchange{{Vendor: f.vendor, Path: f.path, RequestBody: request, ResponseHeaders: http.Header{"Request-Id": {"c1-fixture-fallback"}}, ResponseBody: fixture(t, f.response), StatusCode: 200, StartNs: 100000000, EndNs: 200000000, TTFBMs: 12.5}}})
	}
	return cases
}

func c1Dump(t *testing.T, db *store.Store) []byte {
	t.Helper()
	var spans []store.Span
	if err := db.ExportSpans(context.Background(), store.TraceFilter{}, func(span store.Span) error { spans = append(spans, span); return nil }); err != nil {
		t.Fatal(err)
	}
	dump, err := json.MarshalIndent(spans, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(dump, '\n')
}

func c1Handler(t *testing.T, db *store.Store) *Handler {
	t.Helper()
	h, err := New(Config{Store: db, Version: "c1-golden", Logf: func(format string, args ...any) { t.Errorf(format, args...) }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestC1Golden(t *testing.T) {
	hashes := map[string]string{}
	generating := os.Getenv("C1_GENERATE_GOLDEN") == "1"
	if !generating {
		if err := json.Unmarshal(fixture(t, "c1_golden_hashes.json"), &hashes); err != nil {
			t.Fatal(err)
		}
	}
	for i, c := range c1Cases(t) {
		t.Run(c.Name, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			h := c1Handler(t, db)
			var snapshots [][]byte
			for _, e := range c.Exchanges {
				h.capture(e)
				snapshots = append(snapshots, c2LegacyProjection(t, c1Dump(t, db)))
			}
			dump := bytes.Join(snapshots, []byte("\n"))
			sum := fmt.Sprintf("%x", sha256.Sum256(dump))
			if dir := os.Getenv("C1_COMPARE_GOLDEN"); dir != "" {
				baseline, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%02d.json", i)))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(dump, baseline) {
					t.Fatal("span bytes differ from the HEAD dump")
				}
			}
			if generating {
				dir := "../../tmp/golden-c1"
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.json", i)), dump, 0600); err != nil {
					t.Fatal(err)
				}
				hashes[c.Name] = sum
			} else if sum != hashes[c.Name] {
				t.Fatalf("span bytes differ from HEAD: got %s want %s", sum, hashes[c.Name])
			}
		})
	}
	if generating {
		data, err := json.MarshalIndent(hashes, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("testdata/c1_golden_hashes.json", append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
