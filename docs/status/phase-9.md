# Phase 9 status: dashboard and accessibility

## What works
- **One-page dashboard** (`web/index.html`, `web/app.js`, `web/styles.css`): no
  framework, no build step, no external requests. Served by the Ops agent at
  `/ui/` (embedded via `web/embed.go`, `nosniff`, three known content types).
- **Structure (master plan §12):** skip link (inside the banner, targeting a
  focusable Pass schedule section); header with h1 "Overpass", satellite
  name + NORAD ID, and a high-contrast toggle; main with h2 sections Active pass,
  Pass schedule, Agents and trust, Attack battery, Event log. Native elements
  only — no clickable divs.
- **Accessibility:** every status is icon (inline SVG, `aria-hidden`, distinct
  shape) + text + colour; trust dimensions render as "Integrity 89 of 100" with a
  `<meter>`, inactive ones as "no signal registered". Two live regions created
  empty at load — `role="log"`/polite for events, `role="alert"` for session cuts
  and rejections. Countdown digits are `aria-hidden`; an announced copy updates
  each minute and at 30 and 10 seconds. After "Run battery" focus moves to the
  Results heading. Errors appear next to the control (`aria-describedby`) and in
  the alert region. Focus is always visible (3px outline, never removed).
- **Themes:** colour tokens in three authoritative blocks — light, dark, contrast.
  `prefers-contrast` / `prefers-color-scheme` honoured via `matchMedia` at load
  (toggle overrides, stored per viewer); `prefers-reduced-motion` disables
  transitions. Blue/orange accents, never red-on-green. Responsive to 360px; wide
  tables scroll inside a focusable, labelled container.
- **Ops routes (§12 data path):** `POST /ui/verify-station` (a real Phase 3
  webmesh call to GoDaddy's agent), `POST /ui/run-demo-pass` and
  `POST /ui/run-battery`. The dashboard opens one `EventSource` to `/events` and
  renders by event kind.

## How it was tested
- `gofmt -l`, `go vet ./...`, `go test -race ./...` — all clean/green.
- `scripts/accept/phase-9.sh` passes all five criteria:
  1. `TestContrastAllThemes` — every declared pair clears 4.5:1 (text) / 3:1
     (icons, large text, borders) in light, dark and contrast.
  2. `TestHTMLStructure` — one h1, no skipped heading levels, every button/input
     has an accessible name, every table has a caption, no non-native click
     handler, `lang` set.
  3. **axe-core 4.13.0 (Chrome headless) reports 0 violations** on the served
     page. The acceptance script runs axe when Node + a browser + network are
     present, and reports a skip otherwise (per the prompt).
  4. `web/dom_test.js` replays `web/testdata/demo-events.json` through the real
     `app.js` in a self-contained DOM shim (no npm install) and asserts the
     schedule, the trust table (with an impostor rejection and inactive
     dimensions), the active pass, a session cut in the alert region, and battery
     results.
  5. `docs/a11y-manual.md` — a VoiceOver + keyboard walkthrough of the demo
     script with expected announcements, for a human to perform.
- Regression: phases 0–8 acceptance all still pass.

## What is stubbed / documented depth (hard rule 9)
- `POST /ui/run-demo-pass` and `POST /ui/run-battery` replay the recorded event
  stream (`web/testdata/demo-events.json`) onto the bus — demo drivers that light
  the dashboard from recorded data, not a live pass or a live battery run. A live
  pass is the ops→authority→station→spacecraft flow; a live battery is
  `cmd/battery` with its own credentials. `POST /ui/verify-station` is a real
  webmesh call and needs `ui.webmesh_url` + a station host configured.
- No committed dependency added: `golang.org/x/net/html` (used by the structure
  test) was already in the module graph.

## What needs a human
See `docs/human-actions.md` (Phase 9): configure `ui.webmesh_url` and
`ui.verify_host` on the Ops agent for the verify button; perform the
`docs/a11y-manual.md` VoiceOver walkthrough before judging; optionally wire a
live pass/battery to the demo buttons if a scripted demo is not desired.
