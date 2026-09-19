# Phase 4 status: Passes and greedy planner

## What works
- **`internal/passes`**
  - It propagates CUTE-1 (CO-55, NORAD 27844) with SGP4 (go-satellite), steps every 10 s over
    24 h, and computes elevation for a WGS84 observer at each configured site.
  - AOS and LOS are refined to 1 s, with a 10° mask. The peak elevation is refined to 1 s.
  - Output is integers only: epoch seconds and whole degrees.
  - Passes under 60 s are dropped as unusable.
  - A pass that rises inside the window is followed to its LOS. A pass already in progress at the
    start is left out.
  - Guards:
    - Every TLE field go-satellite parses is validated first, since go-satellite would otherwise
      `log.Fatal`.
    - Implausible propagated positions are errors.
    - Predictions more than 30 days from the TLE epoch are refused.
- **Stations from config:** `sites` defaults to Blacksburg 37.23 N 80.42 W, Svalbard 78.23 N 15.41 E
  and Awarua 46.53 S 168.38 E. `satellite{norad_id, tle_file}` defaults to 27844 and
  `data/27844.tle`.
- **`internal/planner`**
  - `score (centipoints) = 100·max_el + 100·bonus − points_per_dollar·cents`, all integers, with
    bounded prices and weights.
  - Hard constraints: the station must be verified, and its tier must allow the mode (uplink needs
    FIDUCIARY; downlink needs TRANSACTIONAL or better).
  - Greedy selection by score, with ties broken by AOS then host. No overlaps (one spacecraft), and
    it stops at the contact goal.
  - Every pass not taken keeps a named code (`POLICY_REFUSED:tier`, `PLAN_SKIPPED:*`) and a reason.
  - `Replan(plan, host, emit)` removes a station, accumulates exclusions, and emits `replan` with
    before, after, dropped and added.
- **`--demo-pass`:** the next real pass (station, satellite, peak) is replayed in [now, now+90 s].
  The elevation profile is not replayed; nothing downstream needs it.
- **`cmd/passes`:** prints the table. Flags: `-config`, `-tle`, `-start`, `-hours`, `-demo-pass`,
  `-json`, `-refresh-tle`. The refresh uses the configured NORAD ID and writes atomically.

## How it was tested
- `gofmt` is clean, `go vet` passes, and `go test -race ./...` passes.
- `scripts/accept/phase-4.sh` passes:
  1. **Golden table:** the cached TLE plus start 2026-09-20T00:00Z gives exactly
     `internal/passes/testdata/golden-passes.txt` (22 passes, all three stations). `bin/passes`
     prints the same bytes.
  2. **Sanity:** every pass lasts 1–15 min and peaks at 10–90°. Also tested: window edges,
     malformed TLE fields (no process exit), and stale or decayed TLEs (an error, not an empty
     table).
  3. **Planner rules:**
     - no overlaps
     - never the unverified station or the READ_ONLY lookalike for uplink
     - identical plans for 20 shuffles of input order
     - tier-by-mode, the score formula, and hostile prices are refused
  4. **Replan** drops the removed station's passes, emits one `replan` event, and the diff adds up.
  5. **Demo pass:** starts within 5 s of now and lasts 90 s, checked in the package, the command
     and the binary.
- **Independent check (master plan 7.5):** skyfield 1.55, in a scratch venv and not a repo
  dependency, predicted the same 22 passes for the same TLE, sites and mask. AOS and LOS agree
  within **1 s** for every pass, including the one crossing the horizon. So the skyfield fallback
  (`scripts/passes.py`) was not needed.
- A separate review subagent reviewed the diff. Its 11 findings are listed in
  `docs/plans/phase-4.md`, and all are fixed. The two high-severity ones:
  - go-satellite exits the process on a malformed TLE.
  - Its propagation errors are invisible, because they are set on a copy.

## Stubbed or deferred
- Prices, verification results and tiers are inputs. They come from quotes (Phase 5), the
  verifier (Phase 3) and trust (Phase 8).
- The LLM planner is Phase 10.

## Needs a human
See `docs/human-actions.md`:
- confirm the satellite choice and the planner defaults
- refresh the TLE before the demo
