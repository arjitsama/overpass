# Phase 7 plan: Adversaries, battery, auditor

Goal: every attack in the plan is a runnable check with a verdict, and a misbehaving registered
station gets caught.

## Files
- internal/battery: `Verdict` (BLOCKED, VULNERABLE, INCONCLUSIVE), `Result{Name, Verdict, Code,
  Detail}`, `Battery` holding two registered Ops identities (primary + wrong-key), an authority
  key the target trusts, an untrusted key, and a second-authority key (not_owner). A `caller`
  builds a SendMessage body for a skill, signs it with a chosen `verify.Outbound`, POSTs, and
  returns the observed errs.Code; a replay path signs once and resends the identical request.
  One method per attack; `Run(ctx)` runs all and returns `[]Result`. Attacks:
  the 12 table rows + 3 probes (canonicalization_probe, payto_binding_check, card_drift_watch)
  + Overpass-only (not_owner, overlap, typ_confusion, forged_command, replayed_command,
  late_command, class_escalation, jku_injection, log_outage). Emits bus events.
- cmd/battery: subcommands named after each attack + `run`. Flags: -station, -authority,
  -spacecraft URLs, -config (credentials/keys). Table + `-json`. Non-zero exit if any honest-target
  check is not BLOCKED (acceptance 7). Needs a real registered Ops identity; local uses the
  reference stack.
- internal/auditor: passive `Audit(ctx, in) (AuditReport, signed string)`: takes booking receipt,
  mandate, both Evidence (ops+station), verification events; re-verifies identity (VerifyPeer),
  mandate + DPoP jkt binding, compares chain heads, counts missing acks, signs an
  overpass-audit+jws. Active `Canary(ctx, station)`: two probes (flipped mandate sig byte; valid
  mandate wrong DPoP key). Acceptance -> CANARY_ACCEPTED with the response as evidence.
  `TrustSink` interface + `LogSink` stub (Phase 8).
- cmd/agent: role auditor (skills audit_pass, canary; config auditor{trust_sink,...}).
- deploy/local/: README + configs for honest stations, gs-rogue (rogue+ack), impostor
  (self-signed, unregistered), registered lookalike (registered, READ_ONLY, no seed). A
  local-battery.sh that registers them on the reference stack and runs the battery.

## Codes reused (all in internal/errs already): the section 10 table's codes, plus
CANARY_ACCEPTED, CARD_REJECTED:jku, CARD_CLAIM_MISMATCH, CHAIN_MISMATCH, ACK_MISSING, WINDOW_CLOSED,
CLASS_REJECTED, TYP_REJECTED, COMMAND_REJECTED:{signature,counter}, POLICY_REFUSED:tier.

## Tests, one per acceptance criterion (in-process real agents over httptest TLS, as Phase 5/6)
1. TestBatteryHonest: every attack BLOCKED with the master-plan code.
2. TestBatteryRogue: tamper_mandate and wrong_dpop_key_attack VULNERABLE against a rogue station.
3. TestImpostorRefusedNoQuote: VerifyPeer fails; a counting server proves no quote request went out.
4. TestLookalikeVerifiesButNoUplink: lookalike verifies; authority refuses uplink POLICY_REFUSED:tier.
5. TestCanary: honest station rejects both probes; rogue accepts, report says CANARY_ACCEPTED.
6. TestCleanPassReport: a clean pass -> verdict pass, chain head equal on both sides.
7. TestBatteryExitCode: Run over an honest station -> AllBlocked() true; over rogue -> false.
## Dependencies: none new.

## Review (hostile pass, findings fixed)
1. HIGH flipSig flipped base64 *characters*, so ~1/4 of forged signatures became
   undecodable base64 -> MANDATE_PARSE_ERROR not MANDATE_REJECTED:signature, so an
   honest station read VULNERABLE nondeterministically. Fixed: decode with
   jose.B64Decode, XOR the last two *bytes*, re-encode. TestBatteryHonest/TestCanaryLive
   now pass -count=5.
2. HIGH auditor role was dead code: buildRole never dispatched cfg.Role=="auditor".
   Fixed: added the dispatch after spacecraft.
3. MEDIUM canaryReport called any Vulnerable verdict CANARY_ACCEPTED, conflating a
   wrong-code rejection with acceptance. Fixed: require r.Observed == "".
4. MEDIUM auditor station keys were keyed by the auditor's own card version, so a
   version skew broke the lookup. Fixed: key by ANS host via ansHost().
5. LOW dpop_binding passed when the receipt could not be verified (rerr != nil).
   Fixed: gate on rerr == nil.
6. LOW AllBlocked(nil) returned true -> a battery that ran nothing could pass the
   gate. Fixed: empty set is not "all blocked".
7. LOW Canary did not emit bus events. Fixed: emit each result.
