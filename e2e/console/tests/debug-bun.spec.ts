import { test } from "@playwright/test";
import { caption, consoleURL, expectConnected, openDebug, openFile, pauseContinue, setBreakpoint } from "./debug-helpers";

// Driver: aiman-harness-e2e --fixture bun --up --debug. Bun runs
// src/index.ts itself; line 9 is bump's body, called every 100ms.
const BUMP_LINE = 9;

test("bun: breakpoint in the TypeScript source hits", async ({ page }) => {
  test.skip(!consoleURL, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");

  await caption(page, "Bun debug run: open the console and the Debug tab", async () => {
    await openDebug(page);
  });

  await caption(page, "The tab attaches to Bun's inspector through bun-dap", async () => {
    await expectConnected(page);
  });

  await caption(page, "Open src/index.ts and set a breakpoint on line " + BUMP_LINE, async () => {
    await openFile(page, "src/index.ts");
    await setBreakpoint(page, BUMP_LINE);
  });

  await pauseContinue(page, "src/index.ts", BUMP_LINE, "src/index.ts:" + BUMP_LINE, "bun");
});
