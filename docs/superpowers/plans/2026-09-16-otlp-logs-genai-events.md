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

- [ ] Create the sanitized three-record Gemini CLI fixture from the captured payload.
- [ ] Add the specified logs decoder determinism/merge/ignore assertions.
- [ ] Add session fallback and `gcp.gen_ai` pricing alias assertions.
- [ ] Add root/logs/metrics/auth/rendering end-to-end assertions.
- [ ] Run focused tests and confirm they fail because the feature is absent.

### Task 2: Implement the logs adapter

**Files:**
- Create: `internal/otlp/logs.go`
- Modify: `internal/normalize/normalize.go`
- Modify: `internal/pricing/pricing.go`

- [ ] Decode OTLP logs in protobuf and JSON.
- [ ] Pair request/response records and synthesize deterministic IDs/timestamps/attributes.
- [ ] Fall back to resource `session.id` during normalization.
- [ ] Add the `gcp.gen_ai` pricing alias.
- [ ] Run focused decoder, normalize, and pricing tests until green.
- [ ] Commit with the required co-author trailer.

## Chunk 2: HTTP routing and docs

### Task 3: Route traces, logs, metrics, and root POST

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/auth.go`
- Modify: `internal/web/ingest.go`

- [ ] Add `/v1/logs` and `/v1/metrics` routes and make root POST enter ingest while root GET remains UI.
- [ ] Detect root JSON signal by top-level key; keep root protobuf trace-only.
- [ ] Return signal-specific empty OTLP responses, discard metrics, and log the discard warning once per process.
- [ ] Apply Bearer authentication to root POST as well as `/v1/*`.
- [ ] Run focused web tests until green.
- [ ] Commit with the required co-author trailer.

### Task 4: Document behavior and result

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-09-15-spanbox-design.md`
- Create: `docs/superpowers/specs/2026-09-16-otlp-logs-genai-events-result.md`

- [ ] Add Gemini CLI setup and verified Bearer-header limitation.
- [ ] Correct Python Google GenAI message-capture configuration.
- [ ] Update v1 non-goals for accepted logs/metrics and retained gRPC exclusion.
- [ ] Record implementation result and anything skipped.
- [ ] Run `gofmt` on changed Go files.
- [ ] Run fresh `go vet ./... && go test ./...`.
- [ ] Review the diff and commit docs/result with the required co-author trailer.
