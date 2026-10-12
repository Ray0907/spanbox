# Give an Agent Access

`skills/spanbox/SKILL.md` teaches a coding agent (Claude Code, Codex) to read spanbox through the [JSON API](json-api.md) from coarse to fine. The agent follows `next_cursor` and `next_offset` instead of loading whole prompts into its context.

## Install

Copy the directory into the agent's skills folder. For Claude Code:

```sh
cp -r skills/spanbox ~/.claude/skills/spanbox
```

Set the environment the skill reads:

```sh
export SPANBOX_URL=http://localhost:4318
export SPANBOX_TOKEN=<AUTH_TOKEN>   # only if auth is on
```

## The ladder

The skill walks seven rungs and stops at the first one that answers the question:

1. Overview from `/dashboard/data?range=24h`.
2. The agent's own session, tagged through the proxy header `X-Spanbox-Session`.
3. Trace list with filters.
4. Full-text search.
5. Trace outline (no bodies).
6. Span head (first 2000 characters).
7. A single field window with `offset` and `len`.

## Tag the agent's own traces

Route the agent's model calls through the [proxy](proxy.md) and add `X-Spanbox-Session: $HERDR_PANE_ID`. The agent can then list `?session=$HERDR_PANE_ID` to inspect its own run.

## Verify

`skills/spanbox/e2e.sh [bash|zsh]` runs every command in the skill against a freshly built binary.
