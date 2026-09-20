# Phase 10 plan: LLM planner

Prompt: master plan §7 "Revision 3: LLM planner, bounded by policy". Ship as
"phase 10: llm planner".

## API lookup (hard rule 9, "do not rely on memory")
Fetched the current Anthropic Messages API tool-use docs (platform.claude.com):
POST https://api.anthropic.com/v1/messages; headers x-api-key, anthropic-version:
2023-06-01, content-type: application/json; request {model, max_tokens, system,
tools:[{name, description, input_schema}], tool_choice:{type:"auto",
disable_parallel_tool_use:true}, messages}; response content[] with tool_use
blocks {type,id,name,input} and stop_reason "tool_use"; reply with a user message
carrying tool_result {tool_use_id, content}. Latest model claude-sonnet-5 /
claude-opus-5. Client uses net/http only — no new dependency (rule 7).

## Files
- internal/planner: add Planner interface, Input, Greedy (adapts Schedule),
  Proposer + Proposal + Outcome; add Plan.Explanation and Plan.Source.
- internal/authority/proposer.go: Authority.Propose implements planner.Proposer
  (forwards to issue_mandate; a named refusal is a non-accepted Outcome).
- internal/llmplan: model.go (Model interface + wire types), client.go (real
  Anthropic client, key from ANTHROPIC_API_KEY / H5), tools.go (4 tool defs +
  system prompt), sanitize.go (structured-only views, 128-char caps), planner.go
  (tool loop, 10s timeout, greedy fallback, plain-language explanation).
- Tests + scripts/accept/phase-10.sh.

## Frozen-schema decision
schema.Quote has no note field and the wire schema is frozen (changing it needs a
human). The demo's "quote note" injection is modelled as station-supplied free
text in planner.Input.Notes (host -> text), which the greedy planner ignores and
the LLM planner never places in a prompt. No wire-schema change.

## Tools (structured fields only; Notes never read)
list_passes, get_trust{host}, get_pass_quote{host,aos}, propose_booking{host,aos}.
propose_booking forwards to planner.Proposer (the authority), which applies the
flight rules and the trust tier. The model proposes; policy disposes.

## Tests (one per acceptance criterion)
1. TestProposeLookalikeRefusedOnTier: fake model proposes the READ_ONLY lookalike
   for uplink -> real authority refuses POLICY_REFUSED:tier; nothing selected.
2. TestInjectionNeverReachesPrompt: poisoned Notes; assert every captured request
   (incl. tool-result turns) is free of the injection.
3. TestFallsBackToGreedy: error and timeout both fall back to greedy and book.
4. TestLiveAnomalyChangesPlan: real client + ANS_LIVE_LLM=1; anomaly context makes
   the LLM take the early pass where greedy takes the later high-elevation one;
   skipped otherwise.

## Risks / assumptions
- The real Proposer in production is an A2A client to the authority; here Propose
  is the in-process authority (faithful to "the authority refuses"). HTTP wiring
  of the planner into the ops role is out of scope (greedy isn't wired either).
- Live test is nondeterministic; it asserts the early pass is chosen, not exact
  wording.

## Review (hostile pass, findings fixed)
1. Test caller JKT must be a real RFC 7638 thumbprint (mandate.Validate rejected
   the placeholder); compute it with jose.Thumbprint.
2. A transient Propose error showed a pass as "not proposed"; now recorded with
   its reason so the plan is honest about what happened.
3. Confirmed: no model-facing string omits capStr; the fallback runs on the
   original context (not the timed-out one); maxTurns bounds the tool loop; the
   API key is read only from the environment; no new dependency added.
