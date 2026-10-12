# Data Model

spanbox keeps two tables and one full-text index in `spanbox.db`. Query them with the [SQL console](sql-console.md).

## `spans`

One row per span, unique on `(trace_id, span_id)`.

| Column | Type | Notes |
|---|---|---|
| `trace_id` | text | 32 characters |
| `span_id` | text | 16 characters |
| `parent_span_id` | text | Empty for a root |
| `name` | text | |
| `kind` | text | `llm`, `embedding`, `tool`, `retrieval`, `agent`, `other` |
| `service_name` | text | Default `unknown` |
| `start_ns`, `end_ns` | integer | Unix nanoseconds |
| `duration_ms` | real | |
| `status_code`, `status_message` | integer, text | `2` is error |
| `provider` | text | |
| `request_model`, `response_model` | text | |
| `input_tokens`, `output_tokens`, `cache_read_tokens` | integer | Null when unreported |
| `cost_usd` | real | Null when unknown |
| `cost_source` | text | `explicit` or `pricing` |
| `input_content`, `output_content` | text | Prompt and completion |
| `tool_name`, `tool_call_id` | text | |
| `finish_reason` | text | |
| `session_id`, `user_id` | text | |
| `trace_state` | text | |
| `attributes`, `events`, `links`, `resource`, `scope` | text (JSON) | Original OTLP data |

Indexes cover start time, kind plus day, model, session, and user.

## `traces`

One row per trace, aggregated from its spans.

| Column | Notes |
|---|---|
| `trace_id` | Primary key |
| `name`, `service_name` | |
| `start_ns`, `end_ns`, `duration_ms` | |
| `span_count`, `llm_count` | |
| `input_tokens`, `output_tokens`, `cost_usd` | Sums |
| `has_error` | 0 or 1 |
| `session_id`, `user_id` | |
| `models` | Models used |

## `spans_fts`

FTS5 index over `input_content`, `output_content`, and `name`, kept in sync by triggers. `/search` reads it.

## Example queries

Most expensive traces in the last day:

```sql
SELECT trace_id, name, cost_usd
FROM traces
WHERE start_ns > (strftime('%s','now') - 86400) * 1000000000
ORDER BY cost_usd DESC
LIMIT 10;
```

Tool call counts:

```sql
SELECT tool_name, count(*) FROM spans WHERE kind = 'tool' GROUP BY tool_name ORDER BY 2 DESC;
```
