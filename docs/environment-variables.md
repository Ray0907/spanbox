# Environment Variables

All configuration is optional.

| Variable | Default | Description |
|---|---:|---|
| `PORT` | `4318` | HTTP listen port on all interfaces. Integer, with no range check |
| `DATA_DIR` | `./data` | Directory containing `spanbox.db` |
| `RETENTION_DAYS` | `30` | Delete complete traces older than this. Integer 0–106751; `0` disables retention |
| `PURGE_BATCH_SIZE` | `10` | Complete traces per retention delete transaction. Integer 1–200 |
| `FTS_MERGE_EVERY` | `256` | Delete batches per explicit FTS merge step. Integer 1–1024; automatic merges stay on |
| `AUTH_TOKEN` | empty | Bearer token for ingest and login token for the UI |
| `PRICING_FILE` | empty | Replacement LiteLLM model pricing JSON file |
| `ANTHROPIC_UPSTREAM` | `https://api.anthropic.com` | Anthropic proxy upstream |
| `OPENAI_UPSTREAM` | `https://api.openai.com` | OpenAI proxy upstream |
| `OPENAI_COMPAT_UPSTREAMS` | empty | Named OpenAI-compatible upstreams as `name=url,name2=url2` |
| `CHATGPT_UPSTREAM` | `https://chatgpt.com/backend-api` | ChatGPT-authenticated Codex proxy upstream |
| `GEMINI_UPSTREAM` | `https://generativelanguage.googleapis.com` | Gemini proxy upstream |

The upstream defaults are applied by the proxy. `config.FromEnv` leaves unset upstream overrides empty.

## Related

- [Configure spanbox](configuration.md)
- [Retention and upgrades](retention.md) for `RETENTION_DAYS`, `PURGE_BATCH_SIZE`, `FTS_MERGE_EVERY`.
- [Capture with the proxy](proxy.md) for the upstream variables.
