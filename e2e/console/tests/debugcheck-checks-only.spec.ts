import { expect, test } from "@playwright/test";
import { BREAK_LINE, checkItem, gutter, hitAndContinue, openChecks, openReady, openTestFile, pause, shot, url } from "./debugcheck.helpers";

// Scenario 2: a run that debugs only its checks (driver: --fixture vitest
// --up --debug-checks). The app runs plainly; Debug check makes the check
// the debug target and the Debug tab stops in the .ts test.
test("checks-only run: the app runs plainly and Debug check stops in the test", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL");
  await openReady(page);

  // No app debugger: the app runs and logs on its own.
  await page.locator("#tab-logs").click();
  await page.locator("#log-service").selectOption("app");
  await expect(page.locator("#log")).toContainText("ticks", { timeout: 60_000 });
  await pause(page);
  await shot(page, "01-app-runs-plainly");

  await page.locator("#tab-debug").click();
  await expect(page.locator("#dbg-empty")).toContainText("This run debugs its checks");
  await pause(page);
  await shot(page, "02-debug-tab-waits-for-a-check");

  await openChecks(page);
  const sum = checkItem(page, "sum");
  await expect(sum.getByRole("button", { name: "Debug check" })).toBeEnabled();
  await shot(page, "03-checks");
  await sum.getByRole("button", { name: "Debug check" }).click();
  await expect(page.locator("#tab-debug")).toHaveAttribute("aria-selected", "true", { timeout: 60_000 });
  await expect(page.locator("#dbg-holder-text")).toContainText("check sum", { timeout: 60_000 });

  // The breakpoint can only be set once the check is the target.
  await openTestFile(page);
  await gutter(page, BREAK_LINE).click();
  await expect(page.locator("#dbg-bps")).toContainText(`tests/sum.test.ts:${BREAK_LINE}`);
  await shot(page, "04-breakpoint-set");

  await hitAndContinue(page, "05");

  await openChecks(page);
  await expect(sum.locator(".badge")).toHaveText("exit 0 (debugged)", { timeout: 2 * 60 * 1000 });
  await expect(sum.locator("pre.log")).toContainText("1 passed");
  await expect(page.locator("#notice")).toBeHidden();
  await pause(page, 1500);
  await shot(page, "06-check-exit-0");

  // With the check gone the run has nothing to debug again.
  await page.locator("#tab-debug").click();
  await expect(page.locator("#dbg-empty")).toContainText("This run debugs its checks", { timeout: 60_000 });
  await pause(page, 2000);
  await shot(page, "07-back-to-no-target");
});
