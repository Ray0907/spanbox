# HTTP Routes

When `AUTH_TOKEN` is unset, every route needs no authentication except `/sql`, which is disabled. When it is set:

| Route | Authentication and behavior |
|---|---|
| `GET /healthz`, `GET /api/public/health` | Public health checks |
| `/login`, `/static/` | Public. Login serves the app shell; valid `POST /login` sets a session cookie, an invalid token returns 401 with the shell. UI assets live under `/static/ui/`; retired static URLs return 404 |
| `POST /`, `POST /v1/traces`, `POST /v1/logs`, `POST /v1/metrics` | `Authorization: Bearer <AUTH_TOKEN>`. Metrics are accepted and discarded |
| `POST /api/public/otel/v1/traces` | Bearer auth, or Basic auth with `AUTH_TOKEN` as the password (username ignored) |
| `GET /`, `/sessions`, `/search`, `/traces/`, `/spans/` | App shell needs a login cookie, otherwise redirects to `/login`. Data requests with `?format=json` also accept Bearer and return JSON 401 when unauthorized |
| `GET /traces`, `/ui`, `/dashboard` | App shell; login cookie, otherwise redirect to `/login`. `/traces` and `/ui` show the trace list |
| `/dashboard/data` | Bearer or cookie; JSON 401 otherwise |
| `/events` | Bearer or cookie; JSON 401 otherwise. See [Watch live events](events.md) |
| `/sql` | App shell requires a login cookie. JSON (`?format=json` or `Accept: application/json`) also accepts Bearer. Non-JSON POST returns the shell, not result markup; form/query validation still applies |
| `GET /export` | Bearer or cookie; 401 otherwise |
| `POST /import` | Bearer or cookie; 401 otherwise |
| `/proxy/` | `X-Spanbox-Token: <AUTH_TOKEN>` or `/proxy/t/<token>/<vendor>/...` |

UI pages are app shells, not server-rendered result pages. The UI fetches data from the JSON routes above and `/dashboard/data`; their authentication rules still apply.

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
