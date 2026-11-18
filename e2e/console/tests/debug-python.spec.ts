import { test } from "@playwright/test";
import { caption, consoleURL, expectConnected, openDebug, openFile, pauseContinue, setBreakpoint } from "./debug-helpers";

// Driver: aiman-harness-e2e --fixture python --up --debug. app.py line 6
// is bump's body, called every 100ms.
const BUMP_LINE = 6;

test("python: breakpoint hits under debugpy", async ({ page }) => {
  test.skip(!consoleURL, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");

  await caption(page, "Python debug run: open the console and the Debug tab", async () => {
    await openDebug(page);
  });

  await caption(page, "The tab attaches to debugpy in the app container", async () => {
    await expectConnected(page);
  });

  await caption(page, "Open app.py and set a breakpoint on line " + BUMP_LINE, async () => {
    await openFile(page, "app.py");
    await setBreakpoint(page, BUMP_LINE);
  });

  await pauseContinue(page, "app.py", BUMP_LINE, "app.py:" + BUMP_LINE, "python");
});
