# Mobile E2E

From `/private/tmp/spanbox`:

```sh
pnpm --dir e2e-mobile install --frozen-lockfile
pnpm --dir e2e-mobile exec playwright install chromium
pnpm --dir e2e-mobile test
```

Requires Go, Node.js and pnpm. The script builds the real Go server, starts it on
`http://127.0.0.1:14318` (override with `E2E_PORT`), prints its generated auth token,
and seeds two traces/sessions through authenticated OTLP. It uses isolated temporary
data, then stops the server and removes that data. Existing repository data is untouched.

Playwright Chromium runs the iPhone 13 profile with `hasTouch: true` at 390×844.
The test exercises login, traces, span selection, search, sessions and dashboard,
plus SQL for a real textarea. Each assertion is recorded in `results.log`; server
output is in `server.log`. Viewport screenshots are saved beside the script.
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
