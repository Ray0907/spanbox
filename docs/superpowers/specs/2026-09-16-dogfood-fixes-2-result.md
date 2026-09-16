# Dogfood fixes round 2 result

## Changed

1. **Dashboard range:** `DashboardData` now includes Unix-second `RangeFrom` and `RangeTo`. Every uPlot chart pins its x scale to that requested range, and every data series renders six-pixel points so a single UTC-day bucket remains visible.
2. **Cross-batch GenAI logs:** the web handler now owns a mutex-protected `LogsAdapter`. Pending request records survive OTLP requests, pair FIFO by resource identity and request model, expire after 10 minutes through an injected clock, and are capped at 1,000 entries with oldest-first eviction. Response-only records still produce zero-duration spans.
3. **Trace waterfall:** trace and tree queries now expose exact start/end timestamps. Go computes clamped integer offsets and widths against the trace window, including a one-percent minimum width. The trace page renders a 160px kind-colored waterfall and compact time ruler on wider screens, hides both below 700px, and shows each selected span's start offset.

## Tests and visual verification

- Added 24-hour dashboard range assertions.
- Added same-batch determinism, cross-batch pairing, response-only, expiry, capacity, and persisted-span coverage for OTLP logs.
- Added waterfall geometry, minimum-width, ruler, and start-offset rendering assertions.
- `go test -race ./internal/otlp ./internal/web` passed during the adapter change.
- `go vet ./... && go test ./...` passed after all changes.
- Browser-checked dark and light desktop layouts plus the narrow layout against a local instance on port 4319. The Impeccable detector returned no findings in degraded regex mode because its optional HTML parser modules were unavailable.
- Refreshed only `docs/screenshots/trace.png` and `docs/screenshots/trace-light.png` using newly populated demo data.

## Follow-up: CSP-safe waterfall

A live check found that CSP correctly blocked the waterfall's inline `style` geometry, leaving every bar at zero width. The bars now use inline SVG with numeric `x` and `width` geometry attributes while retaining `fill: var(--kind-color)` in the external stylesheet. A regression assertion rejects any `style="` attribute on the trace page, browser verification confirmed non-zero rendered SVG widths under the existing CSP, and both trace screenshots were refreshed.

## Skipped

Nothing from the spec.
