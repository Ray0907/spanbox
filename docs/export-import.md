# Move Data Between Instances

Export and import use NDJSON, one span per line. Both need Bearer auth or a login cookie when `AUTH_TOKEN` is set; otherwise they return 401.

## Export

```sh
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  'http://localhost:4318/export?range=7d&model=claude-sonnet' > spans.ndjson
```

`GET /export` streams `application/x-ndjson` and takes the trace-list filters (`range`, `model`, `errors`, and so on). Filters select spans, not whole traces, so a filtered export can contain part of a trace.

## Import

```sh
curl -H "Authorization: Bearer $AUTH_TOKEN" -H 'Content-Type: application/x-ndjson' \
  --data-binary @spans.ndjson http://localhost:4318/import
```

`POST /import` returns the imported count. Import streams with bounded lines and batches rather than a fixed body limit, and pauses retention while it runs. Spans are unique on `(trace_id, span_id)`, so re-importing updates rather than duplicates.

Imports do not broadcast on [`/events`](events.md).

## Round trip

Invalid UTF-8 survives export and import byte for byte. `scripts/roundtrip-e2e.sh` exports from one fresh instance and imports into a second to prove it.

## Handle with care

Exports contain prompts and completions. See [Run spanbox safely](security.md).
