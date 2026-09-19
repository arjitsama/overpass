# Overpass — progress from start to finish

A multi-agent broker for satellite ground-station passes (VTHacks 14). One phase
per row, in order. Each links its plan and its shipped status report. "Done"
means it passed its acceptance script and a hostile review, and was tagged.

| Phase | Title | Status | Plan | Report |
| --- | --- | --- | --- | --- |
| 0 | Preflight, skeleton, smoke | ✅ done, tag `phase-0` | [plan](docs/plans/phase-0.md) | [status](docs/status/phase-0.md) |
| 1 | Schemas, JCS, chains, thumbprints | ✅ done, tag `phase-1` | [plan](docs/plans/phase-1.md) | [status](docs/status/phase-1.md) |
| 2 | Agent cards, A2A, DPoP inbound | ✅ done, tag `phase-2` | [plan](docs/plans/phase-2.md) | [status](docs/status/phase-2.md) |
| 3 | ANS verification, webmesh, jku pinning | ✅ done, tag `phase-3` | [plan](docs/plans/phase-3.md) | [status](docs/status/phase-3.md) |
| 4 | Passes (SGP4) and the greedy planner | ✅ done, tag `phase-4` | [plan](docs/plans/phase-4.md) | [status](docs/status/phase-4.md) |
| 5 | Quote, mandate, booking | ✅ done, tag `phase-5` | [plan](docs/plans/phase-5.md) | [status](docs/status/phase-5.md) |
| 6 | Pass session (commands, revocation) | ✅ done, tag `phase-6` | [plan](docs/plans/phase-6.md) | [status](docs/status/phase-6.md) |
| 7 | Adversaries, battery, auditor | ✅ done, tag `phase-7` | [plan](docs/plans/phase-7.md) | [status](docs/status/phase-7.md) |
| 8 | Trust index and tiers | ✅ done, tag `phase-8` | [plan](docs/plans/phase-8.md) | [status](docs/status/phase-8.md) |
| 9 | Dashboard and accessibility | ⬜ not started | — | — |
| 10 | LLM planner | ⬜ not started | — | — |
| 11 | Live deploy and demo | ⬜ not started | — | — |

## Where things stand
- Phases 0–8 are shipped: an agent binary (`--role ops|authority|station|auditor|spacecraft`),
  a red-team `battery`, a `passes`/`satreg`/`cardhash` toolset, and a forked trust
  index (`third_party/agent-trust-discovery`) with an Overpass `pass_delivery`
  behavior signal.
- Trust tiers are enforced by the authority's flight rules on the **truthful**
  trust vector (never on fabricated identity/integrity); see
  [docs/status/phase-8.md](docs/status/phase-8.md).
- Everything a human must do to go live (register agents, DNS/DNSSEC, run the
  index and prober, keys) is tracked in [docs/human-actions.md](docs/human-actions.md).

## Build and test
`make build` · `make test` · `make lint` · `make trust-up` · `make accept`
(runs every phase's acceptance script).
