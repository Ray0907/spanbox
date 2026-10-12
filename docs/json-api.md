# Query the JSON API

Add `?format=json` to the trace, session, search, trace detail, and span pages to get JSON instead of HTML. `/dashboard/data` is JSON without a format parameter.

With `AUTH_TOKEN` set, send `Authorization: Bearer <token>` or use a login cookie. The HTML dashboard still requires a login cookie.

## Routes

| Route | Returns |
|---|---|
| `/?format=json` | Traces. Filters: `range`, `errors=1`, `service`, `model`, `session`, `user`, `min_duration`. See [Web UI](web-ui.md#filter-traces). A custom `to` must be after `from` |
| `/sessions?format=json` | Sessions. `range` and custom `from`/`to` as above |
| `/dashboard/data?range=24h` | Overview with PascalCase keys: `TotalCost`, `TotalInput`, `TotalOutput`, `TraceCount`, `ErrorRate`, `Days`, `DailyCost`, `DailyP50`, `DailyP95`, `Models` |
| `/search?format=json&q=...` | Full-text hits with `trace_id`, `span_id`, and a snippet |
| `/traces/{trace_id}?format=json` | Trace summary and flat span list with tokens, cost, status, `input_chars`, `output_chars`; no bodies |
| `/spans/{trace_id}/{span_id}?format=json` | Span metadata and the first 2000 characters of `input`, `output`, and `attributes` |

## Paging

Trace, session, and search lists take `limit` (positive integer, default 20, maximum 50). They return `items` and `next_cursor`. Pass the cursor back with the other parameters unchanged until it is `null`.

| List | Cursor shape |
|---|---|
| Traces | `<start_ns>:<trace_id>` |
| Sessions | `<last_ns>:<session_id>` |
| Search | `<start_ns>:<span_id>` |

### Search order

`sort=recent` (default) returns newest first and pages with cursors. `sort=relevance` returns only the top `limit` hits ranked by bm25 over all matches. Equal scores sort by write order. There is no next page, and a `cursor` with `sort=relevance` returns HTTP 400.

## Reading long span fields

Span fields are windows with `text`, `total_chars`, and `next_offset`. Read further with `field=input|output|attributes&offset=N&len=L`.

- Omit `field` to get all three.
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

`/dashboard/data` returns plain-text handler errors for 400 and 500.

## Examples

```sh
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  'http://localhost:4318/?format=json&errors=1&range=24h&limit=10'

curl -H "Authorization: Bearer $AUTH_TOKEN" \
  'http://localhost:4318/spans/<trace_id>/<span_id>?format=json&field=output&offset=2000&len=4000'
```

To let a coding agent use this API without flooding its context, see [Give an agent access](agent-skill.md).
