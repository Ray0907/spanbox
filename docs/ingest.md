# Send OpenTelemetry Traces

spanbox accepts OTLP/HTTP, protobuf or JSON, on the same port as the UI.

| Route | Notes |
|---|---|
| `POST /v1/traces` | Standard OTLP traces |
| `POST /v1/logs` | OTel GenAI log events, adapted into `llm` spans |
| `POST /v1/metrics` | Accepted and discarded |
| `POST /` | Detects JSON signals; protobuf is treated as traces, so send protobuf logs to `/v1/logs` |
| `POST /api/public/otel/v1/traces` | Langfuse-compatible; Bearer auth, or Basic auth with `AUTH_TOKEN` as the password |

With `AUTH_TOKEN` set, send `Authorization: Bearer <token>`.

## Python OpenTelemetry

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>"
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=SPAN_AND_EVENT
```

The official `opentelemetry-instrumentation-google-genai` requires `SPAN_AND_EVENT`. It rejects `true` and falls back to capturing no message content. Other GenAI instrumentations commonly accept `true`.

If the SDK logs that its queue is full, raise `max_queue_size` on `BatchSpanProcessor`. The default of 2048 drops spans under bursts before they reach spanbox.

## Gemini CLI

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

Gemini CLI reports model calls as GenAI log events. spanbox turns each into an `llm` span and groups calls into one trace per CLI session.

Gemini CLI has no `otlpHeaders` setting, but honours the standard environment variable:

```sh
OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>" gemini
```

## Langfuse Python SDK (v3 and v4)

Point an existing Langfuse app at spanbox with the standard variables. The public key is accepted but unused; the secret key must equal `AUTH_TOKEN`.

```sh
export LANGFUSE_HOST=http://localhost:4318
export LANGFUSE_SECRET_KEY=<AUTH_TOKEN>
export LANGFUSE_PUBLIC_KEY=anything
```

`generation`, `agent`, and `tool` observations, `usage_details` (including `input_cached_tokens`), `cost_details`, and `propagate_attributes(session_id=..., user_id=...)` map to spanbox columns. For explicit exporter control:

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

## Vercel AI SDK

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

Enable telemetry on AI SDK calls as documented for your SDK version.

## Verify end to end

`examples/otel-python/emit.py` drives the real OpenTelemetry Python SDK against a running spanbox and produces agent, chat, and tool spans with token usage and cache reads:

```sh
SPANBOX_URL=http://localhost:4318 AUTH_TOKEN=<token> python examples/otel-python/emit.py 100
```

On a laptop, 9,000 spans sent in one burst land in SQLite in under two seconds.

## Next

- [Capture with the proxy](proxy.md) when you cannot add instrumentation.
- [Data model](data-model.md) for what each span becomes.
