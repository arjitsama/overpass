// Shared dashboard runtime: escaping, status icons, theme selection and the
// server-sent-events client. Loaded before app.js by every dashboard page, so
// the live connection behaves identically wherever it is mounted. No framework,
// no build step, no external requests.
(function (global) {
  "use strict";

  function esc(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // Hostnames wrap at their dots; <wbr> gives the browser a break opportunity
  // without letting it split a label letter by letter.
  function hostHTML(h) {
    return esc(h).split(".").join(".<wbr>");
  }

  // A distinct shape per status so meaning never rests on colour alone.
  var ICONS = {
    ok: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M6.2 11.3 3 8.1l1.1-1.1 2.1 2.1 5-5L12.3 5z"/></svg>',
    bad: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M4.5 3.4 8 6.9l3.5-3.5 1.1 1.1L9.1 8l3.5 3.5-1.1 1.1L8 9.1l-3.5 3.5-1.1-1.1L6.9 8 3.4 4.5z"/></svg>',
    warn: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 1.5 15 14H1zm-.8 4.2v4h1.6v-4zm0 5.2v1.6h1.6v-1.6z"/></svg>',
    pending: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 1a7 7 0 1 0 0 14A7 7 0 0 0 8 1zm0 2v5l3.5 2-.8 1.3L7 10.5V3z"/></svg>',
    cut: '<svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path fill="currentColor" d="M5 1h6l4 4v6l-4 4H5l-4-4V5zM4 7v2h8V7z"/></svg>'
  };

  function statusSpan(kind, text) {
    var cls = kind === "ok" ? "ok" : (kind === "bad" || kind === "cut") ? "bad"
      : kind === "warn" ? "warn" : "pending";
    var icon = ICONS[kind] || ICONS.pending;
    return '<span class="status ' + cls + '">' + icon + '<span class="label">' + esc(text) + "</span></span>";
  }

  // --- theme ---------------------------------------------------------------
  // Order: an explicit ?theme= (for axe runs and screenshots, never stored),
  // then the stored choice, then the OS preferences.
  function initTheme(doc, win) {
    var root = doc.documentElement;
    var q = null;
    try {
      var m = /[?&]theme=(light|dark|contrast)\b/.exec(String(win.location && win.location.search || ""));
      q = m && m[1];
    } catch (e) { q = null; }
    var stored = null;
    try { stored = win.localStorage.getItem("overpass-theme"); } catch (e) { stored = null; }
    var choice = q || stored;
    if (choice === "contrast" || choice === "dark") {
      root.setAttribute("data-theme", choice);
    } else if (choice === "light") {
      root.setAttribute("data-theme", "");
    } else if (win.matchMedia && win.matchMedia("(prefers-contrast: more)").matches) {
      root.setAttribute("data-theme", "contrast");
    } else if (win.matchMedia && win.matchMedia("(prefers-color-scheme: dark)").matches) {
      root.setAttribute("data-theme", "dark");
    }
    var toggle = doc.getElementById("contrast-toggle");
    if (!toggle) { return; }
    toggle.setAttribute("aria-pressed", root.getAttribute("data-theme") === "contrast" ? "true" : "false");
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

  // --- connection state ----------------------------------------------------
  // One small status element, announced once per change. Connection state is
  // never written to the alert region: a flapping network must not shout.
  var liveState = null;
  function setLive(doc, state) {
    if (state === liveState) { return; }
    liveState = state;
    var el = doc.getElementById("live-status");
    if (!el) { return; }
    el.className = "live-pill " + (state === "connected" ? "ok" : "pending");
    el.innerHTML = state === "connected"
      ? statusSpan("ok", "Live: connected")
      : statusSpan("pending", "Live: reconnecting…");
  }

  // connectEvents opens /events and keeps it open. EventSource reconnects on
  // its own while the connection merely stalls; when the browser gives up
  // (readyState CLOSED) we reopen ourselves with exponential backoff.
  function connectEvents(doc, win, onEvent) {
    if (typeof win.EventSource !== "function") { return function () {}; }
    var es = null, attempt = 0, timer = null, stopped = false;

    function open() {
      es = new win.EventSource("/events");
      es.onopen = function () { attempt = 0; setLive(doc, "connected"); };
      es.onmessage = function (m) {
        try { onEvent(JSON.parse(m.data)); } catch (e) { /* ignore malformed */ }
      };
      es.onerror = function () {
        setLive(doc, "reconnecting");
        if (es.readyState !== 2 /* CLOSED */ || stopped) { return; }
        es.close();
        var wait = Math.min(30000, 1000 * Math.pow(2, attempt)) + Math.floor(Math.random() * 250);
        attempt += 1;
        if (win.setTimeout) { timer = win.setTimeout(open, wait); }
      };
    }

    setLive(doc, "reconnecting");
    open();
    return function () {
      stopped = true;
      if (timer && win.clearTimeout) { win.clearTimeout(timer); }
      if (es) { es.close(); }
    };
  }

  var api = {
    esc: esc, hostHTML: hostHTML, ICONS: ICONS, statusSpan: statusSpan,
    initTheme: initTheme, connectEvents: connectEvents,
    setLive: function (state) { setLive(global.document, state); }
  };
  global.OverpassLive = api;
  if (typeof module !== "undefined" && module.exports) { module.exports = api; }
})(typeof window !== "undefined" ? window : globalThis);
