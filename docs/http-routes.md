# HTTP Routes

When `AUTH_TOKEN` is unset, every route needs no authentication except `/sql`, which is disabled. When it is set:

| Route | Authentication and behavior |
|---|---|
| `GET /healthz`, `GET /api/public/health` | Public health checks |
| `/login`, `/static/` | Public. `POST /login` exchanges the token for a session cookie |
| `POST /`, `POST /v1/traces`, `POST /v1/logs`, `POST /v1/metrics` | `Authorization: Bearer <AUTH_TOKEN>`. Metrics are accepted and discarded |
| `POST /api/public/otel/v1/traces` | Bearer auth, or Basic auth with `AUTH_TOKEN` as the password (username ignored) |
| `GET /`, `/sessions`, `/search`, `/traces/`, `/spans/` | HTML needs a login cookie, otherwise redirects to `/login`. `?format=json` also accepts Bearer and returns JSON 401 |
| `/dashboard` | Login cookie, otherwise redirect to `/login` |
| `/dashboard/data` | Bearer or cookie; JSON 401 otherwise |
| `/events` | Bearer or cookie; JSON 401 otherwise. See [Watch live events](events.md) |
| `/sql` | Login cookie only. Bearer alone does not grant access |
| `GET /export` | Bearer or cookie; 401 otherwise |
| `POST /import` | Bearer or cookie; 401 otherwise |
| `/proxy/` | `X-Spanbox-Token: <AUTH_TOKEN>` or `/proxy/t/<token>/<vendor>/...` |

## Proxy paths

- `/proxy/anthropic/...`
- `/proxy/openai/...`
- `/proxy/chatgpt/codex/responses` (the only ChatGPT path)
- `/proxy/gemini/...`
- `/proxy/openai-compat/<name>/...`

Vendor credentials pass through unchanged. spanbox headers are stripped before forwarding. See [Capture with the proxy](proxy.md).

## Related

- [Query the JSON API](json-api.md) for parameters and response shapes.
- [Move data between instances](export-import.md) for `/export` and `/import`.
