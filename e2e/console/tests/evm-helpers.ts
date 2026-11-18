import { execFileSync } from "node:child_process";
import { type APIRequestContext, type Page, expect } from "@playwright/test";

// E2E_CONSOLE_URL is a console whose run uses the evm fixture (scenario
// chain). E2E_RUN is that run's id, for the step that sends a transaction
// from inside the run.
export const evmURL = process.env.E2E_CONSOLE_URL;
export const otherURL = process.env.E2E_OTHER_CONSOLE_URL;
export const runID = process.env.E2E_RUN;

// pause paces the recording for a human watching it.
export const pause = (page: Page, ms = 1500) => page.waitForTimeout(ms);

export async function openReady(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  // Every stylesheet loaded and parsed: an unstyled tab still passes the
  // DOM assertions, so check what the recording shows.
  const sheets = await page.evaluate(() =>
    Array.from(document.styleSheets).map((s) => ({ href: s.href, rules: s.cssRules.length })));
  for (const s of sheets) expect(s.rules, `rules in ${s.href}`).toBeGreaterThan(0);
  expect(sheets.map((s) => s.href)).toContainEqual(expect.stringMatching(/\/evm\.css$/));
  await pause(page);
}

export async function openEVMTab(page: Page) {
  await expect(page.locator("#tab-evm")).toBeVisible();
  await page.locator("#tab-evm").click();
  await expect(page.locator("#panel-evm")).toBeVisible();
  await expect(page.locator("#evm-status")).toContainText(/^Chain \d+, block \d+\./);
  await pause(page);
}

// consoleAPI calls the console's API as the page does: the token comes from
// the URL's fragment.
export async function consoleAPI(request: APIRequestContext, url: string, path: string) {
  const u = new URL(url);
  const token = new URLSearchParams(u.hash.slice(1)).get("token") || "";
  const res = await request.get(u.origin + path, { headers: { "X-Aiman-Console-Token": token } });
  expect(res.status()).toBe(200);
  return res.json();
}

// sendFromApp sends a transaction from inside the run: the app container
// reaches the chain service with full JSON-RPC, while the published relay is
// read-only.
export function sendFromApp(run: string, tx: object): string {
  const body = JSON.stringify({ jsonrpc: "2.0", id: 1, method: "eth_sendTransaction", params: [tx] });
  return execFileSync("docker", [
    "exec", `harness-${run}-app-1`, "wget", "-Y", "off", "-q", "-O-",
    "--header", "Content-Type: application/json", "--post-data", body, "http://chain:8545/",
  ], { encoding: "utf8" });
}
