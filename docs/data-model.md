# Data Model

spanbox keeps span/trace tables, proxy correlation/turn tables, and one full-text index in `spanbox.db` (schema v4). Query them with the [SQL console](sql-console.md).

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

One row per trace, aggregated from its spans. Session-aware proxy capture uses one trace per user turn, rooted at an `invoke_agent` agent span, with model children and tools beneath their issuing model. Root spans carry no tokens or cost; totals sum only `llm`/`embedding` spans. Sessions group multiple turns. Capture without a session has no agent root, and existing session-level traces are not rewritten.

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

## `proxy_tool_calls`

Internal proxy correlation (schema v3). `key` identifies the route/session/call; `trace_id`, `span_id`, `end_ns`, `first_result_ns`, `has_result`, `name` and JSON `arguments` preserve the issuing model and first-result timing. `created_ns` controls retention.

## `proxy_turns`

Internal proxy turn state (schema v4). `key` encodes route, scoped session, anchor index and anchor JSON hash. `route` and `scope_session` identify the scope; indexed `start_ns` selects its latest turn when a continuation omits the anchor. `end_ns` and JSON `root` persist the merged root span (including deterministic IDs, input/output and error status). `created_ns` controls retention. Root/turn updates, model/tool spans and tool correlation share a transaction. Sweeps expire these records alongside correlation records; `RETENTION_DAYS=0` disables sweeping.

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
