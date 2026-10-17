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

## Agent turns

With `X-Spanbox-Session` or a vendor conversation/session ID, each user prompt becomes a separate trace rooted at `invoke_agent <service.name>` (`kind=agent`). Model spans are children of this root; tool spans keep their issuing model as parent. The header takes precedence for identity, while vendor conversation and user attributes are preserved. Without either session source, capture is unchanged: no agent root is created.

The anchor is the last real user message in request history:
- Anthropic: a user message with any non-`tool_result` content; tool-result-only messages continue the existing turn.
- OpenAI chat: the last `role=user` message, ignoring tool messages.
- Responses / ChatGPT Codex: the last user message item in `input` (a string input is also a prompt); `function_call_output` continues the turn.
- Gemini: the last user entry in `contents` containing a text part rather than only `functionResponse` parts.

Turn keys encode the route, scoped session, anchor history index and a hash of the anchor JSON. Trace and root IDs are deterministic hashes of this key, so unchanged history produces the same IDs after restart. If an incremental continuation has no anchor, SQLite's `proxy_turns` supplies that route/session's latest turn; without a retained turn, the current call starts one. Pruned or rewritten anchors can change identity, and repeated identical prompts at the same history index cannot be distinguished.

The root spans the earliest model start through the latest model end, contains the anchor's visible text and the latest model output text, and keeps error status if any model call fails. It has no token or cost fields; trace totals still sum only model spans. Turn state, root updates, model/tool spans and tool correlation commit together. Retention expires turn records by creation time (`RETENTION_DAYS=0` keeps them). Schema v4 adds this state without backfilling or rewriting old session-level traces.

## Tool spans

The proxy derives `execute_tool <name>` spans from tool results after the last assistant/model turn: Anthropic `tool_result`, OpenAI chat `role=tool`, OpenAI/ChatGPT Responses `function_call_output`, and Gemini `functionResponse`. Names and arguments come from matching calls in the same request history; results become the span's output, and Anthropic `is_error` or an explicit Gemini `response.error` field marks an error.

Span identity is deterministic, scoped by vendor (including the configured OpenAI-compatible route), session, and call ID. Session uses `X-Spanbox-Session`, then the vendor conversation/session ID, then empty. Gemini uses supplied `functionCall.id` / `functionResponse.id` values when present, preserving identity across pruned history and pairing reordered results by ID. Without an ID, it falls back to the issuing turn's position in `contents`, the function name, and its ordinal among calls with that name in the turn; this fallback requires stable history positions across retries. Persisted correlation links results to their issuing model span and measures the gap to the first observed result, clamped to zero for overlapping streams; retries, restarts, and cache eviction do not extend that duration. Issuing calls and first-result timestamps are committed in the same transaction as their spans. It also supplies missing names and arguments for output-only Responses continuations.

Known limits:
- Identical prompts on the same route/session with the same `anchorIndex` (for example, repeated single-message requests without history) produce the same `turnKey` and merge into one agent root because retries and repeated prompts are indistinguishable from the request alone; conversations with growing history are unaffected because `anchorIndex` increases as messages accumulate.
- Correlation is stored in SQLite (`proxy_tool_calls`); a mutex-protected, 1,024-call FIFO cache accelerates reads, with database lookup on a miss.
- Each retention sweep deletes correlation records older than `RETENTION_DAYS`, based on when they were created, not their last replay. `RETENTION_DAYS=0` keeps them indefinitely.
- A tool span may still be an orphan with no parent and zero duration when its issuing call was never captured, capture order flips, or its correlation record has expired and is no longer cached. Restarting or evicting a cached call alone does not lose a retained link or add an orphan.

## Pitfalls

- **Double capture.** If Gemini CLI telemetry also points at spanbox while `GOOGLE_GEMINI_BASE_URL` uses the proxy, every call is recorded twice. Pick one path per tool.
- **In the request path.** Restarting spanbox interrupts in-flight model calls.
- **Exposure.** Run the proxy on loopback or set `AUTH_TOKEN`.
