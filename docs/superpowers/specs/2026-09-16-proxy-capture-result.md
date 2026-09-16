# v0.3.0 proxy capture implementation result

## What changed

1. Added streaming passthrough routes for Anthropic, OpenAI, and Gemini, including upstream overrides, header filtering, request limits, idle handling, cancellation, and non-inference accounting.
2. Added vendor request/response parsers that synthesize OTLP-style spans, reconstruct streamed output, preserve inclusive token semantics, group sessions into traces, and price cache creation tokens. Fixtures are trimmed and scrubbed derivatives of the supplied captures.
3. Added proxy authentication through `X-Spanbox-Token` and `/proxy/t/<token>/...`.
4. Added the cursor-paginated `/sessions` view and session aggregate query.
5. Added Langfuse's native OTLP and health paths, including Basic auth using `AUTH_TOKEN` as the secret key.
6. Added parser, relay, cancellation, error, credential-leak, auth, sessions, and Langfuse end-to-end coverage.
7. Documented proxy setup, security constraints, upstream overrides, the Codex limitation, and the updated architecture.

## Verification

- `go vet ./...`
- `go test -race ./...`
- `CGO_ENABLED=0 go build ./cmd/spanbox`
- Langfuse 4.15.3 from the supplied virtualenv: a native `generation` sent using only `LANGFUSE_HOST`, `LANGFUSE_SECRET_KEY`, and `LANGFUSE_PUBLIC_KEY` landed with 11 input and 3 output tokens.
- Anthropic real traffic on a free local port: Claude Code 2.1.273 sent two `POST /proxy/anthropic/v1/messages?beta=true` calls; both landed as `anthropic` / `claude-cli` spans with token usage.
- Gemini real traffic on the same free local port: Gemini CLI 0.26.0 sent `POST /proxy/gemini/v1beta/models/gemini-3.6-flash:streamGenerateContent?alt=sse`; it landed as a `gcp.gen_ai` / `GeminiCLI` span with token usage.

## Skipped

- `docs/screenshots/traces.png` was not refreshed because the trace list did not gain a column.
- No required implementation or verification step was skipped.
