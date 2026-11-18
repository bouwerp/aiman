// Harness console page. Every value from serve is set as text, never as HTML.
"use strict";

(function () {
  const TOKEN_KEY = "aiman-console-token";
  const TOKEN_HEADER = "X-Aiman-Console-Token";
  const POLL_MS = 2000;

  const $ = (id) => document.getElementById(id);
  const el = (tag, props, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (k === "text") n.textContent = v;
      else if (k === "class") n.className = v;
      else if (k.startsWith("data-") || k.startsWith("aria-") || k === "role" || k === "for") n.setAttribute(k, v);
      else n[k] = v;
    }
    for (const kid of kids) if (kid != null) n.append(kid);
    return n;
  };

  // The token arrives in the fragment, which no request carries. It moves to
  // sessionStorage (this origin only) and leaves the address bar.
  function takeToken() {
    const m = /(?:^#|&)token=([A-Za-z0-9_-]+)/.exec(location.hash);
    if (m) {
      try { sessionStorage.setItem(TOKEN_KEY, m[1]); } catch (_) { /* storage off: keep it in memory */ }
      history.replaceState(null, "", "/");
      return m[1];
    }
    try { return sessionStorage.getItem(TOKEN_KEY) || ""; } catch (_) { return ""; }
  }
  const token = takeToken();

  async function api(path, body) {
    const opts = { headers: { [TOKEN_HEADER]: token }, cache: "no-store", credentials: "omit" };
    if (body !== undefined) {
      opts.method = "POST";
      opts.headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetch(path, opts);
    } catch (_) {
      throw { code: "console_gone", message: "The console stopped. Run aiman harness console again." };
    }
    let data = null;
    try { data = await res.json(); } catch (_) { /* empty body */ }
    if (!res.ok) throw (data && data.error) || { code: String(res.status), message: res.statusText };
    return data;
  }

  // ---- notices ----
  function notice(kind, text, code) {
    const n = $("notice");
    n.replaceChildren();
    if (!text) { n.hidden = true; return; }
    n.dataset.kind = kind;
    if (code) n.append(el("code", { text: code }), ": ");
    n.append(text);
    n.hidden = false;
  }
  const showError = (e) => notice("error", (e && e.message) || String(e), e && e.code);

  // ---- state ----
  let state = { run: null, forwards: [] };
  let stepsSig = "";
  const notes = {}; // step number -> note typed but not sent
  let busy = false;

  function urls() {
    const m = {};
    for (const f of state.forwards || []) m[f.name] = f.url;
    return m;
  }

  // Step text with {{port}} turned into a link to the forward.
  function stepText(text) {
    const frag = document.createDocumentFragment();
    const map = urls();
    const re = /\{\{([A-Za-z0-9_.-]+)\}\}(\S*)/g;
    let last = 0;
    let m;
    while ((m = re.exec(text)) !== null) {
      frag.append(text.slice(last, m.index));
      const base = map[m[1]];
      let path = m[2];
      const trail = /[.,;:)]+$/.exec(path);
      if (trail) path = path.slice(0, -trail[0].length);
      if (base) {
        const href = base + path;
        frag.append(el("a", { href: href, text: href, target: "_blank", rel: "noopener noreferrer" }));
      } else {
        frag.append("{{" + m[1] + "}}" + path);
      }
      if (trail) frag.append(trail[0]);
      last = re.lastIndex;
    }
    frag.append(text.slice(last));
    return frag;
  }

  function verdictControls(step, current) {
    const row = el("div", { class: "verdict-row" });
    const label = step === 0 ? "Note on the whole scenario" : "Note on step " + step;
    const note = el("input", { type: "text", class: "verdict-note", "data-step": String(step), placeholder: "Note (what you saw)", maxLength: 2000, "aria-label": label });
    note.value = notes[step] || "";
    note.addEventListener("input", () => { notes[step] = note.value; });
    row.append(note);
    for (const [result, text] of [["pass", "Pass"], ["fail", "Fail"], ["skip", "Skip"]]) {
      const b = el("button", { type: "button", class: "btn v-" + result, text: text, "aria-pressed": String(current === result) });
      b.addEventListener("click", () => sendVerdict(step, result, note.value, row));
      row.append(b);
    }
    return row;
  }

  // scenarioVerdict is the latest verdict on the whole scenario, if any.
  function scenarioVerdict(run) {
    let last = null;
    for (const v of run.verdicts || []) if (v.step === 0) last = v;
    return last;
  }

  function scenarioResult(run) {
    if (run.result) return run.result;
    const v = scenarioVerdict(run);
    return v ? v.result : "";
  }

  function renderSteps(run) {
    const sig = JSON.stringify([run && run.id, run && run.steps, run && scenarioResult(run), run && scenarioVerdict(run), state.forwards]);
    if (sig === stepsSig) return;
    stepsSig = sig;
    // A poll can rebuild the list while a note is being typed: keep its
    // focus and caret, or the rest of the typing goes nowhere.
    const active = document.activeElement;
    const typing = active && active.classList && active.classList.contains("verdict-note")
      ? { step: active.dataset.step, start: active.selectionStart, end: active.selectionEnd } : null;
    rebuildSteps(run);
    if (typing) {
      const again = document.querySelector('input.verdict-note[data-step="' + typing.step + '"]');
      if (again) { again.focus(); again.setSelectionRange(typing.start, typing.end); }
    }
  }

  function rebuildSteps(run) {
    const list = $("steps");
    list.replaceChildren();
    const steps = (run && run.steps) || [];
    $("steps-empty").hidden = steps.length > 0;
    $("scenario-verdict").hidden = !run;
    if (!run) return;
    for (const s of steps) {
      const body = el("div", null, el("p", { class: "step-do" }, stepText(s.do)));
      if (s.expect) body.append(el("p", { class: "step-expect" }, el("b", { text: "Expect " }), stepText(s.expect)));
      if (s.result) {
        const rec = el("p", { class: "step-recorded" }, el("span", { class: "badge", "data-result": s.result, text: s.result }));
        if (s.note) rec.append(" " + s.note);
        body.append(rec);
      }
      list.append(el("li", { class: "step" }, el("span", { class: "step-n", text: String(s.n) }), body, verdictControls(s.n, s.result)));
    }
    if (steps.length === 0) $("steps-empty").textContent = "This scenario has no human steps. Record a verdict on the whole scenario.";
    $("scenario-row").replaceWith(Object.assign(verdictControls(0, scenarioResult(run)), { id: "scenario-row" }));
    const sv = scenarioVerdict(run);
    const rec = $("scenario-recorded");
    rec.hidden = !sv;
    if (sv) {
      rec.replaceChildren(el("span", { class: "badge", "data-result": sv.result, text: sv.result }));
      if (sv.note) rec.append(" " + sv.note);
    }
  }

  function renderHeader(run, err) {
    const st = $("state");
    if (run) {
      $("scenario").textContent = run.title ? run.title : run.scenario;
      const meta = $("meta");
      meta.replaceChildren(
        "scenario ", el("code", { text: run.scenario }),
        ", run ", el("code", { text: run.id }),
        ", sha ", el("code", { text: (run.sha || "").slice(0, 12) }),
        run.dirty ? ", with uncommitted changes" : ", clean tree",
        ", harness from the " + (run.source === "config" ? "operator config" : "repository file"),
      );
      st.dataset.state = run.state;
      st.textContent = run.state;
      $("down").hidden = !(run.state === "starting" || run.state === "ready");
      document.title = run.scenario + " (" + run.state + ") - Harness console";
    } else {
      $("scenario").textContent = "Harness console";
      $("meta").textContent = "session " + (state.session || "?");
      st.dataset.state = "none";
      st.textContent = err && err.code === "harness_not_running" ? "no run" : "unknown";
      $("down").hidden = true;
    }
  }

  function renderApps() {
    const nav = $("apps");
    nav.replaceChildren();
    for (const f of state.forwards || []) {
      if (f.name === "dap" && state.run && state.run.debug) continue; // the debugger's port, used by the Debug tab
      if (state.run && (state.run.evm || []).includes(f.name)) continue; // an EVM node's read-only relay, used by the EVM tab
      if (state.run && (state.run.aws_services || []).includes(f.name)) continue; // an AWS emulator's read relay, used by the AWS tab
      if (state.run && (state.run.databases || []).some((d) => d.service === f.name)) {
        // A postgres relay speaks the wire protocol, not HTTP: show where psql connects.
        nav.append(el("span", { class: "app-link db-link" },
          el("strong", { text: f.name }), "postgres 127.0.0.1:" + f.local_port, el("span", { text: "host port " + f.host_port })));
        continue;
      }
      nav.append(el("a", { class: "app-link", href: f.url, target: "_blank", rel: "noopener noreferrer" },
        el("strong", { text: f.name }), f.url, el("span", { text: "host port " + f.host_port })));
    }
    nav.hidden = nav.childElementCount === 0;
  }

  function fact(dl, k, v) { if (v) dl.append(el("dt", { text: k }), el("dd", { text: v })); }

  function renderFacts(run) {
    const dl = $("facts");
    dl.replaceChildren();
    fact(dl, "Session", state.session);
    if (!run) return;
    fact(dl, "Run", run.id);
    fact(dl, "State", run.state + (run.stop_reason ? " (" + run.stop_reason + ")" : ""));
    fact(dl, "Commit", run.sha);
    if (run.dirty) fact(dl, "Changes", "uncommitted, sha256 " + (run.diff_sha256 || "").slice(0, 12));
    fact(dl, "Network", run.network);
    if (run.state === "failed") {
      fact(dl, "Failed at", [run.failed_step, run.failed_service].filter(Boolean).join(" / "));
      fact(dl, "Error", run.error);
    }
    fact(dl, "Started", run.started_at ? new Date(run.started_at).toLocaleString() : "");
  }

  // ---- checks ----
  let checkOutput = null; // last harness.check answer, shown until the next
  // waitNotice is the debug check whose "waits for the debugger" notice is
  // shown. The notice goes once the run has shown that check as its debug
  // target and then no longer does: the check ended.
  let waitNotice = null;
  function clearWaitNotice(run) {
    if (!waitNotice) return;
    const dc = run && run.debug_check;
    if (dc && dc.name === waitNotice.name) {
      waitNotice.seen = true;
    } else if (waitNotice.seen || !run || run.id !== waitNotice.run) {
      if ($("notice").textContent === waitNotice.text) notice("", "");
      waitNotice = null;
    }
  }
  function renderChecks(run) {
    clearWaitNotice(run);
    const list = $("checks");
    const checks = (checkOutput && run && checkOutput.run === run.id) ? checkOutput.checks : ((run && run.checks) || []);
    list.replaceChildren();
    $("checks-empty").hidden = checks.length > 0;
    $("check-all").disabled = busy || !run || run.state !== "ready" || checks.length === 0;
    for (const c of checks) {
      const head = el("div", { class: "check-head" }, el("span", { class: "check-name", text: c.name }));
      let badge = el("span", { class: "badge", text: "not run" });
      if (c.debugging) {
        badge = el("span", { class: "badge", "data-result": "debugging", text: "under the debugger" });
      } else if (c.exit !== undefined && c.exit !== null) {
        badge = el("span", { class: "badge", "data-result": c.exit === 0 ? "pass" : "fail", text: "exit " + c.exit + (c.debug ? " (debugged)" : "") });
      } else if (c.error_code) {
        badge = el("span", { class: "badge", "data-result": "fail", text: c.error_code === "harness_debug_timeout" ? "no debugger attached" : c.error_code });
      }
      const ready = !busy && !!run && run.state === "ready";
      const go = el("button", { type: "button", class: "btn", text: "Run" });
      go.disabled = !ready || !!c.debugging;
      go.addEventListener("click", () => runCheck(c.name));
      const btns = el("span", null, badge, " ", go);
      // A check with a debug block runs under the debugger on a debug run;
      // a run debugs one target at a time.
      if (c.debug_lang && run && run.debug) {
        const dbg = el("button", { type: "button", class: "btn", text: "Debug check" });
        dbg.disabled = !ready || !!run.debug_check;
        dbg.title = "Runs this check in its own container under the " + c.debug_lang + " debugger and opens it in the Debug tab.";
        dbg.addEventListener("click", () => debugCheck(c.name));
        btns.append(" ", dbg);
      }
      head.append(btns);
      const item = el("li", { class: "check-item" }, head);
      if (c.run) item.append(el("p", { class: "check-cmd", text: c.run }));
      if (c.error) item.append(el("p", { class: "check-cmd", text: c.error }));
      if (c.output) item.append(el("pre", { class: "log", text: c.output }));
      list.append(item);
    }
  }

  async function runCheck(name) {
    const run = state.run;
    if (!run) return;
    busy = true;
    renderChecks(run);
    notice("info", name ? "Running check " + name + "…" : "Running every check…");
    try {
      checkOutput = await api("/api/check", { run: run.id, name: name || "" });
      notice("", "");
    } catch (e) { showError(e); }
    busy = false;
    renderChecks(state.run);
  }

  // debugCheck starts a check as the run's debug target and moves to the
  // Debug tab, which connects to it. The page lets go of the app's debugger
  // first and asks serve to take it over, since the page itself is the
  // usual holder.
  async function debugCheck(name) {
    const run = state.run;
    if (!run) return;
    busy = true;
    renderChecks(run);
    notice("info", "Starting check " + name + " under the debugger…");
    try {
      if (window.aimanDebug) window.aimanDebug.leave();
      await api("/api/check", { run: run.id, name: name, debug: true, take: true });
      checkOutput = null;
      const text = "Check " + name + " waits for the debugger. Its result shows here when it ends.";
      notice("info", text);
      waitNotice = { name: name, run: run.id, text: text, seen: false };
      selectTab("tab-debug");
    } catch (e) { showError(e); }
    busy = false;
    renderChecks(state.run);
  }

  // upChecksOnly starts the scenario again as a run that debugs only its
  // checks: the fallback when the app's runtime cannot be debugged.
  async function upChecksOnly(scenario) {
    try {
      await api("/api/up", { scenario: scenario, debug_checks: true });
      checkOutput = null;
      notice("info", "Starting " + scenario + " for check debugging…");
    } catch (e) { showError(e); }
  }

  // ---- logs ----
  let logPaused = false;
  let logTimer = null;
  function containers(run) {
    if (!run) return [];
    return Array.from(new Set(["app", "edge"].concat(run.services || [])));
  }
  function renderServices(run) {
    const sel = $("log-service");
    const want = containers(run);
    const have = Array.from(sel.options).map((o) => o.value);
    if (JSON.stringify(want) === JSON.stringify(have)) return;
    const keep = sel.value;
    sel.replaceChildren(...want.map((c) => el("option", { value: c, text: c })));
    if (want.includes(keep)) sel.value = keep;
  }
  async function pollLogs() {
    clearTimeout(logTimer);
    if ($("panel-logs").hidden || logPaused || !state.run) return;
    const svc = $("log-service").value;
    if (svc) {
      try {
        const q = new URLSearchParams({ run: state.run.id, service: svc, lines: "300" });
        const ans = await api("/api/logs?" + q.toString());
        const pre = $("log");
        const atEnd = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 24;
        pre.textContent = (ans.lines || []).join("\n");
        if (atEnd) pre.scrollTop = pre.scrollHeight;
        $("log-status").textContent = (ans.lines || []).length + " lines" +
          (ans.redacted ? ", " + ans.redacted + " values redacted" : "") + ", updated " + new Date().toLocaleTimeString();
      } catch (e) {
        $("log-status").textContent = (e.code ? e.code + ": " : "") + (e.message || "");
      }
    }
    logTimer = setTimeout(pollLogs, POLL_MS);
  }

  // ---- verdicts, up, down ----
  async function sendVerdict(step, result, note, row) {
    const run = state.run;
    if (!run) return;
    for (const b of row.querySelectorAll("button")) b.disabled = true;
    try {
      const ans = await api("/api/verdict", { run: run.id, step: step, result: result, note: note });
      delete notes[step];
      const what = step === 0 ? "the scenario" : "step " + step;
      notice("info", "Recorded " + result + " for " + what + (ans && ans.todo ? ". It added to-do " + ans.todo + " on the session." : "."));
      if (ans && ans.run) { state.run = ans.run; stepsSig = ""; render(); }
    } catch (e) {
      showError(e);
    }
    for (const b of row.querySelectorAll("button")) b.disabled = false;
  }

  async function loadScenarios() {
    const sel = $("up-scenario");
    try {
      const ans = await api("/api/list");
      const list = ans.scenarios || [];
      sel.replaceChildren(...list.map((s) => el("option", { value: s.id, text: s.title ? s.id + ": " + s.title : s.id })));
      if (ans.file_error) {
        $("up-hint").textContent = "The harness file cannot be used: " + ans.file_error.code + ": " + ans.file_error.message;
      } else {
        $("up-hint").textContent = list.length ? "From " + (ans.source === "config" ? "the operator config" : "the harness file") + " at " + String(ans.sha || "").slice(0, 12) + "." : "The harness file lists no scenarios.";
      }
      $("up-go").disabled = list.length === 0;
    } catch (e) {
      $("up-hint").textContent = (e.code ? e.code + ": " : "") + (e.message || "");
      $("up-go").disabled = true;
    }
  }

  async function startRun(ev) {
    ev.preventDefault();
    const scenario = $("up-scenario").value;
    if (!scenario) return;
    $("up-go").disabled = true;
    notice("info", "Starting " + scenario + "…");
    try {
      await api("/api/up", { scenario: scenario, dirty: $("up-dirty").checked, force: $("up-force").checked, debug: $("up-debug").checked });
      notice("info", "Run of " + scenario + " is starting. The app links appear once it is ready.");
      checkOutput = null;
      await poll();
    } catch (e) { showError(e); }
    $("up-go").disabled = false;
  }

  async function stopRun() {
    const run = state.run;
    if (!run) return;
    if (!confirm("Stop run " + run.id + "? Its containers and volumes are removed; verdicts and results stay.")) return;
    $("down").disabled = true;
    try {
      await api("/api/down", { run: run.id });
      notice("info", "Run " + run.id + " stopped.");
      await poll();
    } catch (e) { showError(e); }
    $("down").disabled = false;
  }

  // ---- tabs ----
  function selectTab(id) {
    for (const t of document.querySelectorAll('[role="tab"]')) {
      const on = t.id === id;
      t.setAttribute("aria-selected", String(on));
      t.tabIndex = on ? 0 : -1;
      $(t.getAttribute("aria-controls")).hidden = !on;
    }
    if (id === "tab-logs") pollLogs();
    $("layout").dataset.focus = id === "tab-debug" ? "debug" : id === "tab-db" ? "db" : id === "tab-aws" ? "aws" : "";
    if (window.aimanDebug) window.aimanDebug.shown(id === "tab-debug");
    if (window.aimanDB) window.aimanDB.shown(id === "tab-db");
  }

  // ---- loop ----
  let lastErr = null;
  function render() {
    const run = state.run;
    renderHeader(run, state.error);
    renderApps();
    renderSteps(run);
    renderFacts(run);
    renderChecks(run);
    renderServices(run);
    if (window.aimanDebug) window.aimanDebug.update(state);
    if (window.aimanBrowser) window.aimanBrowser.update(state);
    if (window.aimanEVM) window.aimanEVM.update(state);
    if (window.aimanDB) window.aimanDB.update(state);
    if (window.aimanNet) window.aimanNet.update(state);
    if (window.aimanAWS) window.aimanAWS.update(state);
  }

  async function poll() {
    try {
      const s = await api("/api/state");
      const changed = !state.run || !s.run || state.run.id !== (s.run && s.run.id);
      state = s;
      if (changed) checkOutput = null;
      render();
      const err = s.forward_error ? { code: "forward", message: s.forward_error } :
        (s.error && s.error.code !== "harness_not_running" ? s.error : null);
      const key = err ? err.code + err.message : null;
      if (key !== lastErr) {
        lastErr = key;
        if (err) showError(err);
      }
    } catch (e) {
      showError(e);
    }
  }

  function start() {
    if (!token) {
      notice("error", "Open the URL that aiman harness console printed in the terminal. It carries this console's key.");
      return;
    }
    for (const t of document.querySelectorAll('[role="tab"]')) t.addEventListener("click", () => selectTab(t.id));
    $("down").addEventListener("click", stopRun);
    $("up-form").addEventListener("submit", startRun);
    $("check-all").addEventListener("click", () => runCheck(""));
    $("log-service").addEventListener("change", () => { $("log").textContent = ""; pollLogs(); });
    $("log-pause").addEventListener("click", () => {
      logPaused = !logPaused;
      $("log-pause").setAttribute("aria-pressed", String(logPaused));
      $("log-pause").textContent = logPaused ? "Resume" : "Pause";
      if (!logPaused) pollLogs();
    });
    poll().then(loadScenarios);
    setInterval(poll, POLL_MS);
  }

  // debug.js reads these; it sends the token only in the debugger WebSocket's
  // subprotocol offer, as the bridge expects.
  window.aimanConsole = { api: api, el: el, $: $, notice: notice, showError: showError, token: token, upChecksOnly: upChecksOnly };

  start();
})();
