import { expect, Page, TestInfo } from "@playwright/test";

// pause lets a human watching the video follow each step.
export const pause = (page: Page, ms = 1200) => page.waitForTimeout(ms);

export async function shot(page: Page, info: TestInfo, name: string) {
  await page.screenshot({ path: info.outputPath(name + ".png"), fullPage: true });
}

// openDB waits for a ready run and opens the Database tab.
export async function openDB(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await pause(page);
  await page.locator("#tab-db").click();
  await expect(page.locator("#db")).toBeVisible();
}

export async function runSQL(page: Page, sql: string) {
  await page.locator("#db-sql").fill(sql);
  await pause(page, 600);
  await page.locator("#db-run").click();
}

// expectRefused waits for an error notice with code and text.
export async function expectRefused(page: Page, code: RegExp | string, text: RegExp | string) {
  const n = page.locator("#notice");
  await expect(n).toBeVisible();
  await expect(n).toHaveAttribute("data-kind", "error");
  await expect(n.locator("code")).toHaveText(code);
  await expect(n).toContainText(text);
}

export const cell = (page: Page, text: string) => page.locator("#db-result td", { hasText: text });
