// Database tab: lists a run database's tables, reads rows and runs one
// statement through serve's harness.db. Reads are aiman_ro (postgres) or a
// read-only file (SQLite); a write runs as the owner after a confirm. Every
// value from serve is set as text.
"use strict";

(function () {
  const C = window.aimanConsole;
  if (!C) return;
  const { api, el, $, showError } = C;

  const ROLE_TEXT = {
    aiman_ro: "Read as aiman_ro, a read-only role",
    read_only: "Read from the file, opened read-only",
    owner: "Written as the database owner",
  };

  const d = { run: null, dbs: [], shown: false, loaded: "", table: "", busy: false };

  const service = () => $("db-service").value;

  function setStatus(text) { $("db-status").textContent = text || ""; }

  function setRole(role) {
    const r = $("db-role");
    r.hidden = !role;
    r.dataset.role = role || "";
    r.textContent = ROLE_TEXT[role] || role || "";
  }

  function update(state) {
    const run = state && state.run;
    const ready = !!(run && run.state === "ready");
    const dbs = (run && run.databases) || [];
    $("db-empty").hidden = dbs.length > 0 && ready;
    $("db-empty").textContent = !run ? "No run yet." :
      dbs.length === 0 ? "This scenario has no database. A service with database: {kind: postgres} or {kind: sqlite} shows here." :
      "The database is readable once the run is ready.";
    $("db").hidden = !(dbs.length > 0 && ready);
    const id = run ? run.id : null;
    const sig = JSON.stringify(dbs);
    if (id !== d.run || sig !== JSON.stringify(d.dbs)) {
      d.run = id;
      d.dbs = dbs;
      d.loaded = "";
      $("db-service").replaceChildren(...dbs.map((x) => el("option", { value: x.service, text: x.service + " (" + x.kind + ")" })));
      $("db-tables").replaceChildren();
      $("db-result").replaceChildren();
      setRole("");
      setStatus("");
    }
    if (d.shown && ready) loadTables(false);
  }

  function shown(on) {
    d.shown = on;
    if (on) loadTables(false);
  }

  async function loadTables(force) {
    const svc = service();
    if (!d.run || !svc || d.busy) return;
    const key = d.run + "/" + svc;
    if (!force && d.loaded === key) return;
    d.loaded = key;
    d.busy = true;
    setStatus("Loading tables");
    try {
      const q = new URLSearchParams({ run: d.run, service: svc, op: "tables" });
      const res = await api("/api/db?" + q.toString());
      setRole(res.role);
      renderTables(res.tables || []);
      setStatus((res.tables || []).length ? "" : "No tables yet.");
    } catch (e) {
      d.loaded = "";
      setStatus("");
      showError(e);
    }
    d.busy = false;
  }

  function renderTables(tables) {
    $("db-tables").replaceChildren(...tables.map((t) => {
      const b = el("button", { type: "button", class: "btn", text: t });
      b.setAttribute("aria-pressed", String(t === d.table));
      b.addEventListener("click", () => readRows(t));
      return el("li", {}, b);
    }));
  }

  async function readRows(table) {
    if (!d.run || d.busy) return;
    d.table = table;
    for (const b of $("db-tables").querySelectorAll("button")) b.setAttribute("aria-pressed", String(b.textContent === table));
    d.busy = true;
    setStatus("Reading " + table);
    try {
      const q = new URLSearchParams({ run: d.run, service: service(), op: "rows", table: table });
      renderResult(await api("/api/db?" + q.toString()));
    } catch (e) {
      setStatus("");
      showError(e);
    }
    d.busy = false;
  }

  async function runQuery(ev) {
    ev.preventDefault();
    const sql = $("db-sql").value.trim();
    if (!d.run || !sql || d.busy) return;
    const write = $("db-write").checked;
    if (write && !confirm("Run this statement as the database owner on run " + d.run + "? It changes the data the tester sees, and is recorded in the session's ledger by its hash.\n\n" + sql)) return;
    d.busy = true;
    $("db-run").disabled = true;
    setStatus(write ? "Writing" : "Running");
    try {
      const res = await api("/api/db/query", { run: d.run, service: service(), sql: sql, write: write });
      renderResult(res);
      if (write) {
        $("db-write").checked = false;
        loadTables(true);
      }
    } catch (e) {
      // The last result would read as this statement's.
      $("db-result").replaceChildren();
      setStatus("");
      showError(e);
    }
    $("db-run").disabled = false;
    d.busy = false;
  }

  function renderResult(res) {
    setRole(res.role);
    const out = $("db-result");
    const redacted = new Set(res.redacted_columns || []);
    if (res.columns && res.columns.length) {
      const head = el("tr", {}, ...res.columns.map((c) => el("th", { scope: "col", class: redacted.has(c) ? "redacted" : "", text: c })));
      const body = (res.rows || []).map((row) => el("tr", {}, ...row.map((cell, i) => {
        if (cell === null) return el("td", { class: "null", text: "NULL" });
        return el("td", { class: redacted.has(res.columns[i]) ? "redacted" : "", text: cell });
      })));
      out.replaceChildren(el("table", {}, el("thead", {}, head), el("tbody", {}, ...body)));
    } else if (res.output && res.output.length) {
      out.replaceChildren(el("pre", { text: res.output.join("\n") }));
    } else {
      out.replaceChildren(el("pre", { text: res.tag || "No rows." }));
    }
    const notes = [];
    if (res.columns && res.columns.length) notes.push((res.rows || []).length + " rows");
    if (res.truncated) notes.push("cut at " + res.limit);
    if (redacted.size) notes.push("hidden columns: " + [...redacted].join(", "));
    if (res.redacted) notes.push(res.redacted + " values redacted");
    setStatus(notes.join(" · "));
  }

  function start() {
    $("db-service").addEventListener("change", () => {
      d.table = "";
      $("db-result").replaceChildren();
      loadTables(true);
    });
    $("db-refresh").addEventListener("click", () => loadTables(true));
    $("db-query").addEventListener("submit", runQuery);
    $("db-sql").addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) runQuery(e);
    });
  }

  start();
  window.aimanDB = { update: update, shown: shown };
})();
