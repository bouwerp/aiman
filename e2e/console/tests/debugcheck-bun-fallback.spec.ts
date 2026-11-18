import { expect, test } from "@playwright/test";
import { BREAK_LINE, checkItem, gutter, hitAndContinue, openChecks, openTestFile, pause, shot, url } from "./debugcheck.helpers";

// Scenario 6: the Bun probe fallback (driver: --fixture vitest-bun12 --up
// --debug). The app's Bun 1.2 fails the debug probe; the Debug tab says
// why and offers check debugging, which starts a checks-only run whose
// Vitest check debugs under Node.
test("bun probe failure: the Debug tab explains and starts check debugging", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL");
  await page.goto(url!);
  await expect(page.locator("#state")).toHaveText("failed", { timeout: 5 * 60 * 1000 });
  const failedRun = (await page.locator("#meta").textContent()) || "";
  await pause(page);
  await shot(page, "01-run-failed");

  await page.locator("#tab-debug").click();
  const empty = page.locator("#dbg-empty");
  await expect(empty).toContainText("The debugger cannot attach to this app. The app runs Bun");
  await expect(empty).toContainText("Bun 1.2.23");
  await expect(empty).toContainText("This scenario's checks sum can run under the Node debugger instead");
  const start = empty.getByRole("button", { name: "Start for check debugging" });
  await expect(start).toBeVisible();
  await pause(page, 3000);
  await shot(page, "02-debug-tab-fallback");

  await start.click();
  await expect(page.locator("#notice")).toContainText("Starting smoke for check debugging");
  await expect(page.locator("#meta")).not.toHaveText(failedRun, { timeout: 60_000 });
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await expect(empty).toContainText("This run debugs its checks", { timeout: 30_000 });
  await pause(page);
  await shot(page, "03-checks-only-run-ready");

  await openChecks(page);
  const sum = checkItem(page, "sum");
  await sum.getByRole("button", { name: "Debug check" }).click();
  await expect(page.locator("#dbg-holder-text")).toContainText("check sum", { timeout: 60_000 });
  await openTestFile(page);
  await gutter(page, BREAK_LINE).click();
  await hitAndContinue(page, "04");
  await openChecks(page);
  await expect(sum.locator(".badge")).toHaveText("exit 0 (debugged)", { timeout: 2 * 60 * 1000 });
  await pause(page, 2000);
  await shot(page, "05-check-exit-0");
});
