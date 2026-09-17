# spanbox

spanbox is a single-binary OpenTelemetry trace monitor for LLM applications. It accepts OTLP/HTTP protobuf or JSON on port 4318, stores complete span data in embedded SQLite, and serves trace, token, cost, latency, search, and read-only SQL views from the same port—without Postgres, ClickHouse, Redis, or object storage.

![Trace detail: span tree with prompt and completion](docs/screenshots/trace.png)

<details>
<summary>More screenshots: trace list and dashboard</summary>

![Trace list](docs/screenshots/traces.png)
![Dashboard: cost, tokens, latency percentiles, error rate by day](docs/screenshots/dashboard.png)

</details>

## Install

Download a binary from the [releases page](https://github.com/Ray0907/spanbox/releases), or use the container image below. There are no required environment variables; run `./spanbox` and open http://localhost:4318.

## Run with Docker

Generate a token with at least 32 cryptographically random bytes, then start spanbox:

```sh
docker run -p 4318:4318 -v ./data:/data -e AUTH_TOKEN=$(openssl rand -hex 32) ghcr.io/ray0907/spanbox
```

For a bind mount, create the directory first and restrict it to the container user (for example, `mkdir -m 700 data`). spanbox warns when an existing data directory is accessible by group or other users. The database file is created with mode `0600`.

## Configuration

All configuration is optional.

| Environment variable | Default | Description |
|---|---:|---|
| `PORT` | `4318` | HTTP listen port on all interfaces |
| `DATA_DIR` | `./data` | Directory containing `spanbox.db` |
| `RETENTION_DAYS` | `30` | Delete complete traces older than this; `0` disables retention |
| `AUTH_TOKEN` | empty | Bearer token for ingest and login token for the UI |
| `PRICING_FILE` | empty | Replacement LiteLLM model pricing JSON file |
| `ANTHROPIC_UPSTREAM` | `https://api.anthropic.com` | Anthropic proxy upstream override |
| `OPENAI_UPSTREAM` | `https://api.openai.com` | OpenAI proxy upstream override |
| `OPENAI_COMPAT_UPSTREAMS` | empty | Named OpenAI-compatible upstreams as `name=url,name2=url2` |
| `CHATGPT_UPSTREAM` | `https://chatgpt.com/backend-api` | ChatGPT-authenticated Codex proxy upstream override |
| `GEMINI_UPSTREAM` | `https://generativelanguage.googleapis.com` | Gemini proxy upstream override |

Without `AUTH_TOKEN`, ingest and the UI are open and the SQL console is disabled.

## Durability with Litestream

[Litestream](https://litestream.io/) continuously replicates `spanbox.db` with an RPO of approximately its 1-second sync interval, restores the database onto a new machine, and requires no spanbox changes. Install Litestream 0.5.x on macOS with `brew install benbjohnson/litestream/litestream`, then copy [`deploy/litestream/.env.example`](deploy/litestream/.env.example) to `deploy/litestream/.env` and use the [Docker Compose recipe](deploy/litestream/docker-compose.yml). On a fresh volume, restore before starting spanbox:

```sh
cd deploy/litestream
docker compose run --rm litestream restore -if-replica-exists -o /data/spanbox.db /data/spanbox.db
```

Keep a single writer: never run two spanbox instances against the same restored database file. Restore before spanbox starts because spanbox creates an empty database otherwise, which Litestream could replicate over the good replica; `-if-replica-exists` makes first deployment safe when no backup exists, and automated deployments should enforce restore-before-spanbox with an init container and `depends_on` ordering.

## Capture without instrumentation (proxy)

Point an application's vendor base URL at spanbox to capture prompts, tools, responses, usage, cache tokens, cost, and latency without adding an SDK:

```sh
export ANTHROPIC_BASE_URL=http://localhost:4318/proxy/anthropic
export GOOGLE_GEMINI_BASE_URL=http://localhost:4318/proxy/gemini
export OPENAI_BASE_URL=http://localhost:4318/proxy/openai/v1
```

When `AUTH_TOKEN` is set, Claude Code can send it as a custom header. Add the Herdr pane ID in the same shell setting to tag every trace without replacing Claude's conversation ID:

```sh
export ANTHROPIC_CUSTOM_HEADERS="X-Spanbox-Token: <token>,X-Spanbox-Session: $HERDR_PANE_ID"
```

Without authentication, use only `export ANTHROPIC_CUSTOM_HEADERS="X-Spanbox-Session: $HERDR_PANE_ID"` in the shell rc. Both spanbox headers are removed before the request is sent upstream.

Gemini CLI has no custom-header hook, so put the spanbox token in its proxy path instead:

```sh
export GOOGLE_GEMINI_BASE_URL=http://localhost:4318/proxy/t/<token>/gemini
```

Vendor credentials pass through unchanged and are never stored. Run the proxy on loopback or set `AUTH_TOKEN`; because spanbox is in the request path, restarting it interrupts in-flight model calls. Custom gateways and regional endpoints can be selected with `ANTHROPIC_UPSTREAM`, `OPENAI_UPSTREAM`, `CHATGPT_UPSTREAM`, and `GEMINI_UPSTREAM`.

ChatGPT-authenticated Codex uses the `openai-codex` provider. Route it through spanbox without changing its OAuth login by merging this provider override into `~/.pi/agent/models.json`:

```json
{
  "providers": {
    "openai-codex": {
      "baseUrl": "http://localhost:4318/proxy/chatgpt"
    }
  }
}
```

Codex API-key mode can instead use `OPENAI_BASE_URL=http://localhost:4318/proxy/openai/v1`.

To capture multiple OpenAI-compatible services at once, register lowercase names containing only letters, digits, and hyphens, then route each client through its name:

```sh
export OPENAI_COMPAT_UPSTREAMS="vllm=http://localhost:18080/v1,local-ai=http://localhost:8080/v1"
export OPENAI_BASE_URL=http://localhost:4318/proxy/openai-compat/vllm
```

Named routes reuse OpenAI inference parsing and have the form `/proxy/openai-compat/<name>/...`; `server.address` records that named upstream's host.

Pick one capture path per tool. If Gemini CLI telemetry (`~/.gemini/settings.json`) also points at spanbox while `GOOGLE_GEMINI_BASE_URL` goes through the proxy, every model call is recorded twice, once from the log event and once from the proxy. The proxy sees more (full request and response, cache and thinking tokens), so disable the CLI telemetry when you use it.

## Export traces

### Python OpenTelemetry

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>"
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=SPAN_AND_EVENT
```

Use these variables with the standard OTLP HTTP exporter and your OpenTelemetry instrumentation. The official `opentelemetry-instrumentation-google-genai` requires `SPAN_AND_EVENT`; it rejects `true` and falls back to capturing no message content. Other GenAI instrumentations commonly accept `true` instead.

### Gemini CLI

```json
// ~/.gemini/settings.json
{
  "telemetry": {
    "enabled": true,
    "target": "local",
    "otlpEndpoint": "http://localhost:4318",
    "otlpProtocol": "http",
    "logPrompts": true,
    "useCollector": false
  }
}
```

Gemini CLI reports model calls as OpenTelemetry GenAI log events. spanbox turns each call into an `llm` span and groups calls into one trace per CLI session; metrics are accepted and discarded. `POST /` detects JSON signals, but treats protobuf as traces; send protobuf logs to `/v1/logs`.

Gemini CLI has no `otlpHeaders` setting, but its exporters honour the standard OpenTelemetry environment variable, so with `AUTH_TOKEN` set start the CLI like this (verified with Gemini CLI 0.26.0):

```sh
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>" gemini
```

### Langfuse Python SDK (v3 and v4)

Point an existing Langfuse app at spanbox with only the standard Langfuse environment variables. The public key is accepted but not used; the secret key must equal `AUTH_TOKEN`.

```sh
export LANGFUSE_HOST=http://localhost:4318
export LANGFUSE_SECRET_KEY=<AUTH_TOKEN>
export LANGFUSE_PUBLIC_KEY=anything
```

Verified with langfuse 4.15.3: `generation`, `agent`, and `tool` observations, `usage_details` (including `input_cached_tokens`), `cost_details`, and `propagate_attributes(session_id=..., user_id=...)` all map to spanbox columns. A custom exporter remains available when an application needs explicit exporter control:

```python
from langfuse import Langfuse
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

langfuse = Langfuse(
    span_exporter=OTLPSpanExporter(
        endpoint="http://localhost:4318/v1/traces",
        headers={"Authorization": "Bearer <token>"},
    )
)
```

### Vercel AI SDK

Register AI SDK telemetry, then send it through the standard OTLP HTTP exporter:

```ts
import { registerTelemetry } from "ai";
import { OpenTelemetry } from "@ai-sdk/otel";
import { NodeSDK } from "@opentelemetry/sdk-node";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-http";

registerTelemetry(new OpenTelemetry());

const sdk = new NodeSDK({
  traceExporter: new OTLPTraceExporter({
    url: "http://localhost:4318/v1/traces",
    headers: { Authorization: "Bearer <token>" },
  }),
});
sdk.start();
```

Enable telemetry on AI SDK calls as documented for the SDK version you use.

### Verified end to end

`examples/otel-python/emit.py` drives the real OpenTelemetry Python SDK (OTLP protobuf, gzip, batch export) against a running spanbox and produces agent, chat, and tool spans with token usage and cache reads:

```sh
pip install opentelemetry-sdk opentelemetry-exporter-otlp-proto-http
SPANBOX_URL=http://localhost:4318 AUTH_TOKEN=<token> python examples/otel-python/emit.py 100
```

On a laptop, 9,000 spans sent in one burst land in SQLite in under two seconds. If the SDK logs that its queue is full, raise `max_queue_size` on `BatchSpanProcessor`; the default of 2048 drops spans under bursts before they reach spanbox.

## SQL console security

`/sql` is available only when `AUTH_TOKEN` is set and only to an authenticated UI session. Queries run through a read-only SQLite connection with `query_only`, a tokenizer guard, a 5-second timeout, and strict result limits. SQLite's Go driver does not expose an engine-level authorizer, so this console is for trusted operators holding the token—not untrusted users.

## Update model pricing

The embedded pricing table is LiteLLM's `model_prices_and_context_window.json`:

```sh
curl -o internal/pricing/model_prices.json https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
```

Rebuild spanbox after updating the file.

## Build from source

```sh
go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" ./cmd/spanbox
```

The project builds with `CGO_ENABLED=0`.
