// Overpass dashboard (master plan §12). No framework, no build, no external
// requests. Renders from server-sent events and the Ops JSON routes.
// applyEvent(ev) is the single render entry point, exported for the DOM test.
//
// Truthfulness rule: everything on this page is either live (from /ui/agents,
// /ui/battery-live, /ui/fraud-redteam or a live event) or carries a visible
// "Recorded" tag with the date it was recorded. Nothing in between.
(function (global) {
  "use strict";

  var live = global.OverpassLive || (typeof require === "function" ? require("./live.js") : null);
  var esc = live.esc, hostHTML = live.hostHTML, statusSpan = live.statusSpan;

  function el(id) { return global.document.getElementById(id); }

  function clearEmptyRow(tbody) {
    var empty = tbody.querySelector(".empty-row");
    if (empty) { tbody.removeChild(empty); }
  }

  function clearRows(tbody) {
    while (tbody.children.length) { tbody.removeChild(tbody.children[0]); }
  }

  // A recorded row says so, with the date it was recorded.
  function recordedTag(d) {
    if (!d || !d.recorded) { return ""; }
    var when = d.recorded_at ? " " + esc(d.recorded_at) : "";
    return ' <span class="tag recorded">Recorded' + when + "</span>";
  }

  // --- renderers ------------------------------------------------------------
  function renderPass(d) {
    var tbody = el("schedule-body");
    if (!tbody) { return; }
    clearEmptyRow(tbody);
    var tr = global.document.createElement("tr");
    var status = d.status === "booked" ? statusSpan("ok", "Booked")
      : d.status === "rejected" ? statusSpan("bad", "Rejected")
      : statusSpan("pending", d.status || "Pending");
    tr.innerHTML =
      "<td>" + hostHTML(d.station) + recordedTag(d) + "</td>" +
      '<td class="nowrap">' + esc(d.mode) + "</td>" +
      '<td class="nowrap">' + esc(d.aos) + "</td>" +
      '<td class="nowrap">' + esc(d.los) + "</td>" +
      '<td class="nowrap">' + esc(d.max_elevation_deg != null ? d.max_elevation_deg + "°" : "—") + "</td>" +
      '<td class="nowrap">' + status + "</td>";
    tbody.appendChild(tr);
  }

  // One chip per verification check. The check's name is always shown, so a
  // warn (card_hash: not registered) is visible by name, never a bare pass.
  function checkChips(checks) {
    if (!checks || !checks.length) { return '<span class="muted">no checks recorded</span>'; }
    var html = '<span class="chips">';
    for (var i = 0; i < checks.length; i++) {
      var c = checks[i], v = String(c.verdict || "").toLowerCase();
      var word = v === "pass" ? "Pass" : v === "warn" ? "Warn" : v === "fail" ? "Fail" : v === "skip" ? "Skipped" : esc(c.verdict);
      html += '<span class="chip ' + esc(v) + '"><span class="k">' + esc(c.name) + "</span> " + word + "</span>";
    }
    return html + "</span>";
  }

  function agentVerdictCell(a) {
    if (a.deployed === false) { return statusSpan("pending", "Not deployed"); }
    if (a.verified) {
      var warned = (a.checks || []).some(function (c) { return String(c.verdict).toLowerCase() === "warn"; });
      return statusSpan(warned ? "warn" : "ok", warned ? "Verified with warnings" : "Verified");
    }
    return statusSpan("bad", "Failed" + (a.reason ? ": " + a.reason : ""));
  }

  // renderAgents replaces the whole table from one live snapshot (GET /ui/agents
  // or the "agents" event the Ops poller publishes). There is no trust index in
  // production, so no scores and no tier are shown — the access decision is the
  // authority's flight rules, stated in words.
  function renderAgents(payload) {
    var tbody = el("agents-body");
    if (!tbody || !payload) { return; }
    var agents = payload.agents || [];
    // A recorded replay carries its stamp on the snapshot, not on each agent.
    // Push it down so every row it draws is labelled: a replay overwrites the
    // live table, and an unlabelled row would read as a live verification.
    var stamp = payload.recorded ? { recorded: true, recorded_at: payload.recorded_at } : null;
    clearRows(tbody);
    if (!agents.length) {
      var none = global.document.createElement("tr");
      none.className = "empty-row";
      none.innerHTML = '<td colspan="5">No agents checked yet.</td>';
      tbody.appendChild(none);
    }
    for (var i = 0; i < agents.length; i++) {
      var a = agents[i];
      var tr = global.document.createElement("tr");
      tr.innerHTML =
        "<td>" + hostHTML(a.host) + recordedTag(stamp || a) + "</td>" +
        '<td class="nowrap">' + esc(a.role || "—") + "</td>" +
        "<td>" + agentVerdictCell(a) + "</td>" +
        '<td class="nowrap">' + esc(a.dane || "—") + "</td>" +
        "<td>" + checkChips(a.checks) + "</td>";
      tbody.appendChild(tr);
    }
    var note = el("agents-note");
    if (note) {
      note.textContent = "Trust index: " + (payload.trust_index || "not deployed") + ". " +
        (payload.access_basis || "Uplink: operator allow-list (flight rules)") + ".";
    }
    var when = el("agents-checked");
    if (when) {
      when.textContent = stamp ? "Recorded " + payload.recorded_at + " — not a live check."
        : payload.checked_at ? "Last checked: " + payload.checked_at
          : "Not checked yet.";
    }
  }

  function renderActivePass(d) {
    if (d.station != null) { setHTML("ap-station", hostHTML(d.station) + recordedTag(d)); }
    if (d.session_state != null) {
      var kind = d.session_state === "active" ? "ok" : d.session_state === "cut" ? "cut" : "pending";
      setHTML("ap-state", statusSpan(kind, d.session_state));
    }
    if (d.token_age_s != null) { setText("ap-token", d.token_age_s + " s"); }
    if (d.last_ack != null) { setText("ap-ack", d.last_ack); }
    if (d.countdown_s != null) { startCountdown(Number(d.countdown_s)); }
  }

  function batteryRow(d) {
    var tr = global.document.createElement("tr");
    var v = String(d.verdict || "").toUpperCase();
    var status = v === "BLOCKED" ? statusSpan("ok", "Blocked")
      : v === "VULNERABLE" ? statusSpan("bad", "Vulnerable")
      : statusSpan("pending", d.verdict || "Inconclusive");
    tr.innerHTML =
      "<td>" + esc(d.name) + recordedTag(d) + "</td>" +
      '<td class="nowrap">' + status + "</td>" +
      '<td class="nowrap">' + esc(d.observed || "—") + "</td>" +
      "<td>" + esc(d.detail || "") + "</td>";
    return tr;
  }

  // Recorded battery rows go to their own table inside the Recorded replay
  // group; the live table above is never mixed with them.
  function renderBattery(d) {
    var tbody = el(d && d.recorded ? "battery-recorded-body" : "battery-body");
    if (!tbody) { return; }
    clearEmptyRow(tbody);
    tbody.appendChild(batteryRow(d));
  }

  // The last live run of bin/battery against production, as recorded in
  // docs/status/battery-live.json. Every row is shown, not a selection.
  function renderBatteryLive(payload) {
    var tbody = el("battery-body");
    if (!tbody || !payload) { return; }
    clearRows(tbody);
    var results = payload.results || [];
    for (var i = 0; i < results.length; i++) { tbody.appendChild(batteryRow(results[i])); }
    var head = el("battery-live-summary");
    if (!head) { return; }
    if (!results.length) {
      head.textContent = "No live run recorded yet.";
      return;
    }
    var target = (payload.target && payload.target.station_host) || "the production station";
    head.textContent = "Last live run: " + (payload.ran_at || "unknown") + " against " + target +
      " — " + payload.blocked + " of " + payload.total + " blocked" +
      (payload.inconclusive ? ", " + payload.inconclusive + " inconclusive" : "") +
      (payload.vulnerable ? ", " + payload.vulnerable + " vulnerable" : "") + ".";
  }

  // GoDaddy's fraud agent: shown only from docs/status/fraud-redteam.md, with
  // that file's own status line. No verdict is claimed here.
  function renderFraud(payload) {
    var line = el("fraud-line");
    if (!line || !payload) { return; }
    line.textContent = payload.status_line || "No GoDaddy fraud-agent results recorded.";
  }

  // Neutral wording for GoDaddy's evidence states: "absent" and
  // "unable-to-check" are not failures.
  var STATE_WORDS = {
    "verified": ["ok", "verified"], "pass": ["ok", "pass"], "yes": ["ok", "yes"], "true": ["ok", "yes"],
    "fail": ["bad", "fail"], "no": ["bad", "no"], "false": ["bad", "no"],
    "observed": ["pending", "observed, not validated"], "absent": ["pending", "not published (optional)"],
    "unable-to-check": ["pending", "not checked"], "unknown": ["pending", "unknown"],
    "unreachable": ["pending", "not reachable"], "unreachable/tls-error": ["pending", "not reachable (TLS)"]
  };
  function stateWord(v) {
    var k = String(v === undefined || v === null ? "unknown" : v).toLowerCase();
    return STATE_WORDS[k] || ["pending", k];
  }
  function verifyRow(label, value, plain) {
    var w = plain ? ["pending", String(value || "—")] : stateWord(value);
    return "<div><dt>" + esc(label) + "</dt><dd>" + (plain ? esc(w[1]) : statusSpan(w[0], w[1])) + "</dd></div>";
  }

  // The verdict from GoDaddy's agent as a summary card: heading, a definition
  // list of icon + text rows, one sentence for the live region, and the raw
  // response collapsed in <details>. Hashes and JSON never reach a live region.
  function renderVerification(d) {
    var host = d.host || "", raw = d.verdict || d.result || "", v = null;
    if (typeof raw === "string") { try { v = JSON.parse(raw); } catch (e) { v = null; } }
    else if (raw && typeof raw === "object") { v = raw; raw = JSON.stringify(v, null, 2); }
    var ok = !!(v && v.ans_verified);
    var heading = (ok ? "GoDaddy's agent verified " : "GoDaddy's agent could not verify ") + host;
    var rows = "";
    if (v) {
      var ev = v.evidence_states || {}, cv = (v.compatibility_verdict || {}).dimensions || {};
      rows += verifyRow("ANS registered", v.ans_registered ? "yes" : "no");
      rows += verifyRow("Environment", v.environment, true);
      rows += verifyRow("ANS name", v.ans_name || "none", true);
      rows += verifyRow("Transparency log", ev.tl);
      rows += verifyRow("DNSSEC", ev.dnssec);
      rows += verifyRow("Agent card", ev.card);
      rows += verifyRow("DNSid", ev.dnsid);
      ["identity", "protocol", "auth", "attestations"].forEach(function (k) {
        if (cv[k]) { rows += verifyRow("Compatibility: " + k, cv[k].state); }
      });
      if (v.compatibility_verdict && v.compatibility_verdict.can_traveler_transact !== undefined) {
        rows += verifyRow("Can transact", v.compatibility_verdict.can_traveler_transact);
      }
    }
    var html = "<h3>" + esc(heading) + "</h3>" +
      (rows ? '<dl class="verify-rows">' + rows + "</dl>" : "<p>No structured verdict was returned.</p>") +
      '<details><summary>Raw response from agent.webmesh.ai</summary>' +
      '<div class="raw-scroll" tabindex="0" aria-label="Raw response from agent.webmesh.ai, scrollable"><pre>' +
      esc(typeof raw === "string" ? raw : JSON.stringify(raw)) + "</pre></div></details>";
    setHTML("verify-card", html);
    var sec = el("verify-section");
    if (sec) { sec.hidden = false; if (sec.focus) { sec.focus(); } }
    // One sentence, no JSON, for the live region and the log.
    var sentence = heading;
    if (v) {
      var ev2 = v.evidence_states || {};
      sentence += ": " + (v.ans_registered ? "registered in " + (v.environment || "ANS") : "not registered") +
        ", transparency log " + stateWord(ev2.tl)[1] + ", DNSSEC " + stateWord(ev2.dnssec)[1] + ".";
    }
    logLine("verification", sentence);
    setText("hero-godaddy", ok ? "Verified" : "Not verified");
  }

  // --- alerts ---------------------------------------------------------------
  // Session cuts and rejections only, never connection state. Repeats are
  // dropped and the region is capped, so a flapping condition cannot bury the
  // page under identical paragraphs.
  // #alert-region holds paragraphs only; the dismiss button is a sibling, so
  // the cap below cannot evict it. A message already on show is never repeated,
  // however often its condition recurs.
  var ALERT_MAX = 3;
  function alertMsg(msg) {
    var region = el("alert-region");
    if (!region) { return; }
    for (var i = 0; i < region.children.length; i++) {
      if (region.children[i].textContent === msg) { return; }
    }
    while (region.children.length >= ALERT_MAX) { region.removeChild(region.children[0]); }
    var p = global.document.createElement("p");
    p.textContent = msg;
    region.appendChild(p);
  }

  function wireDismiss() {
    var btn = el("alert-dismiss");
    var region = el("alert-region");
    if (!btn || !region) { return; }
    btn.addEventListener("click", function () {
      while (region.children.length) { region.removeChild(region.children[0]); }
      btn.hidden = true;
    });
  }

  // Recorded-replay banner: visible and announced (role=status) while a recorded
  // control is playing, so no one mistakes recorded data for a live pass.
  function showReplayBanner(msg) {
    var b = el("replay-banner");
    if (!b) { return; }
    b.textContent = "▶ " + msg;
    b.hidden = false;
  }
  function hideReplayBannerSoon() {
    var b = el("replay-banner");
    if (!b) { return; }
    if (global.setTimeout) {
      global.setTimeout(function () { b.hidden = true; b.textContent = ""; }, 6000);
    }
  }

  var LOG_MAX = 200;
  function logLine(kind, text) {
    var logEl = el("event-log");
    if (!logEl) { return; }
    var line = global.document.createElement("div");
    line.className = "log-line";
    line.textContent = "[" + kind + "] " + text;
    logEl.appendChild(line);
    while (logEl.children.length > LOG_MAX) { logEl.removeChild(logEl.children[0]); }
    logEl.scrollTop = logEl.scrollHeight;
  }

  function setText(id, text) { var e = el(id); if (e) { e.textContent = text; } }
  function setHTML(id, html) { var e = el(id); if (e) { e.innerHTML = html; } }

  // --- countdown: ticking digits aria-hidden; announce at each minute, 30, 10 -
  var countdownTimer = null;
  function startCountdown(seconds) {
    if (countdownTimer && global.clearInterval) { global.clearInterval(countdownTimer); }
    var remaining = seconds;
    render();
    if (!global.setInterval) { return; }
    countdownTimer = global.setInterval(function () {
      remaining -= 1;
      if (remaining < 0) { global.clearInterval(countdownTimer); return; }
      render();
    }, 1000);
    function render() {
      var m = Math.floor(remaining / 60), s = remaining % 60;
      setText("ap-countdown", pad(m) + ":" + pad(s));
      if (remaining % 60 === 0 || remaining === 30 || remaining === 10) {
        announceCountdown(remaining);
      }
    }
  }
  function announceCountdown(remaining) {
    var msg;
    if (remaining <= 0) { msg = "Pass window open"; }
    else if (remaining < 60) { msg = remaining + " seconds to acquisition"; }
    else { msg = Math.round(remaining / 60) + " minutes to acquisition"; }
    setText("ap-countdown-a11y", msg);
  }
  function pad(n) { return (n < 10 ? "0" : "") + n; }

  // --- the single event entry point ----------------------------------------
  function applyEvent(ev) {
    if (!ev || !ev.kind) { return; }
    var d = ev.data || {};
    switch (ev.kind) {
      case "recording":
        showReplayBanner("Recorded replay of " + (d.recorded_at || "an earlier run") + " — not a live pass.");
        break;
      case "pass": renderPass(d); break;
      case "agents": renderAgents(d); break;
      case "active_pass": renderActivePass(d); break;
      case "battery": renderBattery(d); break;
      case "verification": renderVerification(d); break;
      case "session_cut":
        setHTML("ap-state", statusSpan("cut", "Session cut"));
        alertMsg("Session cut" + (d.station ? " for " + d.station : "") +
          (ev.reason || d.reason ? ": " + (ev.reason || d.reason) : ""));
        logLine("session_cut", (d.station || "") + " " + (ev.reason || d.reason || ""));
        break;
      case "rejection":
        alertMsg("Rejected " + (d.agent || ev.subject || "") +
          (ev.reason || d.reason ? ": " + (ev.reason || d.reason) : "") +
          (d.code ? " (" + d.code + ")" : ""));
        logLine("rejection", (d.agent || ev.subject || "") + " " + (d.code || ""));
        break;
      default:
        // verify_check events name the check so a warn (e.g. card_hash: not
        // registered) is visible by name, never a bare verdict.
        logLine(ev.kind, [ev.agent, ev.subject, d && d.check, ev.result, ev.reason].filter(Boolean).join(" "));
    }
    var btn = el("alert-dismiss");
    if (btn && el("alert-region") && el("alert-region").children.length) { btn.hidden = false; }
  }

  // --- routes --------------------------------------------------------------
  function post(path, body) {
    return global.fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {})
    }).then(readJSON);
  }

  function get(path) { return global.fetch(path).then(readJSON); }

  function readJSON(r) {
    return r.text().then(function (t) {
      if (!r.ok) { throw new Error(t || r.status); }
      return t ? JSON.parse(t) : {};
    });
  }

  function wireButton(id, errId, run) {
    var btn = el(id);
    if (!btn) { return; }
    btn.addEventListener("click", function () {
      var errEl = el(errId);
      if (errEl) { errEl.hidden = true; errEl.textContent = ""; }
      run().catch(function (e) {
        if (errEl) { errEl.hidden = false; errEl.textContent = "Failed: " + e.message; }
      });
    });
  }

  function loadAgents() {
    return get("/ui/agents").then(renderAgents).catch(function () { /* shown as "not checked yet" */ });
  }

  function init() {
    var doc = global.document, win = global;
    if (!doc) { return; }
    live.initTheme(doc, win);
    wireDismiss();

    loadAgents();
    get("/ui/battery-live").then(renderBatteryLive).catch(function () {
      setText("battery-live-summary", "No live run recorded yet.");
    });
    get("/ui/fraud-redteam").then(renderFraud).catch(function () { /* line stays as authored */ });

    wireButton("btn-refresh-agents", "agents-err", function () {
      return post("/ui/refresh-agents", {}).then(loadAgents);
    });
    wireButton("btn-demo", "demo-err", function () {
      showReplayBanner("Recorded replay: showing recorded demo data, not a live pass.");
      return post("/ui/run-demo-pass", {}).then(hideReplayBannerSoon);
    });
    wireButton("btn-verify", "verify-err", function () {
      return post("/ui/verify-station", {}).then(function (res) { renderVerification(res); });
    });
    // The impostor is the same card one click away: gs-sva1bard-eu under this
    // dashboard's own base domain (ops.<domain> -> gs-sva1bard-eu.<domain>).
    wireButton("btn-verify-impostor", "verify-err", function () {
      var parts = String((win.location && win.location.hostname) || "").split(".");
      var impostor = "gs-sva1bard-eu" + (parts.length > 1 ? "." + parts.slice(1).join(".") : ".localhost");
      return post("/ui/verify-station", { host: impostor }).then(function (res) { renderVerification(res); });
    });
    wireButton("btn-battery", "battery-err", function () {
      showReplayBanner("Recorded replay: showing recorded battery results, not a live run.");
      return post("/ui/run-battery", {}).then(function (res) {
        var results = (res && res.results) || [];
        results.forEach(renderBattery);
        var h = el("battery-results-h");
        if (h && h.focus) { h.focus(); } // move focus to results heading (§12)
        hideReplayBannerSoon();
      });
    });

    // Simulate compromise: a repeatable, resettable TEST control that cuts the
    // session and re-plans. Toggles between arm and reset.
    var compromise = el("btn-compromise");
    if (compromise) {
      compromise.addEventListener("click", function () {
        var arming = compromise.getAttribute("aria-pressed") !== "true";
        post("/ui/simulate-compromise", { on: arming }).then(function () {
          compromise.setAttribute("aria-pressed", arming ? "true" : "false");
          compromise.textContent = arming ? "Reset compromise" : "Simulate compromise";
        }).catch(function (e) {
          var errEl = el("compromise-err");
          if (errEl) { errEl.hidden = false; errEl.textContent = "Failed: " + e.message; }
        });
      });
    }

    live.connectEvents(doc, win, applyEvent);
  }

  var api = {
    applyEvent: applyEvent, init: init,
    renderAgents: renderAgents, renderBatteryLive: renderBatteryLive, renderVerification: renderVerification
  };
  global.Overpass = api;
  if (typeof module !== "undefined" && module.exports) { module.exports = api; }
  if (global.document && global.addEventListener) {
    global.addEventListener("DOMContentLoaded", init);
  }
})(typeof window !== "undefined" ? window : globalThis);
