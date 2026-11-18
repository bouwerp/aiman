import { expect, test } from "@playwright/test";
import { checkItem, openChecks, openReady, pause, shot, url } from "./debugcheck.helpers";

// Scenario 4: two testers on one debug run (driver: --fixture vitest --up
// --debug). The first starts Debug check; the second asks for one while
// the first is still starting and gets harness_debug_busy as a plain
// notice. The page shows a check only once it runs, so the second page's
// button is still enabled during that start.
test("a second Debug check while one runs is refused with harness_debug_busy", async ({ browser }) => {
  test.skip(!url, "set E2E_CONSOLE_URL");
  const opts = { viewport: { width: 1440, height: 900 }, recordVideo: { dir: process.env.E2E_SHOTS || "test-results", size: { width: 1440, height: 900 } } };
  const ctxA = await browser.newContext(opts);
  const ctxB = await browser.newContext(opts);
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();

  await openReady(a);
  await openReady(b);
  await openChecks(a);
  await openChecks(b);
  const sumA = checkItem(a, "sum");
  const sumB = checkItem(b, "sum");
  await expect(sumB.getByRole("button", { name: "Debug check" })).toBeEnabled();

  await sumA.getByRole("button", { name: "Debug check" }).click();
  await sumB.getByRole("button", { name: "Debug check" }).click();

  const n = b.locator("#notice");
  await expect(n).toHaveAttribute("data-kind", "error", { timeout: 60_000 });
  await expect(n.locator("code")).toHaveText("harness_debug_busy");
  await expect(n).toContainText("check sum is under the debugger");
  await pause(b, 2500);
  await shot(b, "02-second-tester-busy");
  // The second page leaves before the check starts, so its own Debug tab
  // does not compete for the check's debugger.
  await ctxB.close();

  // The first tester gets the check; once it runs nobody else can start one.
  await expect(a.locator("#tab-debug")).toHaveAttribute("aria-selected", "true", { timeout: 60_000 });
  await expect(a.locator("#dbg-holder-text")).toContainText("check sum", { timeout: 60_000 });
  await shot(a, "03-first-tester-holds-check");
  await openChecks(a);
  await expect(sumA.getByRole("button", { name: "Debug check" })).toBeDisabled();
  await expect(sumA.locator(".badge")).toHaveText("under the debugger");
  await shot(a, "04-debug-check-disabled-while-running");

  // Let the check run to its end: continue past Vitest's stop before the
  // test file.
  await a.locator("#tab-debug").click();
  const st = a.locator("#dbg-status");
  await expect(st).toContainText(/Paused|Running|ended/, { timeout: 2 * 60 * 1000 });
  if ((await st.textContent())?.includes("Paused")) await a.locator("#dbg-continue").click();
  await pause(a);
  await openChecks(a);
  await expect(sumA.locator(".badge")).toHaveText("exit 0 (debugged)", { timeout: 2 * 60 * 1000 });
  await expect(sumA.getByRole("button", { name: "Debug check" })).toBeEnabled();
  await pause(a, 1500);
  await shot(a, "05-check-ended");
  await ctxA.close();
});
