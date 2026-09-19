# Phase 4 plan: Passes and greedy planner

Goal: real pass predictions and a deterministic schedule.

## Files
- internal/passes: `ParseTLE`, `LoadTLE(path)`, `FetchTLE(ctx, norad)` (CelesTrak gp.php, only
  when asked); `Site{Name, Host, LatDeg, LonDeg, AltM}`; `Predict(tle, site, start, horizon)`
  steps 10 s with go-satellite (SGP4, WGS84), refines AOS/LOS by bisection to 1 s, mask 10 deg.
  `Pass{Station, Host, NoradID, AOS, LOS, MaxElevationDeg, DurationS}`: integers only.
  `Table(tle, sites, start, horizon)` merges and sorts (AOS, station). `DemoPass(p, now)`:
  same station, satellite and peak elevation replayed in [now, now+90 s] (profile not replayed).
  Floats stay internal to geometry; every output field is an integer.
- testdata: 27844.tle (CUTE-1 / CO-55, sun-synchronous 98.7 deg so all three sites see it,
  fetched 2026-09-19), golden-passes.txt for start 2026-09-20T00:00:00Z, 24 h.
- internal/planner: `Station{Host, Verified, Tier, PriorityBonus}`, `Request{Mode, GoalS,
  PointsPerDollar}`, `Plan(passes, stations, prices, req) Plan`. score (centipoints) =
  100*maxEl + 100*bonus - PointsPerDollar*cents (integers, explainable). Eligible only if
  verified and tier allows mode (uplink FIDUCIARY; downlink TRANSACTIONAL or FIDUCIARY; master
  plan 11). Greedy by score (ties: AOS, host), no overlap (one spacecraft, one link), stop at
  goal. Ineligible and skipped passes stay listed with a reason. `Replan(prev, removeHost)`
  returns the new plan and a Diff, emitted as bus event `replan`.
- config: `sites[{name, host, lat, lon, alt_m}]`, `satellite{norad_id, tle_file}`; defaults are
  the three master-plan sites.
- cmd/passes: tonight's table (`--tle`, `--start`, `--hours`, `--refresh-tle`, `--demo-pass`,
  `--json`).

## Dependencies (rule 7)
- github.com/joshuaferrara/go-satellite: the SGP4 port the master plan names (Vallado-based);
  pure Go. Pulls github.com/pkg/errors.

## Tests, one per acceptance criterion
1. TestGoldenTable: cached TLE + fixed start -> byte-equal golden file.
2. TestSanity: every pass 1-15 min, max elevation 10-90 (all three sites, 24 h).
   Cross-checked once against skyfield (scratch venv, not a dependency); AOS within 60 s.
3. TestPlannerRules: no overlaps, never unverified/under-tier, same output on shuffled input.
4. TestReplanDiff: removing a station drops its passes, emits `replan` with before/after.
5. TestDemoPass: window starts within 5 s of now and lasts 90 s.

## Risks
- go-satellite's LLAToECI uses a spherical Earth for the observer; small elevation error.
  The skyfield cross-check quantifies it; the skyfield fallback stays unused if AOS agrees.

## Review
Separate review subagent. (H) go-satellite log.Fatal on bad numbers: every field it parses is
pre-parsed and bounded, in ParseTLE and again in sat(). (H) Propagate errors lived on a copy:
position plausibility check + 30-day epoch guard. (M) passes crossing the horizon dropped: now
followed to LOS; in-progress-at-start rule documented. (M) hostile prices: bounded, named code.
(M) reasons now errs codes (POLICY_REFUSED:tier, PLAN_SKIPPED:*). (M) request validated. (L)
DemoPass doc corrected, 1 s peak refine, duplicate hosts rejected, json tags, -refresh-tle uses
config NORAD + atomic write to data/27844.tle (not the fixture), go mod tidy.
