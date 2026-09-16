# v0.3.0: capture by proxy ("change one URL")

## Why

Coding agents and most apps do not emit spans. pxpipe (teamchong/pxpipe) shows the reliable way to see every model call: a local passthrough proxy on the vendor base URL. spanbox adds that as a capture path. Anything that talks to Anthropic, OpenAI or Gemini becomes fully visible (prompt, tools, response, usage, cache, latency) by changing one environment variable. No SDK, no instrumentation. This is the strongest Langfuse-replacement story spanbox can offer.

Live captures used to write this spec (real Claude Code 2.1.273 and Gemini CLI 0.26.0 traffic through a recording proxy; auth headers redacted). Use them as the source for fixtures, but trim the huge system prompts and remove `device_id` / `account_uuid` / `x-gemini-api-privileged-user-id` values before committing anything:

- `/private/tmp/claude-501/-private-tmp/78ff5489-5250-44fc-ad90-eaf1a8c5bc5a/scratchpad/cap/claude/*.json` (two `POST /v1/messages?beta=true`, streaming)
- `/private/tmp/claude-501/-private-tmp/78ff5489-5250-44fc-ad90-eaf1a8c5bc5a/scratchpad/cap/gemini/*.json` (one `POST /v1beta/models/gemini-3.6-flash:streamGenerateContent?alt=sse`)
- Each file: `req_headers`, `req_body`, `resp_headers`, `resp_body` (raw SSE text, already gunzipped), `ttfb_ms`, `total_ms`.

Observed facts that the implementation must respect:

| | Anthropic (Claude Code) | Gemini (Gemini CLI) | OpenAI (not captured live; Codex in ChatGPT auth mode ignores `OPENAI_BASE_URL`) |
|---|---|---|---|
| Client env | `ANTHROPIC_BASE_URL=http://host:4318/proxy/anthropic` | `GOOGLE_GEMINI_BASE_URL=http://host:4318/proxy/gemini` | `OPENAI_BASE_URL=http://host:4318/proxy/openai/v1` |
| Auth header | `Authorization: Bearer <oauth>` or `x-api-key` | `x-goog-api-key` | `Authorization: Bearer` |
| Inference path | `POST /v1/messages` (query `?beta=true` present) | `POST /v1beta/models/{model}:streamGenerateContent?alt=sse` or `:generateContent` | `POST /v1/chat/completions`, `POST /v1/responses` |
| Model | body `model` | path segment | body `model` |
| Streaming | `stream: true`, SSE `event:` lines; usage in `message_start.message.usage` (input, cache_creation_input_tokens, cache_read_input_tokens, output=partial) and final `message_delta.usage` (authoritative `output_tokens`, may include `output_tokens_details.thinking_tokens`); `message_delta.delta.stop_reason` | SSE `data:` JSON chunks, every chunk carries `usageMetadata` (`promptTokenCount` inclusive of cache, `cachedContentTokenCount` when cached, `candidatesTokenCount`, `thoughtsTokenCount`), `modelVersion`, `responseId`; last chunk `candidates[0].finishReason` | Chat: `data:` chunks, usage only in the final chunk when `stream_options.include_usage`; Responses: event `response.completed` with `response.usage` (`input_tokens`, `input_tokens_details.cached_tokens`, `output_tokens`, `output_tokens_details.reasoning_tokens`) |
| Non-stream | JSON body with `usage`, `stop_reason`, `content[]` | JSON with `usageMetadata`, `candidates` | JSON with `usage`, `choices` / `output` |
| Session / user | `metadata.user_id` is a JSON string `{"device_id","account_uuid","session_id"}` → `session_id`; `user_id` = `account_uuid` | header `x-gemini-api-privileged-user-id` → `user_id`; no session | body `user` → `user_id` |
| Response id | header `request-id`, also `message_start.message.id` | `responseId` | `id` / `response.id` |
| Response encoding | upstream returns `Content-Encoding: gzip` when asked | identity | varies |
| Token semantics | `input_tokens` EXCLUDES cache tokens: inclusive total = `input_tokens + cache_read_input_tokens + cache_creation_input_tokens` | `promptTokenCount` already inclusive | `prompt_tokens` / `input_tokens` inclusive |

## Scope

### 1. Passthrough proxy routes

`/proxy/anthropic/{rest}` → `https://api.anthropic.com/{rest}`, `/proxy/openai/{rest}` → `https://api.openai.com/{rest}`, `/proxy/gemini/{rest}` → `https://generativelanguage.googleapis.com/{rest}`. Optional env `ANTHROPIC_UPSTREAM`, `OPENAI_UPSTREAM`, `GEMINI_UPSTREAM` override the base (for gateways / regional endpoints). Any method, any sub-path, query string preserved.

