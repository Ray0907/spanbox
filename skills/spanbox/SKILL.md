---
name: spanbox
description: Query LLM traces stored in a spanbox server (cost, latency, errors, prompts, completions, tool calls). Use when debugging an LLM app or agent run, answering "why did this trace fail / cost so much / say X", or searching past prompts. Reads coarse-to-fine with cursors so trace bodies never flood context.
---

# spanbox

Read traces over HTTP with `format=json`. Prompts and completions can be tens of thousands of tokens; this skill exists to read only the slice that answers the question.

## Setup

```sh
q() { curl -sS ${SPANBOX_TOKEN:+-H} ${SPANBOX_TOKEN:+"Authorization: Bearer $SPANBOX_TOKEN"} "${SPANBOX_URL:-http://localhost:4318}$1"; }
```

`q '/?format=json&limit=1'` returns `{"items":[...],"next_cursor":...}` when ready. HTML or `/login` redirect means `format=json` is missing; `{"error":"unauthorized"}` means `SPANBOX_TOKEN` is wrong or unset.

## Ladder

Stop at the first rung that answers the question.

1. **Overview** — cost, tokens, trace count, errors, daily latency and model breakdown for the last 24 hours.
   `q '/dashboard/data?range=24h'`
   Keys are PascalCase: `TotalCost`, `TotalInput`, `TotalOutput`, `TraceCount`, `ErrorRate`, `Days`, `DailyCost`, `DailyP50`, `DailyP95`, `Models`.
2. **Your own session** — tag proxy calls with `X-Spanbox-Session: $HERDR_PANE_ID`, then inspect that pane's traces. A non-empty header sets the captured span's `session_id` and appears on the sessions page. It takes precedence over the vendor's conversation ID, which remains stored as `gen_ai.conversation.id`; without the header, session assignment is unchanged. Keep the tag consistent within a vendor conversation: trace filters and session grouping still use the earliest non-empty span session.
   `q "/?format=json&session=$HERDR_PANE_ID"`
3. **Find traces** — narrow with filters before paging.
   `q '/?format=json&limit=10&range=24h&errors=1'`
   Filters: `range` (`15m` `1h` `24h` `7d`, or `custom` with RFC3339 `from`/`to`), `errors=1`, `service`, `model`, `session`, `user`, `min_duration` (ms).
   Each item has `trace_id`, `name`, `duration_ms`, `cost_usd`, `input_tokens`, `output_tokens`, `has_error`, `span_count`.
4. **Or search content** — FTS over span input, output and name.
   `q '/search?format=json&limit=10&q=rate%20limit'`
   Items carry `trace_id`, `span_id` and a short `snippet`. The snippet often is the answer.
5. **Trace outline** — span tree, no bodies.
   `q '/traces/<trace_id>?format=json'`
   Per span: `span_id`, `parent_span_id`, `name`, `kind` (`llm` `tool` `agent` `retrieval` `embedding` `other`), `model`, `start_offset_ms`, `duration_ms`, tokens, `cost_usd`, `status_code` (2 = error), `input_chars`, `output_chars`. Pick the span from this; use `*_chars` to judge how much reading it costs.
6. **Span head** — metadata plus the first 2000 chars of `input`, `output`, `attributes`.
   `q '/spans/<trace_id>/<span_id>?format=json'`
   Each field is `{"text", "total_chars", "next_offset"}`. `next_offset: null` means you have it all.
7. **Page one field** — only if the head did not answer.
   `q '/spans/<trace_id>/<span_id>?format=json&field=output&offset=2000&len=4000'`
   `len` max 20000. Follow `next_offset`; stop as soon as you have the answer. Output is usually the end of the story — read `output` before a long `input`.

## Rules

- Lists default to 20 items, max 50 (`limit`). Use `next_cursor` only when the current page did not answer; pass it back as `cursor=` with every other parameter unchanged.
- Never fetch a whole span "to be thorough". Grep a field window with `jq -r .input.text | grep -n ...` rather than reading it all.
- `invalid_utf8: true` on a field means stored bytes were not valid UTF-8; the text shown is lossy.
- Report findings as `trace_id/span_id` plus a one-line conclusion, not pasted bodies.
- Investigation needs more than ~5 span reads: hand it to a subagent and take back only the conclusion.
