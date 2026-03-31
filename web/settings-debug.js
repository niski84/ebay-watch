/**
 * Shared client debug log for Settings + main app (sessionStorage so it survives / → /settings).
 * Console: [ebay-watch][settings-debug]
 */
(function () {
  var STORAGE_KEY = "ebay-watch-debug-log-v1";
  var MAX = 80;
  var buffer = [];

  function load() {
    try {
      var raw = sessionStorage.getItem(STORAGE_KEY);
      if (!raw) return;
      var p = JSON.parse(raw);
      if (Array.isArray(p)) {
        buffer = p.slice(-MAX);
      }
    } catch (e) {}
  }

  function save() {
    try {
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(buffer));
    } catch (e) {}
  }

  function render() {
    var text = buffer.join("\n");
    var pre = document.getElementById("settings-debug-log");
    if (pre) {
      pre.textContent = text;
      pre.scrollTop = pre.scrollHeight;
    }
  }

  function log(message, detail) {
    var ts = new Date().toISOString();
    var line =
      detail !== undefined && detail !== null
        ? "[" +
          ts +
          "] " +
          message +
          " " +
          (typeof detail === "object" ? JSON.stringify(detail) : String(detail))
        : "[" + ts + "] " + message;
    buffer.push(line);
    while (buffer.length > MAX) buffer.shift();
    save();
    render();
    console.info("[ebay-watch][settings-debug]", line);
  }

  /**
   * @param {string} [reason]
   * @param {{ getSearchesCount?: () => number }} [opts]
   */
  async function snapshot(reason, opts) {
    opts = opts || {};
    var extra = {
      href: typeof location !== "undefined" ? location.href : "",
      userAgent: typeof navigator !== "undefined" ? navigator.userAgent : "",
    };
    if (typeof opts.getSearchesCount === "function") {
      try {
        extra.searchesCount = opts.getSearchesCount();
      } catch (e) {
        extra.searchesCountError = String(e && e.message ? e.message : e);
      }
    }
    log(reason || "snapshot", extra);
    try {
      var r = await fetch("/api/health", { method: "GET", cache: "no-store" });
      var text = await r.text();
      var parsed;
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = { raw: text.slice(0, 200) };
      }
      log("GET /api/health", { ok: r.ok, status: r.status, body: parsed });
    } catch (e) {
      log("GET /api/health failed", e && e.message ? e.message : String(e));
    }
  }

  /**
   * @param {{ getSearchesCount?: () => number }} [options]
   */
  function initUI(options) {
    options = options || {};
    load();
    render();
    var refreshId = options.refreshBtnId || "btn-settings-refresh-debug";
    var copyId = options.copyBtnId || "btn-settings-copy-debug";
    document.getElementById(refreshId)?.addEventListener("click", function () {
      snapshot("manual refresh", { getSearchesCount: options.getSearchesCount });
    });
    document.getElementById(copyId)?.addEventListener("click", async function () {
      var t = buffer.join("\n");
      try {
        await navigator.clipboard.writeText(t);
        if (typeof options.onToast === "function") options.onToast("Debug log copied");
        log("copy to clipboard", { chars: t.length });
      } catch (e) {
        console.error("[ebay-watch] copy debug failed", e);
        if (typeof options.onToast === "function") options.onToast(e.message || "Copy failed");
      }
    });
  }

  window.ebayWatchSettingsDebug = {
    log: log,
    snapshot: snapshot,
    initUI: initUI,
    getBufferText: function () {
      return buffer.join("\n");
    },
  };
})();
