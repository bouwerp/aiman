// EVM tab: recent transactions of the run's EVM service and one's call tree
// with decoded revert reasons. Serve reads the chain through the edge's
// read-only relay. Every value from serve is set as text, never as HTML.
"use strict";

(function () {
  const C = window.aimanConsole;
  if (!C) return;
  const { api, el, $, showError } = C;
  const REFRESH_MS = 5000;
  const HASH_RE = /^0x[0-9a-fA-F]{64}$/;

  let runID = "";
  let services = [];
  let shown = false;
  let loading = false;
  let selected = "";
  let timer = null;

  const short = (h) => (h && h.length > 14 ? h.slice(0, 8) + "…" + h.slice(-4) : h || "");
  const service = () => $("evm-service").value || services[0] || "";
  const q = (params) => Object.entries(params).filter(([, v]) => v !== "" && v != null)
    .map(([k, v]) => encodeURIComponent(k) + "=" + encodeURIComponent(v)).join("&");

  function update(state) {
    const run = state && state.run;
    const evm = (run && run.state === "ready" && run.evm) || [];
    $("tab-evm").hidden = evm.length === 0;
    $("evm-empty").hidden = evm.length > 0;
    $("evm").hidden = evm.length === 0;
    const id = run ? run.id : "";
    if (id !== runID || evm.join(",") !== services.join(",")) {
      runID = id;
      services = evm.slice();
      selected = "";
      const sel = $("evm-service");
      sel.replaceChildren(...services.map((s) => el("option", { value: s, text: s })));
      $("evm-service-row").hidden = services.length < 2;
      $("evm-txs").replaceChildren();
      $("evm-trace").replaceChildren();
      $("evm-status").textContent = "";
      if (evm.length === 0 && $("tab-evm").getAttribute("aria-selected") === "true") $("tab-run").click();
      if (shown && evm.length) loadTxs();
    }
  }

  function setShown(on) {
    shown = on;
    clearInterval(timer);
    timer = null;
    if (on && services.length) {
      loadTxs();
      timer = setInterval(() => { if (!loading) loadTxs(); }, REFRESH_MS);
    }
  }

  async function loadTxs() {
    if (!runID || !services.length) return;
    loading = true;
    try {
      const res = await api("/api/evm/txs?" + q({ run: runID, service: service(), blocks: $("evm-blocks").value }));
      renderTxs(res.evm);
    } catch (e) {
      $("evm-status").textContent = (e && e.message) || String(e);
    }
    loading = false;
  }

  function statusBadge(status) {
    return el("span", { class: "evm-badge", "data-status": status, text: status });
  }

  function renderTxs(list) {
    $("evm-status").textContent = "Chain " + list.chain_id + ", block " + list.head + ". " +
      list.txs.length + " transaction" + (list.txs.length === 1 ? "" : "s") + " in the last " + list.blocks +
      " block" + (list.blocks === 1 ? "" : "s") + (list.truncated ? " (newest 100 shown)." : ".");
    const ul = $("evm-txs");
    ul.replaceChildren();
    if (!list.txs.length) {
      ul.append(el("li", { class: "empty", text: "No transactions yet. Send one from the app or a setup step." }));
      return;
    }
    for (const tx of list.txs) {
      const target = tx.to ? short(tx.to) + (tx.selector ? " " + tx.selector : "") :
        "create" + (tx.contract ? " " + short(tx.contract) : "");
      const btn = el("button", { type: "button", class: "evm-tx", "aria-pressed": String(tx.hash === selected), title: tx.hash },
        statusBadge(tx.status),
        el("span", { class: "evm-hash", text: short(tx.hash) }),
        el("span", { class: "evm-meta", text: "#" + tx.block + " · " + short(tx.from) + " → " + target + " · gas " + tx.gas_used }));
      btn.addEventListener("click", () => loadTrace(tx.hash));
      ul.append(el("li", {}, btn));
    }
  }

  async function loadTrace(hash) {
    selected = hash;
    for (const b of document.querySelectorAll(".evm-tx")) b.setAttribute("aria-pressed", String(b.title === hash));
    const box = $("evm-trace");
    box.replaceChildren(el("p", { class: "hint", text: "Tracing " + short(hash) + "…" }));
    try {
      const res = await api("/api/evm/trace?" + q({ run: runID, service: service(), tx: hash }));
      renderTrace(res.trace);
    } catch (e) {
      box.replaceChildren();
      showError(e);
    }
  }

  function revertText(r) {
    if (!r) return "";
    switch (r.kind) {
      case "error": return "Error(\"" + r.message + "\")";
      case "panic": return "Panic(" + r.code + "): " + r.message;
      case "custom": return "custom error " + r.selector + " (" + r.bytes + " bytes)";
      case "raw": return "raw revert data " + r.data;
      default: return "no revert data";
    }
  }

  // failing marks frames on the path to a failure, which open by default.
  function failing(c) {
    return !!c.error || (c.calls || []).some(failing);
  }

  function frame(c, depth) {
    const d = el("details", { class: "evm-frame" });
    d.open = depth < 2 || failing(c);
    const label = [c.type, short(c.from) + " → " + (c.to ? short(c.to) : "new")];
    if (c.selector) label.push(c.selector);
    if (c.value) label.push(c.value + " wei");
    label.push("gas " + c.gas_used);
    const sum = el("summary", { "data-failed": String(!!c.error) }, el("span", { text: label.join(" · ") }));
    if (c.error) sum.append(el("span", { class: "evm-err", text: c.error + (c.revert ? ": " + revertText(c.revert) : "") }));
    d.append(sum);
    const dl = el("dl", { class: "evm-io" });
    const row = (k, v) => { if (v) dl.append(el("dt", { text: k }), el("dd", {}, el("code", { text: v }))); };
    row("from", c.from);
    row("to", c.to);
    row("input", c.input ? c.input + (c.input_bytes > (c.input.length - 2) / 2 ? " … (" + c.input_bytes + " bytes)" : "") : "");
    row("output", c.output ? c.output + (c.output_bytes > (c.output.length - 2) / 2 ? " … (" + c.output_bytes + " bytes)" : "") : "");
    if (dl.childElementCount) d.append(dl);
    if (c.calls && c.calls.length) {
      const ol = el("ol", { class: "evm-calls" });
      for (const sub of c.calls) ol.append(el("li", {}, frame(sub, depth + 1)));
      d.append(ol);
    }
    return d;
  }

  function renderTrace(t) {
    const box = $("evm-trace");
    box.replaceChildren();
    const head = el("div", { class: "evm-trace-head" }, statusBadge(t.status),
      el("code", { class: "evm-hash", text: t.tx }),
      el("span", { class: "evm-meta", text: "block " + t.block + " · gas " + t.gas_used + " · " + t.frames + " frame" + (t.frames === 1 ? "" : "s") +
        (t.contract ? " · created " + t.contract : "") }));
    box.append(head);
    if (t.revert) box.append(el("p", { class: "evm-revert", role: "status", text: "Reverted: " + revertText(t.revert) }));
    if (t.truncated) box.append(el("p", { class: "hint", text: "The tree is cut at 5000 frames." }));
    if (t.root) box.append(frame(t.root, 0));
  }

  function start() {
    $("tab-evm").addEventListener("click", () => setShown(true));
    for (const t of document.querySelectorAll('[role="tab"]')) {
      if (t.id !== "tab-evm") t.addEventListener("click", () => setShown(false));
    }
    $("evm-refresh").addEventListener("click", loadTxs);
    $("evm-service").addEventListener("change", () => { selected = ""; $("evm-trace").replaceChildren(); loadTxs(); });
    $("evm-blocks").addEventListener("change", loadTxs);
    $("evm-find").addEventListener("submit", (ev) => {
      ev.preventDefault();
      const h = $("evm-hash").value.trim();
      if (!HASH_RE.test(h)) { $("evm-hash").setCustomValidity("A transaction hash: 0x and 64 hex digits"); $("evm-hash").reportValidity(); return; }
      $("evm-hash").setCustomValidity("");
      loadTrace(h);
    });
  }

  start();
  window.aimanEVM = { update: update };
})();
