# Spanbox UI polish result

## Changed

- Replaced the generic SaaS-blue styling with the seeded dark-first ink system: rose accent, 2px radii, hairline structure, compact 32px rows, finished light-theme tokens, and tabular numerals.
- Self-hosted Bricolage Grotesque for display type and Source Sans 3 for UI/body type; both use latin variable WOFF2 files and `font-display: swap`.
- Replaced span-kind pills with accessible left-bar markers, added selected and keyboard-focus states, and kept kind text visible.
- Restyled filters, tables, trace tree/detail, search, SQL, login, empty/error states, summary metrics, and all four uPlot charts. Chart colors come from CSS tokens and y axes remain `size: 64`.
- Made authenticated deployments serve shared static assets so the login page uses the same self-hosted design without changing CSP.
- Replaced dark desktop screenshots and added light desktop plus dark/light 400px mobile captures in `docs/screenshots/`.

## Contrast

WCAG relative-luminance ratios from the shipped tokens:

| Theme | Body text / canvas | Muted text / canvas | Accent / canvas | Accent / panel | Button text / accent |
|---|---:|---:|---:|---:|---:|
| Dark | 17.62:1 | 8.94:1 | 7.00:1 | 6.65:1 | 6.68:1 |
| Light | 15.00:1 | 6.12:1 | 6.59:1 | 7.15:1 | 7.25:1 |

The accent focus ring exceeds 3:1 against canvas and panel surfaces in both themes.

## Fonts

- `bricolage-grotesque-latin.woff2`: 131,548 bytes
- `source-sans-3-latin.woff2`: 28,740 bytes

Both are OFL-1.1 and recorded in `NOTICE`.

## Skipped / caveats

- No brief requirements were skipped.
- The required Impeccable detector returned zero findings, but reported degraded regex fallback because its optional HTML/CSS parser modules were unavailable. Visual desktop/mobile, dark/light, keyboard, overflow, and route checks were completed independently.
