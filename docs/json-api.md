# Query the JSON API

UI pages serve the Astryx app shell. Add `?format=json` to the trace list (`/`), session (list or detail), search, trace detail, span, and SQL routes to get data instead of the shell. `/dashboard/data` is JSON without a format parameter. The SQL console also accepts `Accept: application/json`.

With `AUTH_TOKEN` set, send `Authorization: Bearer <token>` or use a login cookie. The dashboard app shell requires a login cookie.

## Routes

| Route | Returns |
|---|---|
| `/?format=json` | Traces. Filters: `range`, `errors=1`, `service`, `model`, `session`, `user`, `min_duration`. See [Web UI](web-ui.md#filter-traces). A custom `to` must be after `from` |
| `/sessions?format=json` | Sessions. `range` and custom `from`/`to` as above |
| `/sessions?id={session_id}&format=json` | Session detail: `session_id`, `traces`, `totals`, `truncated`; unknown ID returns JSON 404 |
| `/dashboard/data?range=24h` | Overview with PascalCase keys: `TotalCost`, `TotalInput`, `TotalOutput`, `TraceCount`, `ErrorRate`, `Days`, `DailyCost`, `DailyP50`, `DailyP95`, `Models` |
| `/search?format=json&q=...` | Full-text hits with `trace_id`, `span_id`, and a snippet |
| `/traces/{trace_id}?format=json` | Trace summary and flat span list with tokens, cost, status, `input_chars`, `output_chars`; no bodies |
| `/spans/{trace_id}/{span_id}?format=json` | Span metadata and the first 2000 characters of `input`, `output`, `attributes`, `events`, `links`, `resource`, and `scope` |
| `GET /sql?format=json` | Schema: `{"tables":[{"name":"traces","columns":["trace_id TEXT", "..."]}, ...]}`; requires `AUTH_TOKEN` to be set |
| `POST /sql?format=json` | Form-encoded `sql` query: `columns`, `rows`, `truncated`, `error`. See [SQL console](sql-console.md#json-api) |

## Paging

Trace, session, and search lists take `limit` (positive integer, default 20, maximum 50). They return `items` and `next_cursor`. Pass the cursor back with the other parameters unchanged until it is `null`.

| List | Cursor shape |
|---|---|
| Traces | `<start_ns>:<trace_id>` |
| Sessions | `<last_ns>:<session_id>` |
| Search | `<start_ns>:<span_id>` |

### Search order

`sort=recent` (default) returns newest first and pages with cursors. `sort=relevance` returns only the top `limit` hits ranked by bm25 over all matches. Equal scores sort by write order. There is no next page, and a `cursor` with `sort=relevance` returns HTTP 400.

## Session detail

URL-encode the session ID. Detail supplies the session UI's data, ordering, totals, and truncation, not list pagination:

```json
{
  "session_id": "session-1",
  "traces": [],
  "totals": {
    "turns": 0,
    "traces": 0,
    "input_tokens": null,
    "output_tokens": null,
    "cost_usd": null
  },
  "truncated": false
}
```

The shape above illustrates the keys; an unknown or empty session returns 404 instead of an empty detail. Each `traces` entry has the existing trace summary fields (`trace_id`, `name`, `service`, `models`, `session_id`, `user_id`, `start_ns`, `end_ns`, `duration_ms`, `span_count`, `llm_count`, `input_tokens`, `output_tokens`, `cost_usd`, `has_error`) plus:

- `has_root`: whether the trace has a parentless agent span.
- `number`: one-based turn number for traces with an agent root; otherwise 0.
- `prompt`, `output`: root-span text, clipped to 600 Unicode characters plus `…` when cut.

Traces are oldest first (trace ID breaks timestamp ties). At most 200 traces are returned; `truncated: true` means more exist. Totals cover only the returned traces; `turns` counts agent-root traces, while `traces` counts all returned traces. A token/cost total is `null` if any returned trace lacks that value. Missing agent roots are included, not discarded.

## Reading long span fields

Span fields are windows with `text`, `total_chars`, and `next_offset`. Read further with `field=input|output|attributes|events|links|resource|scope&offset=N&len=L`. `resource` contains resource attributes, and `scope` contains instrumentation scope data. These windows contain stored JSON text (except input/output, which may also be plain text), not decoded JSON objects.

- Omit `field` to get all seven fields. Existing metadata and the `input`, `output`, and `attributes` window shapes are unchanged.
- `offset` is a non-negative integer (default 0).
- `len` is positive (default 2000, clamped to 20000).
- An offset at or past the end returns empty text and `next_offset: null`.
- Offsets count Unicode code points, so valid UTF-8 windows reassemble byte for byte.
- Invalid stored UTF-8 is replaced with U+FFFD and flagged `"invalid_utf8": true`.

## Errors

| Case | Response |
|---|---|
| Unauthorized | `401` `{"error": "unauthorized"}` |
| Validation or not found | `400` or `404` `{"error": "..."}` |
| Store failure | `500` `{"error": "..."}` |

`/dashboard/data` returns plain-text handler errors for 400 and 500. SQL query failures return HTTP 400 with the four-field result envelope and a nonempty `error`; SQL request-validation errors use `{"error":"..."}`. SQL returns JSON 404 when `AUTH_TOKEN` is unset, even in JSON mode. See [SQL console](sql-console.md#json-api).

## Examples

```sh
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  'http://localhost:4318/?format=json&errors=1&range=24h&limit=10'

curl -H "Authorization: Bearer $AUTH_TOKEN" \
  'http://localhost:4318/spans/<trace_id>/<span_id>?format=json&field=output&offset=2000&len=4000'
```

To let a coding agent use this API without flooding its context, see [Give an agent access](agent-skill.md).
