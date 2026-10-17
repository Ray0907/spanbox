package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

// Run before changing production code; the same fixtures exercise C1's e2e,
// parser, unknown-call and boundary-string cases, with and without sessions.
func TestC2Golden(t *testing.T) {
	var hashes map[string]string
	if err := json.Unmarshal(fixture(t, "c2_golden_hashes.json"), &hashes); err != nil {
		t.Fatal(err)
	}
	for i, c := range c1Cases(t) {
		for _, noSession := range []bool{false, true} {
			name := fmt.Sprintf("%02d-%t.json", i, noSession)
			t.Run(name, func(t *testing.T) {
				db, err := store.Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				h := c1Handler(t, db)
				var snapshots [][]byte
				for _, e := range c.Exchanges {
					if noSession {
						e.RequestHeaders = e.RequestHeaders.Clone()
						if e.RequestHeaders == nil {
							e.RequestHeaders = http.Header{}
						}
						e.RequestHeaders.Del("X-Spanbox-Session")
						request := decodeObject(e.RequestBody)
						delete(request, "conversation")
						delete(request, "session")
						delete(request, "conversation_id")
						delete(request, "session_id")
						delete(request, "metadata")
						e.RequestBody, err = json.Marshal(request)
						if err != nil {
							t.Fatal(err)
						}
					}
					h.capture(e)
					snapshot := c1Dump(t, db)
					if !noSession {
						snapshot = c2LegacyProjection(t, snapshot)
					}
					snapshots = append(snapshots, snapshot)
				}
				dump := bytes.Join(snapshots, []byte("\n"))
				path := filepath.Join("../../tmp/golden-c2", name)
				if os.Getenv("C2_GENERATE_GOLDEN") == "1" {
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, dump, 0600); err != nil {
						t.Fatal(err)
					}
					return
				}
				if sum := fmt.Sprintf("%x", sha256.Sum256(dump)); sum != hashes[name] {
					t.Fatalf("out-of-scope span bytes changed: got %s want %s", sum, hashes[name])
				}
				if os.Getenv("C2_COMPARE_GOLDEN") != "1" {
					return
				}
				baseline, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(dump, baseline) {
					t.Fatal("out-of-scope span bytes differ from HEAD")
				}
			})
		}
	}
}

// Remove only C2's intentional root/trace/model-parent changes. Every other
// exported byte must still match the unmodified C1 golden snapshots.
func c2LegacyProjection(t *testing.T, dump []byte) []byte {
	t.Helper()
	var spans []store.Span
	if err := json.Unmarshal(dump, &spans); err != nil {
		t.Fatal(err)
	}
	roots := map[string]string{}
	for _, s := range spans {
		if s.Kind != "agent" {
			continue
		}
		var attrs map[string]any
		if err := json.Unmarshal([]byte(s.Attributes), &attrs); err != nil {
			t.Fatal(err)
		}
		session := text(attrs["spanbox.session"])
		if session == "" {
			session = text(attrs["gen_ai.conversation.id"])
		}
		roots[s.TraceID] = hash("spanbox-proxy-trace"+session, 16)
	}
	var legacy []store.Span
	for _, s := range spans {
		if s.Kind == "agent" {
			continue
		}
		if trace, ok := roots[s.TraceID]; ok {
			s.TraceID = trace
			if s.Kind == "llm" {
				s.ParentSpanID = ""
			}
		}
		legacy = append(legacy, s)
	}
	sort.Slice(legacy, func(i, j int) bool {
		a, b := legacy[i], legacy[j]
		if a.TraceID != b.TraceID {
			return a.TraceID < b.TraceID
		}
		if a.StartNs != b.StartNs {
			return a.StartNs < b.StartNs
		}
		return a.SpanID < b.SpanID
	})
	result, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(result, '\n')
}
