# spanbox

spanbox is a single-binary OpenTelemetry trace monitor for LLM applications. It accepts OTLP/HTTP on one port, stores spans in embedded SQLite, and serves trace, token, cost, latency, search, and read-only SQL views from the same port. It needs no Postgres, ClickHouse, Redis, or object storage.

Use spanbox to see what your LLM app or coding agent sent to a model, what came back, how many tokens it used, what it cost, and where the time went.

## Start using spanbox

New to spanbox? Follow the [Quickstart](quickstart.md) to run the binary and see your first trace.

If spanbox is already running, choose what you want to do:

- [Send OpenTelemetry traces](ingest.md) from Python, the Vercel AI SDK, Langfuse, or Gemini CLI.
- [Capture without instrumentation](proxy.md) by pointing a vendor base URL at spanbox.
- [Use the web UI](web-ui.md) to browse traces, sessions, search, and the dashboard.
- [Query the JSON API](json-api.md) from scripts and agents.
- [Understand how spanbox works](how-spanbox-works.md): ingest, normalization, cost, and storage.

## Automate and integrate

- [Watch live events](events.md) for errors, slow spans, and expensive traces over SSE.
- [Run SQL queries](sql-console.md) against the read-only console.
- [Give a coding agent access](agent-skill.md) with the bundled skill.
- [Move data between instances](export-import.md) with NDJSON export and import.

## Operate spanbox

- [Configure spanbox](configuration.md) and look up every [environment variable](environment-variables.md).
- [Run spanbox safely](security.md): authentication, the proxy, and the SQL console.
- [Deploy and back up](deployment.md) with Docker and Litestream.
- [Plan retention and upgrades](retention.md).

## Reference

- [HTTP routes](http-routes.md) and their authentication rules.
- [Data model](data-model.md): the `spans` and `traces` tables.
- [Update model pricing](pricing.md).

## Work safely

Without `AUTH_TOKEN`, ingest and the UI are open and the SQL console is disabled. Run spanbox on loopback or set `AUTH_TOKEN` before exposing it. See [Security](security.md).
