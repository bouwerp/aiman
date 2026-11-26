// aiman artifact kit: themes mermaid from the page's CSS tokens, draws legends,
// wraps tables, and adds a theme switcher. Load as a module after the content.
// A sandboxed artifact has no storage, so the switcher does not persist.
// Classic script (a sandboxed page cannot load a same-host module: no CORS).
// It runs on DOMContentLoaded unless <html data-kit-auto="off">; window.aimanKit.init(scope) runs it by hand.
(() => {

const MERMAID = "https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.esm.min.mjs";
const root = document.documentElement;

function token(name) {
  return getComputedStyle(root).getPropertyValue("--" + name).trim();
}

function hexToRgb(hex) {
  const m = hex.replace("#", "").match(/^([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i);
  return m ? `rgb(${parseInt(m[1], 16)},${parseInt(m[2], 16)},${parseInt(m[3], 16)})` : hex;
}

function isDark() {
  const mode = root.dataset.kitMode;
  if (mode === "dark") return true;
  if (mode === "light") return false;
  return matchMedia("(prefers-color-scheme: dark)").matches;
}

// themeVariables for mermaid's "base" theme, from the active tokens.
function mermaidTheme() {
  const t = (n) => token(n);
  return {
    darkMode: isDark(),
    background: t("surface"),
    fontFamily: t("sans"),
    fontSize: "14px",
    primaryColor: t("surface"),
    primaryTextColor: t("fg"),
    primaryBorderColor: t("muted"),
    secondaryColor: t("accent-soft"),
    secondaryTextColor: t("fg"),
    secondaryBorderColor: t("accent"),
    tertiaryColor: t("sunk"),
    tertiaryTextColor: t("fg"),
    tertiaryBorderColor: t("line"),
    mainBkg: t("surface"),
    nodeBorder: t("muted"),
    nodeTextColor: t("fg"),
    lineColor: t("muted"),
    textColor: t("fg"),
    titleColor: t("fg"),
    clusterBkg: t("sunk"),
    clusterBorder: t("line"),
    edgeLabelBackground: t("surface"),
    actorBkg: t("surface"),
    actorBorder: t("muted"),
    actorTextColor: t("fg"),
    actorLineColor: t("line"),
    signalColor: t("fg"),
    signalTextColor: t("fg"),
    labelBoxBkgColor: t("sunk"),
    labelBoxBorderColor: t("line"),
    labelTextColor: t("fg"),
    loopTextColor: t("fg"),
    noteBkgColor: t("chg-soft"),
    noteBorderColor: t("chg"),
    noteTextColor: t("fg"),
    activationBkgColor: t("accent-soft"),
    activationBorderColor: t("accent"),
    sequenceNumberColor: t("surface"),
    classText: t("fg"),
  };
}

// The design template's delta classes and sequence rects are fixed colours in
// the source; map them to the theme's tokens before each render.
function themeSource(src) {
  const cls = {
    new: `fill:${token("new-soft")},stroke:${token("new")},color:${token("fg")},stroke-width:1.6px`,
    changed: `fill:${token("chg-soft")},stroke:${token("chg")},color:${token("fg")},stroke-width:1.6px`,
    removed: `fill:${token("del-soft")},stroke:${token("del")},color:${token("fg")},stroke-width:1.6px,stroke-dasharray:5 4`,
  };
  return src
    .replace(/^(\s*)classDef (new|changed|removed)\b.*$/gm, (_, ind, n) => `${ind}classDef ${n} ${cls[n]}`)
    .replace(/rect rgb\(212,\s*247,\s*212\)/g, `rect ${hexToRgb(token("new-soft"))}`)
    .replace(/rect rgb\(253,\s*233,\s*184\)/g, `rect ${hexToRgb(token("chg-soft"))}`);
}

let mermaidMod = null;
const blocks = [];

// Renders run one at a time: an overlap makes mermaid lay out a half-built SVG.
let queue = Promise.resolve();
function renderMermaid() {
  queue = queue.then(renderOnce, renderOnce);
  return queue;
}

async function renderOnce() {
  for (const s of document.querySelectorAll(".kit-switch select")) {
    if (s.getAttribute("aria-label") === "Theme") s.value = root.dataset.kitTheme || "plex";
  }
  if (!blocks.length) return;
  mermaidMod ??= (await import(MERMAID)).default;
  mermaidMod.initialize({
    startOnLoad: false, theme: "base", themeVariables: mermaidTheme(), securityLevel: "strict",
    // Fit the page width. Tight spacing and wrapped labels keep the text readable when a wide diagram shrinks.
    flowchart: { useMaxWidth: true, curve: "basis", padding: 10, nodeSpacing: 28, rankSpacing: 36, wrappingWidth: 160 },
    class: { useMaxWidth: true }, sequence: { useMaxWidth: true, wrap: true }, state: { useMaxWidth: true },
    er: { useMaxWidth: true },
  });
  for (const b of blocks) {
    b.el.removeAttribute("data-processed");
    b.el.textContent = themeSource(b.src);
  }
  try {
    await mermaidMod.run({ nodes: blocks.map((b) => b.el) });
  } catch (e) {
    console.warn("mermaid", e);
  }
}

// Click a diagram to see it at the window's width.
function zoom(scope) {
  scope.addEventListener("click", (e) => {
    const src = e.target.closest("pre.mermaid, figure");
    if (!src) return;
    const svg = src.querySelector("svg");
    if (!svg) return;
    const box = document.createElement("div");
    box.className = "kit-zoom";
    const inner = document.createElement("div");
    inner.append(svg.cloneNode(true));
    box.append(inner);
    const close = () => { box.remove(); removeEventListener("keydown", esc); };
    const esc = (ev) => ev.key === "Escape" && close();
    box.addEventListener("click", close);
    addEventListener("keydown", esc);
    document.body.append(box);
  });
}

function legends(scope) {
  // A ```text block holding only "classDef …" lines becomes a swatch legend.
  for (const code of scope.querySelectorAll("pre > code.language-text")) {
    const lines = code.textContent.trim().split("\n");
    if (!lines.length || !lines.every((l) => l.startsWith("classDef "))) continue;
    const box = document.createElement("div");
    box.className = "legend";
    const map = { new: "new", changed: "chg", removed: "del" };
    for (const l of lines) {
      const name = l.split(" ")[1];
      const span = document.createElement("span");
      span.className = map[name] || "";
      span.append(document.createElement("i"), document.createTextNode(name));
      box.append(span);
    }
    code.parentElement.replaceWith(box);
  }
}

function tables(scope) {
  for (const t of scope.querySelectorAll("table")) {
    if (t.parentElement.classList.contains("scroll")) continue;
    const wrap = document.createElement("div");
    wrap.className = "scroll";
    t.replaceWith(wrap);
    wrap.append(t);
  }
  // Triage words in tables become tags.
  for (const td of scope.querySelectorAll("td")) {
    const w = td.textContent.trim();
    if (["accept", "reject", "defer"].includes(w)) td.innerHTML = `<span class="tag ${w}">${w}</span>`;
  }
}

function switcher() {
  const box = document.createElement("div");
  box.className = "kit-switch";
  box.innerHTML = `<select aria-label="Theme"><option value="plex">Plex</option><option value="grotesk">Grotesk</option><option value="signal">Signal</option></select>
<select aria-label="Mode"><option value="">Auto</option><option value="light">Light</option><option value="dark">Dark</option></select>`;
  const [theme, mode] = box.querySelectorAll("select");
  theme.value = root.dataset.kitTheme || "plex";
  mode.value = root.dataset.kitMode || "";
  theme.onchange = () => { root.dataset.kitTheme = theme.value; renderMermaid(); };
  mode.onchange = () => {
    if (mode.value) root.dataset.kitMode = mode.value; else delete root.dataset.kitMode;
    renderMermaid();
  };
  document.body.append(box);
}

async function init(scope = document) {
  for (const code of scope.querySelectorAll("pre > code.language-mermaid")) {
    const el = document.createElement("pre");
    el.className = "mermaid";
    blocks.push({ el, src: code.textContent });
    code.parentElement.replaceWith(el);
  }
  for (const el of scope.querySelectorAll("pre.mermaid:not([data-kit])")) {
    if (blocks.some((b) => b.el === el)) continue;
    el.dataset.kit = "1";
    blocks.push({ el, src: el.textContent });
  }
  legends(scope);
  tables(scope);
  zoom(scope);
  if (root.dataset.kitSwitch !== "off") switcher();
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (!root.dataset.kitMode) renderMermaid();
  });
  await renderMermaid();
}

window.aimanKit = { init, render: renderMermaid };
if (root.dataset.kitAuto !== "off") {
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => init());
  else init();
}
})();
