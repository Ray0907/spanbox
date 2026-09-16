# Dogfood Fixes Round 2 Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix dashboard chart domains, preserve GenAI request/response pairing across OTLP batches, and replace relative span meters with a trace waterfall.

**Architecture:** Keep range metadata in the existing dashboard response, move log pairing state into a handler-owned mutex-protected adapter, and compute trusted integer waterfall geometry in the Go trace view model. Reuse the current templates, kind accents, and responsive layout.

**Tech Stack:** Go, OTLP protobuf/protojson, SQLite, html/template, vanilla JavaScript/CSS, uPlot.

---

### Task 1: Pin dashboard charts to the requested range

**Files:**
- Modify: `internal/store/query.go`
- Modify: `internal/web/server_test.go`
- Modify: `internal/web/static/app.js`

- [ ] Extend the dashboard endpoint test with the 24-hour range assertions and verify RED.
- [ ] Return `RangeFrom` and `RangeTo` in Unix seconds.
- [ ] Use the fixed x range and visible six-pixel points on every chart series.
- [ ] Run focused and full verification, then commit with the required trailer.

### Task 2: Pair GenAI logs across batches

**Files:**
- Modify: `internal/otlp/logs.go`
- Modify: `internal/otlp/decode_test.go`
- Modify: `internal/web/server.go`
- Modify: `internal/web/ingest.go`
- Modify: `internal/web/server_test.go`

- [ ] Add cross-batch storage, response-only, expiry, and bounded-pending tests and verify RED.
- [ ] Introduce a handler-owned logs adapter with mutex, injected clock, 10-minute expiry, and 1,000-entry cap.
- [ ] Preserve FIFO matching by resource identity and request model.
- [ ] Run focused and full verification, then commit with the required trailer.

### Task 3: Add the trace waterfall

**Files:**
- Modify: `internal/store/query.go`
- Modify: `internal/web/pages.go`
- Modify: `internal/web/server_test.go`
- Modify: `internal/web/templates/trace.html`
- Modify: `internal/web/templates/span.html`
- Modify: `internal/web/static/app.css`
- Refresh: `docs/screenshots/trace.png`
- Refresh: `docs/screenshots/trace-light.png`

- [ ] Add trace page geometry and start-offset assertions and verify RED.
- [ ] Return start/end timestamps in tree queries and compute clamped integer offsets/widths against the trace window.
- [ ] Render the 160px waterfall, time ruler, and span start offset while preserving the narrow layout.
- [ ] Run the Impeccable detector and browser-check dark/light desktop renders on port 4319.
- [ ] Populate a port-4319 instance and refresh only the two required screenshots.
- [ ] Run focused and full verification, then commit with the required trailer.

### Task 4: Record the result

**Files:**
- Create: `docs/superpowers/specs/2026-09-16-dogfood-fixes-2-result.md`
- Modify: `docs/superpowers/plans/2026-09-16-dogfood-fixes-2.md`

- [ ] Record changes, verification, and anything skipped.
- [ ] Mark plan items complete, verify a clean diff, and commit with the required trailer.
