package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAgentJSONFiltersAndWindow(t *testing.T) {
	handler, database := newTestHandler(t, "")
	text := strings.Repeat("字éa", 4000)
	_, traceID, spanID := agentSeed(t, handler, 9, text)
	if err := database.Reader().QueryRow("SELECT input_content FROM spans WHERE trace_id=? AND span_id=?", traceID, spanID).Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{"service=demo&errors=1", "service=missing"} {
		path := "/?format=json&limit=2&" + filter
		seen := map[string]bool{}
		cursor := ""
		for {
			page := agentGet(t, handler, path+"&cursor="+url.QueryEscape(cursor))
			for _, item := range page["items"].([]any) {
				id := item.(map[string]any)["trace_id"].(string)
				if seen[id] || filter == "service=demo&errors=1" && !item.(map[string]any)["has_error"].(bool) {
					t.Fatalf("filter %s: unexpected row %v", filter, item)
				}
				seen[id] = true
			}
			cursor, _ = page["next_cursor"].(string)
			if cursor == "" {
				break
			}
		}
		want := 4
		if filter == "service=missing" {
			want = 0
		}
		if len(seen) != want {
			t.Fatalf("filter %s: got %d want %d", filter, len(seen), want)
		}
	}
	trace := agentGet(t, handler, "/traces/"+traceID+"?format=json")
	if strings.Contains(fmt.Sprint(trace), text) {
		t.Fatal("trace leaked span body")
	}
	spans := trace["spans"].([]any)
	if len(spans) != 1 || spans[0].(map[string]any)["input_chars"].(float64) != float64(len([]rune(text))) {
		t.Fatalf("trace span metadata: %v", spans)
	}
	base := "/spans/" + traceID + "/" + spanID + "?format=json"
	defaultWindow := agentGet(t, handler, base)
	if defaultWindow["input"] == nil || defaultWindow["output"] == nil || defaultWindow["attributes"] == nil {
		t.Fatalf("missing default windows: %v", defaultWindow)
	}
	var collected strings.Builder
	offset := 0
	for {
		result := agentGet(t, handler, fmt.Sprintf("%s&field=input&offset=%d&len=777", base, offset))
		if result["output"] != nil || result["attributes"] != nil {
			t.Fatalf("field-only response: %v", result)
		}
		window := result["input"].(map[string]any)
		collected.WriteString(window["text"].(string))
		if window["next_offset"] == nil {
			break
		}
		offset = int(window["next_offset"].(float64))
	}
	if collected.String() != text {
		t.Fatalf("window reassembly: %d runes, want %d", len([]rune(collected.String())), len([]rune(text)))
	}
	past := agentGet(t, handler, base+"&field=input&offset=99999")
	if win := past["input"].(map[string]any); win["text"] != "" || win["next_offset"] != nil {
		t.Fatalf("past end: %v", win)
	}
}

func TestAgentJSONWindowsAcrossScripts(t *testing.T) {
	for _, script := range []struct{ name, text string }{
		{"English", "ordinary english words and punctuation! "},
		{"Latin precomposed", "café déjà vu àèîôü "},
		{"Latin combining", "cafe\u0301 de\u0301ja\u0300 e\u0302 "},
		{"Cyrillic", "Привет мир и здравствуйте "},
		{"Arabic", "مرحبا بالعالم أهلاً وسهلاً "},
		{"Thai", "สวัสดีชาวโลกภาษาไทย "},
		{"Devanagari", "नमस्ते दुनिया हिंदी भाषा "},
		{"emoji ZWJ", "👩‍💻 👨‍👩‍👧‍👦 🧑🏽‍🚀 "},
		{"4-byte runes", "𐍈 𠀀 𝄞 🦊 "},
	} {
		t.Run(script.name, func(t *testing.T) {
			handler, database := newTestHandler(t, "")
			text := strings.Repeat(script.text, 1+777/len([]rune(script.text)))
			_, traceID, spanID := agentSeed(t, handler, 1, text)
			original := text
			if err := database.Reader().QueryRow("SELECT input_content FROM spans WHERE trace_id=? AND span_id=?", traceID, spanID).Scan(&text); err != nil {
				t.Fatal(err)
			}
			var stored map[string]string
			if err := json.Unmarshal([]byte(text), &stored); err != nil || stored["messages"] != original {
				t.Fatalf("ingest changed script %q: %v", script.name, err)
			}
			base := "/spans/" + traceID + "/" + spanID + "?format=json&field=input"
			for _, length := range []int{1, 777} {
				var collected strings.Builder
				offset := 0
				for {
					result := agentGet(t, handler, fmt.Sprintf("%s&offset=%d&len=%d", base, offset, length))
					window := result["input"].(map[string]any)
					if got := int(window["total_chars"].(float64)); got != len([]rune(text)) {
						t.Fatalf("len=%d total_chars=%d want %d", length, got, len([]rune(text)))
					}
					chunk := window["text"].(string)
					if count := len([]rune(chunk)); count == 0 || count > length {
						t.Fatalf("len=%d offset=%d: %d runes", length, offset, count)
					}
					collected.WriteString(chunk)
					offset += len([]rune(chunk))
					if window["next_offset"] == nil {
						break
					}
					if next := int(window["next_offset"].(float64)); next != offset {
						t.Fatalf("len=%d next_offset=%d want %d", length, next, offset)
					}
				}
				if collected.String() != text || offset != len([]rune(text)) {
					t.Fatalf("len=%d: reassembled bytes differ, %d/%d runes", length, offset, len([]rune(text)))
				}
			}
			if script.name == "English" {
				path := "/search?q=ordinary+english+words"
				before := request(t, handler, http.MethodGet, path, "", "", nil).Body.String()
				hits := agentGet(t, handler, path+"&format=json")
				items := hits["items"].([]any)
				if len(items) != 1 || items[0].(map[string]any)["span_id"] != spanID || !strings.Contains(before, `id="root"`) || !strings.Contains(items[0].(map[string]any)["snippet"].(string), "ordinary english words") {
					t.Fatalf("English search: HTML=%q JSON=%v", before, items)
				}
				if after := request(t, handler, http.MethodGet, path, "", "", nil).Body.String(); after != before {
					t.Fatal("English HTML search changed after JSON search")
				}
			}
		})
	}
}

func TestAgentJSONSearchRelevance(t *testing.T) {
	handler, _ := newTestHandler(t, "")
	agentSeed(t, handler, 6, "needle")
	page := agentGet(t, handler, "/search?format=json&q=needle&sort=relevance&limit=2")
	if items := page["items"].([]any); len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if page["next_cursor"] != nil {
		t.Fatalf("relevance order has no cursor, got %v", page["next_cursor"])
	}
	recent := agentGet(t, handler, "/search?format=json&q=needle&sort=recent&limit=2")
	if recent["next_cursor"] == nil {
		t.Fatal("sort=recent keeps the default cursor")
	}
	for _, path := range []string{
		"/search?format=json&q=needle&sort=bogus",
		"/search?format=json&q=needle&sort=relevance&cursor=123:" + strings.Repeat("a", 16),
	} {
		resp := request(t, handler, http.MethodGet, path, "", "", nil)
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("GET %s: want 400, got %d %s", path, resp.Code, resp.Body.String())
		}
	}
}
