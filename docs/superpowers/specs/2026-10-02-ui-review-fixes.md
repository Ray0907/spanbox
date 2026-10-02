# UI review fixes (2026-10-02)

Source: interface review of HEAD 24c666b. Files: `internal/web/templates/*.html`, `internal/web/static/app.css`, `internal/web/static/app.js`. Keep project idiom: hand-written CSS with existing tokens, htmx, Go `html/template`. No new deps.

## Scope: do these, in order

1. **Span tree selection collapses subtree, no disclosure cue** (HIGH) — `trace.html:20`, `app.css:527-545`.
   - `summary` is `display:grid`, so the marker is gone; clicking a summary both selects (htmx) and toggles `<details>`.
   - Move `hx-get`/`hx-target`/`hx-swap` onto an `<a class="tree-select" href="/spans/{trace}/{span}">` wrapping the span name inside the summary. Clicks on interactive content inside `summary` do not toggle.
   - Restore a visible cue: `.tree-node>summary::before{content:"▸"}`, `.tree-node[open]>summary::before{content:"▾"}`; hide it (visibility) for leaf nodes with no children. Add a grid column for it.
   - Update `app.js` `markSelectedSpan` so selection state still works (`htmx:beforeRequest` now fires from the link; use `closest('summary')`).
2. **Panel visibility** (MEDIUM) — `app.css:484,501,820`. Desktop: `.span-panel{position:sticky;top:56px;max-height:calc(100vh - 72px);overflow:auto}`. Mobile (<=640px): swap with `show:#panel:top`.
3. **Live region noise** (MEDIUM) — `trace.html:13`. Remove `aria-live` from `#panel`. Add stable empty `<p id="panel-status" role="status" class="visually-hidden">`; in `app.js` on `htmx:afterSwap` set its text to the new span name. Add `.visually-hidden` to `app.css`.
4. **Waterfall svg** (MEDIUM) — `trace.html:26`: `aria-hidden="true"`, drop `role="img"` and `aria-label`.
5. **Result count announce** (MEDIUM) — `traces_rows.html:2`: move count out of swapped section into a `role="status"` element updated via `hx-swap-oob`, same as `#export-link`.
6. **Charts alt** (MEDIUM) — `dashboard.html:23-26`: add `role="img"` to each `.chart`.
7. **Custom range fields** (MEDIUM) — `traces.html:21-26`, `sessions.html:20-21`: show From/To only when Range=Custom via CSS `:has(option[value=custom]:checked)`; label `From (UTC, RFC 3339)`.
8. **Empty states** (MEDIUM) — `traces_rows.html:25`: `No traces match these filters. <a href="/">Clear filters</a>`. `sessions.html:37` similar. Do not add new page-data flags.
9. **Type floor** (MEDIUM) — `app.css`: raise sizes below 0.8rem to 0.8rem; `pre` to 0.867rem.
10. **Small** (LOW): `dashboard.html:11` "Update" -> "Apply"; breadcrumbs as `<ol>` with `aria-current="page"` and CSS separators; skip link + `id="main"`; `.table-wrap` scroll-shadow; `.topbar nav a{padding:0 8px}` at <=640px.

## Out of scope
Color token changes (light-theme contrast) — design decision, do not touch.

## Failure modes (write first, then code)
- Link inside summary breaks keyboard: Enter on summary must still toggle, Enter on link must select.
- Selected-row marking lost after swap.
- `:has()` hides From/To on a page where Custom is preselected from the URL.
- Leaf nodes show a chevron.
- `go test ./...` must still pass (templates are parsed in tests).

## Verify
`go build ./... && go test ./...`; run server, check `/traces/<id>` at 1280px and 320px in a browser: click parent span -> subtree stays open, panel updates, chevron toggles separately.