Forwarding rules:
- Copy all request headers except hop-by-hop (`Connection`, `Keep-Alive`, `Proxy-*`, `TE`, `Trailer`, `Transfer-Encoding`, `Upgrade`), `Host`, and `Accept-Encoding` (drop it so the upstream answers identity and the tee can parse; do not re-add). Set `Host` to the upstream host. Do not add or remove auth headers; never read credential values into any struct that is stored or logged.
- Read the request body fully (cap 32 MiB via `http.MaxBytesReader`, 413 beyond) so it can be parsed and forwarded as bytes with `Content-Length`.
- Use one shared `http.Client` per upstream with `Transport` settings: `ResponseHeaderTimeout` 120s, `IdleConnTimeout` 90s, `MaxIdleConnsPerHost` 16, no overall client timeout (streams can run minutes). The upstream request uses the incoming request's context so a client disconnect cancels the upstream.
- Stream the response: copy status and headers (same hop-by-hop exclusions, and drop `Content-Length` when streaming), then copy the body in 32 KiB reads, `Flush` after every write, and tee every byte into the parser. Enforce an upstream idle timeout of 120s between reads (abort with 502 if headers have not been sent yet, otherwise close the connection).
- Upstream dial/TLS failure → 502 with a JSON error body; timeouts → 504. Upstream non-2xx passes through unchanged to the client and still produces a span with `status_code = 2` and the first 2 KiB of the body as `status_message`.
- Paths that are not inference calls (`/v1/models`, `count_tokens`, `/v1beta/models` GET, embeddings for now) pass through without creating a span; count them in a metric log line.

### 2. Span extraction

One `store.Span` per inference request, via a `RawSpan` built in a new package `internal/proxy` (uses `otlp.RawSpan`, `normalize.Span`, `pricing`, `store.InsertBatch`; insertion happens after the response is fully relayed, never blocking the relay). Produce these attributes so the existing normalize table maps them without new candidates:

- `gen_ai.operation.name` = `chat` (Anthropic messages, OpenAI chat, Responses) or `generate_content` (Gemini); `gen_ai.provider.name` = `anthropic` / `openai` / `gcp.gen_ai`; `gen_ai.request.model`; `gen_ai.response.model` (Anthropic `message_start.message.model`, Gemini `modelVersion`, OpenAI chunk `model`); `gen_ai.response.id`; `gen_ai.response.finish_reasons` (array).
- `gen_ai.usage.input_tokens` = inclusive total per the table above; `gen_ai.usage.output_tokens` = total output including thinking / reasoning (Gemini: `candidatesTokenCount + thoughtsTokenCount`); `gen_ai.usage.cache_read.input_tokens`; `gen_ai.usage.cache_creation.input_tokens` (Anthropic only, raw attribute; see pricing below); `gen_ai.usage.reasoning.output_tokens` when present.
- `gen_ai.system_instructions` (Anthropic `system`, Gemini `systemInstruction`, Responses `instructions`), `gen_ai.input.messages` = JSON of the vendor messages array as sent (`messages` / `contents` / `input`), `gen_ai.output.messages` = JSON array of the assembled assistant output: for streams, reconstruct blocks in order from `content_block_start` / `content_block_delta` (text and `input_json_delta` for tool_use, thinking blocks kept as `{"type":"thinking","chars":N}` only), Gemini `candidates[0].content.parts` concatenated per part kind, OpenAI `choices[0].delta` text and `tool_calls` merged by index, Responses `response.output` from `response.completed`. `gen_ai.tool.definitions` = count and names only (`{"count":51,"names":[...]}`), not the full schemas (they are already in the request body if anyone needs them; do not store the 200 KB body twice).
- `gen_ai.conversation.id` from Anthropic `metadata.user_id` JSON `session_id`; `user.id` from `account_uuid` / `x-gemini-api-privileged-user-id` / OpenAI `user`.
- `spanbox.source = "proxy"`, `spanbox.proxy.ttfb_ms`, `spanbox.proxy.stream = true|false`, `http.response.status_code`, `server.address` = upstream host, `url.path`, `gen_ai.request.max_tokens`, `gen_ai.request.temperature` when present, Anthropic `service_tier`.
- Resource: `service.name` from the `User-Agent` product token before the first `/` (`claude-cli`, `GeminiCLI`, `codex_cli_rs`, `OpenAI/Python`…), else `proxy`; `service.version` from the token after `/`. Scope `{name:"spanbox/proxy", version: build version}`.
- Stored request headers: allowlist only (`user-agent`, `anthropic-version`, `anthropic-beta`, `x-app`, `x-goog-api-client`, `x-stainless-lang`, `openai-organization`), under `http.request.header.<name>`. Nothing else from headers is ever stored.
- Timing: `StartNs` at request arrival, `EndNs` at last byte relayed.
- IDs: `span_id = sha256("spanbox-proxy-span" + response id or request-id or random)[:8]`; `trace_id = sha256("spanbox-proxy-trace" + session_id)[:16]` when a session id exists (all calls of one Claude Code session form one trace, same as the logs adapter), else `sha256("spanbox-proxy-trace" + span_id)[:16]`. `parent_span_id = ""`.

