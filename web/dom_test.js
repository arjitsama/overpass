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

console.log("dom_test: pass (" + events.length + " events replayed)");
