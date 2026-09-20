// Overpass dashboard (master plan §12). No framework, no build, no external
// requests. Renders from server-sent events and calls three Ops POST routes.
// applyEvent(ev) is the single render entry point, exported for the DOM test.
(function (global) {
  "use strict";

  function el(id) { return global.document.getElementById(id); }

  // --- status icons: inline SVG, aria-hidden; the text carries the meaning ---
  // A distinct shape per status so meaning never rests on colour alone.
  var ICONS = {
    ok: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M6.2 11.3 3 8.1l1.1-1.1 2.1 2.1 5-5L12.3 5z"/></svg>',
    bad: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M4.5 3.4 8 6.9l3.5-3.5 1.1 1.1L9.1 8l3.5 3.5-1.1 1.1L8 9.1l-3.5 3.5-1.1-1.1L6.9 8 3.4 4.5z"/></svg>',
    pending: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1zm0 2v5l3.5 2-.8 1.3L7 10.5V3z"/></svg>',
    cut: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M5 1h6l4 4v6l-4 4H5l-4-4V5zM4 7v2h8V7z"/></svg>'
  };

  function statusSpan(kind, text) {
    var cls = kind === "ok" ? "ok" : kind === "bad" ? "bad" : kind === "cut" ? "bad" : "pending";
    var icon = ICONS[kind] || ICONS.pending;
    return '<span class="status ' + cls + '">' + icon + '<span class="label">' + esc(text) + "</span></span>";
  }

  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function clearEmptyRow(tbody) {
    var empty = tbody.querySelector(".empty-row");
    if (empty) { tbody.removeChild(empty); }
  }

  // --- dimension cell: "Integrity 89 of 100" + a meter, or "no signal" ---
  function dimCell(name, value) {
    if (value == null) {
      return '<td><span class="no-signal">no signal registered</span></td>';
    }
    var v = Number(value);
    return '<td><span class="meter-cell"><span>' + esc(name) + " " + v + " of 100</span>" +
      '<meter min="0" max="100" value="' + v + '" aria-hidden="true"></meter></span></td>';
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
      "<td>" + esc(d.station) + "</td>" +
      "<td>" + esc(d.mode) + "</td>" +
      "<td>" + esc(d.aos) + "</td>" +
      "<td>" + esc(d.los) + "</td>" +
      "<td>" + esc(d.max_elevation_deg != null ? d.max_elevation_deg + "°" : "—") + "</td>" +
      "<td>" + esc(d.tier || "—") + "</td>" +
      "<td>" + status + "</td>";
    tbody.appendChild(tr);
  }

  function renderAgent(d) {
    var tbody = el("agents-body");
    if (!tbody) { return; }
    clearEmptyRow(tbody);
    var tr = global.document.createElement("tr");
    var verif = d.verified ? statusSpan("ok", "Verified")
      : statusSpan("bad", "Rejected" + (d.reason ? ": " + d.reason : ""));
    // Show the DANE outcome by name (Verified / Skipped / NoRecords / Mismatch),
    // never a bare pass. Skipped/NoRecords are warnings that still verify.
    if (d.dane) { verif += ' <span class="muted">DANE ' + esc(d.dane) + "</span>"; }
    tr.innerHTML =
      "<td>" + esc(d.name || d.ans) + "</td>" +
      "<td>" + verif + "</td>" +
      dimCell("Integrity", d.integrity) +
      dimCell("Identity", d.identity) +
      dimCell("Solvency", d.solvency) +
      dimCell("Behavior", d.behavior) +
      dimCell("Safety", d.safety) +
      "<td>" + esc(d.tier || "—") + "</td>";
    tbody.appendChild(tr);
  }

  function renderActivePass(d) {
    if (d.station != null) { setText("ap-station", d.station); }
    if (d.session_state != null) {
      var kind = d.session_state === "active" ? "ok" : d.session_state === "cut" ? "cut" : "pending";
      setHTML("ap-state", statusSpan(kind, d.session_state));
    }
    if (d.token_age_s != null) { setText("ap-token", d.token_age_s + " s"); }
    if (d.last_ack != null) { setText("ap-ack", d.last_ack); }
    if (d.countdown_s != null) { startCountdown(Number(d.countdown_s)); }
  }

  function renderBattery(d) {
    var tbody = el("battery-body");
    if (!tbody) { return; }
    clearEmptyRow(tbody);
    var tr = global.document.createElement("tr");
    var v = String(d.verdict || "").toUpperCase();
    var status = v === "BLOCKED" ? statusSpan("ok", "Blocked")
      : v === "VULNERABLE" ? statusSpan("bad", "Vulnerable")
      : statusSpan("pending", d.verdict || "Inconclusive");
    tr.innerHTML =
      "<td>" + esc(d.name) + "</td>" +
      "<td>" + status + "</td>" +
      "<td>" + esc(d.observed || "—") + "</td>" +
      "<td>" + esc(d.detail || "") + "</td>";
    tbody.appendChild(tr);
  }

  function renderVerification(d) {
    // A webmesh verdict for one station: log it and mark the agent row's note.
    logLine("verification", (d.host || "") + ": " + (d.verdict || d.result || ""));
    if (d.host && d.verdict) {
      alertMsg("GoDaddy's agent verified " + d.host + ": " + d.verdict);
    }
  }

  function alertMsg(msg) {
    var region = el("alert-region");
    if (!region) { return; }
    var p = global.document.createElement("p");
    p.textContent = msg;
    region.appendChild(p);
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

  function logLine(kind, text) {
    var logEl = el("event-log");
    if (!logEl) { return; }
    var line = global.document.createElement("div");
    line.className = "log-line";
    line.textContent = "[" + kind + "] " + text;
    logEl.appendChild(line);
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
      case "pass": renderPass(d); break;
      case "agent": renderAgent(d); break;
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
        logLine(ev.kind, [ev.agent, ev.subject, ev.result, ev.reason].filter(Boolean).join(" "));
    }
  }

  // --- theme ---------------------------------------------------------------
  function initTheme(doc, win) {
    var root = doc.documentElement;
    var stored = null;
    try { stored = win.localStorage.getItem("overpass-theme"); } catch (e) { stored = null; }
    if (stored === "contrast") {
      root.setAttribute("data-theme", "contrast");
    } else if (win.matchMedia && win.matchMedia("(prefers-contrast: more)").matches) {
      root.setAttribute("data-theme", "contrast");
    } else if (win.matchMedia && win.matchMedia("(prefers-color-scheme: dark)").matches) {
      root.setAttribute("data-theme", "dark");
    }
    var toggle = doc.getElementById("contrast-toggle");
    if (!toggle) { return; }
    var on = root.getAttribute("data-theme") === "contrast";
    toggle.setAttribute("aria-pressed", on ? "true" : "false");
    toggle.addEventListener("click", function () {
      var nowOn = root.getAttribute("data-theme") !== "contrast";
      // Turning contrast off restores the OS base theme (dark or light), not
      // an unconditional light, so a dark-mode user is not forced to light.
      var base = (win.matchMedia && win.matchMedia("(prefers-color-scheme: dark)").matches) ? "dark" : "";
      root.setAttribute("data-theme", nowOn ? "contrast" : base);
      toggle.setAttribute("aria-pressed", nowOn ? "true" : "false");
      try { win.localStorage.setItem("overpass-theme", nowOn ? "contrast" : "base"); } catch (e) { /* ignore */ }
    });
  }

  // --- routes --------------------------------------------------------------
  function post(path, body) {
    return global.fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body || {})
    }).then(function (r) {
      return r.text().then(function (t) {
        if (!r.ok) { throw new Error(t || r.status); }
        return t ? JSON.parse(t) : {};
      });
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
        alertMsg("Action failed: " + e.message);
      });
    });
  }

  function init() {
    var doc = global.document, win = global;
    if (!doc) { return; }
    initTheme(doc, win);

    wireButton("btn-demo", "demo-err", function () {
      showReplayBanner("Recorded replay: showing recorded demo data, not a live pass.");
      return post("/ui/run-demo-pass", {}).then(hideReplayBannerSoon);
    });
    wireButton("btn-verify", "verify-err", function () {
      return post("/ui/verify-station", {}).then(function (res) { renderVerification(res); });
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
          alertMsg(arming ? "Simulated compromise armed (test control): session cut, re-planning."
            : "Simulated compromise reset (test control).");
        }).catch(function (e) { alertMsg("Simulate compromise failed: " + e.message); });
      });
    }

    if (typeof win.EventSource === "function") {
      var es = new win.EventSource("/events");
      es.onmessage = function (m) {
        try { applyEvent(JSON.parse(m.data)); } catch (e) { /* ignore malformed */ }
      };
      es.onerror = function () { alertMsg("Live connection lost; retrying."); };
    }
  }

  var api = { applyEvent: applyEvent, init: init };
  global.Overpass = api;
  if (typeof module !== "undefined" && module.exports) { module.exports = api; }
  if (global.document && global.addEventListener) {
    global.addEventListener("DOMContentLoaded", init);
  }
})(typeof window !== "undefined" ? window : globalThis);
