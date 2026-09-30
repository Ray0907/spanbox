# Changelog

## Unreleased

### Added
- `PURGE_BATCH_SIZE` (default 10, 1–200) and `FTS_MERGE_EVERY` (default 256, 1–1024) tune retention. Defaults keep `/search` p95 within 2x baseline and ingest p95 within 3x during a purge (500k spans: search 150ms baseline, 209ms during purge).
- `scripts/roundtrip-e2e.sh` (export/import round trip across two fresh instances) and `scripts/load-e2e.sh` (retention, concurrent read/ingest and upgrade load check; opt-in, not part of `go test`).
- End-to-end test for the ChatGPT-authenticated Codex proxy.

### Changed
- `/search` selects a page of matches from an index before reading span bodies, and the dashboard reads a covering index. Schema v1→v2 builds these at startup: a 500k-span upgrade took 1.384s with 41.98 MiB peak WAL and 73.71 MiB peak additional DB/WAL/SHM space. Keep at least 512 MiB extra data/temp filesystem headroom for a comparable DB, beyond backups; see README.
- Retention deletes in small turns with yields, compacts the FTS index and WAL cooperatively and reclaims pages. At 500k spans a purge takes about 68s and the DB shrinks from 1212 to 608 MiB. Legacy non-incremental `auto_vacuum` DBs warn once instead of failing cleanup; a purge skipped because an import is running retries after one minute.
- Export filters (range, model, errors) select spans, not whole traces. Import streams with bounded lines and batches instead of a 32 MiB body limit, and pauses retention while it runs.

### Fixed
- Retention no longer deletes traces that still contain unfinished spans or recent arrivals.
- Invalid UTF-8 survives export/import byte for byte.
- `/proxy/chatgpt/` accepts only `codex/responses`; a client disconnect mid-stream keeps the partial Responses output.
- Documented configuration defaults and routes match the code.

## 0.4.1 — 2026-09-28

### Added
- `/dashboard/data` accepts Bearer authentication and returns JSON 401 for unauthorized agents while preserving cookie access for the UI.
- Agent skill rungs for the 24-hour dashboard overview and proxy-tagged self-session traces, covered by the skill E2E test.

### Fixed
- Bump Go to 1.26.8 to avoid race-detector crashes caused by [golang/go#78059](https://github.com/golang/go/issues/78059).

## 0.4.0 — 2026-09-28

### Added
- `?format=json` on `/`, `/sessions`, `/search`, `/traces/{id}` and `/spans/{trace}/{span}` for agents and scripts. Lists take `limit` (default 20, max 50) and return a keyset `next_cursor`; `/search` is now pageable. Span `input`, `output` and `attributes` are windowed by character with `field`, `offset`, `len` (default 2000, max 20000) and `next_offset`, and flag invalid UTF-8 with `invalid_utf8`. These requests accept `Authorization: Bearer <AUTH_TOKEN>` and return JSON errors.
- Agent skill in `skills/spanbox/` that reads traces coarse to fine, with `e2e.sh` running every skill command against a fresh binary.
- NDJSON export and import for spans.
- ChatGPT-authenticated Codex proxy vendor, including compressed requests.
- Named OpenAI-compatible upstreams (`OPENAI_COMPAT_UPSTREAMS`).
- Herdr pane trace tags via `X-Spanbox-Session`.
- AUR `spanbox-bin` package published on release.
- CA certificates in the container image; staticcheck in CI.

### Changed
- Proxy spans are captured asynchronously with capped response buffering.
- Parsed templates and static vendor assets are cached.
- Dashboard total and daily trace queries merged into one scan.
- Trace model filter matches spans instead of a denormalized string.
- Ad-hoc SQL runs on an isolated reader pool.

### Fixed
- OTLP/JSON accepts spec hex-encoded trace and span IDs, not only base64.
- Shutdown drain, an Anthropic delta panic and an unbounded retention purge.
- Large SSE lines in proxy capture; zstd decoder reuse.

## 0.3.0 — 2026-09-17

### Added
- Capture proxy for Anthropic, OpenAI and Gemini with passthrough routes, model call span extraction and authenticated capture routes.
- Sessions page.
- Langfuse native export ingestion.

### Fixed
- Missing proxy usage stays unknown instead of zero.

## 0.2.1 — 2026-09-16

### Added
- Litestream durability recipe.

## 0.2.0 — 2026-09-16

### Added
- Trace waterfall timeline.
- OTLP logs and metrics routes; GenAI log events adapted into spans.

### Fixed
- GenAI logs paired across batches.
- Dashboard charts pinned to the selected range.
- CSP-safe waterfall.

## 0.1.1 — 2026-09-16

### Changed
- Compact responsive layout and visual polish.

### Fixed
- Span tree overflow, plain-text message parts, chart axis width.

## 0.1.0 — 2026-09-16

### Added
- OTLP/HTTP trace ingest (protobuf and JSON) into embedded SQLite, with vendor attribute mapping and span kind detection.
- Trace list, trace tree, span detail, dashboard, full-text search and read-only SQL console.
- Bearer auth, retention, embedded LiteLLM pricing.
