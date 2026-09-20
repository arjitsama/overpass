# Demo runbook — 3 minutes, keyboard only

Two people: one drives (keyboard only), one talks. Cold start with
`scripts/demo.sh` (local) or the deployed dashboard at
`https://ops.blacksburgbytes.club/ui/`. Say the pitch first:
**"Verified command authority for rented ground stations."**

Before you start: `scripts/demo.sh` prints the dashboard URL and the exact
`ans-cli revoke …` line for the 1:45 beat. Have a second terminal open for it.
Keyboard: Tab / Shift-Tab to move, Enter/Space to activate. VoiceOver: ⌘F5.

| Time | Beat | Drive (key / command) | Say | Fallback |
| --- | --- | --- | --- | --- |
| 0:00 | Pass schedule | Focus loads on the page; Tab to **Run demo pass**, Enter. The schedule table fills. | "Real passes of a real CubeSat tonight, from public orbit data. University and community stations share antenna time with missions they have no contract with." | If the button hangs, the schedule is already populated from the recorded stream; read the table. |
| 0:20 | Agents & trust | Tab to the Agents and trust table; read a row. | "Ops found these stations through ANS by capability. Badge, cert, TLSA, SCITT receipt, five trust dimensions." | Screenshot in `docs/` / the recorded video. |
| 0:25 | Live webmesh verify | Tab to **Ask GoDaddy's agent to verify this station**, Enter. The alert region announces the verdict. | "GoDaddy's own agent just verified this station for us, live. We verify them, and they verify us." | If webmesh is unreachable, show the recorded verdict; say it's GoDaddy's production agent. |
| 0:45 | Impostor rejected | Point to the impostor row: Rejected. | "A lookalike with no ANS registration. We never read its quote." | The row is in the trust table from the demo stream. |
| 0:55 | Registered lookalike | Point to gs-svalbard-eu: Verified, READ_ONLY. | "It registered properly and passes every identity check. Identity is not trust. No audited history, so the authority refuses an uplink mandate for it — even when its quote tried to talk our AI planner into it." | — |
| 1:25 | Active pass | Show the Active pass section: station, countdown, session state, last Ack. | "The mandate names this station, this satellite, this window. Ops signs each command with a counter, so the station can relay but cannot forge or replay." | — |
| 1:45 | **Revoke (H6)** | In the second terminal paste the line `scripts/demo.sh` printed: `ans-cli revoke <gs-blacksburg AgentID> --reason CERTIFICATE_HOLD`. | "The station was just revoked in the registry. Next status token, session cut, next pass re-booked elsewhere. A network blip alone would not have cut it." | If the live revoke stalls, the recorded run shows the session cut + replan. |
| 2:10 | Battery | Tab to **Run battery**, Enter; focus lands on Results. | "GoDaddy's fraud battery plus our own: forged mandates, replayed proofs, a real authority that does not own this satellite. All blocked, each with a named reason." | Results also render from the recorded stream. |
| 2:35 | Auditor / rogue drop | Show gs-rogue's tier dropped after the canary. | "This station was trusted and its passes looked clean. Our auditor sent it a forged mandate as a canary and it accepted. It lost uplink rights." | — |
| 2:50 | Accessibility close | Take your hand off the mouse. | "Keyboard only, the whole way. It works with a screen reader, in high contrast, and without color." | — |

## If everything fails
Play the saved screen recording (make one during rehearsal). Venue Wi-Fi down:
phone hotspot, then the recording. Keep the "Honest limits" list (README) handy
for a skeptical judge.

## Accessibility re-run
For the accessibility judges, rerun the first minute with VoiceOver audible; see
`docs/a11y-manual.md` for the expected announcements.
