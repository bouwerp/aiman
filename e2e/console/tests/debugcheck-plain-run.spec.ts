import { expect, test } from "@playwright/test";
import { checkItem, openChecks, openReady, pause, shot, url } from "./debugcheck.helpers";

// Scenario 3: Run on a check of a debug run (driver: --fixture vitest --up
// --debug). It runs plainly in app-check, not under the debugger, and the
// page shows its exit and output.
test("debug run: plain Run of a check returns its exit and output", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL");
  await openReady(page);
  await openChecks(page);
  const sum = checkItem(page, "sum");
  await shot(page, "01-checks");

  await sum.getByRole("button", { name: "Run", exact: true }).click();
  await expect(page.locator("#notice")).toContainText("Running check sum");
  await shot(page, "02-running");
  await expect(sum.locator(".badge")).toHaveText("exit 0", { timeout: 5 * 60 * 1000 });
  const out = sum.locator("pre.log");
  await expect(out).toContainText("1 passed");
  await expect(out).toContainText("tests/sum.test.ts");
  // Not under the debugger: compose ran it in app-check, with no inspector,
  // and the output reads as text.
  await expect(out).toContainText("-app-check-run-");
  await expect(out).not.toContainText("Debugger listening");
  expect(await out.textContent()).not.toContain("\u001b[");
  await out.scrollIntoViewIfNeeded();
  await pause(page, 2500);
  await shot(page, "03-exit-0");
});
