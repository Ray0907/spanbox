# Mobile E2E

From the repository root:

```sh
pnpm --dir e2e-mobile install --frozen-lockfile
pnpm --dir e2e-mobile exec playwright install chromium
pnpm --dir e2e-mobile test
```

Requires Go, Node.js and pnpm. The script builds the real Go server, starts it on
`http://127.0.0.1:14318` (override with `E2E_PORT`), uses a throwaway test credential
(redacted in logs), and seeds invented traces/sessions through authenticated OTLP. It uses isolated temporary
data, then stops the server and removes that data. Existing repository data is untouched.

Playwright Chromium runs the iPhone 13 profile with `hasTouch: true` at 390×844.
The test exercises the Astryx login, traces, span selection and text paging, search,
sessions, dashboard, and SQL textarea, plus the `/ui` trace-list alias. It builds
`web-ui` first. Each assertion is recorded in `results.log`; server output is in
`server.log`. Viewport screenshots are saved beside the script.

Tracked screenshots (refreshed from the real suite):

| Files | Shows |
|---|---|
| `login.png` | Empty sign-in form; no credential shown |
| `traces.png`, `traces-after-tap.png` | Trace filters and table after touch scrolling |
| `trace-detail.png`, `trace-detail-selected.png` | Span tree and selected LLM inspector |
| `search.png` | Search form after navigation |
| `sessions.png`, `sessions-after-tap.png` | Invented session list before/after touch interaction |
| `dashboard.png` | Demo totals and painted cost chart |
| `ui-traces.png`, `ui-trace.png` | Current UI through the alias and a selected agent span |

Other PNGs are transient test output, not part of this screenshot set.

Full-page screenshots are deliberately avoided: Playwright 1.61 resets Chromium
touch emulation when capturing taller-than-viewport pages.

This is emulation, not real iOS Safari. Exploratory desktop Playwright WebKit does
not implement `-webkit-text-size-adjust` and reports the required
`interactive-widget` viewport key as unsupported. It cannot provide a clean
zero-console-error run for this exact specification. No errors are filtered in
the Chromium test, and the server's CSP is not bypassed.

Real-phone checks remain: tap delay, notch/home-indicator safe areas, keyboard
resize behavior, and persistent Safari hover after taps. `env()` resolves to zero
in this emulator, so visual safe-area correctness cannot be established here.
