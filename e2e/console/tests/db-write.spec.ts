import { expect, test } from "@playwright/test";
import { cell, openDB, pause, runSQL, shot } from "./db-helpers";

// Runs against the db fixture by default. E2E_DB_TABLE, E2E_DB_INSERT and
// E2E_DB_MARKER point it at another database, such as the SQLite one.
const url = process.env.E2E_CONSOLE_URL;
const table = process.env.E2E_DB_TABLE || "public.orders";
const insert = process.env.E2E_DB_INSERT || "insert into orders values (44, 'written-by-e2e', 'k-789')";
const marker = process.env.E2E_DB_MARKER || "written-by-e2e";

test("write: confirm, cancel does nothing, confirm writes", async ({ page }, info) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  await openDB(page, url!);
  const tables = page.locator("#db-tables button");
  const rows = page.locator("#db-result tbody tr");
  await tables.filter({ hasText: table }).click();
  await expect(rows.first()).toBeVisible();
  const before = await rows.count();
  await expect(page.locator("#db-result")).not.toContainText(marker);
  await pause(page);

  await page.locator("#db-write").check();
  await pause(page, 600);

  // Cancel: the page asked, nothing ran.
  const asked: string[] = [];
  page.once("dialog", async (d) => {
    asked.push(d.message());
    await pause(page, 1500);
    await d.dismiss();
  });
  await runSQL(page, insert);
  await expect.poll(() => asked.length).toBe(1);
  expect(asked[0]).toContain("as the database owner");
  expect(asked[0]).toContain(insert);
  await expect(page.locator("#db-write")).toBeChecked();
  await tables.filter({ hasText: table }).click();
  await expect(rows).toHaveCount(before);
  await expect(page.locator("#db-result")).not.toContainText(marker);
  await pause(page);
  await shot(page, info, "01-after-cancel");

  // Confirm: the statement runs as the owner.
  page.once("dialog", async (d) => {
    await pause(page, 1500);
    await d.accept();
  });
  await page.locator("#db-run").click();
  await expect(page.locator("#db-role")).toHaveText(/owner/);
  await expect(page.locator("#db-write")).not.toBeChecked();
  await pause(page);
  await shot(page, info, "02-written");

  await tables.filter({ hasText: table }).click();
  await expect(rows).toHaveCount(before + 1);
  await expect(cell(page, marker)).toBeVisible();
  await pause(page, 1500);
  await shot(page, info, "03-row-shown");
});
