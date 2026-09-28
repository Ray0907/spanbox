# Changelog

## Unreleased

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
