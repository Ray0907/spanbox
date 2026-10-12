# Run SQL Queries

`/sql` is a read-only SQL console over the spanbox database.

## Availability

- Only when `AUTH_TOKEN` is set.
- Only for an authenticated UI session. Bearer auth alone does not grant access.

## Limits

| Limit | Value |
|---|---|
| Statements | One, starting with `SELECT`, `WITH`, or `EXPLAIN` |
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

## Trust

SQLite's Go driver exposes no engine-level authorizer, so the guard is a tokenizer, not a sandbox. The console is for trusted operators holding the token, not untrusted users.
