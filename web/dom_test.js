// Acceptance 4: replay the recorded event stream through the real app.js and
// assert the page renders the schedule, the trust table (with an impostor
// rejection and inactive dimensions), a session cut in the alert region, and
// battery results. Self-contained: `node web/dom_test.js`, no npm install.
"use strict";

const fs = require("fs");
const path = require("path");
const assert = require("assert");

const shim = require("./domshim.js"); // installs globalThis.document etc.
const app = require("./app.js"); // exports { applyEvent, init }

const events = JSON.parse(fs.readFileSync(path.join(__dirname, "testdata", "demo-events.json"), "utf8"));
events.forEach(app.applyEvent);

// Pass schedule: both booked passes rendered.
const schedule = shim.rowsHTML("schedule-body");
assert.ok(schedule.includes("gs-blacksburg.example.com"), "schedule missing blacksburg");
assert.ok(schedule.includes("gs-awarua.example.com"), "schedule missing awarua");
assert.strictEqual(shim.registry["schedule-body"].children.length, 2, "want two schedule rows");
assert.ok(schedule.includes("Booked"), "schedule status not rendered");

// Agents and trust: rows, a real dimension figure, inactive dimensions labelled,
// the impostor shown Rejected, and the lookalike READ_ONLY.
const agents = shim.rowsHTML("agents-body");
assert.ok(agents.includes("Integrity 89 of 100"), "trust dimension text missing");
assert.ok(agents.includes("no signal registered"), "inactive dimension not labelled");
assert.ok(agents.includes("Verified"), "verified status missing");
assert.ok(agents.includes("Rejected"), "impostor rejection not in trust table");
assert.ok(agents.includes("READ_ONLY"), "lookalike tier missing");
assert.ok(agents.includes("<meter"), "meter element missing for a dimension");
assert.ok(/DANE Skipped/.test(agents), "DANE outcome not rendered by name");

// Active pass rendered.
assert.strictEqual(shim.registry["ap-station"].textContent, "gs-blacksburg.example.com", "active pass station");

// Battery results.
const battery = shim.rowsHTML("battery-body");
assert.ok(battery.includes("replay_booking") && battery.includes("tamper_mandate"), "battery rows missing");
assert.ok(battery.includes("Blocked"), "battery verdict not rendered");

// Alert region carries the impostor rejection AND the session cut.
const alert = shim.alertText();
assert.ok(/Rejected .*impostor/.test(alert), "impostor rejection not in alert region: " + alert);
assert.ok(/Session cut/.test(alert), "session cut not in alert region: " + alert);

// GoDaddy's agent verdict renders as a summary card; the live region gets one
// sentence (no JSON, no hashes); the raw response sits in a collapsed details.
const okVerdict = JSON.stringify({ ans_registered: true, environment: "prod",
  ans_name: "ans://v0.1.0.gs-blacksburg.example.com", ans_verified: true,
  evidence_states: { tl: "verified", dnssec: "verified", card: "observed", dnsid: "absent" },
  compatibility_verdict: { dimensions: { identity: { state: "pass" }, attestations: { state: "unable-to-check" } },
    can_traveler_transact: "yes" }, tl_badge_raw: { leafHash: "0d7449003ea1975b25c88837174417a1" } });
app.applyEvent({ kind: "verification", data: { host: "gs-blacksburg.example.com", verdict: okVerdict } });
const card = shim.registry["verify-card"].innerHTML;
assert.ok(/GoDaddy's agent verified gs-blacksburg\.example\.com/.test(card), "card heading: " + card.slice(0, 120));
assert.ok(/ANS registered/.test(card) && /Transparency log/.test(card) && /DNSSEC/.test(card), "card rows missing");
assert.ok(/not published \(optional\)/.test(card), "absent must read as neutral wording");
assert.ok(/not checked/.test(card), "unable-to-check must read as neutral wording");
assert.ok(/<details><summary>Raw response from agent\.webmesh\.ai<\/summary>/.test(card), "raw details missing");
assert.ok(/class="raw-scroll" tabindex="0" aria-label=/.test(card), "raw pre must be in a focusable scroll box");
assert.strictEqual(shim.registry["verify-section"].hidden, false, "verify section must be shown");
const alert2 = shim.alertText();
assert.ok(/GoDaddy's agent verified gs-blacksburg\.example\.com: registered in prod, transparency log verified, DNSSEC verified\./.test(alert2), "live sentence: " + alert2);
assert.ok(!/[{}]|0d7449003ea/.test(alert2), "live region must never carry JSON or hashes: " + alert2);
app.applyEvent({ kind: "verification", data: { host: "gs-sva1bard-eu.example.com",
  verdict: JSON.stringify({ ans_registered: false, ans_verified: false, environment: "prod",
    evidence_states: { tl: "unreachable", dnssec: "observed", card: "unreachable/tls-error", dnsid: "absent" } }) } });
const card2 = shim.registry["verify-card"].innerHTML;
assert.ok(/could not verify gs-sva1bard-eu\.example\.com/.test(card2), "impostor heading: " + card2.slice(0, 120));
assert.ok(/not reachable \(TLS\)/.test(card2), "tls-error wording");

console.log("dom_test: pass (" + events.length + " events replayed + 2 verifications)");
