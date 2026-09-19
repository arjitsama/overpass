# Overpass

Multi-agent broker for satellite ground station passes. A booking gives a
station time-boxed authority to relay commands, and Agent Name Service (ANS)
verification gates every step. Built for VTHacks 14; deadline Sunday 8:00 AM.

## Sources of truth
- docs/master-plan.md: the design. Phase prompts cite its section numbers.
- docs/webmesh-spec.md: exact field names for agent cards and trust cards.
- docs/schemas.md: wire formats. Frozen after Phase 1. Changing it needs a
  human to say yes.

## Stack
- Go, version required by ans-sdk-go's go.mod. Module github.com/arjitsama/overpass.
- github.com/agentnameservice/ans-sdk-go packages: ans, verify, verify/scitt, pop.
- SQLite through modernc.org/sqlite (pure Go, no cgo).
- web/: static HTML, vanilla JS, no framework, no build step.
- One binary, cmd/agent, with --role ops|authority|station|auditor|spacecraft.

## Hard rules
1. Never run ans-cli register, revoke, or any other write against
   https://api.godaddy.com. The production log is append-only. Scripts that
   write default to --dry-run and a human runs them. Reads are fine.
2. Never commit secrets. certs/, *.key, *.pem, .env are gitignored. Keys and
   tokens come from environment variables.
3. Signed bodies use JCS (RFC 8785), integers only (cents, epoch seconds), no
   floats. Every JWS has its own typ and every verifier checks typ first.
4. Every rejection returns a named code from internal/errs. Malformed input
   never produces a 500 or a panic; verifier entry points recover.
5. Do not reimplement what ans-sdk-go provides. Read its source in the module
   cache before writing verification or DPoP code.
6. An agent card declares only what the agent enforces.
7. No new dependency without a one-line reason in the phase plan.
8. web/ uses native elements, visible labels, icon plus text for every status,
   and live regions. Master plan section 12 is the checklist.
9. Do not invent the shape of an external API. Read its source or docs. If it
   cannot be known, put it behind an interface, stub it, and list it in the
   status report.

## Loop protocol (every phase)
1. PLAN. Read the phase prompt, the cited doc sections and the existing code.
   Write docs/plans/phase-N.md, under 60 lines: files to touch, interfaces,
   one test per acceptance criterion, risks, assumptions. If an unknown can
   only be settled by a human, stop and ask. Otherwise continue without
   waiting.
2. CODE. Implement with tests alongside. Build often. Keep functions small.
3. REVIEW. Read the whole diff since the phase started as a hostile reviewer;
   use a separate review subagent if one is available. Check: every
   acceptance criterion has a test; hard rules hold; error paths return named
   codes; no data races; inputs are size-limited; logs hold no secrets; no
   dead code. Append findings to the phase plan under "Review", then fix them.
4. TEST. gofmt -l, go vet ./..., go test -race ./..., then
   scripts/accept/phase-N.sh. All must pass.
5. If anything fails, return to CODE. After three failed loops, stop, commit
   to branch wip/phase-N, push, and report what blocks.
6. SHIP. Write docs/status/phase-N.md: what works, how it was tested, what is
   stubbed, what needs a human. Commit as "phase N: <title>", tag phase-N,
   push the branch and the tag to origin.

## Commands
make build | make test | make lint | make run-local | make smoke
