// Debug tab: a Debug Adapter Protocol client over the console's WebSocket
// bridge. It knows DAP only; the launch request comes from serve as it is,
// so another language's adapter needs no change here. Every value from the
// debugger or serve is set as text, never as HTML.
"use strict";

(function () {
  const C = window.aimanConsole;
  if (!C) return;
  const { api, el, $ } = C;
  const POLL_MS = 2000;
  const MAX_FILE = 2 << 20;
  const MAX_OUT = 64 << 10;
  const CLIENT_NAME = "aiman harness console";

  const d = {
    sessions: [], // DAP sessions, the first one's children after it
    active: null,
    stopped: null,
    frames: [],
    frameId: 0,
    runId: "",
    launch: null,
    info: null,
    shown: false,
    timer: null,
    autoTried: "",
    files: [],
    file: "",
    lines: [],
    current: 0,
    bps: new Map(), // relative path -> Set of lines
    verified: new Map(), // relative path -> Set of verified lines
    out: "",
  };

  const root = () => (d.launch && d.launch.source_root) || "/src";
  function relOf(path) {
    if (!path) return "";
    const r = root() + "/";
    return path.startsWith(r) ? path.slice(r.length) : "";
  }

  // ---- DAP transport ----
  // One session is one WebSocket bridge, which is one DAP connection at the
  // edge under this console's lease. Go has one session. js-debug opens the
  // Node process as a child session through the startDebugging reverse
  // request; the page answers it with another bridge, so every session
  // holds the same lease. Controls act on the active session: the one that
  // last stopped, else the newest child, else the first.
  function sessionOf(sess) {
    return sess || d.active || d.sessions[0] || null;
  }

  function send(command, args, sess) {
    const s = sessionOf(sess);
    return new Promise((resolve, reject) => {
      if (!s || s.ws.readyState !== WebSocket.OPEN) {
        reject({ message: "The debugger is not connected." });
        return;
      }
      const seq = s.seq++;
      s.pending.set(seq, { resolve, reject, command });
      s.ws.send(JSON.stringify({ seq: seq, type: "request", command: command, arguments: args || {} }));
    });
  }

  function reply(s, req, ok, message) {
    const msg = { seq: s.seq++, type: "response", request_seq: req.seq, command: req.command, success: ok };
    if (message) msg.message = message;
    s.ws.send(JSON.stringify(msg));
  }

  function onMessage(s, msg) {
    if (msg.type === "response") {
      const p = s.pending.get(msg.request_seq);
      if (!p) return;
      s.pending.delete(msg.request_seq);
      if (msg.success) p.resolve(msg);
      else p.reject({ code: msg.command, message: msg.message || (msg.body && msg.body.error && msg.body.error.format) || "failed" });
      return;
    }
    if (msg.type === "request") {
      if (msg.command === "startDebugging" && msg.arguments && msg.arguments.configuration) {
        const cfg = msg.arguments.configuration;
        openSession({ name: cfg.name || "child", request: msg.arguments.request || "attach", args: cfg, parent: s });
        reply(s, msg, true);
        return;
      }
      // runInTerminal needs a terminal the console does not have.
      reply(s, msg, false, "not supported by the harness console");
      return;
    }
    if (msg.type === "event") onEvent(s, msg.event, msg.body || {});
  }

  function onEvent(s, name, body) {
    switch (name) {
      case "initialized":
        configure(s);
        break;
      case "stopped":
        d.active = s;
        d.stopped = body;
        if (body.threadId) s.threadId = body.threadId;
        status("Paused: " + (body.description || body.reason || "stopped"));
        renderSessions();
        loadStack();
        break;
      case "continued":
        if (s === d.active) running();
        break;
      case "output":
        if (body.category !== "telemetry") out(body.output || "");
        break;
      case "breakpoint":
        if (body.breakpoint && body.breakpoint.source) markVerified(relOf(body.breakpoint.source.path), body.breakpoint);
        renderSource();
        break;
      case "terminated":
      case "exited":
        if (s.parent) { s.ws.close(); break; }
        status("The program ended" + (body.exitCode !== undefined ? " with exit code " + body.exitCode : "") + ". Connect again to restart it.");
        break;
    }
    renderControls();
  }

  // ---- sessions ----
  function connect() {
    if (d.sessions.length || !d.launch) return;
    openSession({ name: "main", request: d.launch.request || "launch", args: d.launch.arguments, parent: null });
  }

  function openSession(spec) {
    const ws = new WebSocket((location.protocol === "https:" ? "wss:" : "ws:") + "//" + location.host + "/api/dap",
      ["aiman-dap", "aiman-token." + C.token]);
    const s = { ws: ws, seq: 1, pending: new Map(), configured: false, threadId: 0, name: spec.name, parent: spec.parent };
    d.sessions.push(s);
    if (s.parent) d.active = s;
    status(s.parent ? "Opening " + s.name + "…" : "Connecting…");
    ws.onmessage = (ev) => {
      let msg;
      try { msg = JSON.parse(ev.data); } catch (_) { return; }
      onMessage(s, msg);
    };
    ws.onopen = async () => {
      try {
        await send("initialize", {
          clientID: "aiman-console", clientName: CLIENT_NAME, adapterID: spec.args.type || d.launch.lang,
          pathFormat: "path", linesStartAt1: true, columnsStartAt1: true, supportsVariableType: true,
          supportsStartDebuggingRequest: true, locale: navigator.language,
        }, s);
        if (!s.parent) status(d.launch.request === "attach" ? "Attaching…" : "Starting the program…");
        await send(spec.request, spec.args, s);
      } catch (e) {
        status((e && e.message) || "The debugger refused the session.");
        C.showError(e);
      }
    };
    ws.onclose = () => closed(s);
    renderSessions();
    renderControls();
  }

  // closed drops one session. The first session closing ends them all: the
  // children belong to it.
  function closed(s) {
    for (const p of s.pending.values()) p.reject({ message: "The debugger disconnected." });
    s.pending.clear();
    d.sessions = d.sessions.filter((x) => x !== s);
    if (d.active === s) {
      d.active = d.sessions[d.sessions.length - 1] || null;
      d.stopped = null;
      d.frames = [];
      d.current = 0;
    }
    if (!s.parent) {
      for (const x of d.sessions.slice()) x.ws.close();
      d.sessions = [];
      d.active = null;
      d.stopped = null;
      d.frames = [];
      d.current = 0;
      status("Not connected");
      renderAll();
      refresh();
      return;
    }
    renderSessions();
    renderAll();
  }

  function renderSessions() {
    const ul = $("dbg-sessions");
    const many = d.sessions.length > 1;
    $("dbg-sessions-h").hidden = !many;
    ul.hidden = !many;
    ul.replaceChildren();
    if (!many) return;
    for (const s of d.sessions) {
      const b = el("button", { type: "button", class: "frame", "aria-current": String(s === sessionOf()) },
        el("span", { class: "frame-name", text: s.name }),
        el("span", { class: "frame-where", text: s.parent ? "child session" : "attach" }));
      b.addEventListener("click", () => {
        d.active = s;
        d.stopped = null;
        d.frames = [];
        renderSessions();
        renderAll();
        loadStack();
      });
      ul.append(el("li", null, b));
    }
  }

  async function configure(s) {
    try {
      for (const rel of d.bps.keys()) await pushBreakpoints(rel, s);
      await send("configurationDone", {}, s);
      s.configured = true;
      if (s === sessionOf()) running();
    } catch (e) { C.showError(e); }
  }

  function running() {
    d.stopped = null;
    d.frames = [];
    d.current = 0;
    status("Running");
    renderStack();
    renderVars([]);
    renderSource();
  }

  async function threadId() {
    const s = sessionOf();
    if (!s) return 0;
    if (s.threadId) return s.threadId;
    const r = await send("threads", {}, s);
    const t = (r.body && r.body.threads) || [];
    s.threadId = t.length ? t[0].id : 0;
    return s.threadId;
  }

  async function step(command) {
    try {
      const tid = await threadId();
      if (command !== "pause") running();
      await send(command, { threadId: tid });
    } catch (e) { C.showError(e); }
  }

  async function loadStack() {
    try {
      const r = await send("stackTrace", { threadId: await threadId(), startFrame: 0, levels: 50 });
      d.frames = (r.body && r.body.stackFrames) || [];
      renderStack();
      if (d.frames.length) selectFrame(d.frames[0]);
    } catch (e) { C.showError(e); }
  }

  async function selectFrame(f) {
    d.frameId = f.id;
    renderStack();
    const rel = relOf(f.source && f.source.path);
    if (rel) await openFile(rel, f.line);
    else { d.current = 0; $("dbg-src-hint").textContent = "This frame is outside the run's source tree (" + ((f.source && f.source.path) || "no source") + ")."; }
    try {
      const r = await send("scopes", { frameId: f.id });
      const scopes = (r.body && r.body.scopes) || [];
      renderVars(scopes.map((s) => ({ name: s.name, value: "", variablesReference: s.variablesReference, expensive: s.expensive })));
    } catch (e) { C.showError(e); }
  }

  // ---- breakpoints ----
  // pushBreakpoints sets rel's breakpoints in one session, or in every
  // configured one; a line is verified when any session verified it.
  async function pushBreakpoints(rel, only) {
    const lines = Array.from(d.bps.get(rel) || []).sort((a, b) => a - b);
    const targets = only ? [only] : d.sessions.filter((x) => x.configured);
    const ok = only ? new Set(d.verified.get(rel) || []) : new Set();
    // Sessions answer on their own time (js-debug's parent session holds no
    // script), so one slow session must not hold back the others.
    const args = { source: { path: root() + "/" + rel, name: rel.split("/").pop() }, breakpoints: lines.map((l) => ({ line: l })) };
    const results = await Promise.allSettled(targets.map((t) => send("setBreakpoints", args, t)));
    for (const res of results) {
      if (res.status !== "fulfilled") continue;
      const r = res.value;
      for (const bp of (r.body && r.body.breakpoints) || []) if (bp.verified && bp.line) ok.add(bp.line);
    }
    if (results.length && results.every((res) => res.status === "rejected")) throw results[0].reason;
    d.verified.set(rel, ok);
    if (lines.length === 0) d.bps.delete(rel);
    renderSource();
    renderBps();
  }

  function markVerified(rel, bp) {
    if (!rel || !bp.line) return;
    const s = d.verified.get(rel) || new Set();
    if (bp.verified) s.add(bp.line); else s.delete(bp.line);
    d.verified.set(rel, s);
  }

  async function toggleBreakpoint(rel, line) {
    const s = d.bps.get(rel) || new Set();
    if (s.has(line)) s.delete(line); else s.add(line);
    d.bps.set(rel, s);
    renderSource();
    renderBps();
    if (anyConfigured()) {
      try { await pushBreakpoints(rel); } catch (e) { C.showError(e); }
    }
  }

  // ---- source ----
  async function openFile(rel, line) {
    if (rel !== d.file) {
      let data = "";
      let offset = 0;
      try {
        for (;;) {
          const q = new URLSearchParams({ run: d.runId, path: rel, offset: String(offset) });
          const page = await api("/api/file?" + q.toString());
          if (page.binary) { data = null; break; }
          data += page.data;
          offset = page.offset + new TextEncoder().encode(page.data).length;
          if (page.eof || offset >= MAX_FILE || page.data === "") break;
        }
      } catch (e) {
        $("dbg-src-hint").textContent = (e.code ? e.code + ": " : "") + (e.message || "");
        return;
      }
      d.file = rel;
      $("dbg-file").value = rel;
      d.lines = data === null ? null : data.replace(/\n$/, "").split("\n");
    }
    d.current = line || 0;
    renderSource();
    if (line) {
      const li = $("dbg-src").children[line - 1];
      if (li) li.scrollIntoView({ block: "center" });
    }
  }

  function renderSource() {
    const ol = $("dbg-src");
    ol.replaceChildren();
    if (!d.file) return;
    if (d.lines === null) {
      $("dbg-src-hint").textContent = d.file + " is a binary file.";
      return;
    }
    const set = d.bps.get(d.file) || new Set();
    const ok = d.verified.get(d.file) || new Set();
    const paused = d.stopped && d.frames.length && relOf(d.frames.find((f) => f.id === d.frameId)?.source?.path) === d.file;
    $("dbg-src-hint").textContent = d.file + ", " + d.lines.length + " lines. Click a line number to set or clear a breakpoint.";
    const frag = document.createDocumentFragment();
    d.lines.forEach((text, i) => {
      const n = i + 1;
      const gut = el("button", { type: "button", class: "gutter", text: String(n), "aria-pressed": String(set.has(n)),
        "aria-label": (set.has(n) ? "Clear breakpoint on line " : "Set breakpoint on line ") + n });
      if (set.has(n)) gut.dataset.bp = ok.has(n) ? "verified" : (anyConfigured() ? "unverified" : "pending");
      gut.addEventListener("click", () => toggleBreakpoint(d.file, n));
      const li = el("li", null, gut, el("code", { text: text || " " }));
      if (paused && n === d.current) li.dataset.current = "true";
      frag.append(li);
    });
    ol.append(frag);
  }

  function renderBps() {
    const ul = $("dbg-bps");
    ul.replaceChildren();
    for (const [rel, set] of d.bps) {
      for (const line of Array.from(set).sort((a, b) => a - b)) {
        const open = el("button", { type: "button", class: "linkish", text: rel + ":" + line });
        open.addEventListener("click", () => openFile(rel, line));
        const clear = el("button", { type: "button", class: "btn small", text: "Clear", "aria-label": "Clear breakpoint " + rel + ":" + line });
        clear.addEventListener("click", () => toggleBreakpoint(rel, line));
        ul.append(el("li", null, open, clear));
      }
    }
    if (!ul.childElementCount) ul.append(el("li", { class: "empty", text: "None yet." }));
  }

  // ---- stack and variables ----
  function renderStack() {
    const ol = $("dbg-stack");
    ol.replaceChildren();
    if (!d.frames.length) {
      ol.append(el("li", { class: "empty", text: d.stopped ? "No frames." : "Pause the program or hit a breakpoint to see the stack." }));
      return;
    }
    for (const f of d.frames) {
      const where = relOf(f.source && f.source.path) || (f.source && (f.source.name || f.source.path)) || "?";
      const b = el("button", { type: "button", class: "frame", "aria-current": String(f.id === d.frameId) },
        el("span", { class: "frame-name", text: f.name }), el("span", { class: "frame-where", text: where + ":" + f.line }));
      b.addEventListener("click", () => selectFrame(f));
      ol.append(el("li", null, b));
    }
  }

  function varNode(v) {
    const row = el("div", { class: "var", role: "treeitem" });
    const label = el("span", { class: "var-name", text: v.name });
    const value = el("span", { class: "var-value", text: v.value === undefined ? "" : String(v.value) });
    const type = v.type ? el("span", { class: "var-type", text: v.type }) : null;
    if (v.variablesReference > 0) {
      const kids = el("div", { class: "var-kids", role: "group" });
      kids.hidden = true;
      const tog = el("button", { type: "button", class: "var-toggle", "aria-expanded": "false" }, label);
      tog.addEventListener("click", async () => {
        const open = tog.getAttribute("aria-expanded") !== "true";
        tog.setAttribute("aria-expanded", String(open));
        kids.hidden = !open;
        if (open && !kids.childElementCount) {
          try {
            const r = await send("variables", { variablesReference: v.variablesReference });
            for (const c of (r.body && r.body.variables) || []) kids.append(varNode(c));
            if (!kids.childElementCount) kids.append(el("div", { class: "empty", text: "empty" }));
          } catch (e) { kids.append(el("div", { class: "empty", text: e.message || "unavailable" })); }
        }
      });
      row.append(...[tog, " ", value, type ? " " : null, type, kids].filter(Boolean));
      row.setAttribute("aria-expanded", "false");
      if (v.autoOpen) setTimeout(() => tog.click(), 0);
    } else {
      row.append(...[label, " ", value, type ? " " : null, type].filter(Boolean));
    }
    return row;
  }

  function renderVars(scopes) {
    const box = $("dbg-vars");
    box.replaceChildren();
    if (!scopes.length) {
      box.append(el("p", { class: "empty", text: "Variables show while the program is paused." }));
      return;
    }
    scopes.forEach((s, i) => box.append(varNode(Object.assign({}, s, { autoOpen: i === 0 && !s.expensive }))));
  }

  // ---- evaluate and output ----
  function out(text) {
    d.out = (d.out + text).slice(-MAX_OUT);
    const pre = $("dbg-out");
    const atEnd = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
    pre.textContent = d.out;
    if (atEnd) pre.scrollTop = pre.scrollHeight;
  }

  async function evaluate(ev) {
    ev.preventDefault();
    const expr = $("dbg-expr").value.trim();
    if (!expr) return;
    out("> " + expr + "\n");
    try {
      const r = await send("evaluate", { expression: expr, frameId: d.frameId || undefined, context: "repl" });
      out(((r.body && r.body.result) || "") + "\n");
    } catch (e) { out((e.message || "failed") + "\n"); }
  }

  // ---- holder ----
  function mine() {
    return d.sessions.length > 0;
  }

  function anyConfigured() {
    return d.sessions.some((x) => x.configured);
  }

  function disconnect() {
    for (const x of d.sessions.slice()) x.ws.close();
  }

  async function refresh() {
    clearTimeout(d.timer);
    if (!d.runId || !d.launch) return;
    try {
      d.info = await api("/api/debug?run=" + encodeURIComponent(d.runId));
    } catch (e) {
      d.info = null;
      $("dbg-holder-text").textContent = (e.code ? e.code + ": " : "") + (e.message || "");
    }
    renderHolder();
    maybeAutoConnect();
    if (d.shown) d.timer = setTimeout(refresh, POLL_MS);
  }

  // The app starts when a debugger launches it, so the console connects by
  // itself once per run while nobody holds the debugger.
  function maybeAutoConnect() {
    if (d.autoTried === d.runId || mine() || !d.info || !d.info.debug || d.info.debug.holder) return;
    d.autoTried = d.runId;
    connect();
  }

  function renderHolder() {
    const h = d.info && d.info.debug && d.info.debug.holder;
    const box = $("dbg-holder");
    let text;
    if (mine()) {
      box.dataset.held = "you";
      text = "You hold the debugger. The app runs while this page stays connected.";
    } else if (h) {
      box.dataset.held = "other";
      const who = h.client_name || h.client_id || "another client";
      const since = h.since ? new Date(h.since).toLocaleTimeString() : "";
      text = "Held by " + who + (since ? " since " + since : "") + (h.connections > 1 ? ", " + h.connections + " connections" : "") + ".";
    } else {
      box.dataset.held = "free";
      text = "Debugger free. The app is not running until a debugger connects.";
    }
    $("dbg-holder-text").textContent = text;
    $("dbg-connect").hidden = mine() || !!h;
    $("dbg-take").hidden = mine() || !h;
    $("dbg-release").hidden = !mine();
    renderEditor();
  }

  async function takeOver() {
    if (!confirm("Take over the debugger? The current holder is disconnected and the app restarts for you.")) return;
    try {
      await api("/api/debug/op", { run: d.runId, op: "take" });
      setTimeout(connect, 300);
    } catch (e) { C.showError(e); }
  }

  async function release() {
    disconnect();
    try { await api("/api/debug/op", { run: d.runId, op: "release" }); } catch (e) { C.showError(e); }
    refresh();
  }

  function renderEditor() {
    const cfg = d.info && d.info.editor;
    $("dbg-editor").hidden = !cfg;
    if (!cfg) return;
    $("dbg-launch").textContent = JSON.stringify({ version: "0.2.0", configurations: [cfg] }, null, 2);
  }

  // ---- controls ----
  function status(text) { $("dbg-status").textContent = text; }

  function renderControls() {
    const s = sessionOf();
    const live = !!s && s.configured;
    $("dbg-continue").disabled = !live || !d.stopped;
    $("dbg-pause").disabled = !live || !!d.stopped;
    for (const id of ["dbg-next", "dbg-in", "dbg-out"]) $(id).disabled = !live || !d.stopped;
    renderHolder();
  }

  function renderAll() {
    renderControls();
    renderStack();
    renderVars([]);
    renderSource();
    renderBps();
    renderSessions();
  }

  async function loadFiles() {
    try {
      const ans = await api("/api/files?run=" + encodeURIComponent(d.runId));
      d.files = ans.files || [];
      $("dbg-files").replaceChildren(...d.files.map((f) => el("option", { value: f })));
    } catch (e) { $("dbg-src-hint").textContent = (e.code ? e.code + ": " : "") + (e.message || ""); }
  }

  function keys(ev) {
    if (!d.shown || !mine()) return;
    const map = { F8: d.stopped ? "continue" : "pause", F10: "next", F11: ev.shiftKey ? "stepOut" : "stepIn" };
    const cmd = map[ev.key];
    if (!cmd) return;
    ev.preventDefault();
    if (cmd === "pause" || d.stopped) step(cmd);
  }

  window.aimanDebug = {
    // update follows the console's poll: a new run resets the tab.
    update(state) {
      const run = state.run;
      const debugRun = run && run.debug && run.state === "ready" && run.debug_launch;
      $("dbg-empty").hidden = !!debugRun;
      $("dbg").hidden = !debugRun;
      if (!debugRun) {
        $("dbg-empty").textContent = run && run.debug ? "The debug run is " + run.state + "." : "This run has no debugger. Start a run with \"Start with the debugger\" under Run.";
        if (mine() && (!run || run.id !== d.runId)) disconnect();
        return;
      }
      if (run.id !== d.runId) {
        disconnect();
        Object.assign(d, { runId: run.id, launch: run.debug_launch, file: "", lines: [], bps: new Map(), verified: new Map(), out: "", frames: [], stopped: null });
        renderAll();
        loadFiles();
        refresh();
      }
    },
    shown(on) {
      d.shown = on;
      if (on) refresh();
      else clearTimeout(d.timer);
    },
  };

  $("dbg-connect").addEventListener("click", connect);
  $("dbg-take").addEventListener("click", takeOver);
  $("dbg-release").addEventListener("click", release);
  $("dbg-continue").addEventListener("click", () => step("continue"));
  $("dbg-pause").addEventListener("click", () => step("pause"));
  $("dbg-next").addEventListener("click", () => step("next"));
  $("dbg-in").addEventListener("click", () => step("stepIn"));
  $("dbg-out").addEventListener("click", () => step("stepOut"));
  $("dbg-eval").addEventListener("submit", evaluate);
  $("dbg-file").addEventListener("change", () => {
    // The field fires change again when it loses focus, which a click on a
    // line number does at mousedown; reopening the shown file would redraw
    // the lines under the pointer and lose that click.
    const rel = $("dbg-file").value.trim();
    if (rel !== d.file && d.files.includes(rel)) openFile(rel, 0);
  });
  $("dbg-copy").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($("dbg-launch").textContent);
      $("dbg-copy").textContent = "Copied";
      setTimeout(() => { $("dbg-copy").textContent = "Copy"; }, 1500);
    } catch (_) { $("dbg-copy").textContent = "Select and copy the text"; }
  });
  document.addEventListener("keydown", keys);
  renderAll();
})();
