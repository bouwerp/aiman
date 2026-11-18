import { expect, test } from "@playwright/test";
import { caption, consoleURL, expectConnected, expectPausedAt, finalShot, line, openDebug, openFile, setBreakpoint } from "./debug-helpers";

// Driver: aiman-harness-e2e --fixture go --up --debug. web.go line 14 is
// the HTTP handler's Fprintf.
const HANDLER_LINE = 14;

test("go: breakpoint in the web handler, stack, variables, step, continue", async ({ page, request }) => {
  test.skip(!consoleURL, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");

  await caption(page, "Go debug run: open the console and the Debug tab", async () => {
    await openDebug(page);
  });

  await caption(page, "The tab connects to Delve by itself; the app starts", async () => {
    await expectConnected(page);
  });

  await caption(page, "Open web.go and click line " + HANDLER_LINE + " to set a breakpoint", async () => {
    await openFile(page, "web.go");
    await setBreakpoint(page, HANDLER_LINE);
  });

  const web = page.locator("#apps a.app-link", { hasText: "web" });
  const href = await web.getAttribute("href");
  expect(href).toBeTruthy();
  let answer: Promise<string> = Promise.resolve("");
  await caption(page, "Trigger the app: request its web link " + href, async () => {
    answer = request.get(href!, { timeout: 120 * 1000 }).then((r) => r.text());
    await expectPausedAt(page, HANDLER_LINE);
  });

  await caption(page, "Paused in the handler: call stack and variables", async () => {
    await expect(page.locator("#dbg-stack")).toContainText("web.go:" + HANDLER_LINE);
    await expect(page.locator("#dbg-vars")).toContainText("Locals");
    await page.screenshot({ path: test.info().outputPath("go-paused.png") });
  });

  await caption(page, "Evaluate an expression in the paused frame", async () => {
    await page.locator("#dbg-expr").fill("1 + 41");
    await page.locator("#dbg-eval button[type=submit]").click();
    await expect(page.locator("#dbg-output")).toContainText("> 1 + 41");
    await expect(page.locator("#dbg-output")).toContainText("42");
    await expect(page.locator("#dbg-out")).toHaveText("Step out");
  });

  await caption(page, "Step over", async () => {
    await page.locator("#dbg-next").click();
    await expect(page.locator("#dbg-status")).toContainText("Paused", { timeout: 30 * 1000 });
    await expect(page.locator("#dbg-src > li[data-current=true]")).toHaveCount(1);
    await expect(line(page, HANDLER_LINE)).not.toHaveAttribute("data-current", "true");
  });

  await caption(page, "Clear the breakpoint and continue: the request completes", async () => {
    await page.locator("#dbg-bps button", { hasText: "Clear" }).first().click();
    await page.locator("#dbg-continue").click();
    await expect(page.locator("#dbg-status")).toHaveText("Running");
    expect(await answer).toContain("hello from the harness fixture");
  });

  await finalShot(page, "go-final");
});
