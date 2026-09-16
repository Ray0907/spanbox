# OTLP logs GenAI events result

## Changed

- Added OTLP/HTTP ingest for `/v1/logs`, `/v1/metrics`, and signal-aware `POST /`; `GET /` remains the UI and protobuf sent to `/` remains trace-only.
- Added a generic logs adapter for `gen_ai.client.inference.operation.details`. It pairs request/response records, synthesizes deterministic trace/span IDs, preserves resource and scope data, marks `spanbox.source=otlp-logs`, and sends spans through existing normalization, pricing, and batch storage.
- Added resource `session.id` normalization fallback and the `gcp.gen_ai` pricing alias.
- Metrics now return an empty successful OTLP response, increment an in-process counter, and emit `metrics received and discarded` once per process.
- Added the sanitized Gemini CLI fixture plus decoder, normalization, pricing, routing, authentication, storage, and rendered trace coverage.
- Documented Gemini CLI setup and corrected the Python Google GenAI content-capture value to `SPAN_AND_EVENT`.

## Gemini CLI Bearer authentication

Checked the installed Gemini CLI 0.26.0 settings schema, config type, and `@google/gemini-cli-core/dist/src/telemetry/sdk.js`. There is no `otlpHeaders`-equivalent setting, and the HTTP trace, log, and metric exporters are constructed with only `url`. Correction after a live test: the exporters honour `OTEL_EXPORTER_OTLP_HEADERS`, so `OTEL_EXPORTER_OTLP_HEADERS="Authorization=Bearer <token>" gemini` authenticates against spanbox with `AUTH_TOKEN` set (verified 2026-09-16, Gemini CLI 0.26.0: 401 without the variable, spans stored with it).

## Skipped

Nothing from the spec. By design, non-GenAI log events are ignored, metrics are not decoded or stored, and protobuf on `POST /` is treated as traces.
