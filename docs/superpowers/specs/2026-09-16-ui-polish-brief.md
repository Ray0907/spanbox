# spanbox UI polish brief (seeded)

Owner request: polish the web UI using the `impeccable` skill, with a random seed steering the creative direction so the result is not generic SaaS-blue.

## Seed

`SEED = 1721190399`. Direction derived deterministically from it (do not re-roll):

| Knob | Value |
|---|---|
| Accent hue | 334 (magenta-rose). Use it for the single accent: links, primary button, selected state, LLM markers, chart primary series. Derive a complementary secondary for chart series 2 only. |
| Type | Display: **Bricolage Grotesque** (OFL). Body/UI: **Source Sans 3** (OFL). Numbers: tabular figures everywhere a number appears. |
| Surface | **ink**, dark-first: near-black canvas, one step lighter panels, luminous accent, 1px hairline borders. Light theme still shipped, derived from the same tokens and equally finished. |
| Motif | Left accent bar per span kind (llm / tool / retrieval / agent / embedding / other) replaces the pill badges in the span tree and lists. Kind text stays available (sr-only or small label) for accessibility. |
| Radius | 2px everywhere. |
| Density | Compact: 32px table rows and tree rows. |

The seed picks the world; the owner has explicitly authorized changing the visual identity. Everything else is refinement, not redesign.

## Hard constraints

- Keep `html/template` + htmx + vendored uPlot. No JS framework, no CSS framework, no build step.
- CSP is `default-src 'self'` and must stay. Fonts must be self-hosted: vendor `woff2` files (latin subset is enough) into `internal/web/static/fonts/` and declare `@font-face` with `font-display: swap` and a real system fallback stack. Add both fonts to `NOTICE`.
- Never `template.HTML` / `template.JS`, never inline `<script>` with data. All telemetry values stay text-context.
- Keep every route, query param, form field name, htmx target id, and test selector working. `go vet ./... && go test ./...` must stay green; extend `internal/web/server_test.go` only if a selector you renamed is asserted there.
- Keep `prefers-color-scheme` behaviour: dark is the designed-first theme, light is derived and must pass the same contrast checks (WCAG AA for text, 3:1 for UI controls and focus rings).
- Do not touch Go code outside `internal/web/` except `NOTICE`. Do not change copy that states facts (env var names, limits).
- Keyboard: visible focus ring on every control; span tree rows remain reachable and activatable by keyboard.
- Mobile (≈400px) must still work: no horizontal page scroll, filters stack, span tree above panel.

## Scope of surfaces

`/` traces list with filters, `/traces/{id}` tree + span panel, `/dashboard` cards + 4 uPlot charts + models table, `/search`, `/sql` (only with `AUTH_TOKEN`), `/login`, empty states of each list, error text blocks.

uPlot: restyle via its options in `internal/web/static/app.js` (series colors from the accent tokens, axis/grid colors from tokens, `size: 64` on y axes must stay) and the vendored `uPlot.min.css` untouched.

## How to run and look

```sh
go build -o /tmp/spanbox-ui ./cmd/spanbox
AUTH_TOKEN= PORT=4318 DATA_DIR=/tmp/spanbox-ui-data /tmp/spanbox-ui &
python3 examples/otel-python/populate.py        # needs opentelemetry-sdk + otlp http exporter, already installed
```

Restart the binary after every template/CSS change (assets are embedded). Screenshot desktop (1280 wide) and mobile (400 wide) for `/`, a trace page with an LLM span selected, and `/dashboard?range=7d`, in dark and light. Put them in `docs/screenshots/` replacing the three existing PNGs (`traces.png`, `trace.png`, `dashboard.png`, dark theme) and add `*-light.png` variants.

## Process

1. Run `/impeccable polish internal/web` and follow it. When it asks for quality bar: production, shipping today. When it asks about the visual world: this brief is the authority.
2. Run the impeccable detector once at the end: `node ~/.pi/agent/skills/impeccable/scripts/detect.mjs --json internal/web` and fix what it finds.
3. Commit in logical steps with conventional-commit messages and the trailer `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. Do not push.
4. Write a short summary to `docs/superpowers/specs/2026-09-16-ui-polish-result.md`: what changed, contrast numbers for text/accent on both themes, font file sizes, anything skipped.
