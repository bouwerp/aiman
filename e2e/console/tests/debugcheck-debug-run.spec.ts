import { expect, test } from "@playwright/test";
import { BREAK_LINE, checkItem, gutter, hitAndContinue, openChecks, openReady, openTestFile, pause, shot, url } from "./debugcheck.helpers";

// Scenario 1: a debug run (driver: --fixture vitest --up --debug). The
// check with a debug block offers Debug check; the Debug tab follows the
// check, stops in the .ts test and goes back to the app when it ends.
test("debug run: Debug check stops in the test and returns to the app", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL");
  await openReady(page);

  await openChecks(page);
  const sum = checkItem(page, "sum");
  await expect(sum.getByRole("button", { name: "Debug check" })).toBeEnabled();
  await expect(sum.getByRole("button", { name: "Run", exact: true })).toBeEnabled();
  await shot(page, "01-checks");

  // The Debug tab holds the app; the breakpoint set here is pushed to the
  // check's sessions when it connects.
  await page.locator("#tab-debug").click();
  await expect(page.locator("#dbg-holder-text")).toContainText("the app", { timeout: 60_000 });
  await openTestFile(page);
  await gutter(page, BREAK_LINE).click();
  await expect(page.locator("#dbg-bps")).toContainText(`tests/sum.test.ts:${BREAK_LINE}`);
  await pause(page);
  await shot(page, "02-breakpoint-set");

  await openChecks(page);
  await sum.getByRole("button", { name: "Debug check" }).click();
  await expect(page.locator("#tab-debug")).toHaveAttribute("aria-selected", "true", { timeout: 60_000 });
  await expect(page.locator("#dbg-holder-text")).toContainText("check sum", { timeout: 60_000 });
  await shot(page, "03-check-target");

  await hitAndContinue(page, "04");

  await openChecks(page);
  await expect(sum.locator(".badge")).toHaveText("exit 0 (debugged)", { timeout: 2 * 60 * 1000 });
  await expect(sum.locator("pre.log")).toContainText("1 passed");
  // The "waits for the debugger" notice goes with the check.
  await expect(page.locator("#notice")).toBeHidden();
  await pause(page, 1500);
  await shot(page, "05-check-exit-0");

  await page.locator("#tab-debug").click();
  await expect(page.locator("#dbg-holder-text")).toContainText("the app", { timeout: 60_000 });
  await expect(page.locator("#dbg-holder-text")).not.toContainText("check sum");
  await pause(page, 2000);
  await shot(page, "06-back-to-app");
});
