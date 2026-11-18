import { expect, Page, test } from "@playwright/test";

// The console URL carries its token in the fragment.
export const consoleURL = process.env.E2E_CONSOLE_URL;

// caption shows text in a fixed banner so a recording explains each step,
// and wraps the step in test.step for the report. The banner is styled
// through CSSOM: the console's CSP refuses style attributes.
export async function caption<T>(page: Page, text: string, body: () => Promise<T>): Promise<T> {
  return test.step(text, async () => {
    await page.evaluate((t) => {
      let b = document.getElementById("e2e-caption");
      if (!b) {
        b = document.createElement("div");
        b.id = "e2e-caption";
        const s = b.style;
        s.position = "fixed";
        s.left = "50%";
        s.bottom = "24px";
        s.transform = "translateX(-50%)";
        s.zIndex = "99999";
        s.padding = "10px 18px";
        s.borderRadius = "8px";
        s.background = "rgba(20, 20, 30, 0.88)";
        s.color = "#fff";
        s.font = "600 18px system-ui, sans-serif";
        s.maxWidth = "80vw";
        s.pointerEvents = "none";
        document.body.append(b);
      }
      b.textContent = t;
    }, text);
    await page.waitForTimeout(900);
    return body();
  });
}

// openDebug loads the console, waits for the driver's run to be ready and
// opens the Debug tab.
export async function openDebug(page: Page) {
  await page.goto(consoleURL!);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await page.locator("#tab-debug").click();
  await expect(page.locator("#dbg")).toBeVisible();
}

// expectConnected waits for this page to hold the debugger.
export async function expectConnected(page: Page) {
  await expect(page.locator("#dbg-holder-text")).toContainText("You hold the debugger", { timeout: 60 * 1000 });
  await expect(page.locator("#dbg-status")).toHaveText("Running", { timeout: 60 * 1000 });
}

// openFile picks a file in the source view.
export async function openFile(page: Page, rel: string) {
  const f = page.locator("#dbg-file");
  await f.fill(rel);
  await f.press("Enter");
  await expect(page.locator("#dbg-src-hint")).toContainText(rel + ",");
}

export function line(page: Page, n: number) {
  return page.locator("#dbg-src > li").nth(n - 1);
}

export function gutter(page: Page, n: number) {
  return line(page, n).locator("button.gutter");
}

// setBreakpoint clicks the gutter and waits for the debugger to verify it.
export async function setBreakpoint(page: Page, n: number) {
  await gutter(page, n).click();
  await expect(gutter(page, n)).toHaveAttribute("data-bp", "verified", { timeout: 60 * 1000 });
}

export async function expectPausedAt(page: Page, n: number) {
  await expect(page.locator("#dbg-status")).toContainText("Paused", { timeout: 60 * 1000 });
  await expect(line(page, n)).toHaveAttribute("data-current", "true");
  await expect(page.locator("#dbg-stack > li button.frame").first()).toBeVisible();
  await expect(page.locator("#dbg-vars .var").first()).toBeVisible();
}

// pauseContinue is the shared tail of a breakpoint scenario: the app hits
// the breakpoint by itself (each fixture loops every 100ms), the stack and
// variables show, then the breakpoint is cleared and the app continues.
export async function pauseContinue(page: Page, file: string, n: number, frameText: string, shot: string) {
  await caption(page, "The app loops by itself: it stops at " + file + ":" + n, async () => {
    await expectPausedAt(page, n);
    await expect(page.locator("#dbg-stack")).toContainText(frameText);
  });
  await caption(page, "Call stack and variables of the paused frame", async () => {
    await expect(page.locator("#dbg-vars .var-kids .var").first()).toBeVisible({ timeout: 30 * 1000 });
    await page.screenshot({ path: test.info().outputPath(shot + "-paused.png") });
  });
  await caption(page, "Clear the breakpoint and continue", async () => {
    await page.locator("#dbg-bps button", { hasText: "Clear" }).first().click();
    await page.locator("#dbg-continue").click();
    await expect(page.locator("#dbg-status")).toHaveText("Running");
  });
  await finalShot(page, shot + "-final");
}

export async function finalShot(page: Page, name: string) {
  await page.waitForTimeout(1200);
  await page.evaluate(() => document.getElementById("e2e-caption")?.remove());
  await page.screenshot({ path: test.info().outputPath(name + ".png") });
}
