# How spanbox Works

spanbox is one process with one HTTP port (default `4318`). It receives spans, normalizes them, stores them in SQLite, and serves them back.

## The pipeline

```
OTLP exporters ─┐
OTLP logs ──────┤                         ┌─ web UI (HTML)
Vendor proxy ───┴─► decode ─► normalize ─►│ SQLite ─┼─ JSON API
Import (NDJSON) ─────────────────────────►│         ├─ SSE /events
                                          └─────────┴─ SQL console
```

1. **Decode.** `internal/otlp` reads OTLP/HTTP protobuf or JSON. OTel GenAI log events (as sent by Gemini CLI) are adapted into spans. Metrics are accepted and discarded.
2. **Normalize.** `internal/normalize` flattens attributes into columns: provider, models, token counts, input and output content, tool name, session and user, finish reason. It assigns each span a `kind`: `llm`, `embedding`, `tool`, `retrieval`, `agent`, or `other`.
3. **Price.** `internal/pricing` computes `cost_usd` from the embedded LiteLLM price table when the span carries token counts but no cost. `cost_source` records where the number came from. See [Update model pricing](pricing.md).
4. **Store.** `internal/store` upserts into SQLite. Spans are unique on `(trace_id, span_id)`, so re-sent spans update in place. A `traces` table holds per-trace aggregates.
5. **Serve.** `internal/web` renders HTML, JSON, the SSE stream, and the SQL console from the same store.

## Two capture paths

**OTLP.** Your app exports spans with an OpenTelemetry SDK. spanbox sees whatever the instrumentation recorded. See [Send OpenTelemetry traces](ingest.md).

**Proxy.** Your app sends model requests through `/proxy/<vendor>/...`. spanbox relays them unchanged and records a span per inference call, including streamed responses. No SDK needed. See [Capture with the proxy](proxy.md).

Pick one path per tool. Using both records each call twice.

## What is stored

Everything the OTLP span carried: attributes, events, links, resource, and scope are kept as JSON columns next to the flattened ones. Full-text search covers span input, output, and name through SQLite FTS5. See [Data model](data-model.md).

The proxy stores parsed request and response messages, tool names and calls, cache and thinking token counts. It does not store complete raw bodies. Vendor credentials pass through and are never stored.

## Sessions and users

A span's `session_id` comes from the proxy header `X-Spanbox-Session`, then `gen_ai.conversation.id`, `session.id`, or `langfuse.session.id`; `user_id` comes from the matching user attributes. Trace filters and session grouping use the earliest non-empty session on the trace's spans.

## Where reads come from

Lists (`/`, `/sessions`, `/search`) page with opaque cursors. The dashboard reads a covering index and never touches span bodies. Search selects a page of matches from the FTS index before reading bodies. This keeps reads fast while ingest continues.

## Single writer

SQLite runs in WAL mode with one spanbox process per database file. Never run two instances against the same file. See [Deploy and back up](deployment.md).

## Retention

A background worker deletes complete traces older than `RETENTION_DAYS` (default 30) in small batches, so ingest and search keep working. See [Retention and upgrades](retention.md).
