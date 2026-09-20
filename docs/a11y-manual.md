# Accessibility manual test — Overpass dashboard

A human runs this once before the demo, on macOS Safari with VoiceOver
(⌘F5 to toggle). It follows the 3-minute demo script (master plan §15) using the
keyboard only. "VO" = the VoiceOver keys (Control+Option). Expected announcements
are what VoiceOver should say; exact wording varies by version, so match the
sense, not the syllables.

Automated checks (`scripts/accept/phase-9.sh`) already cover contrast in all three
themes, heading structure, accessible names, captions, and a recorded-event
render. This manual pass covers what only a person with a screen reader can judge.

## Setup
1. `make build && bin/agent --config <ops-config>` (or `make run-local`), then open
   `https://<ops-host>/ui/` in Safari.
2. Turn VoiceOver on (⌘F5). Put the mouse away.

## Walkthrough

### 0. Skip link and landmarks
- Press Tab once from the address bar. **Expect:** "Skip to pass schedule, link."
- Press Enter. **Expect:** focus jumps to the Pass schedule region; VoiceOver reads
  the table caption.
- VO+U opens the rotor; the Headings list shows exactly one level-1 heading
  ("Overpass") and the h2s Active pass, Pass schedule, Agents and trust, Attack
  battery, Event log — in that order, no gaps.

### 1. Header and high-contrast toggle
- Tab to "High-contrast mode, toggle button". **Expect:** the role "toggle button"
  and state "off" are announced.
- Press Space. **Expect:** "on" is announced and the page repaints in the
  high-contrast theme. Press Space again to confirm it returns to "off".

### 2. Pass schedule (0:00 of the demo)
- Tab to the "Pass schedule table, scrollable" region (it is focusable). Use VO+arrows
  to move through cells. **Expect:** each data cell is announced with its column
  header, e.g. "Station, gs-blacksburg…"; "Status, Booked".

### 3. Agents and trust (0:20)
- Move into the Agents and trust table. **Expect:** the verification cell reads
  "Verified" or "Rejected: <reason>" as text (the icon is silent). Dimension cells
  read "Integrity 89 of 100"; inactive ones read "no signal registered". The meter
  is aria-hidden and is not announced separately.
- Tab to "Ask GoDaddy's agent to verify this station, button" and press Enter.
  **Expect:** the alert region announces the returned verdict without moving focus.

### 4. Impostor and lookalike (0:45–0:55)
- After "Run demo pass", **expect** the alert region to announce
  "Rejected gs-sva1bard (impostor): not registered in ANS", and the impostor row in
  the trust table to read "Rejected". The registered lookalike reads "Verified" with
  tier "READ_ONLY".

### 5. Active pass and countdown (1:25)
- Move to the Active pass section. **Expect:** Station, Session state ("active"),
  Token age and Last Ack are read as a description list. The ticking digits are
  silent (aria-hidden); the announced copy updates about once a minute and at 30 and
  10 seconds, e.g. "1 minute to acquisition", then "30 seconds to acquisition".

### 6. Session cut (1:45) — run `ans-cli revoke` in a terminal
- **Expect:** the alert region announces "Session cut for <station>: token_revoked"
  promptly, interrupting, and the Active pass session state reads "Session cut".

### 7. Attack battery (2:10)
- Tab to "Run battery, button" and press Enter. **Expect:** focus moves to the
  "Results" heading (VoiceOver announces "Results, heading level 3"), and the results
  table fills with rows read as "Attack … Verdict Blocked …".

### 8. Keyboard-only and colourless (2:50)
- Confirm the entire run above used only Tab, Shift+Tab, Enter, Space and VO+arrows.
- Toggle high-contrast on and repeat the first minute; confirm every status is still
  distinguishable by its icon shape and text, not colour alone.

## Errors
- With the index or webmesh unreachable, press a button. **Expect:** an error appears
  next to the button (announced via aria-describedby when the button regains focus)
  and in the alert region ("Action failed: …").

## Reduced motion
- With System Settings → Accessibility → Display → Reduce motion on, reload.
  **Expect:** no transitions or animations; the skip link appears without sliding.
