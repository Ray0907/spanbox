# Watch Live Events

`GET /events` is a read-only Server-Sent Events stream for successfully ingested OTLP spans, including adapted logs. Imports and proxy captures do not broadcast.

With `AUTH_TOKEN` set, use a Bearer token or login cookie. Unauthorized requests return JSON 401.

```sh
curl -N -H "Authorization: Bearer $AUTH_TOKEN" \
  'http://localhost:4318/events?min_duration=1000&min_cost=0.10'
```

## Event types

| Event | Fires when | Data |
|---|---|---|
| `error` | An ingested span has error status | `trace_id`, `span_id`, `status_code` |
| `slow` | A span's `duration_ms` exceeds `min_duration` | `trace_id`, `span_id`, `duration_ms` |
| `cost` | A trace's stored aggregate `cost_usd` exceeds `min_cost` | `trace_id`, `span_id`, `cost_usd` |

`cost` is emitted once per affected trace per batch, with the last ingested span's ID. Upserts do not double-count.

## Thresholds

- `min_duration` is in milliseconds; `min_cost` is in USD.
- Both must be finite, non-negative numbers.
- Omit a threshold to disable that event type. Plain `/events` emits only `error`.
- Comparisons are strict `>`. A qualifying ingest emits again even if the trace was already above the threshold.

## Delivery

- A keepalive comment is sent every 15 seconds.
- Delivery is best-effort. Each client has a 32-event buffer; when full, new events are dropped without blocking ingest.
- There is no replay or persistence. Reconnecting clients miss events sent in between.
