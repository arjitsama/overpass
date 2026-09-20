// Minimal DOM shim for the Node replay test (web/dom_test.js). No npm install:
// it implements just the sliver of the DOM that app.js touches, so app.js runs
// unchanged and we can assert on the resulting tree. Not a browser — only the
// behaviour under test.
"use strict";

function Node(tag) {
  this.tag = tag;
  this.children = [];
  this._html = "";
  this.textContent = "";
  this.attrs = {};
  this.hidden = false;
  this.className = "";
  this.scrollTop = 0;
}
Node.prototype.appendChild = function (c) { this.children.push(c); return c; };
Node.prototype.removeChild = function (c) {
  this.children = this.children.filter(function (x) { return x !== c; });
};
Node.prototype.setAttribute = function (k, v) { this.attrs[k] = String(v); };
Node.prototype.getAttribute = function (k) {
  return Object.prototype.hasOwnProperty.call(this.attrs, k) ? this.attrs[k] : null;
};
Node.prototype.addEventListener = function () {};
Node.prototype.focus = function () { module.exports.focused = this; };
Node.prototype.querySelector = function (sel) {
  if (sel === ".empty-row") {
    for (var i = 0; i < this.children.length; i++) {
      if (this.children[i].className === "empty-row") { return this.children[i]; }
    }
  }
  return null;
};
Object.defineProperty(Node.prototype, "innerHTML", {
  get: function () { return this._html; },
  set: function (v) { this._html = String(v); }
});
Object.defineProperty(Node.prototype, "scrollHeight", { get: function () { return 0; } });

// The ids app.js looks up, pre-registered like the real index.html would supply.
var IDS = [
  "sat-name", "norad-id", "contrast-toggle", "live-status",
  "btn-demo", "btn-verify", "btn-battery", "btn-refresh-agents",
  "demo-err", "verify-err", "battery-err", "agents-err", "compromise-err",
  "ap-station", "ap-countdown", "ap-countdown-a11y", "ap-state", "ap-token", "ap-ack",
  "schedule-body", "agents-body", "agents-note", "agents-checked",
  "battery-body", "battery-recorded-body", "battery-results-h", "battery-live-summary",
  "fraud-line", "hero-godaddy",
  "event-log", "alert-region", "alert-dismiss", "replay-banner",
  "verify-card",
  "verify-section",
  "btn-verify-impostor"
];

var registry = {};
IDS.forEach(function (id) { registry[id] = new Node("div"); });

var document = {
  documentElement: new Node("html"),
  getElementById: function (id) { return registry[id] || null; },
  createElement: function (tag) { return new Node(tag); }
};

// Install onto globalThis so app.js (which targets globalThis under Node) sees it.
globalThis.document = document;
globalThis.addEventListener = function () {};
// Deliberately leave setInterval/EventSource/fetch/matchMedia unset: app.js
// guards on them, so countdown renders once and no live wiring runs.

module.exports = { Node: Node, document: document, registry: registry, focused: null };

// Concatenated innerHTML of a container's row children — what a screen reader
// would read out of the rendered table.
module.exports.rowsHTML = function (id) {
  var n = registry[id];
  return n.children.map(function (c) { return c.innerHTML; }).join("\n");
};
module.exports.alertText = function () {
  return registry["alert-region"].children.map(function (c) { return c.textContent; }).join(" | ");
};
