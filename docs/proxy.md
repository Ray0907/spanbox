# Capture with the Proxy

Point an application's vendor base URL at spanbox to capture prompts, tools, responses, usage, cache tokens, cost, and latency without adding an SDK.

```sh
export ANTHROPIC_BASE_URL=http://localhost:4318/proxy/anthropic
export GOOGLE_GEMINI_BASE_URL=http://localhost:4318/proxy/gemini
export OPENAI_BASE_URL=http://localhost:4318/proxy/openai/v1
```

Vendor credentials pass through unchanged and are never stored.

## Authentication

When `AUTH_TOKEN` is set, proxy routes need `X-Spanbox-Token: <AUTH_TOKEN>` or a token in the path: `/proxy/t/<token>/<vendor>/...`. In unauthenticated mode, `/proxy/t/<token>/...` is not rewritten; use `/proxy/<vendor>/...`.

Claude Code can send headers through `ANTHROPIC_CUSTOM_HEADERS`:

```sh
export ANTHROPIC_CUSTOM_HEADERS="X-Spanbox-Token: <token>,X-Spanbox-Session: $HERDR_PANE_ID"
```

Gemini CLI has no custom-header hook, so use the path form:

```sh
export GOOGLE_GEMINI_BASE_URL=http://localhost:4318/proxy/t/<token>/gemini
```

Spanbox headers are stripped before the request goes upstream.

## Tag a session

A non-empty `X-Spanbox-Session` header sets the captured span's `session_id`, so the session filter and sessions page find those traces. It takes precedence over the vendor's conversation ID, which stays stored as `gen_ai.conversation.id`. Without the header, session assignment is unchanged. Keep the tag consistent within one vendor conversation.

## Vendors

| Path | Upstream (override) |
|---|---|
| `/proxy/anthropic/...` | `https://api.anthropic.com` (`ANTHROPIC_UPSTREAM`) |
| `/proxy/openai/...` | `https://api.openai.com` (`OPENAI_UPSTREAM`) |
| `/proxy/chatgpt/codex/responses` | `https://chatgpt.com/backend-api` (`CHATGPT_UPSTREAM`) |
| `/proxy/gemini/...` | `https://generativelanguage.googleapis.com` (`GEMINI_UPSTREAM`) |
| `/proxy/openai-compat/<name>/...` | Named in `OPENAI_COMPAT_UPSTREAMS` |

### ChatGPT-authenticated Codex

Codex uses the `openai-codex` provider. Route it through spanbox without changing its OAuth login by overriding the provider base URL in your client's model configuration:

```json
{
  "providers": {
    "openai-codex": {
      "baseUrl": "http://localhost:4318/proxy/chatgpt"
    }
  }
}
```

`/proxy/chatgpt/codex/responses` is the only accepted ChatGPT path; inference is captured on POST. Codex API-key mode can use `OPENAI_BASE_URL=http://localhost:4318/proxy/openai/v1` instead.

### Several OpenAI-compatible services

Register lowercase names, then route each client through its name:

```sh
export OPENAI_COMPAT_UPSTREAMS="vllm=http://localhost:18080/v1,local-ai=http://localhost:8080/v1"
export OPENAI_BASE_URL=http://localhost:4318/proxy/openai-compat/vllm
```

Names must match `[a-z0-9-]+`. Entries are not whitespace-trimmed, and a repeated name uses its last URL. The upstream base path is prepended to the requested suffix, and `server.address` records the hostname without the port.

## What is captured

POST requests to:

- Anthropic `/v1/messages`
- OpenAI `/v1/chat/completions` and `/v1/responses`
- ChatGPT `/codex/responses`
- Gemini `/v1beta/models/<model>:generateContent` and `:streamGenerateContent`
- Named OpenAI-compatible routes, which also accept `/chat/completions` and `/responses` without `/v1`

Other requests are relayed without capture, except other ChatGPT paths, which are rejected.

The proxy records parsed request and response messages, tool names and calls, and cache and thinking token counts. It does not record complete raw bodies. A client disconnect mid-stream keeps the partial Responses output.

## Tool spans

The proxy derives `execute_tool <name>` spans from tool results after the last assistant/model turn: Anthropic `tool_result`, OpenAI chat `role=tool`, OpenAI/ChatGPT Responses `function_call_output`, and Gemini `functionResponse`. Names and arguments come from matching calls in the same request history; results become the span's output, and Anthropic `is_error` or an explicit Gemini `response.error` field marks an error.

Span identity is deterministic, scoped by vendor (including the configured OpenAI-compatible route), session, and call ID. Session uses `X-Spanbox-Session`, then the vendor conversation/session ID, then empty. Gemini uses supplied `functionCall.id` / `functionResponse.id` values when present, preserving identity across pruned history and pairing reordered results by ID. Without an ID, it falls back to the issuing turn's position in `contents`, the function name, and its ordinal among calls with that name in the turn; this fallback requires stable history positions across retries. A best-effort cache links results to their issuing model span and measures the gap to the first observed result, clamped to zero for overlapping streams; retries do not extend that duration. It also supplies missing names and arguments for output-only Responses continuations.

Known limits:
- Correlation uses a mutex-protected, in-memory cache of 1,024 issuing calls, evicted FIFO; it is not persisted.
- After a restart or eviction, or when capture order flips, a tool span may be an orphan with no parent and zero duration.
- A replay after a restart may add one extra orphan with the same span ID but a different trace ID; the original linked span remains unchanged.

## Pitfalls

- **Double capture.** If Gemini CLI telemetry also points at spanbox while `GOOGLE_GEMINI_BASE_URL` uses the proxy, every call is recorded twice. Pick one path per tool.
- **In the request path.** Restarting spanbox interrupts in-flight model calls.
- **Exposure.** Run the proxy on loopback or set `AUTH_TOKEN`.
