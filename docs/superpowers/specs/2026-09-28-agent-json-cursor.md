# Agent JSON + cursor paging

Goal: let a coding agent read spanbox through curl in small pages, coarse to fine, so trace data never floods its context. HTML behavior stays unchanged.

## Scope

Add `?format=json` to four existing GET routes. Same handlers, same store queries, same auth (Bearer `AUTH_TOKEN` already works on page routes, see `internal/web/auth.go:74`). No new routes, no new dependencies.

| Route | JSON response |
|---|---|
| `/` (trace list, all existing filters) | `{"items":[trace summary...], "next_cursor": "<start_ns>:<trace_id>" \| null}` |
| `/sessions` | `{"items":[session summary...], "next_cursor": "<last_ns>:<session_id>" \| null}` |
| `/search?q=` | `{"items":[hit...], "next_cursor": "<start_ns>:<span_id>" \| null}` |
| `/traces/{trace_id}` | trace summary + flat `spans` list: ids, parent id, name, kind, model, start offset ms, duration ms, tokens, cost, status, `input_chars`, `output_chars`. No bodies. |
| `/spans/{trace_id}/{span_id}` | span metadata + windowed text fields (below) |

### Lists

- `limit` param: default 20, max 50, applies to JSON only. HTML keeps 50.
- Fetch `limit+1` rows; `next_cursor` is non-null only when the extra row exists.
- `/search` gets a keyset cursor: `(s.start_ns, s.span_id) < (?, ?)`, matching the existing `ORDER BY s.start_ns DESC, s.span_id DESC`. Store change is shared; HTML search may keep 100 rows with no next link.
- Paging with `next_cursor` requires the same other query params; document in the response nothing extra, just keep filters working with cursor.

### Span text windows

Fields: `input`, `output`, `attributes`.

- Default (no `field`): each field returned as `{"text": first len chars, "total_chars": N, "next_offset": M | null}`.
- `field=<name>&offset=N&len=L`: return only that field's window.
- `len` default 2000, max 20000. Offsets and lengths count runes, not bytes (CJK content must never split mid-character).
- `offset >= total_chars`: empty text, `next_offset` null, status 200.

### Errors

JSON routes return `{"error":"..."}` with 400 for bad cursor, bad `limit`/`offset`/`len` (negative, non-integer), unknown `field`; 404 for missing trace/span.

## Failure modes (test each)

1. Rows with identical `start_ns` duplicated or skipped across pages — tie-breaker in keyset.
2. `next_cursor` present on the final page, or missing when more rows exist.
3. Filters (e.g. `service=`, `errors=1`) dropped or ignored when combined with `cursor`.
4. Rune offset splits a multi-byte character; reassembled text differs from stored text.
5. Offset past end errors instead of returning empty window.
6. Negative / huge / non-numeric `limit`, `offset`, `len` accepted unclamped or panic.
7. Malformed cursor returns 500 or HTML instead of 400 JSON.
8. `format=json` bypasses auth when `AUTH_TOKEN` is set.
9. HTML output of any touched route changes (existing tests must still pass).
10. Trace JSON leaks full span bodies.
11. Non-CJK text breaks. Windowing must be script-agnostic: plain ASCII English, accented Latin in both precomposed (`é`) and combining (`é`) form, Cyrillic, Arabic (RTL), Thai, Devanagari, emoji incl. ZWJ sequences (`👩‍💻`) and 4-byte runes. Walking with `len=1` and `len=777` must reassemble byte-identical text for each script; `total_chars` must be consistent with the windows. English `/search` results must be unchanged by this work.

## Verification

E2E only, through real HTTP handler + real SQLite (follow `newTestHandler` / `request` in `internal/web/server_test.go`), in one new test file `internal/web/agent_json_test.go`:

- Ingest ≥ 3×limit traces, several sharing one `start_ns`; walk `next_cursor` on `/`, `/sessions`, `/search` with small `limit`; assert set of ids equals the ingested set exactly, no duplicates.
- Ingest one span with ≥ 10k chars of mixed CJK + ASCII input; walk `field=input` by `next_offset` with `len=777`; assert concatenation equals original.
- Cover every failure mode above.

Run `go vet ./... && go test -race ./...`; all must pass.

Artifact: write `docs/superpowers/specs/2026-09-28-agent-json-cursor-result.md` with the exact commands run, pass/fail output summary, and one sample curl sequence (list page 1, page 2 via cursor, span window) with real response JSON captured from a running `./spanbox` binary seeded via `/v1/traces`.

## Out of scope

Span tree paging, MCP server, SKILL.md, CJK tokenizer. Do not commit.
