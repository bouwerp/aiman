// Network tab: the HTTP calls the run's app made through the edge proxy,
// read from serve (harness.capture), which redacts them. Every value is set
// as text.
"use strict";

(function () {
  const C = window.aimanConsole;
  if (!C) return;
  const { api, el, $ } = C;
  const POLL_MS = 2000;
  const KEEP = 1000;

  let runId = null;
  let after = 0;
  let flows = new Map();
  let paused = false;
  let timer = null;
  let sig = "";
  let info = null;
  const open = new Set();

  function reset(id) {
    runId = id;
    after = 0;
    flows = new Map();
    open.clear();
    info = null;
    sig = "";
    render();
  }

  function update(state) {
    const id = state && state.run ? state.run.id : null;
    if (id !== runId) reset(id);
  }

  function shown() { return !$("panel-net").hidden; }

  async function poll() {
    clearTimeout(timer);
    if (shown() && !paused && runId) {
      try {
        let more = true;
        for (let i = 0; more && i < 5; i++) {
          const q = new URLSearchParams({ run: runId, after: String(after), limit: "200" });
          const ans = await api("/api/capture?" + q.toString());
          if (ans.run !== runId) break;
          for (const f of ans.flows || []) flows.set(f.id, f);
          after = Math.max(after, ans.next || 0);
          info = ans;
          more = !!ans.more;
        }
        if (flows.size > KEEP) {
          const ids = Array.from(flows.keys()).sort((a, b) => a - b);
          for (const id of ids.slice(0, flows.size - KEEP)) { flows.delete(id); open.delete(id); }
        }
        status();
        render();
      } catch (e) {
        $("net-status").textContent = (e.code ? e.code + ": " : "") + (e.message || "");
      }
    }
    timer = setTimeout(poll, POLL_MS);
  }

  function status() {
    if (!info) { $("net-status").textContent = ""; return; }
    let t = (info.network === "online" ? "Online: calls go out through the edge proxy. " :
      "Offline: the edge answers from stubs, or 502. ") + info.total + " calls";
    if (info.dropped) t += ", " + info.dropped + " oldest dropped";
    if (info.redacted) t += ", " + info.redacted + " values redacted";
    t += ", updated " + new Date().toLocaleTimeString();
    $("net-status").textContent = t;
  }

  function matches(f, needle) {
    if (!needle) return true;
    const hay = [f.method, f.url, String(f.status || ""), f.outcome, f.error || ""].join(" ").toLowerCase();
    return hay.includes(needle);
  }

  function headerBlock(title, h) {
    const names = Object.keys(h || {}).sort();
    if (!names.length) return null;
    const lines = [];
    for (const n of names) for (const v of h[n]) lines.push(n + ": " + v);
    return [el("h4", { text: title }), el("pre", { class: "log", text: lines.join("\n") })];
  }

  function bodyBlock(title, body, bytes, cut, binary) {
    if (!bytes) return null;
    let note = bytes + " bytes";
    if (binary) note += ", not text";
    else if (cut) note += ", first " + body.length + " characters kept";
    const kids = [el("h4", { text: title + " (" + note + ")" })];
    if (body) kids.push(el("pre", { class: "log", text: body }));
    return kids;
  }

  function detail(f) {
    const d = el("div", { class: "net-detail", id: "net-d-" + f.id });
    if (f.error) d.append(el("p", { class: "net-error", text: f.error }));
    const facts = [f.proto || "", f.tls ? "TLS (run CA)" : "plain HTTP", f.client ? "from " + f.client : ""].filter(Boolean).join(", ");
    d.append(el("p", { class: "net-facts", text: facts }));
    for (const part of [
      headerBlock("Request headers", f.request_headers),
      bodyBlock("Request body", f.request_body, f.request_body_bytes, f.request_truncated, f.request_binary),
      headerBlock("Response headers", f.response_headers),
      bodyBlock("Response body", f.response_body, f.response_body_bytes, f.response_truncated, f.response_binary),
    ]) if (part) d.append(...part);
    return d;
  }

  function row(f) {
    const li = el("li", { class: "net-item" });
    const expanded = open.has(f.id);
    const label = f.status ? f.status + " " + f.outcome : f.outcome;
    const btn = el("button", { type: "button", class: "net-row", "aria-expanded": String(expanded), "aria-controls": "net-d-" + f.id },
      el("span", { class: "net-time", text: new Date(f.start).toLocaleTimeString() }),
      el("span", { class: "net-method", text: f.method }),
      el("span", { class: "net-badge", "data-outcome": f.outcome, text: label }),
      el("span", { class: "net-url", text: f.url }),
      el("span", { class: "net-dur", text: f.outcome === "pending" ? "" : Math.round(f.duration_ms || 0) + " ms" }));
    btn.addEventListener("click", () => {
      if (open.has(f.id)) open.delete(f.id); else open.add(f.id);
      sig = "";
      render();
    });
    li.append(btn);
    if (expanded) li.append(detail(f));
    return li;
  }

  function render() {
    const needle = $("net-filter").value.trim().toLowerCase();
    const list = Array.from(flows.values()).sort((a, b) => b.id - a.id).filter((f) => matches(f, needle));
    const next = needle + "|" + Array.from(open).join(",") + "|" + list.map((f) => f.id + f.outcome + (f.status || "")).join(",");
    if (next === sig) return;
    sig = next;
    $("net-empty").hidden = list.length > 0;
    $("net-empty").textContent = flows.size && needle ? "No call matches the filter." :
      "No HTTP calls yet. Calls the app makes through the edge proxy appear here.";
    $("net-list").replaceChildren(...list.map(row));
  }

  function start() {
    $("net-filter").addEventListener("input", () => render());
    $("net-pause").addEventListener("click", () => {
      paused = !paused;
      $("net-pause").setAttribute("aria-pressed", String(paused));
      $("net-pause").textContent = paused ? "Resume" : "Pause";
      if (!paused) poll();
    });
    $("net-clear").addEventListener("click", () => {
      flows = new Map();
      open.clear();
      sig = "";
      render();
    });
    $("tab-net").addEventListener("click", () => setTimeout(poll, 0));
    poll();
  }

  window.aimanNet = { update: update };
  start();
})();