Pricing: extend `pricing.Cost` with an optional `cacheCreation` count. Formula becomes `uncached = input - cache_read - cache_creation` (clamped at 0), `+ cache_creation * (cache_creation_input_token_cost ?? input_cost_per_token)`. Existing callers pass 0. Add a test with Anthropic numbers from the capture: `input_tokens 10, cache_creation 55784, cache_read 0, output 81` on `claude-haiku-4-5-20251001`.

### 3. Auth for proxy routes

When `AUTH_TOKEN` is set, a proxy request must carry the spanbox token in one of: header `X-Spanbox-Token: <token>`, or the path form `/proxy/t/<token>/anthropic/...`. Constant-time compare via the existing helper. Otherwise 401 with a JSON body. When `AUTH_TOKEN` is empty, proxy routes are open like everything else (startup warning already exists). Document: Claude Code adds headers with `ANTHROPIC_CUSTOM_HEADERS="X-Spanbox-Token: <token>"`; Gemini CLI has no header hook, use the path form.

### 4. Sessions page

`GET /sessions`: rows grouped by `traces.session_id <> ''` over the selected range: session id, service, first / last time, trace count, llm calls, tokens in/out (NULL propagation as everywhere), cost, models. Sort by last time desc, cursor `(last_ns, session_id)`. Row links to `/?session=<id>`. Add to the nav. Query lives in `internal/store/query.go`.

### 5. Langfuse native path

`POST /api/public/otel/v1/traces` behaves exactly like `/v1/traces`, additionally accepting `Authorization: Basic base64(pk:sk)` where `sk` equals `AUTH_TOKEN` (Bearer still works). `GET /api/public/health` → `{"status":"OK"}`. README: switching a Langfuse v3/v4 app now needs only `LANGFUSE_HOST=http://host:4318`, `LANGFUSE_SECRET_KEY=<AUTH_TOKEN>`, `LANGFUSE_PUBLIC_KEY=anything`. Verify with the installed SDK in `/private/tmp/claude-501/-private-tmp/78ff5489-5250-44fc-ad90-eaf1a8c5bc5a/scratchpad/lfv/bin/python` (langfuse 4.15.3): a `generation` observation must land with tokens using only those env vars.

### 6. Tests

- `internal/proxy`: table-driven parser tests from trimmed fixtures (`internal/proxy/testdata/anthropic_stream.txt`, `anthropic_json.json`, `gemini_stream.txt`, `openai_chat_stream.txt`, `openai_responses_stream.txt`, `openai_chat_json.json`) asserting every attribute above, inclusive input tokens, output incl. thinking, finish reasons, session / user extraction, tool definitions summary.
- Relay tests with an `httptest` upstream: (a) streaming: upstream emits 3 SSE chunks 200 ms apart; the client must receive chunk 1 before the upstream has written chunk 3 (use channels to prove ordering); (b) client cancels mid-stream → upstream request context is cancelled within 1 s; (c) upstream 429 JSON passes through unchanged and a span with `status_code 2` is stored; (d) upstream unreachable → 502, span stored with status 2; (e) `Accept-Encoding` is not forwarded; (f) credential headers reach the upstream unchanged and never appear in `spans.attributes`, `resource`, `events` or the log output (grep the DB and a captured logger for the literal secret); (g) auth: with `AUTH_TOKEN`, missing spanbox token → 401, header form and path form → 200; (h) non-inference `GET /v1/models` proxied with no span.
- Sessions and Langfuse alias: e2e in `server_test.go`.
- `go vet ./... && go test -race ./...` green.

### 7. Docs and release

README: new top-level section `## Capture without instrumentation (proxy)` placed before `## Export traces`, with the three env vars, the Claude Code custom-header line, the Gemini CLI path-token line, the security note (credentials pass through untouched and are never stored; run on loopback or with `AUTH_TOKEN`; the proxy is in your request path, so a spanbox restart interrupts in-flight calls), and the Codex limitation (ChatGPT-auth Codex does not honour `OPENAI_BASE_URL`; API-key mode does). Update the design spec §2 non-goals and §4 architecture diagram. Refresh `docs/screenshots/traces.png` only if the list gained a column.

## Constraints

- No new Go dependencies. `CGO_ENABLED=0`. Keep CSP, no `template.HTML`.
- Conventional commits, one per numbered item at least, trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. Do not push.
- Do not commit the raw captures. Fixtures must be trimmed and scrubbed as described above.
- Work on `main` in `/private/tmp/spanbox`; no worktrees or branches.
- Finish with `docs/superpowers/specs/2026-09-16-proxy-capture-result.md`: what changed, which vendor paths were verified with real traffic (you can re-run `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>/proxy/anthropic claude -p "Reply with exactly: proxy ok" --model haiku` and `GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:<port>/proxy/gemini gemini -p "Reply with exactly: proxy ok" -m gemini-3.6-flash` against your own spanbox on a free port; both credentials are in the environment), and anything skipped.
