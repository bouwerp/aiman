import { expect, test } from "@playwright/test";
import { caption, consoleURL, expectConnected, openDebug, openFile, pauseContinue, setBreakpoint } from "./debug-helpers";

// Driver: aiman-harness-e2e --fixture node --up --debug. The app runs
// dist/app.js; its source map points at src/app.ts, where line 9 is tick's
// body.
const TICK_LINE = 9;

test("node: breakpoint in the TypeScript source through the map; child session listed", async ({ page }) => {
  test.skip(!consoleURL, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");

  await caption(page, "Node debug run: open the console and the Debug tab", async () => {
    await openDebug(page);
  });

  await caption(page, "The tab attaches to js-debug, which opens the Node process as a child session", async () => {
    await expectConnected(page);
    await expect(page.locator("#dbg-sessions")).toBeVisible({ timeout: 60 * 1000 });
    await expect(page.locator("#dbg-sessions")).toContainText("child session");
    await expect(page.locator("#dbg-sessions > li")).toHaveCount(2);
  });

  await caption(page, "Open src/app.ts (TypeScript) and set a breakpoint on line " + TICK_LINE, async () => {
    await openFile(page, "src/app.ts");
    await setBreakpoint(page, TICK_LINE);
  });

  await pauseContinue(page, "src/app.ts", TICK_LINE, "src/app.ts:" + TICK_LINE, "node");
});
