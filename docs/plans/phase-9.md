# Phase 9 plan: Dashboard and accessibility

Prompt: master plan §12 (requirements) + §15 (demo script). Ship as
"phase 9: dashboard and accessibility". Out of scope: backend beyond 3 routes.

## Files to touch
- web/index.html, web/app.js, web/styles.css — one page, no framework, no build,
  no external requests. Served by the Ops agent.
- web/embed.go (package web): //go:embed the three files; expose an http.Handler
  serving them with correct content types.
- web/contrast_test.go: parse the token blocks for the three themes, fail any
  text pair < 4.5:1 or large-text/icon pair < 3:1 (§12).
- web/html_test.go: parse index.html (x/net/html, already a dep) — one h1, no
  skipped heading levels, every button/input has an accessible name, every table
  has a caption + th scope, no click handler on a non-button/link, lang set.
- web/testdata/demo-events.json: a recorded event stream (schedule, agents incl.
  impostor rejection + lookalike READ_ONLY, active pass, session_cut, battery).
- web/dom_test.js + web/domshim.js: self-contained Node (v24 present) DOM shim
  that replays demo-events.json through app.js and asserts the rendered DOM
  (schedule, trust table, alert region, battery results). No npm install.
- cmd/agent: serve web/ on the Ops role; add 3 POST routes (below).
- scripts/accept/phase-9.sh; docs/a11y-manual.md.

## Page (in order, §12): skip link; header h1 "Overpass" + sat name/NORAD +
high-contrast toggle; main h2s Active pass, Pass schedule, Agents and trust,
Attack battery, Event log. Native elements only. Status = icon(SVG aria-hidden)
+ text + color. Trust dims as "Integrity 89 of 100" + <meter>; inactive =
"no signal registered". Two live regions empty at load: role=log/polite (events),
role=alert (cuts, rejections). Countdown digits aria-hidden; announced copy at
each minute, 30s, 10s. Buttons: Run demo pass, Ask GoDaddy's agent to verify,
Run battery (then focus -> results heading, tabindex=-1). Errors via
aria-describedby + alert region.

## Themes: CSS var tokens in :root (light), :root[data-theme="dark"],
:root[data-theme="contrast"] (one authoritative block each -> checkable, DRY).
JS honors prefers-contrast / prefers-color-scheme via matchMedia at load unless
toggled; @media prefers-reduced-motion disables transitions. Blue/orange accents,
never red-on-green; shapes differ per status. Responsive to 360px; wide tables in
a focusable (tabindex=0) labelled scroll container. Focus: 3px outline, never none.

## Ops routes (thin; events -> bus; §12 data path)
- POST /ui/verify-station {host} -> webmesh Verify, emit `verification`, return verdict.
- POST /ui/run-battery -> run configured battery canary/subset, emit `battery`.
- POST /ui/run-demo-pass -> publish the scripted demo event sequence to the bus.
Gated to the Ops role; others 404. Depth documented in status (hard rule 9).

## Event vocabulary the page renders (kind -> section)
pass->schedule; agent->trust table; active_pass->active; battery->battery table;
verification->trust/verify; session_cut & rejection->alert; else->event log.

## Tests (one per acceptance criterion)
1. contrast_test: every declared pair, every theme, passes.
2. html_test: the structure assertions above.
3. axe: try `npx --no-install`; not installed offline -> report in status.
4. dom_test.js: replay testdata -> schedule + trust + impostor rejection +
   session cut in alert + battery results present in the DOM.
5. docs/a11y-manual.md: VoiceOver + keyboard walkthrough with expected announcements.

## Risks / assumptions
- axe CLI not installed and no network to fetch it -> #3 reported, not run.
- No committed dep added (x/net/html already present; promote to direct).
- run-demo-pass replays a scripted sequence (demo driver), not a full live pass;
  documented. verify/battery call real subsystems when configured.

## Review (hostile pass, findings fixed)
1. axe (run via `npx @axe-core/cli`, Chrome headless present) found the skip link's
   target `#pass-schedule` did not exist and the link sat outside a landmark.
   Fixed: gave the Pass schedule section id + tabindex=-1, moved the skip link
   inside <header>. axe now reports 0 violations.
2. High-contrast toggle OFF forced light even for a dark-OS user; now restores the
   OS base theme.
3. XSS: all event-derived text inserted via innerHTML goes through esc(); log and
   alert use textContent. Dimension values coerced with Number().
4. UI routes are POST-only, ops-role-only, body-limited (1 MiB), panic-recovered.
   run-demo-pass/run-battery replay recorded events (demo drivers, documented);
   verify-station is a real webmesh call.
5. No external requests from the page (same-origin /events and /ui/* only); assets
   served with nosniff and only three known content types, else 404.
