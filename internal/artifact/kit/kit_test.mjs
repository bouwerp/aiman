// Acceptance tests for docs/specs/artifacts.md piece K (theme kit). Do not edit.
// Renders testdata/page.html (the design page) with the kit under the Worker's
// CSP in Chromium: 3 themes x 2 modes, every diagram drawn, nothing wider than
// the page at 1100 and 390 px, no page errors.
// Needs Playwright with Chromium: run from e2e/console (npm ci) or set NODE_PATH.
import test from "node:test";
import assert from "node:assert/strict";
import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
let chromium;
try {
  ({ chromium } = await import("playwright"));
} catch {
  test("kit (skipped: playwright not installed)", { skip: true }, () => {});
}

function cspFromWorker() {
  const src = fs.readFileSync(path.join(here, "../worker/worker.js"), "utf8");
  const cdns = src.match(/const CDNS = "([^"]+)"/)[1];
  const block = src.match(/const CSP = \[([\s\S]*?)\]\.join/)[1];
  return [...block.matchAll(/(["`])(.*?)\1/g)].map((m) => m[2].replace("${CDNS}", cdns)).join("; ");
}

function serve() {
  const csp = cspFromWorker();
  const types = { ".html": "text/html; charset=utf-8", ".css": "text/css", ".js": "text/javascript", ".woff2": "font/woff2" };
  const srv = http.createServer((req, res) => {
    const url = req.url.split("?")[0];
    const f = url === "/" ? path.join(here, "testdata/page.html") : path.join(here, url.replace(/^\/kit\//, ""));
    if (!f.startsWith(here) || !fs.existsSync(f)) { res.writeHead(404); return res.end(); }
    res.writeHead(200, { "Content-Type": types[path.extname(f)] || "application/octet-stream", "Content-Security-Policy": csp });
    res.end(fs.readFileSync(f));
  });
  return new Promise((r) => srv.listen(0, "127.0.0.1", () => r(srv)));
}

if (chromium) {
  test("themes, modes and fit", { timeout: 180000 }, async () => {
    const srv = await serve();
    const base = `http://127.0.0.1:${srv.address().port}/`;
    const browser = await chromium.launch();
    try {
      for (const theme of ["plex", "grotesk", "signal"]) {
        for (const scheme of ["light", "dark"]) {
          for (const width of [1100, 390]) {
            const page = await browser.newPage({ viewport: { width, height: 1200 }, colorScheme: scheme });
            const errors = [];
            page.on("pageerror", (e) => errors.push(String(e.message || e)));
            await page.goto(base);
            await page.evaluate((t) => { document.documentElement.dataset.kitTheme = t; return window.aimanKit.render(); }, theme);
            await page.waitForFunction(() => {
              const pres = [...document.querySelectorAll("pre.mermaid")];
              return pres.length > 0 && pres.every((p) => p.querySelector("svg"));
            }, null, { timeout: 30000 });
            const r = await page.evaluate(() => ({
              overflow: document.documentElement.scrollWidth > innerWidth,
              wide: [...document.querySelectorAll("pre.mermaid")].filter((p) => p.querySelector("svg").getBoundingClientRect().width > p.clientWidth + 1).length,
              bg: getComputedStyle(document.body).backgroundColor,
            }));
            assert.equal(r.overflow, false, `${theme}/${scheme}/${width}: page scrolls sideways`);
            assert.equal(r.wide, 0, `${theme}/${scheme}/${width}: a diagram is wider than its box`);
            assert.deepEqual(errors, [], `${theme}/${scheme}/${width}: page errors`);
            if (scheme === "dark") {
              const [rr, gg, bb] = r.bg.match(/\d+/g).map(Number);
              assert.ok(rr + gg + bb < 150, `${theme}: dark background expected, got ${r.bg}`);
            }
            await page.close();
          }
        }
      }
    } finally {
      await browser.close();
      srv.close();
    }
  });
}
