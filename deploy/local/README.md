# Local adversary profiles

Four station profiles for the demo and the attack battery (master plan 10),
run against the ANS reference stack (`scripts/local-ans.sh start`).

| Profile | Config | Behaviour | Expected |
| --- | --- | --- | --- |
| Honest station | `station-honest.yaml` | correct `book_pass` | battery: 23/23 BLOCKED |
| Rogue station | `station-rogue.yaml` | `rogue: true` skips the signature and DPoP checks | battery: `tamper_mandate`, `wrong_dpop_key_attack` VULNERABLE; auditor canary → `CANARY_ACCEPTED` |
| Impostor `gs-sva1bard` | `station-impostor.yaml` | self-signed cert, never registered | refused at verification before any quote |
| Registered lookalike | `station-lookalike.yaml` | registered, no seeded trust → READ_ONLY | verifies, but an uplink mandate is refused `POLICY_REFUSED:tier` |

## Run

```sh
scripts/local-ans.sh start
# Register the honest station and the rogue on the reference stack:
scripts/local-register.sh gs-blacksburg.localhost 0.1.0 certs/local/gs-blacksburg deploy/local/station-honest.yaml
scripts/local-register.sh gs-rogue.localhost      0.1.0 certs/local/gs-rogue      deploy/local/station-rogue.yaml
bin/agent --config deploy/local/station-honest.yaml &
bin/agent --config deploy/local/station-rogue.yaml   &
# Point the battery at each (battery.yaml holds the Ops + authority credentials):
bin/battery run    -config deploy/local/battery-honest.yaml            # exits 0: all BLOCKED
bin/battery run    -config deploy/local/battery-rogue.yaml -expect-vulnerable
```

The impostor is never registered, so verification refuses it before a quote is
requested. The lookalike is registered but not seeded, so it stays READ_ONLY and
the authority refuses an uplink mandate for it.

The battery config (`battery-*.yaml`, see `cmd/battery`) holds the Ops
identities and the authority signing keys the target station trusts — a
red-team tool legitimately holds them to craft validly-signed mandates.

`scripts/accept/phase-7.sh` runs every acceptance check as a Go test with real
in-process agents, so the reference stack is not required for CI.
