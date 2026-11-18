import { expect, type Page } from "@playwright/test";

// Shared steps for the check-debugging specs. Each spec runs against a
// driver started with the vitest fixture (`--fixture vitest`), whose check
// "sum" has a node debug block and whose test stops on line 6.
export const url = process.env.E2E_CONSOLE_URL;
export const shotDir = process.env.E2E_SHOTS || "";

export const TEST_FILE = "tests/sum.test.ts";
export const BREAK_LINE = 6;

// pause keeps the recording at a pace a person can follow.
export const pause = (page: Page, ms = 1200) => page.waitForTimeout(ms);

export async function shot(page: Page, name: string) {
  if (shotDir) await page.screenshot({ path: `${shotDir}/${name}.png`, fullPage: true });
}

export async function openReady(page: Page) {
  await page.goto(url!);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await pause(page);
}

export function checkItem(page: Page, name: string) {
  return page.locator("#checks > li.check-item").filter({ has: page.locator(".check-name", { hasText: new RegExp(`^${name}$`) }) });
}

export async function openChecks(page: Page) {
  await page.locator("#tab-checks").click();
  await expect(page.locator("#panel-checks")).toBeVisible();
  await pause(page);
}

// openTestFile shows the test in the Debug tab's source view.
export async function openTestFile(page: Page) {
  const file = page.locator("#dbg-file");
  await file.fill(TEST_FILE);
  await file.dispatchEvent("change");
  await expect(page.locator("#dbg-src > li")).toHaveCount(9, { timeout: 30_000 });
  await pause(page, 600);
}

export function gutter(page: Page, line: number) {
  return page.locator("#dbg-src > li").nth(line - 1).locator("button.gutter");
}

// hitAndContinue waits for the program to stop on the test's line,
// continuing past any earlier stop (Vitest's own break before the test
// file), then continues it.
export async function hitAndContinue(page: Page, label: string) {
  const where = page.locator("#dbg-stack .frame-where").first();
  for (let i = 0; ; i++) {
    await expect(page.locator("#dbg-status")).toContainText("Paused", { timeout: 2 * 60 * 1000 });
    await expect(where).toBeVisible({ timeout: 30_000 });
    const at = (await where.textContent()) || "";
    if (at === `${TEST_FILE}:${BREAK_LINE}`) break;
    console.log(`paused at ${at}, continuing`);
    expect(i, `stopped at ${at} instead of the breakpoint`).toBeLessThan(5);
    await shot(page, `${label}-early-stop-${i}`);
    await pause(page);
    await page.locator("#dbg-continue").click();
    // The next stop can follow at once, so wait for the place to change.
    await expect(where).not.toHaveText(at, { timeout: 30_000 });
  }
  await expect(page.locator(`#dbg-src > li[data-current="true"]`)).toHaveCount(1);
  await pause(page, 1500);
  await shot(page, `${label}-paused`);
  // Step over the sum: total is then 5 in the arrow's scope.
  await page.locator("#dbg-next").click();
  await expect(where).toHaveText(`${TEST_FILE}:${BREAK_LINE + 1}`, { timeout: 30_000 });
  await expect(page.locator("#dbg-vars")).toContainText(/total\s*5/, { timeout: 30_000 });
  await pause(page, 2000);
  await shot(page, `${label}-stepped`);
  await page.locator("#dbg-continue").click();
  await expect(page.locator("#dbg-status")).not.toContainText("Paused", { timeout: 30_000 });
  await pause(page);
}
