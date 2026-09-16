# OTLP Logs GenAI Events Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept OTLP GenAI operation log events, synthesize deterministic LLM spans, discard metrics successfully, and support Gemini CLI's root OTLP endpoint.

**Architecture:** Reuse the existing ingest body/auth/normalization/storage path. Add a logs decoder beside trace decoding, route each signal explicitly, and keep metrics as a no-op response with process-level observability.

**Tech Stack:** Go, OTLP protobuf/protojson, net/http, SHA-256, SQLite-backed existing store.

---

## Chunk 1: Tests and decoder

### Task 1: Add all feature tests first

**Files:**
- Create: `internal/otlp/testdata/gemini_cli_logs.json`
- Modify: `internal/otlp/decode_test.go`
- Modify: `internal/normalize/normalize_test.go`
- Modify: `internal/pricing/pricing_test.go`
- Modify: `internal/web/server_test.go`

- [x] Create the sanitized three-record Gemini CLI fixture from the captured payload.
- [x] Add the specified logs decoder determinism/merge/ignore assertions.
- [x] Add session fallback and `gcp.gen_ai` pricing alias assertions.
- [x] Add root/logs/metrics/auth/rendering end-to-end assertions.
- [x] Run focused tests and confirm they fail because the feature is absent.

### Task 2: Implement the logs adapter

**Files:**
- Create: `internal/otlp/logs.go`
- Modify: `internal/normalize/normalize.go`
- Modify: `internal/pricing/pricing.go`

- [x] Decode OTLP logs in protobuf and JSON.
- [x] Pair request/response records and synthesize deterministic IDs/timestamps/attributes.
- [x] Fall back to resource `session.id` during normalization.
- [x] Add the `gcp.gen_ai` pricing alias.
- [x] Run focused decoder, normalize, and pricing tests until green.
- [x] Commit with the required co-author trailer.

## Chunk 2: HTTP routing and docs

### Task 3: Route traces, logs, metrics, and root POST

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/auth.go`
- Modify: `internal/web/ingest.go`

- [x] Add `/v1/logs` and `/v1/metrics` routes and make root POST enter ingest while root GET remains UI.
- [x] Detect root JSON signal by top-level key; keep root protobuf trace-only.
- [x] Return signal-specific empty OTLP responses, discard metrics, and log the discard warning once per process.
- [x] Apply Bearer authentication to root POST as well as `/v1/*`.
- [x] Run focused web tests until green.
- [x] Commit with the required co-author trailer.

### Task 4: Document behavior and result

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-09-15-spanbox-design.md`
- Create: `docs/superpowers/specs/2026-09-16-otlp-logs-genai-events-result.md`

- [x] Add Gemini CLI setup and verified Bearer-header limitation.
- [x] Correct Python Google GenAI message-capture configuration.
- [x] Update v1 non-goals for accepted logs/metrics and retained gRPC exclusion.
- [x] Record implementation result and anything skipped.
- [x] Run `gofmt` on changed Go files.
- [x] Run fresh `go vet ./... && go test ./...`.
- [x] Review the diff and commit docs/result with the required co-author trailer.
