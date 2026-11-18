import { expect, test } from "@playwright/test";
import { caption, consoleURL, expectConnected, openDebug } from "./debug-helpers";

// Any debug driver (the recording uses --fixture go). Two browser contexts
// open the same console: the first holds the debugger, the second sees the
// holder, takes over, then releases.
test("holder: a second page sees the holder, takes over and releases", async ({ browser }) => {
  test.skip(!consoleURL, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  const size = { width: 1440, height: 900 };
  const mk = (name: string) =>
    browser.newContext({ viewport: size, recordVideo: { dir: test.info().outputPath(name), size } });
  const ctxA = await mk("page-a");
  const ctxB = await mk("page-b");
  const a = await ctxA.newPage();
  const b = await ctxB.newPage();

  await caption(a, "Page A opens the Debug tab and holds the debugger", async () => {
    await openDebug(a);
    await expectConnected(a);
    await expect(a.locator("#dbg-release")).toBeVisible();
  });

  await caption(b, "Page B, a second browser, opens the same console: it shows the holder", async () => {
    await openDebug(b);
    await expect(b.locator("#dbg-holder")).toHaveAttribute("data-held", "other", { timeout: 30 * 1000 });
    await expect(b.locator("#dbg-holder-text")).toContainText("Held by aiman harness console");
    await expect(b.locator("#dbg-take")).toBeVisible();
    await expect(b.locator("#dbg-connect")).toBeHidden();
    await b.screenshot({ path: test.info().outputPath("b-sees-holder.png") });
  });

  await caption(b, "Page B takes over: A is disconnected, B holds the debugger", async () => {
    b.once("dialog", (d) => d.accept());
    await b.locator("#dbg-take").click();
    await expectConnected(b);
  });

  await caption(a, "Page A now shows B as the holder", async () => {
    await expect(a.locator("#dbg-holder")).toHaveAttribute("data-held", "other", { timeout: 30 * 1000 });
    await expect(a.locator("#dbg-take")).toBeVisible();
    await a.screenshot({ path: test.info().outputPath("a-after-take.png") });
  });

  await caption(b, "Page B releases: the debugger is free", async () => {
    await b.locator("#dbg-release").click();
    await expect(b.locator("#dbg-holder")).toHaveAttribute("data-held", "free", { timeout: 30 * 1000 });
    await expect(b.locator("#dbg-connect")).toBeVisible();
    // Free must stay free: nobody connects again by itself.
    await b.waitForTimeout(5000);
    await expect(b.locator("#dbg-holder")).toHaveAttribute("data-held", "free");
  });

  await caption(a, "Page A sees the debugger free too", async () => {
    await expect(a.locator("#dbg-holder")).toHaveAttribute("data-held", "free", { timeout: 30 * 1000 });
    await expect(a.locator("#dbg-connect")).toBeVisible();
    await a.waitForTimeout(1500);
    await a.screenshot({ path: test.info().outputPath("a-final.png") });
    await b.screenshot({ path: test.info().outputPath("b-final.png") });
  });

  await ctxA.close();
  await ctxB.close();
});
