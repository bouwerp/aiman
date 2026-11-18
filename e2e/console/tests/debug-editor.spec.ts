import { expect, test } from "@playwright/test";
import { caption, consoleURL, expectConnected, finalShot, openDebug } from "./debug-helpers";

// Any debug driver: the Debug tab's "Use an editor instead" shows a
// launch.json entry for the laptop port, and Copy puts it on the clipboard.
test("editor: the launch.json entry is shown and copyable", async ({ page, context }) => {
  test.skip(!consoleURL, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  const origin = new URL(consoleURL!).origin;
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin });

  await caption(page, "Open the Debug tab of a debug run", async () => {
    await openDebug(page);
    await expectConnected(page);
  });

  let shown = "";
  await caption(page, "Open \"Use an editor instead\": a launch.json entry for this run", async () => {
    const det = page.locator("#dbg-editor");
    await expect(det).toBeVisible();
    await det.locator("summary").click();
    await expect(page.locator("#dbg-launch")).toContainText("configurations");
    await page.locator("#dbg-launch").scrollIntoViewIfNeeded();
    shown = (await page.locator("#dbg-launch").textContent()) || "";
    const cfg = JSON.parse(shown);
    expect(cfg.version).toBe("0.2.0");
    expect(cfg.configurations).toHaveLength(1);
    expect(cfg.configurations[0].type).toBeTruthy();
    expect(cfg.configurations[0].request).toBeTruthy();
    // Every editor entry points at a port on 127.0.0.1.
    expect(shown).toMatch(/127\.0\.0\.1|"port":\s*\d+|debugServer/);
  });

  await caption(page, "Copy puts the same text on the clipboard", async () => {
    await page.locator("#dbg-copy").click();
    await expect(page.locator("#dbg-copy")).toHaveText("Copied");
    const clip = await page.evaluate(() => navigator.clipboard.readText());
    expect(clip).toBe(shown);
  });

  await finalShot(page, "editor-final");
});
