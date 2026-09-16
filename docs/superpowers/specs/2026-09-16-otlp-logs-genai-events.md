# OTLP logs: GenAI operation events → spans

Dogfood finding (2026-09-16, Gemini CLI 0.26.0): Gemini CLI emits **no spans**. Its LLM calls arrive as OTLP **log records** carrying the standard OTel GenAI event `gen_ai.client.inference.operation.details`, plus metrics. With `otlpProtocol: http` it POSTs traces, logs and metrics all to the configured `otlpEndpoint` URL **verbatim** (no `/v1/<signal>` suffix), so with `otlpEndpoint: http://host:4318` everything lands on `POST /`. Today spanbox answers 404 and sees nothing.

Captured real payload (do not commit as-is, it contains host ids and full prompts): `/private/tmp/claude-501/-private-tmp/78ff5489-5250-44fc-ad90-eaf1a8c5bc5a/scratchpad/gem/gemini_cli_logs_raw.json`. It has 9 log records in one batch; the two that matter are the `gen_ai.client.inference.operation.details` records:

- request half: `gen_ai.operation.name=generate_content`, `gen_ai.provider.name=gcp.gen_ai`, `gen_ai.request.model`, `gen_ai.system_instructions`, `gen_ai.input.messages`, `server.address`, no usage
- response half (2.2 s later): same operation/provider/model plus `gen_ai.response.id`, `gen_ai.response.model`, `gen_ai.response.finish_reasons`, `gen_ai.output.messages`, `gen_ai.usage.input_tokens=9097`, `gen_ai.usage.output_tokens=1`
- neither record has `traceId` / `spanId`; resource has `service.name=gemini-cli` and `session.id`

This is the OTel-standard shape for clients that do not emit spans, so support it generically. Nothing Gemini-specific in code.

## Scope

1. **Routes.** Add `POST /v1/logs`, `POST /v1/metrics`, and `POST /` to the ingest handler. Same auth (Bearer when `AUTH_TOKEN` set), same media types, same gzip and size limits, same response and error encoding as `/v1/traces` (spec §6.1). `GET /` keeps serving the UI.
2. **Signal routing.**
   - `/v1/traces`: traces only, unchanged.
   - `/v1/metrics`: decode nothing, return 200 with empty response, increment a counter, log once per process `metrics received and discarded`.
   - `/v1/logs`: logs adapter below.
   - `/`: JSON → look at the top-level key (`resourceSpans` / `resourceLogs` / `resourceMetrics`) and dispatch; unknown → 400. Protobuf at `/` → treat as traces (Gemini CLI http mode sends JSON; document the limit).
3. **Logs adapter** (`internal/otlp/logs.go`, `Decode`-style function returning `[]RawSpan`). Decode `ExportLogsServiceRequest` (proto or protojson). For every log record whose attribute `event.name == "gen_ai.client.inference.operation.details"`:
   - Response records are those with any `gen_ai.usage.*` or `gen_ai.output.messages` or `gen_ai.response.*` attribute. Each response record becomes one span. A request record is merged into the next response record in the same batch that has the same resource, same `gen_ai.request.model`, and a later or equal time and is not yet paired; merged attributes = request attrs overwritten by response attrs. Unpaired request records are dropped (log at debug level with a count).
   - `StartNs` = paired request time, else the response record's `observedTimeUnixNano`/`timeUnixNano`; `EndNs` = response record time. If either is 0 use the other. `end < start` → swap.
   - `TraceID` / `SpanID`: use the record's own if present and valid. Otherwise derive deterministically: `span_id = sha256("spanbox-log-span" + resource session.id + response.id + endNs)[:8]`; `trace_id = sha256("spanbox-log-trace" + resource session.id)[:16]` when resource `session.id` (or record `gen_ai.conversation.id`, or `session.id` attribute) exists, else `sha256("spanbox-log-trace" + span_id)[:16]`. So one CLI session becomes one trace with several root spans; the trace list shows it once.
   - `Name` = `{gen_ai.operation.name} {gen_ai.request.model}`. `ParentSpanID` = "". `StatusCode` = 2 if attribute `error.type` present else 0. `Attrs` = merged attributes minus `event.name`. `Events` = empty. `Resource`, `Scope` from the record's parents. `TraceState` "". Also set attribute `spanbox.source = "otlp-logs"` so the UI and SQL can tell synthesized spans apart.
   - Records with any other `event.name` (or none) are ignored and counted.
   - Then the normal path: `normalize.Span`, pricing, `store.InsertBatch`. `session_id` must end up populated from resource `session.id` when the record has no `gen_ai.conversation.id`: extend the normalize `session_id` candidates to fall back to resource `session.id` (resource attributes are already available to normalize via `RawSpan.Resource`).
4. **Pricing alias.** Add `gcp.gen_ai` → `gemini` next to the existing `gcp.gemini` alias.
5. **Tests.**
   - `internal/otlp/testdata/gemini_cli_logs.json`: derived from the raw capture. Keep the two `gen_ai.client.inference.operation.details` records, one `gemini_cli.api_response` record (to prove other events are ignored), the scope, and a resource reduced to `service.name`, `service.version`, `session.id`. Shorten `gen_ai.input.messages` / `gen_ai.system_instructions` content to one sentence each; keep the structure (the `[{"role":"user","parts":[{"type":"text","content":...}]}]` JSON string form exactly as sent). Keep the `gen_ai.output.messages` value as sent.
   - `otlp` unit test: fixture decodes to exactly one RawSpan; trace/span ids are 32/16 hex; start is the request record time and end the response time; attrs contain `gen_ai.usage.input_tokens=9097`, `gen_ai.input.messages` (from the request half) and `gen_ai.output.messages`; second decode of the same fixture yields identical ids (determinism).
   - `web` e2e: POST the fixture to `/` (JSON), to `/v1/logs` (JSON and protobuf, build protobuf via protojson→proto), assert 200 and one llm span in the store with `input_tokens=9097`, `output_tokens=1`, `provider=gcp.gen_ai`, `cost_usd` not NULL, `session_id` equal to the fixture session id, and `GET /traces/{id}` renders the model name and the prompt text. POST a `resourceMetrics` JSON body to `/` and to `/v1/metrics` → 200. POST an unknown JSON object to `/` → 400. `AUTH_TOKEN` set: `/v1/logs` without Bearer → 401.
6. **README.** New subsection under Export traces, `### Gemini CLI`:

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

Say: Gemini CLI reports model calls as OTel GenAI log events, spanbox turns each call into an `llm` span grouped into one trace per CLI session; metrics are accepted and discarded; with `AUTH_TOKEN` set add `"otlpHeaders"`-equivalent if the CLI supports it, otherwise note that Gemini CLI http mode cannot send a Bearer header today (check `~/.gemini` docs or the CLI source under `telemetry/sdk.js` and state what you found, do not guess). Also fix the Python OpenTelemetry section: the official `opentelemetry-instrumentation-google-genai` expects `OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=SPAN_AND_EVENT` (`true` is rejected and falls back to no content); other instrumentations accept `true`. Mention both.
7. Update spec §2 non-goals (`docs/superpowers/specs/2026-09-15-spanbox-design.md`): OTLP logs are now accepted for GenAI operation events only; metrics accepted and discarded. Keep gRPC out.

## Constraints

- No new dependencies beyond the `go.opentelemetry.io/proto/otlp` logs/metrics packages already in the module.
- `go vet ./... && go test ./...` green. Conventional commits with trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. Do not push.
- When done, write `docs/superpowers/specs/2026-09-16-otlp-logs-genai-events-result.md` with what changed, what the Gemini CLI Bearer situation is, and anything skipped.
