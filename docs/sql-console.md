# Run SQL Queries

`/sql` is a read-only SQL console over the spanbox database.

## Availability

- Only when `AUTH_TOKEN` is set.
- The app shell requires an authenticated UI session (login cookie); Bearer auth alone does not grant shell access.
- JSON mode accepts either `Authorization: Bearer <token>` or the login cookie. Unauthenticated JSON requests return 401, not a login redirect.
- When `AUTH_TOKEN` is unset, both the page and JSON modes return 404; schema inspection is disabled too.

## Limits

| Limit | Value |
|---|---|
| Statements | One, starting with `SELECT`, `WITH`, or `EXPLAIN` |
| Form request body | 20 KiB |
| SQL text | 16 KiB |
| Timeout | 5 seconds |
| Rows | 1000 |
| Columns | 64 |
| Cell size | 64 KiB |
| Total column and cell text | 4 MiB (not an encoded HTTP response size) |

Queries run on a read-only SQLite connection with `query_only`. A tokenizer guard rejects forbidden mutation and maintenance keywords and dangerous function calls.

## Example

```sql
SELECT request_model, count(*) AS calls, round(sum(cost_usd), 4) AS cost
FROM spans
WHERE kind = 'llm'
GROUP BY request_model
ORDER BY cost DESC;
```

Tables and columns are listed in [Data model](data-model.md).

## JSON API

The UI loads schema and results as JSON using `?format=json`; clients can also send `Accept: application/json`. Non-JSON GET/POST responses serve the app shell, not rendered query results. POST form/query validation still applies, including HTTP 400 for rejected queries.

### Schema

`GET /sql?format=json` returns tables and columns only; it does not run queries supplied in the URL:

```json
{"tables":[{"name":"traces","columns":["trace_id TEXT","name TEXT"]}]}
```

This abbreviated example shows the shape; the response includes all columns of `traces`, `spans`, and `spans_fts`. Each column string is `name TYPE`, using SQLite's declared type.

### Query

`POST /sql` must still use `Content-Type: application/x-www-form-urlencoded` with the `sql` field, even when requesting a JSON response. JSON request bodies are not accepted (415). Both the 20 KiB form-body cap and the 16 KiB decoded SQL-text cap apply.

```sh
curl -H "Authorization: Bearer $AUTH_TOKEN" \
  -H 'Accept: application/json' \
  --data-urlencode 'sql=SELECT 42 AS answer, NULL AS missing' \
  'http://localhost:4318/sql'
```

Successful response (HTTP 200):

```json
{"columns":["answer","missing"],"rows":[["42","NULL"]],"truncated":false,"error":""}
```

Column names and cell values are strings; SQL NULL becomes the string `"NULL"`. `columns` and `rows` are arrays, including when empty. `truncated` reports row, cell, or result-text limits; all existing read-only guards, timeouts, and result limits still apply.

A bad or forbidden query returns HTTP 400, not 500, with the same four keys and a nonempty `error`, for example:

```json
{"columns":[],"rows":[],"truncated":false,"error":"sql rejected: statement must start with SELECT, WITH, or EXPLAIN"}
```

Request-validation failures return `{"error":"..."}` with 400 (invalid or oversized form) or 415 (unsupported media type). Schema inspection failures return JSON 500. With no configured `AUTH_TOKEN`, JSON requests return 404 `{"error":"SQL console disabled: set AUTH_TOKEN to enable"}` before reading schema or executing SQL.

## Trust

SQLite's Go driver exposes no engine-level authorizer, so the guard is a tokenizer, not a sandbox. The console is for trusted operators holding the token, not untrusted users.
