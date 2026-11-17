// Debug tab, Browser section: guidance for debugging the frontend in the
// tester's own DevTools through the forwarded app port. Serve says whether
// the app's scripts carry source maps; every value is set as text.
"use strict";

(function () {
  const C = window.aimanConsole;
  if (!C) return;
  const { el, $ } = C;

  const LABELS = {
    present: "Source maps found",
    absent: "No source maps",
    unknown: "Source maps not confirmed",
    checking: "Checking source maps",
  };

  let sig = "";

  function update(state) {
    const b = state && state.browser;
    $("dbg-browser").hidden = !b;
    if (!b) { sig = ""; return; }
    const next = JSON.stringify(b);
    if (next === sig) return;
    sig = next;

    const maps = $("brw-maps");
    maps.dataset.maps = b.sourcemaps;
    maps.textContent = LABELS[b.sourcemaps] || b.sourcemaps;
    maps.title = b.files && b.files.length ? "Read from " + b.files.join(", ") : "";

    const link = $("brw-url");
    if (b.url) {
      link.href = b.url;
      link.textContent = b.url;
    } else {
      link.removeAttribute("href");
      link.textContent = "the " + (b.port || "web") + " port (its forward is not open yet)";
    }

    $("brw-warn").hidden = !b.warning;
    $("brw-warn-text").textContent = b.warning || "";
    $("brw-fixes").replaceChildren(...(b.fixes || []).map((f) => el("li", { text: f })));

    $("brw-note").hidden = !b.note;
    $("brw-note").textContent = b.note || "";
  }

  window.aimanBrowser = { update: update };
})();
