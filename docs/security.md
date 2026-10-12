# Run spanbox Safely

spanbox stores prompts and completions. Treat the database as sensitive.

## Authentication

Without `AUTH_TOKEN`, ingest and the UI are open and the SQL console is disabled. Run that mode on loopback only.

For anything reachable by others, set `AUTH_TOKEN` to at least 32 cryptographically random bytes:

```sh
export AUTH_TOKEN=$(openssl rand -hex 32)
```

See [HTTP routes](http-routes.md) for what each route requires.

## The proxy

- Vendor credentials pass through unchanged and are never stored.
- spanbox is in the request path. Restarting it interrupts in-flight model calls.
- Run it on loopback or set `AUTH_TOKEN`.
- `X-Spanbox-Token` and `X-Spanbox-Session` are stripped before forwarding.
- A token in the URL path (`/proxy/t/<token>/...`) can appear in client logs. Prefer the header when the client supports it.

## The SQL console

Read-only and token-gated, but guarded by a tokenizer rather than an engine-level authorizer. Give the token only to trusted operators. See [Run SQL queries](sql-console.md).

## Files

- `spanbox.db` is created with mode `0600`.
- Restrict `DATA_DIR` to the service user.
- On Windows, protect `DATA_DIR` with ACLs; POSIX permission bits do not describe Windows access.

## Backups and exports

Backups, Litestream replicas, and [NDJSON exports](export-import.md) contain the same prompts and completions as the database. Protect them equally.

## Public data

Do not put real traces in public repositories. Use invented fixtures.
