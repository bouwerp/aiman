import { expect, test } from "@playwright/test";
import { checkItem, openChecks, openReady, pause, shot, url } from "./debugcheck.helpers";

// Scenario 5: no debugger attaches to a debug check (driver: --fixture
// vitest --up --debug-checks --attach-timeout 40s). The page's debugger
// bridge is cut, as for a tester whose browser cannot reach it; serve stops
// the check at the attach timeout and the Checks section says so.
test("a debug check nobody attaches to stops at the attach timeout", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL");
  // Every debugger WebSocket closes at once: the edge never sees a client.
  await page.routeWebSocket(/\/api\/dap$/, (ws) => ws.close());
  await openReady(page);
  await openChecks(page);
  const sum = checkItem(page, "sum");
  await sum.getByRole("button", { name: "Debug check" }).click();
  await expect(page.locator("#tab-debug")).toHaveAttribute("aria-selected", "true", { timeout: 60_000 });
  await expect(page.locator("#dbg-holder-text")).toContainText("waits for a debugger and stops if none attaches in time", { timeout: 60_000 });
  await expect(page.locator("#dbg-status")).toContainText("The debugger did not answer");
  await pause(page, 2000);
  await shot(page, "01-check-waits-for-a-debugger");

  await openChecks(page);
  await expect(sum.locator(".badge")).toHaveText("under the debugger");
  await expect(sum.locator(".badge")).toHaveText("no debugger attached", { timeout: 3 * 60 * 1000 });
  await expect(sum).toContainText("no debugger attached to check sum within 40s; it was stopped");
  await expect(sum.getByRole("button", { name: "Debug check" })).toBeEnabled();
  await expect(page.locator("#notice")).toBeHidden();
  await pause(page, 2500);
  await shot(page, "02-stopped-at-attach-timeout");
});
