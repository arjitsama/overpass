# Devpost submission — Overpass (draft)

Submit at https://vthacks-14.devpost.com/ with all three tracks selected.

- **Title:** Overpass
- **Tagline:** Verified command authority for rented ground stations
- **Tracks:** GoDaddy Best Use of ANS · Peraton Best Mission Critical AI · Best Accessibility (UI/UX)
- **Repo:** https://github.com/arjitsama/overpass
- **Live dashboard:** https://ops.blacksburgbytes.club/ui/  *(fill in once deployed)*

## Inspiration
Satellite operators increasingly rent antenna time from ground stations they
have no contract with. Letting a stranger's station relay commands to your
spacecraft is a trust problem, not a networking one: you need to know who the
station is, prove it, and bound exactly what it may do — for one satellite, one
pass, one window. Identity alone is not enough; a station can be perfectly
identified and still not be one you should trust with an uplink.

## What it does
Overpass is a multi-agent broker for ground-station passes. **A booking gives a
station time-boxed authority to relay commands, and Agent Name Service (ANS)
verification gates every step.** The flow:

1. Ops predicts tonight's real passes from public orbit data (SGP4).
2. It discovers stations through ANS by capability and verifies each one (badge,
   cert, TLSA/DANE, SCITT receipt, a `jku`-pinned signed agent card).
3. A trust index scores each agent on five dimensions; access tiers gate what a
   station may do.
4. An LLM planner proposes bookings through tools; the mission **authority**, not
   the model, signs a mandate or refuses on policy.
5. The mandate names one station, one satellite, one window; Ops signs each
   command with a per-satellite counter.
6. The station may relay but cannot forge or replay; the spacecraft checks typ,
   signature, NORAD ID and a strictly increasing counter.
7. Revocation (`CERTIFICATE_HOLD`) cuts the session on the next status-token
   fetch and triggers a re-plan; a network blip alone does not.
8. An auditor re-verifies each pass and probes stations with canaries; a station
   that accepts a forged mandate loses its tier.

## How we built it
Go, one binary with `--role ops|authority|station|auditor|spacecraft`. ANS via
`ans-cli` and `ans-sdk-go` (`ans`, `verify`, `verify/scitt`, `pop` for DPoP),
identity/receipt checks cross-checked against `ans-verify`. Trust scoring is a
fork of GoDaddy's `agent-trust-discovery` with an added `pass_delivery` behavior
signal. A2A over JSON-RPC with signed agent/trust cards; JCS (RFC 8785) canonical
bodies, ES256 signatures. SQLite (pure-Go `modernc.org/sqlite`). The dashboard is
framework-free HTML/JS. The LLM planner uses the Anthropic Messages API tool-use
loop. Our agents live under `*.blacksburgbytes.club` (domain at Porkbun, ANS
registrations on GoDaddy).

## Challenges
Registration and DNS are permanent and unforgiving: the transparency log is
append-only, and DANE means the TLSA record must equal the cert actually served,
and without DNSSEC the DANE check is skipped (present but not relied on) rather than trusted. The hardest idea was **cold-start
trust**: a brand-new but honest station has no history, so it starts at
availability/probation and earns uplink rights through audited passes — which is
exactly why a registered lookalike with no history stays READ_ONLY.

## Honest limits
- The spacecraft and the RF link are **simulated** (no real radio, no real bus).
- **No on-chain settlement**; payment options are described, not executed.
- Trust observations are **seeded** for the demo (behavior only, marked as seed
  data); we never fabricate identity or integrity scores.
- We **run our own trust index**; in production a neutral party would.
- Our local identity uses demo-CA (DV) certs, so the index's own FIDUCIARY tier
  isn't reachable locally; Overpass gates on the truthful trust vector in its own
  flight rules instead (see docs/status/phase-8.md).

## Accessibility
Built keyboard-first with a screen reader from the first commit (master plan
§12): one page, native elements only, status as icon + text + shape + colour
(blue/orange, never red-on-green), two live regions (event log + alerts), an
announced countdown, three contrast-checked themes, responsive to 360px.
Automated results: contrast passes for every token pair in all three themes;
structure checks pass; **axe-core reports 0 violations**. Manual VoiceOver +
keyboard walkthrough in `docs/a11y-manual.md`.

## Screenshots to attach
1. Pass schedule table. 2. Impostor rejection (alert region). 3. Session cut
after revoke. 4. Attack battery results. 5. Five-dimension trust breakdown.

## Video
2-minute walkthrough = the rehearsal screen recording (the demo fallback).

## Checklist before submitting
- [ ] All three tracks selected.
- [ ] Every teammate added, registered, checked in.
- [ ] Repo public with README (run instructions + architecture diagram).
- [ ] Live dashboard URL filled in.
- [ ] Screenshots + 2-minute video attached.
