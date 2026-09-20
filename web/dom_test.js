// Acceptance 4: replay the recorded event stream through the real app.js and
// assert the page renders the schedule, the live agents table (verified agents,
// the impostor rejected, every check by name), the recorded battery rows in the
// recorded table, and a session cut in the alert region.
// Self-contained: `node web/dom_test.js`, no npm install.
"use strict";

const fs = require("fs");
const path = require("path");
const assert = require("assert");

const shim = require("./domshim.js"); // installs globalThis.document etc.
const app = require("./app.js"); // exports { applyEvent, ... }

const fixturePath = path.join(__dirname, "testdata", "demo-events.json");
const fixtureText = fs.readFileSync(fixturePath, "utf8");
const events = JSON.parse(fixtureText);

// Truthfulness: the recorded fixture may not carry hosts we do not own, nor a
// trust tier or identity score — production runs no trust index.
[/example\.com/, /FIDUCIARY/, /READ_ONLY/, /"identity":\s*\d/, /"tier":/].forEach(function (bad) {
  assert.ok(!bad.test(fixtureText), "fixture must not contain " + bad);
});

// The fixture declares when it was recorded, and the server stamps every
// replayed event with it (cmd/agent/ui.go markRecorded). Replay the same way,
// so what the test renders is what the dashboard renders.
const recording = events.find(function (e) { return e.kind === "recording"; });
assert.ok(recording && recording.data.recorded_at, "fixture must declare recorded_at");
const recordedAt = recording.data.recorded_at;
events.forEach(function (e) {
  app.applyEvent({ kind: e.kind, subject: e.subject, reason: e.reason,
    data: Object.assign({}, e.data, { recorded: true, recorded_at: recordedAt }) });
});

// Hostnames are rendered with <wbr> break opportunities at each dot, so they
// wrap at label boundaries instead of mid-label.
const unwrap = (s) => s.replace(/<wbr>/g, "");

// Pass schedule: both booked passes rendered, tagged as recorded.
const schedule = unwrap(shim.rowsHTML("schedule-body"));
assert.ok(schedule.includes("gs-blacksburg.blacksburgbytes.club"), "schedule missing blacksburg");
assert.strictEqual(shim.registry["schedule-body"].children.length, 2, "want two schedule rows");
assert.ok(schedule.includes("Booked"), "schedule status not rendered");
assert.ok(/Recorded 2026-09-20/.test(schedule), "recorded rows must carry the recording date");
assert.ok(shim.rowsHTML("schedule-body").includes("<wbr>"), "hostnames must carry wrap opportunities");

// Agents: live verification results, no trust scores, every check by name.
const agents = unwrap(shim.rowsHTML("agents-body"));
assert.strictEqual(shim.registry["agents-body"].children.length, 4, "want four agent rows");
assert.ok(agents.includes("Verified with warnings"), "warned agents must say so, not a bare Verified");
assert.ok(/card_hash<\/span> Warn/.test(agents), "card_hash warn must be shown by name");
// The DANE outcome is shown by name in its own cell, never as a bare pass.
assert.ok(/<td class="nowrap">Verified<\/td>/.test(agents), "DANE outcome not rendered by name: " + agents.slice(0, 300));
assert.ok(/<td class="nowrap">Unknown<\/td>/.test(agents), "impostor DANE outcome missing");
assert.ok(/Failed: registered: no _ans-badge TXT record/.test(agents), "impostor rejection missing: " + agents.slice(0, 200));
assert.ok(!/<meter|Integrity \d|FIDUCIARY|READ_ONLY/.test(agents), "no trust-index scores may be rendered");
assert.ok(/trust index: not deployed/i.test(shim.registry["agents-note"].textContent), "trust-index note: " + shim.registry["agents-note"].textContent);
assert.ok(/operator allow-list \(flight rules\)/.test(shim.registry["agents-note"].textContent), "access basis missing");
// A replay overwrites the live table, so every row it draws — and the "last
// checked" line — must say it is recorded, not a live verification.
assert.ok(/Recorded 2026-09-20/.test(agents), "replayed agent rows must carry the Recorded tag");
assert.ok(/Recorded 2026-09-20.*not a live check/.test(shim.registry["agents-checked"].textContent),
  "agents-checked: " + shim.registry["agents-checked"].textContent);

// Active pass rendered, with the real hostname.
assert.ok(unwrap(shim.registry["ap-station"].innerHTML).includes("gs-blacksburg.blacksburgbytes.club"),
  "active pass station: " + shim.registry["ap-station"].innerHTML);

// Recorded battery rows go to the recorded table, never the live one.
const recordedBattery = shim.rowsHTML("battery-recorded-body");
assert.ok(recordedBattery.includes("replay_booking") && recordedBattery.includes("tamper_mandate"),
  "recorded battery rows missing");
