# Dashboard bearer auth + self-session skill rung

## 1. `/dashboard/data` accepts Bearer

`/dashboard/data` is already JSON but with `AUTH_TOKEN` set it redirects Bearer callers to `/login`, so agents cannot get the cost/latency/error overview.

Working tree already has a partial change (not committed):
- `internal/web/auth.go`: `agentJSON := r.URL.Path == "/dashboard/data" || agentRoute && format==json`, used for both Bearer acceptance and the JSON 401. Review it, keep or fix.
- `internal/web/agent_json_test.go` `TestAgentJSONAuth`: extended with `/dashboard/data` and two new loops (wrong Bearer returns 401 JSON on `/dashboard/data` and `/?format=json`; Bearer on HTML `/dashboard` and `/` still redirects 302). **Currently does not compile**: around line 380 a closing `}` and the next `for` are on one line (`}	for _, path := ...`). Fix it.

Failure modes to cover:
1. Bearer with correct token on `/dashboard/data` redirects or 401s.
2. Wrong or missing Bearer on `/dashboard/data` returns HTML/302 instead of 401 `{"error":"unauthorized"}`.
3. Bearer unlocks HTML pages (`/dashboard`, `/`) — must still redirect.
4. Cookie login to `/dashboard/data` (used by the dashboard UI's fetch) breaks.
5. No `AUTH_TOKEN`: `/dashboard/data` stays open.

## 2. Skill: inspect your own session

`skills/spanbox/SKILL.md`: add a rung/section for an agent inspecting its own recent model calls. The proxy tags traces via `X-Spanbox-Session: $HERDR_PANE_ID` (README "Capture without instrumentation"), and `/?format=json&session=<id>` filters by it. Also add an overview rung using `/dashboard/data?range=24h` (keys are PascalCase: `TotalCost`, `TotalInput`, `TotalOutput`, `TraceCount`, `ErrorRate`, `Days`, `DailyCost`, `DailyP50`, `DailyP95`, `Models`, ...; verify against `store.DashboardData`). Keep SKILL.md concise; no new rules beyond these rungs.

Extend `skills/spanbox/e2e.sh`: seed at least one trace with a session id (check how OTLP maps session: `session.id` / `gen_ai.conversation.id` etc. in `internal/normalize`), assert the session filter returns exactly that trace, and assert the dashboard rung returns `TraceCount` equal to seeded count through the skill's `q()`. Must pass under both `bash` and `zsh`; regenerate `skills/spanbox/e2e-result.md` with bash.

Update README (JSON API / Agent skill sections) and CHANGELOG (new `## Unreleased` above 0.4.0) to match.

## Verification

`go vet ./... && go test -race -count=1 ./...`, `skills/spanbox/e2e.sh bash`, `skills/spanbox/e2e.sh zsh` all pass. Do not commit.
