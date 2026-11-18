// AWS tab: a run's emulated AWS service. Resources is the applied plan's
// manifest with its warnings; S3 and DynamoDB read the emulator through
// serve (harness.aws), which goes through the edge's read-only relay; Calls
// is the tap's call log (harness.aws_calls). Every value from serve is set
// as text, never as HTML. The operator's writes (harness.aws_write: upload,
// delete, put and delete item, send message, reset) each ask for a confirm.
"use strict";

(function () {
  const C = window.aimanConsole;
  if (!C) return;
  const { api, el, $, showError } = C;
  const POLL_MS = 2000;
  const WRITE_MAX = 5 << 20;
  const KEEP = 2000;
  const VIEWS = ["resources", "s3", "ddb", "calls"];
  const AWS_HOST_RE = /(^|\.)(amazonaws\.com|amazonaws\.com\.cn|api\.aws|on\.aws)$/i;

  const a = {
    run: "", services: [], shown: false, view: "resources",
    manifest: null, region: "",
    bucket: "", prefix: "", objects: [], prefixes: [], token: "", key: "", version: "", obj: null,
    table: "", desc: null, items: [], lastKey: "", starts: [], item: null,
    calls: new Map(), after: 0, capAfter: 0, flows: new Map(), paused: false, open: new Set(),
    callsInfo: null, timer: null, callSig: "",
  };

  const service = () => $("aws-service").value || a.services[0] || "";
  const q = (params) => new URLSearchParams(Object.entries(params)
    .filter(([, v]) => v !== "" && v != null)).toString();
  const read = (op, params) => api("/api/aws?" + q(Object.assign({ run: a.run, service: service(), op: op }, params)));
  const status = (t) => { $("aws-status").textContent = t || ""; };
  const fmtBytes = (n) => n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + " MiB" : n >= 1024 ? (n / 1024).toFixed(1) + " KiB" : n + " B";

  // ---- run and view ----

  function update(state) {
    const run = state && state.run;
    const svcs = (run && run.state === "ready" && run.aws_services) || [];
    $("tab-aws").hidden = svcs.length === 0;
    $("aws-empty").hidden = svcs.length > 0;
    $("aws").hidden = svcs.length === 0;
    const id = run ? run.id : "";
    if (id === a.run && svcs.join(",") === a.services.join(",")) {
      renderPlanWarnings(run);
      return;
    }
    a.run = id;
    a.services = svcs.slice();
    $("aws-service").replaceChildren(...a.services.map((s) => el("option", { value: s, text: s })));
    $("aws-service-row").hidden = a.services.length < 2;
    resetService();
    renderPlanWarnings(run);
    if (svcs.length === 0 && $("tab-aws").getAttribute("aria-selected") === "true") $("tab-run").click();
    if (a.shown && svcs.length) loadView();
  }

  // resetService forgets everything read from the previous run or service.
  function resetService() {
    a.manifest = null;
    a.region = "";
    a.bucket = a.prefix = a.token = a.key = a.version = a.table = a.lastKey = "";
    a.objects = []; a.prefixes = []; a.obj = null; a.desc = null; a.items = []; a.starts = []; a.item = null;
    a.calls = new Map(); a.flows = new Map(); a.after = 0; a.capAfter = 0; a.open.clear(); a.callsInfo = null; a.callSig = "";
    for (const id of ["aws-res", "aws-env", "aws-buckets", "aws-crumbs", "aws-objects", "aws-object", "aws-tables",
      "aws-describe", "aws-items", "aws-item", "aws-stream", "aws-call-list", "aws-hints"]) $(id).replaceChildren();
    $("aws-query").hidden = true;
    $("aws-objects-more").hidden = true;
    $("aws-items-more").hidden = true;
    $("aws-items-first").hidden = true;
    status("");
    announce();
  }

  function setShown(on) {
    a.shown = on;
    schedule();
    if (on && a.run && a.services.length) loadView();
  }

  function setView(v) {
    a.view = v;
    for (const name of VIEWS) {
      $("aws-v-" + name).setAttribute("aria-pressed", String(name === v));
      $("aws-" + name).hidden = name !== v;
    }
    status("");
    schedule();
    renderWrite();
    loadView();
  }

  function loadView() {
    if (!a.run) return;
    if (!a.manifest) loadResources();
    if (a.view === "s3" && !$("aws-buckets").childElementCount) loadBuckets();
    if (a.view === "ddb" && !$("aws-tables").childElementCount) loadTables();
    if (a.view === "calls") pollCalls();
  }

  // ---- resources ----

  function renderPlanWarnings(run) {
    const plan = run && run.aws;
    const ws = (plan && plan.warnings) || [];
    const ns = (plan && plan.notes) || [];
    const sig = JSON.stringify([ws, ns]);
    const box = $("aws-warnings");
    if (box.dataset.sig === sig) return;
    box.dataset.sig = sig;
    const where = (w) => [w.stack, w.resource, w.attribute].filter(Boolean).join(".") +
      (w.file ? " (" + w.file + (w.line ? ":" + w.line : "") + ")" : "");
    box.replaceChildren(
      ...ws.map((w) => el("li", { class: "aws-warn", "data-kind": "warning" }, el("strong", { text: "Warning " }),
        where(w) ? el("code", { text: where(w) }) : null, " " + w.message)),
      ...ns.map((w) => el("li", { class: "aws-warn", "data-kind": "note" }, el("strong", { text: "Note " }),
        where(w) ? el("code", { text: where(w) }) : null, " " + w.message)));
  }

  async function loadResources() {
    try {
      const res = await read("resources", {});
      a.manifest = res.resources || null;
      a.region = (a.manifest && a.manifest.region) || "";
      renderResources(res);
      renderWrite();
    } catch (e) {
      $("aws-res").replaceChildren(el("p", { class: "hint", text: (e.code ? e.code + ": " : "") + (e.message || "") }));
    }
  }

  // envFor lists the env variables whose value is one of the resource's
  // name, ARN, URL or stream ARN.
  function envFor(r, env) {
    const vals = new Set([r.physical, ...Object.values(r.attributes || {})].filter(Boolean));
    return Object.keys(env || {}).filter((k) => vals.has(env[k])).sort();
  }

  function renderResources(res) {
    const box = $("aws-res");
    const m = res.resources;
    if (!m) {
      box.replaceChildren(el("p", { class: "hint", text: res.note || "No manifest." }));
      return;
    }
    const head = el("p", { class: "hint" }, "Region ", el("code", { text: m.region || "?" }), ", endpoint ",
      el("code", { text: m.endpoint || "?" }), ". The app reads this manifest at $AIMAN_AWS_RESOURCES.");
    const table = el("table", { class: "aws-grid" });
    table.append(el("thead", {}, el("tr", {}, ...["Stack", "Logical id", "Type", "Physical name", "Env"].map((h) => el("th", { text: h })))));
    const body = el("tbody");
    for (const r of m.resources || []) {
      let name = el("code", { text: r.physical || "" });
      if (r.kind === "bucket" || r.kind === "table") {
        name = el("button", { type: "button", class: "linkish", text: r.physical || "", title: "Open in " + (r.kind === "bucket" ? "S3" : "DynamoDB") });
        name.addEventListener("click", () => {
          if (r.kind === "bucket") { setView("s3"); openBucket(r.physical); } else { setView("ddb"); openTable(r.physical); }
        });
      }
      body.append(el("tr", { "data-kind": r.kind },
        el("td", { text: r.stack }), el("td", {}, el("code", { text: r.logical_id })),
        el("td", { text: r.type }), el("td", {}, name, r.generated ? el("span", { class: "aws-tag", text: "generated" }) : null),
        el("td", { text: envFor(r, m.env).join(", ") })));
    }
    if (!body.childElementCount) body.append(el("tr", {}, el("td", { colSpan: 5, class: "hint", text: "The plan has no resources." })));
    table.append(body);
    box.replaceChildren(head, table);

    const sens = new Set(m.sensitive || []);
    const env = Object.keys(m.env || {}).sort();
    const outs = [];
    for (const [stack, o] of Object.entries(m.outputs || {})) for (const [k, v] of Object.entries(o)) outs.push([stack + ".outputs." + k, v]);
    const dl = el("dl", { class: "aws-kv" });
    for (const k of env) dl.append(el("dt", {}, el("code", { text: k })), el("dd", {}, el("code", { text: sens.has(k) ? "[redacted]" : m.env[k] })));
    for (const [k, v] of outs) dl.append(el("dt", {}, el("code", { text: k })), el("dd", {}, el("code", { text: sens.has(k.replace(".outputs.", ".")) ? "[redacted]" : v })));
    $("aws-env").replaceChildren(dl.childElementCount ? dl : el("p", { class: "hint", text: "The plan maps no env variables or outputs." }));
  }

  // ---- S3 ----

  async function loadBuckets() {
    status("Loading buckets");
    try {
      const res = await read("s3_buckets", {});
      const ul = $("aws-buckets");
      ul.replaceChildren(...(res.buckets || []).map((b) => {
        const btn = el("button", { type: "button", class: "btn", "aria-pressed": String(b.name === a.bucket), title: bucketTitle(b) }, b.name);
        btn.dataset.name = b.name;
        btn.addEventListener("click", () => openBucket(b.name));
        return el("li", {}, btn);
      }));
      status((res.buckets || []).length ? "" : "No buckets.");
    } catch (e) { status(""); showError(e); }
  }

  function bucketTitle(b) {
    return [b.versioning ? "versioning " + b.versioning : "", (b.notifications || []).length ? "notifies " + b.notifications.join(", ") : ""]
      .filter(Boolean).join("; ");
  }

  function openBucket(name) {
    a.bucket = name;
    for (const b of $("aws-buckets").querySelectorAll("button")) b.setAttribute("aria-pressed", String(b.dataset.name === name));
    browse("");
  }

  async function browse(prefix, more) {
    if (!more) { a.prefix = prefix; a.objects = []; a.prefixes = []; a.token = ""; a.key = ""; a.version = ""; $("aws-object").replaceChildren(); announce(); }
    status("Listing " + a.bucket + "/" + a.prefix);
    try {
      const res = await read("s3_objects", { bucket: a.bucket, prefix: a.prefix, delimiter: "/", token: more ? a.token : "" });
      a.objects = a.objects.concat(res.objects || []);
      a.prefixes = a.prefixes.concat(res.prefixes || []);
      a.token = res.next_token || "";
      renderObjects();
      status(a.objects.length + a.prefixes.length ? "" : "Nothing under this prefix.");
    } catch (e) { status(""); showError(e); }
  }

  function renderObjects() {
    const crumbs = [el("button", { type: "button", class: "linkish", text: a.bucket })];
    crumbs[0].addEventListener("click", () => browse(""));
    const parts = a.prefix.split("/").filter(Boolean);
    parts.forEach((p, i) => {
      const pre = parts.slice(0, i + 1).join("/") + "/";
      const b = el("button", { type: "button", class: "linkish", text: p });
      b.addEventListener("click", () => browse(pre));
      crumbs.push(" / ", b);
    });
    $("aws-crumbs").replaceChildren(...crumbs);
    const rows = [];
    for (const p of a.prefixes) {
      const b = el("button", { type: "button", class: "aws-obj", "data-kind": "prefix" },
        el("span", { class: "aws-obj-key", text: p.slice(a.prefix.length) }), el("span", { class: "aws-obj-meta", text: "folder" }));
      b.addEventListener("click", () => browse(p));
      rows.push(el("li", {}, b));
    }
    for (const o of a.objects) {
      const b = el("button", { type: "button", class: "aws-obj", "data-kind": "object", "aria-pressed": String(o.key === a.key) },
        el("span", { class: "aws-obj-key", text: o.key.slice(a.prefix.length) }),
        el("span", { class: "aws-obj-meta", text: fmtBytes(o.size) + (o.modified ? " · " + o.modified : "") }));
      b.dataset.key = o.key;
      b.addEventListener("click", () => openObject(o.key, ""));
      rows.push(el("li", {}, b));
    }
    $("aws-objects").replaceChildren(...rows);
    $("aws-objects-more").hidden = !a.token;
  }

  async function openObject(key, version) {
    a.key = key;
    a.version = version || "";
    for (const b of $("aws-objects").querySelectorAll(".aws-obj[data-kind=object]")) b.setAttribute("aria-pressed", String(b.dataset.key === key));
    announce();
    const box = $("aws-object");
    box.replaceChildren(el("p", { class: "hint", text: "Reading " + key }));
    try {
      const [res, vers] = await Promise.all([
        read("s3_object", { bucket: a.bucket, key: key, version: version }),
        read("s3_versions", { bucket: a.bucket, key: key }).catch(() => null),
      ]);
      a.obj = res.object;
      renderObject(res.object, vers ? vers.versions || [] : null, version);
    } catch (e) { box.replaceChildren(); showError(e); }
  }

  function renderObject(o, versions, version) {
    const box = $("aws-object");
    const head = el("dl", { class: "aws-kv" });
    const row = (k, v) => { if (v !== "" && v != null) head.append(el("dt", { text: k }), el("dd", {}, el("code", { text: String(v) }))); };
    row("key", o.key);
    row("version", o.version);
    row("type", o.content_type);
    row("size", o.size + " bytes");
    row("etag", o.etag);
    row("modified", o.modified);
    if (o.lock) row("lock", [o.lock.mode, o.lock.retain_until, o.lock.legal_hold ? "legal hold " + o.lock.legal_hold : ""].filter(Boolean).join(" "));
    for (const [k, v] of Object.entries(o.metadata || {})) row("x-amz-meta-" + k, v);
    for (const [k, v] of Object.entries(o.tags || {})) row("tag " + k, v);
    const dl = el("button", { type: "button", class: "btn", id: "aws-download", text: "Download" });
    dl.addEventListener("click", () => download(o, version));
    const kids = [el("h4", { text: o.key }), head, el("div", { class: "panel-actions" }, dl)];
    if (versions && versions.length > 1) {
      const ul = el("ul", { class: "aws-versions", "aria-label": "Versions" });
      const latest = versions.find((v) => v.latest);
      const showing = version || (latest ? latest.version : "");
      for (const v of versions) {
        const label = (v.delete_marker ? "delete marker" : fmtBytes(v.size || 0)) + (v.latest ? " · latest" : "") + (v.modified ? " · " + v.modified : "");
        const b = el("button", { type: "button", class: "linkish", text: v.version, disabled: v.delete_marker });
        b.addEventListener("click", () => openObject(o.key, v.version));
        ul.append(el("li", { "aria-current": String(v.version === showing) }, b, " " + label));
      }
      kids.push(el("h4", { text: "Versions" }), ul);
    }
    if (o.binary) {
      kids.push(el("p", { class: "hint", text: "Binary content: no preview. Download it instead." }));
    } else {
      const pre = el("pre", { class: "log aws-preview", id: "aws-preview", text: o.data });
      kids.push(el("h4", { text: "Preview" }), pre);
      if (!o.eof) {
        const more = el("button", { type: "button", class: "btn", text: "Load more" });
        let next = o.next;
        more.addEventListener("click", async () => {
          try {
            const r = await read("s3_object", { bucket: a.bucket, key: o.key, version: version, offset: next });
            pre.textContent += r.object.data;
            next = r.object.next;
            if (r.object.eof) more.remove();
          } catch (e) { showError(e); }
        });
        kids.push(more);
      }
    }
    box.replaceChildren(...kids);
  }

  // download assembles the object from base64 pages into a Blob of type
  // application/octet-stream, so the browser saves it and never renders it.
  async function download(o, version) {
    const parts = [];
    let offset = 0, page = null;
    try {
      do {
        status("Downloading " + o.key + ": " + fmtBytes(offset));
        const r = await read("s3_download", { bucket: a.bucket, key: o.key, version: version, offset: offset });
        page = r.download;
        const bin = atob(page.data || "");
        const buf = new Uint8Array(bin.length);
        for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
        parts.push(buf);
        if (page.next <= offset) break;
        offset = page.next;
      } while (!page.eof);
    } catch (e) { status(""); showError(e); return; }
    const name = (o.key.split("/").pop() || "object") + (page && page.capped ? ".part" : "");
    const url = URL.createObjectURL(new Blob(parts, { type: "application/octet-stream" }));
    const link = el("a", { href: url, download: name });
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
    status(page && page.capped ?
      "Saved the first " + fmtBytes(page.max) + " of " + fmtBytes(page.size) + " as " + name + ": the download cap is " + fmtBytes(page.max) + "." :
      "Saved " + name + " (" + fmtBytes(offset) + ").");
  }

  // ---- DynamoDB ----

  async function loadTables() {
    status("Loading tables");
    try {
      const res = await read("ddb_tables", {});
      $("aws-tables").replaceChildren(...(res.tables || []).map((t) => {
        const b = el("button", { type: "button", class: "btn", "aria-pressed": String(t === a.table), text: t });
        b.dataset.name = t;
        b.addEventListener("click", () => openTable(t));
        return el("li", {}, b);
      }));
      status((res.tables || []).length ? "" : "No tables.");
    } catch (e) { status(""); showError(e); }
  }

  async function openTable(name) {
    a.table = name;
    a.item = null;
    $("aws-item").replaceChildren();
    $("aws-stream").replaceChildren();
    for (const b of $("aws-tables").querySelectorAll("button")) b.setAttribute("aria-pressed", String(b.dataset.name === name));
    announce();
    try {
      const res = await read("ddb_describe", { table: name });
      a.desc = res.table;
      renderDescribe(res.table);
      renderQueryForm();
      scan(true);
    } catch (e) { showError(e); }
  }

  const keyText = (ks) => (ks || []).map((k) => k.name + " (" + k.type + ")").join(", ");

  function renderDescribe(t) {
    const dl = el("dl", { class: "aws-kv" });
    const row = (k, v) => { if (v !== "" && v != null) dl.append(el("dt", { text: k }), el("dd", {}, el("code", { text: String(v) }))); };
    row("status", t.status);
    row("key", keyText(t.key_schema));
    row("attributes", Object.entries(t.attributes || {}).map(([k, v]) => k + " " + v).join(", "));
    for (const g of t.gsis || []) row("GSI " + g.name, keyText(g.key_schema) + (g.projection ? " · " + g.projection : ""));
    for (const l of t.lsis || []) row("LSI " + l.name, keyText(l.key_schema) + (l.projection ? " · " + l.projection : ""));
    row("stream", t.stream_arn ? (t.stream_view || "") + " " + t.stream_arn : "");
    row("TTL", t.ttl_attribute ? t.ttl_attribute + (t.ttl_status ? " (" + t.ttl_status + ")" : "") : "");
    row("items", t.item_count + " (" + fmtBytes(t.size_bytes || 0) + ", as the emulator counts)");
    $("aws-describe").replaceChildren(dl);
    $("aws-stream-load").hidden = !t.stream_arn;
  }

  // keysOf is the partition and sort key of the table or an index.
  function keysOf(index) {
    const t = a.desc;
    if (!t) return [];
    const idx = index ? (t.gsis || []).concat(t.lsis || []).find((i) => i.name === index) : null;
    return (idx ? idx.key_schema : t.key_schema) || [];
  }

  function renderQueryForm() {
    const t = a.desc;
    $("aws-query").hidden = !t;
    if (!t) return;
    const idx = $("aws-q-index");
    idx.replaceChildren(el("option", { value: "", text: "table" }),
      ...(t.gsis || []).concat(t.lsis || []).map((i) => el("option", { value: i.name, text: i.name })));
    // Key values typed for another table would query this one's keys.
    for (const id of ["aws-q-pk", "aws-q-sk1", "aws-q-sk2", "aws-q-skop"]) $(id).value = "";
    syncQueryKeys();
  }

  function syncQueryKeys() {
    const ks = keysOf($("aws-q-index").value);
    const hash = ks.find((k) => k.type === "HASH");
    const range = ks.find((k) => k.type === "RANGE");
    $("aws-q-pk-label").textContent = "Partition key " + (hash ? hash.name : "");
    $("aws-q-pk").dataset.name = hash ? hash.name : "";
    $("aws-q-sk-row").hidden = !range;
    $("aws-q-sk-label").textContent = "Sort key " + (range ? range.name : "");
    $("aws-q-sk2").hidden = $("aws-q-skop").value !== "between";
  }

  function readParams() {
    const p = { table: a.table, index: $("aws-q-index").value };
    const pk = $("aws-q-pk").value;
    if (pk === "") return p;
    p.pk_name = $("aws-q-pk").dataset.name;
    p.pk_value = pk;
    p.forward = String(!$("aws-q-desc").checked);
    const op = $("aws-q-skop").value;
    if (op && !$("aws-q-sk-row").hidden) {
      p.sk_op = op;
      p.sk_value = [$("aws-q-sk1").value].concat(op === "between" ? [$("aws-q-sk2").value] : []);
    }
    return p;
  }

  // scan runs the form: a query when the partition key has a value, else a
  // scan. first restarts paging; otherwise the next page follows last_key.
  async function scan(first) {
    if (!a.table) return;
    const p = readParams();
    const op = p.pk_name ? "ddb_query" : "ddb_scan";
    if (first) { a.starts = []; a.lastKey = ""; }
    const start = first ? "" : a.lastKey;
    const sk = p.sk_value || [];
    delete p.sk_value;
    let qs = q(Object.assign({ run: a.run, service: service(), op: op, start: start }, p));
    for (const v of sk) qs += "&sk_value=" + encodeURIComponent(v);
    status((op === "ddb_query" ? "Querying " : "Scanning ") + a.table + (p.index ? " (" + p.index + ")" : ""));
    try {
      const res = await api("/api/aws?" + qs);
      if (start) a.starts.push(start);
      a.items = res.items || [];
      a.lastKey = res.last_key || "";
      renderItems(res, op);
    } catch (e) { status(""); showError(e); }
  }

  const cellText = (v) => v === undefined ? "" : v === null ? "null" : typeof v === "object" ? JSON.stringify(v) : String(v);

  function renderItems(res, op) {
    const ks = keysOf("").concat(keysOf($("aws-q-index").value)).map((k) => k.name);
    const cols = [];
    for (const k of ks) if (!cols.includes(k)) cols.push(k);
    for (const it of a.items) for (const k of Object.keys(it).sort()) if (!cols.includes(k)) cols.push(k);
    const red = new Set(res.redacted_attributes || []);
    const table = el("table", { class: "aws-grid" });
    table.append(el("thead", {}, el("tr", {}, ...cols.map((c) => el("th", { class: red.has(c) ? "redacted" : "", text: c })))));
    const body = el("tbody");
    a.items.forEach((it, i) => {
      const tr = el("tr", { tabIndex: 0, class: "aws-item-row", "aria-label": "Item " + (i + 1) });
      for (const c of cols) tr.append(el("td", { class: red.has(c) ? "redacted" : "", text: cellText(it[c]) }));
      const pick = () => showItem(it, tr);
      tr.addEventListener("click", pick);
      tr.addEventListener("keydown", (ev) => { if (ev.key === "Enter") pick(); });
      body.append(tr);
    });
    table.append(body);
    $("aws-items").replaceChildren(a.items.length ? table : el("p", { class: "hint", text: "No items." }));
    $("aws-items-more").hidden = !a.lastKey;
    $("aws-items-first").hidden = a.starts.length === 0;
    status((op === "ddb_query" ? "Query" : "Scan") + ": " + a.items.length + " item" + (a.items.length === 1 ? "" : "s") +
      (a.starts.length ? " on page " + (a.starts.length + 1) : "") + (a.lastKey ? ", more on the next page" : "") +
      (res.truncated ? " (reply cut at 1 MiB)" : "") + (red.size ? ". Redacted: " + Array.from(red).join(", ") : "") + ".");
  }

  function showItem(it, tr) {
    a.item = it;
    for (const r of $("aws-items").querySelectorAll("tr[aria-selected]")) r.removeAttribute("aria-selected");
    if (tr) tr.setAttribute("aria-selected", "true");
    $("aws-item").replaceChildren(el("h4", { text: "Item" }), el("pre", { class: "log", id: "aws-item-json", text: JSON.stringify(it, null, 2) }));
    announce();
  }

  async function loadStream() {
    status("Reading the stream of " + a.table);
    try {
      const res = await read("ddb_stream", { table: a.table });
      const recs = (res.records || []).slice().sort((x, y) => (y.sequence.length - x.sequence.length) || y.sequence.localeCompare(x.sequence));
      $("aws-stream").replaceChildren(...recs.map((r) => {
        const d = el("details", { class: "aws-rec" });
        d.append(el("summary", {}, el("span", { class: "aws-badge", "data-event": r.event, text: r.event }),
          " " + JSON.stringify(r.keys || {}) + (r.time ? " · " + r.time : "") + " · seq " + r.sequence));
        if (r.old) d.append(el("h5", { text: "Old image" }), el("pre", { class: "log", text: JSON.stringify(r.old, null, 2) }));
        if (r.new) d.append(el("h5", { text: "New image" }), el("pre", { class: "log", text: JSON.stringify(r.new, null, 2) }));
        return el("li", {}, d);
      }));
      status(recs.length ? recs.length + " stream record" + (recs.length === 1 ? "" : "s") + ", newest first." : "No stream records.");
    } catch (e) { status(""); showError(e); }
  }

  // ---- calls ----

  function schedule() {
    clearTimeout(a.timer);
    a.timer = null;
    if (a.shown && a.view === "calls" && !a.paused && a.run) a.timer = setTimeout(pollCalls, POLL_MS);
  }

  async function pollCalls() {
    clearTimeout(a.timer);
    if (!(a.shown && a.view === "calls" && a.run)) return;
    const run = a.run;
    try {
      let more = true;
      for (let i = 0; more && i < 10; i++) {
        const ans = await api("/api/aws/calls?" + q({ run: run, after: a.after, limit: 200 }));
        if (run !== a.run) return;
        for (const c of ans.calls || []) a.calls.set(c.id, c);
        a.after = Math.max(a.after, ans.next || 0);
        a.callsInfo = ans;
        more = !!ans.more;
      }
      await pollRefused(run);
      if (a.calls.size > KEEP) {
        const ids = Array.from(a.calls.keys()).sort((x, y) => x - y);
        for (const id of ids.slice(0, a.calls.size - KEEP)) { a.calls.delete(id); a.open.delete(id); }
      }
      renderCalls();
    } catch (e) {
      $("aws-calls-status").textContent = (e.code ? e.code + ": " : "") + (e.message || "");
    }
    schedule();
  }

  // pollRefused reads the capture for flows the AWS hints name: a refused
  // call to a real AWS host, and a virtual-hosted S3 request (a bucket
  // name in front of the service's host name), which the proxy cannot
  // route.
  async function pollRefused(run) {
    const svcs = a.services;
    for (let i = 0; i < 5; i++) {
      const ans = await api("/api/capture?" + q({ run: run, after: a.capAfter, limit: 200 }));
      for (const f of ans.flows || []) {
        const host = (f.host || "").replace(/:\d+$/, "").toLowerCase();
        const aws = AWS_HOST_RE.test(host);
        const vhost = svcs.some((s) => host.endsWith("." + s));
        if (aws || vhost) a.flows.set(f.id, { host: host, aws: aws, vhost: vhost, outcome: f.outcome, url: f.url });
      }
      a.capAfter = Math.max(a.capAfter, ans.next || 0);
      if (!ans.more) break;
    }
  }

  function hints() {
    const out = [];
    const calls = Array.from(a.calls.values());
    const first = (pred) => calls.find(pred);
    const region = a.region;
    const wrongRegion = first((c) => c.error_code === "PermanentRedirect" || c.error_code === "AuthorizationHeaderMalformed" ||
      (region && c.region && c.region !== region));
    if (wrongRegion) {
      out.push(["region", "Region mismatch: " + wrongRegion.operation + " from " + (wrongRegion.client || "?") + " was signed for " +
        (wrongRegion.region || "another region") + (wrongRegion.error_code ? " (" + wrongRegion.error_code + ")" : "") +
        "; the emulator runs in " + (region || "the run's region") + ". Let the SDK read AWS_REGION, which the harness sets."]);
    }
    const foreign = first((c) => c.foreign_key);
    if (foreign) {
      out.push(["foreign_key", "A call used an access key other than test (" + foreign.operation + " from " + (foreign.client || "?") +
        "): the app brings its own credentials from code, a profile or a file. Let the SDK read AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, which the harness sets."]);
    }
    const sig = first((c) => c.error_code === "SignatureDoesNotMatch");
    if (sig) {
      out.push(["signature", "SignatureDoesNotMatch on " + sig.operation + ": the request changed after it was signed (a proxy, a rewritten host or body, a presigned URL used for another method). The emulator checks signatures while the aws block's verify_signatures is true."]);
    }
    const flows = Array.from(a.flows.values());
    const vh = flows.find((f) => f.vhost);
    if (vh) {
      out.push(["virtual_host", "Virtual-hosted S3 request to " + vh.host + ": the bucket name went into the host name, which the run cannot resolve. Turn on path-style addressing (forcePathStyle, s3.addressing_style = path, AWS_S3_FORCE_PATH_STYLE)."]);
    }
    const real = flows.find((f) => f.aws);
    if (real) {
      out.push(["real_aws", "The app called real AWS (" + real.host + "), which the run blocks: that client ignores AWS_ENDPOINT_URL. See the refused call in the Network tab."]);
    }
    return out;
  }

  function callMatches(c) {
    const svc = $("aws-f-service").value, client = $("aws-f-client").value;
    if (svc && c.service !== svc) return false;
    if (client && c.client !== client) return false;
    if ($("aws-f-errors").checked && !(c.error_code || c.error || c.status >= 400)) return false;
    const needle = $("aws-f-text").value.trim().toLowerCase();
    if (!needle) return true;
    const r = c.resource || {};
    return [c.operation, c.service, c.client, r.bucket, r.key, r.table, r.index, r.queue, r.topic, r.stream, c.error_code, String(c.status || "")]
      .join(" ").toLowerCase().includes(needle);
  }

  function resourceText(r) {
    if (!r) return "";
    if (r.bucket) return r.bucket + (r.key ? "/" + r.key : "");
    if (r.table) return r.table + (r.index ? " (" + r.index + ")" : "");
    return r.queue || r.topic || r.stream || "";
  }

  function syncFilter(id, values) {
    const sel = $(id);
    const have = Array.from(sel.options).map((o) => o.value).slice(1);
    const want = Array.from(new Set(values.filter(Boolean))).sort();
    if (have.join(",") === want.join(",")) return;
    const cur = sel.value;
    sel.replaceChildren(el("option", { value: "", text: "all" }), ...want.map((v) => el("option", { value: v, text: v })));
    sel.value = want.includes(cur) ? cur : "";
  }

  function callDetail(c) {
    const d = el("div", { class: "aws-call-detail", id: "aws-cd-" + c.id });
    const dl = el("dl", { class: "aws-kv" });
    const row = (k, v) => { if (v !== "" && v != null && v !== false) dl.append(el("dt", { text: k }), el("dd", {}, el("code", { text: String(v) }))); };
    row("protocol", c.protocol);
    row("method", c.method);
    row("region", c.region);
    row("access key", c.access_key || (c.foreign_key ? "not test (hidden)" : c.unsigned ? "unsigned" : ""));
    row("presigned", c.presigned ? "yes" : "");
    row("request", fmtBytes(c.request_bytes || 0));
    row("response", fmtBytes(c.response_bytes || 0));
    row("error", c.error);
    row("redacted", c.redacted ? c.redacted + " values" : "");
    d.append(dl);
    if (c.params) {
      let text = c.params;
      try { text = JSON.stringify(JSON.parse(c.params), null, 2); } catch (_) { /* cut at 4 KiB: shown as sent */ }
      d.append(el("h5", { text: "Params" + (c.params_truncated ? " (cut at 4 KiB)" : "") }), el("pre", { class: "log", text: text }));
    }
    return d;
  }

  function renderCalls() {
    const all = Array.from(a.calls.values());
    syncFilter("aws-f-service", all.map((c) => c.service));
    syncFilter("aws-f-client", all.map((c) => c.client));
    const hs = hints();
    const hsig = hs.map((h) => h[0]).join(",");
    if ($("aws-hints").dataset.sig !== hsig) {
      $("aws-hints").dataset.sig = hsig;
      $("aws-hints").replaceChildren(...hs.map(([kind, text]) => el("li", { class: "aws-hint", "data-hint": kind, text: text })));
    }
    const info = a.callsInfo;
    if (info) {
      let t = info.total + " calls";
      if (info.dropped) t += ", " + info.dropped + " oldest dropped";
      if (info.redacted) t += ", " + info.redacted + " values redacted";
      $("aws-calls-status").textContent = t + ", updated " + new Date().toLocaleTimeString();
    }
    const list = all.filter(callMatches).sort((x, y) => y.id - x.id);
    const sig = [$("aws-f-service").value, $("aws-f-client").value, $("aws-f-errors").checked, $("aws-f-text").value,
      Array.from(a.open).join(","), list.map((c) => c.id + ":" + (c.status || "") + (c.pending ? "p" : "")).join(",")].join("|");
    if (sig === a.callSig) return;
    a.callSig = sig;
    $("aws-calls-empty").hidden = list.length > 0;
    $("aws-calls-empty").textContent = all.length ? "No call matches the filters." : "No AWS calls yet. Calls the app makes to the emulator appear here.";
    $("aws-call-list").replaceChildren(...list.map((c) => {
      const open = a.open.has(c.id);
      const failed = !!(c.error_code || c.error || c.status >= 400);
      const btn = el("button", { type: "button", class: "aws-call", "aria-expanded": String(open), "aria-controls": "aws-cd-" + c.id, "data-failed": String(failed) },
        el("span", { class: "aws-call-time", text: new Date(c.start).toLocaleTimeString() }),
        el("span", { class: "aws-call-client", text: c.client || "" }),
        el("span", { class: "aws-call-op", text: c.service + " " + c.operation }),
        el("span", { class: "aws-call-res", text: resourceText(c.resource) }),
        el("span", { class: "aws-badge", "data-failed": String(failed), text: c.pending ? "pending" : (c.status || "") + (c.error_code ? " " + c.error_code : "") }),
        el("span", { class: "aws-call-dur", text: c.pending ? "" : Math.round(c.duration_ms || 0) + " ms · " + fmtBytes(c.response_bytes || 0) }));
      btn.addEventListener("click", () => {
        if (a.open.has(c.id)) a.open.delete(c.id); else a.open.add(c.id);
        renderCalls();
      });
      const li = el("li", { class: "aws-call-item" }, btn);
      if (open) li.append(callDetail(c));
      return li;
    }));
  }

  // ---- operator writes ----

  // announce redraws the write area for what the tab shows and tells
  // other listeners.
  function announce() {
    renderWrite();
    document.dispatchEvent(new CustomEvent("aiman-aws-context", { detail: context() }));
  }

  const wstatus = (t) => { $("aws-w-status").textContent = t || ""; };
  const queues = () => ((a.manifest && a.manifest.resources) || []).filter((r) => r.kind === "queue" && r.physical).map((r) => r.physical);

  function renderWrite() {
    $("aws-write").hidden = !a.run || a.services.length === 0;
    const s3 = a.view === "s3" && !!a.bucket;
    $("aws-w-s3").hidden = !s3;
    $("aws-w-delobj").hidden = !(s3 && a.key);
    $("aws-w-delobj").textContent = a.version ? "Delete this version" : "Delete object";
    const ddb = a.view === "ddb" && !!a.table;
    $("aws-w-ddb").hidden = !ddb;
    $("aws-w-delitem").hidden = !(ddb && a.item);
    if (ddb && a.item && $("aws-w-item").dataset.from !== JSON.stringify(a.item)) {
      $("aws-w-item").dataset.from = JSON.stringify(a.item);
      $("aws-w-item").value = JSON.stringify(a.item, null, 2);
    }
    const qs = queues();
    $("aws-w-sqs").hidden = !(a.view === "resources" && qs.length);
    const sel = $("aws-w-queue");
    if (Array.from(sel.options).map((o) => o.value).join(",") !== qs.join(",")) {
      sel.replaceChildren(...qs.map((n) => el("option", { value: n, text: n })));
    }
  }

  // write sends one harness.aws_write after the tester confirms ask.
  async function write(body, ask) {
    if (!confirm(ask)) return null;
    wstatus("Writing");
    try {
      return await api("/api/aws/write", Object.assign({ run: a.run, service: service() }, body));
    } catch (e) {
      wstatus("");
      showError(e);
      return null;
    }
  }

  function readFile(f) {
    return new Promise((resolve, reject) => {
      const r = new FileReader();
      r.onload = () => resolve(String(r.result).replace(/^data:[^,]*,/, ""));
      r.onerror = () => reject(r.error);
      r.readAsDataURL(f);
    });
  }

  async function upload() {
    const f = $("aws-w-file").files[0];
    if (!f) { wstatus("Pick a file first."); return; }
    const key = $("aws-w-key").value.trim() || a.prefix + f.name;
    if (f.size > WRITE_MAX) { wstatus("The console uploads at most 5 MiB; use aiman harness aws write s3-put on the host."); return; }
    const data = await readFile(f);
    const res = await write({ op: "s3_put", bucket: a.bucket, key: key, data: data, content_type: f.type || "" },
      "Upload " + f.name + " (" + fmtBytes(f.size) + ") to " + a.bucket + "/" + key + " on run " + a.run + "? It replaces an object with that key.");
    if (!res) return;
    wstatus("Uploaded " + a.bucket + "/" + key + (res.version_id ? ", version " + res.version_id : "") + ".");
    $("aws-w-file").value = "";
    $("aws-w-key").value = "";
    await browse(a.prefix);
    openObject(key, "");
  }

  async function deleteObject() {
    const key = a.key, version = a.version;
    const res = await write({ op: "s3_delete", bucket: a.bucket, key: key, version: version },
      version ? "Delete version " + version + " of " + a.bucket + "/" + key + " for good?" :
        "Delete " + a.bucket + "/" + key + "? In a versioned bucket this adds a delete marker.");
    if (!res) return;
    wstatus("Deleted " + a.bucket + "/" + key + (res.delete_marker ? " (delete marker " + (res.version_id || "") + ")" : "") + ".");
    a.key = ""; a.version = ""; a.obj = null;
    $("aws-object").replaceChildren();
    await browse(a.prefix);
    announce();
  }

  function itemFromForm() {
    let v = null;
    try { v = JSON.parse($("aws-w-item").value); } catch (_) { /* not JSON */ }
    if (!v || typeof v !== "object" || Array.isArray(v)) { wstatus("The item must be a JSON object."); return null; }
    return v;
  }

  async function putItem() {
    const item = itemFromForm();
    if (!item) return;
    const res = await write({ op: "ddb_put", table: a.table, item: item },
      "Put this item into " + a.table + " on run " + a.run + "? It replaces an item with the same key.\n\n" + JSON.stringify(item));
    if (!res) return;
    wstatus("Put an item into " + a.table + ".");
    scan(true);
  }

  async function deleteItem() {
    const key = {};
    for (const k of (a.desc && a.desc.key_schema) || []) key[k.name] = a.item[k.name];
    const res = await write({ op: "ddb_delete", table: a.table, key: key },
      "Delete the item " + JSON.stringify(key) + " from " + a.table + " on run " + a.run + "?");
    if (!res) return;
    wstatus("Deleted " + JSON.stringify(key) + " from " + a.table + (res.found === false ? " (it was not there)" : "") + ".");
    a.item = null;
    $("aws-item").replaceChildren();
    announce();
    scan(true);
  }

  async function sendMessage() {
    const queue = $("aws-w-queue").value, body = $("aws-w-body").value;
    if (!queue || !body) { wstatus("Pick a queue and write a body."); return; }
    const res = await write({ op: "sqs_send", queue: queue, body: body }, "Send this message to " + queue + " on run " + a.run + "?\n\n" + body);
    if (!res) return;
    wstatus("Sent message " + (res.message_id || "") + " to " + queue + ".");
    $("aws-w-body").value = "";
  }

  async function reset() {
    const res = await write({ op: "reset" },
      "Reset " + service() + " on run " + a.run + "? This wipes every bucket, object, table, item, queue and message in the emulator, then applies the plan and the seed again. The app keeps running.");
    if (!res) return;
    let t = "Reset " + service() + ": " + (res.resources || 0) + " resources, " + (res.calls || 0) + " calls applied again.";
    if (res.failed_step) t += " Failed at " + res.failed_step + ".";
    if (res.note) t += " " + res.note;
    if ((res.env_changed || []).length) {
      t += " These env variables now have other values, and the running app still has the old ones: " +
        res.env_changed.join(", ") + ". Start the run again to pass them.";
    }
    const view = a.view;
    resetService();
    a.view = view;
    loadView();
    wstatus(t);
  }

  function context() {
    return {
      run: a.run, service: service(), view: a.view,
      bucket: a.bucket, prefix: a.prefix, key: a.key,
      table: a.table, keySchema: a.desc ? a.desc.key_schema : [], item: a.item,
    };
  }

  // reload rereads what a write may have changed.
  function reload() {
    if (a.view === "s3" && a.bucket) { browse(a.prefix); if (a.key) openObject(a.key, a.version); }
    if (a.view === "ddb" && a.table) scan(true);
    if (a.view === "resources") { a.manifest = null; loadResources(); }
  }

  function start() {
    $("tab-aws").addEventListener("click", () => setShown(true));
    for (const t of document.querySelectorAll('[role="tab"]')) {
      if (t.id !== "tab-aws") t.addEventListener("click", () => setShown(false));
    }
    for (const v of VIEWS) $("aws-v-" + v).addEventListener("click", () => setView(v));
    $("aws-service").addEventListener("change", () => { resetService(); loadView(); });
    $("aws-buckets-reload").addEventListener("click", loadBuckets);
    $("aws-objects-more").addEventListener("click", () => browse(a.prefix, true));
    $("aws-tables-reload").addEventListener("click", loadTables);
    $("aws-q-index").addEventListener("change", syncQueryKeys);
    $("aws-q-skop").addEventListener("change", syncQueryKeys);
    $("aws-query").addEventListener("submit", (ev) => { ev.preventDefault(); scan(true); });
    $("aws-items-more").addEventListener("click", () => scan(false));
    $("aws-items-first").addEventListener("click", () => scan(true));
    $("aws-stream-load").addEventListener("click", loadStream);
    for (const id of ["aws-f-service", "aws-f-client", "aws-f-errors"]) $(id).addEventListener("change", renderCalls);
    $("aws-f-text").addEventListener("input", renderCalls);
    $("aws-w-upload").addEventListener("click", upload);
    $("aws-w-delobj").addEventListener("click", deleteObject);
    $("aws-w-put").addEventListener("click", putItem);
    $("aws-w-delitem").addEventListener("click", deleteItem);
    $("aws-w-send").addEventListener("click", sendMessage);
    $("aws-w-reset").addEventListener("click", reset);
    $("aws-calls-pause").addEventListener("click", () => {
      a.paused = !a.paused;
      $("aws-calls-pause").setAttribute("aria-pressed", String(a.paused));
      $("aws-calls-pause").textContent = a.paused ? "Resume" : "Pause";
      if (a.paused) schedule(); else pollCalls();
    });
  }

  start();
  window.aimanAWS = { update: update, context: context, reload: reload };
})();