assert.ok(recordedBattery.includes("Blocked"), "battery verdict not rendered");
assert.strictEqual(shim.registry["battery-body"].children.length, 0,
  "recorded rows must not appear in the live battery table");

// The live battery table is filled only from a recorded LIVE run.
app.renderBatteryLive({ ran_at: "2026-09-20T05:30:00Z", target: { station_host: "gs-blacksburg.blacksburgbytes.club" },
  blocked: 21, inconclusive: 2, vulnerable: 0, total: 23,
  results: [{ name: "forged_mandate", verdict: "BLOCKED", observed: "MANDATE_REJECTED:signature", detail: "" }] });
assert.ok(shim.rowsHTML("battery-body").includes("forged_mandate"), "live battery row missing");
assert.ok(/Last live run: 2026-09-20T05:30:00Z/.test(shim.registry["battery-live-summary"].textContent),
  "live battery summary: " + shim.registry["battery-live-summary"].textContent);
assert.ok(/21 of 23 blocked, 2 inconclusive/.test(shim.registry["battery-live-summary"].textContent),
  "live battery counts missing");

// Alert region carries the impostor rejection AND the session cut, deduped.
const alert = shim.alertText();
assert.ok(/Rejected gs-sva1bard-eu/.test(alert), "impostor rejection not in alert region: " + alert);
assert.ok(/Session cut/.test(alert), "session cut not in alert region: " + alert);
const before = shim.registry["alert-region"].children.length;
app.applyEvent({ kind: "rejection", subject: "gs-sva1bard-eu.blacksburgbytes.club",
  reason: "not registered in ANS", data: { agent: "gs-sva1bard-eu.blacksburgbytes.club", code: "CALLER_REJECTED" } });
assert.strictEqual(shim.registry["alert-region"].children.length, before,
  "an identical alert must not be repeated");

// GoDaddy's agent verdict renders as a summary card; the live region gets one
// sentence (no JSON, no hashes); the raw response sits in a collapsed details.
const okVerdict = JSON.stringify({ ans_registered: true, environment: "prod",
  ans_name: "ans://v0.1.0.gs-blacksburg.blacksburgbytes.club", ans_verified: true,
  evidence_states: { tl: "verified", dnssec: "verified", card: "observed", dnsid: "absent" },
  compatibility_verdict: { dimensions: { identity: { state: "pass" }, attestations: { state: "unable-to-check" } },
    can_traveler_transact: "yes" }, tl_badge_raw: { leafHash: "0d7449003ea1975b25c88837174417a1" } });
app.applyEvent({ kind: "verification", data: { host: "gs-blacksburg.blacksburgbytes.club", verdict: okVerdict } });
const card = shim.registry["verify-card"].innerHTML;
assert.ok(/GoDaddy's agent verified gs-blacksburg\.blacksburgbytes\.club/.test(card), "card heading: " + card.slice(0, 120));
assert.ok(/ANS registered/.test(card) && /Transparency log/.test(card) && /DNSSEC/.test(card), "card rows missing");
assert.ok(/not published \(optional\)/.test(card), "absent must read as neutral wording");
assert.ok(/not checked/.test(card), "unable-to-check must read as neutral wording");
assert.ok(/<details><summary>Raw response from agent\.webmesh\.ai<\/summary>/.test(card), "raw details missing");
assert.ok(/class="raw-scroll" tabindex="0" aria-label=/.test(card), "raw pre must be in a focusable scroll box");
assert.strictEqual(shim.registry["verify-section"].hidden, false, "verify section must be shown");
// Connection state and verdicts never reach the alert region.
const alert2 = shim.alertText();
assert.ok(!/GoDaddy/.test(alert2), "verification must not be written to the alert region: " + alert2);
assert.ok(!/[{}]|0d7449003ea/.test(alert2), "alert region must never carry JSON or hashes: " + alert2);

app.applyEvent({ kind: "verification", data: { host: "gs-sva1bard-eu.blacksburgbytes.club",
  verdict: JSON.stringify({ ans_registered: false, ans_verified: false, environment: "prod",
    evidence_states: { tl: "unreachable", dnssec: "observed", card: "unreachable/tls-error", dnsid: "absent" } }) } });
const card2 = shim.registry["verify-card"].innerHTML;
assert.ok(/could not verify gs-sva1bard-eu\.blacksburgbytes\.club/.test(card2), "impostor heading: " + card2.slice(0, 120));
assert.ok(/not reachable \(TLS\)/.test(card2), "tls-error wording");

console.log("dom_test: pass (" + events.length + " events replayed + 2 verifications)");
