import { expect, test } from "@playwright/test";
import { cell, expectRefused, openDB, pause, runSQL, shot } from "./db-helpers";

// Run against the driver's sqlite fixture: --fixture sqlite --up.
const url = process.env.E2E_CONSOLE_URL;

test("sqlite: tables, rows, redaction, query, refusals and a write", async ({ page }, info) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  await openDB(page, url!);
  await expect(page.locator("#db-service")).toHaveValue("lite");

  const tables = page.locator("#db-tables button");
  await expect(tables).toHaveText(["notes", "users"]);
  await expect(page.locator("#db-role")).toHaveText(/read-only/);
  await pause(page);
  await shot(page, info, "01-tables");

  await tables.filter({ hasText: "users" }).click();
  await expect(cell(page, "Ada")).toBeVisible();
  await expect(page.locator("#db-result td.redacted")).toHaveText("[redacted]");
  await expect(page.locator("#db-result")).not.toContainText("hunter2");
  await pause(page);
  await shot(page, info, "02-users-redacted");

  await tables.filter({ hasText: "notes" }).click();
  await expect(page.locator("#db-result tbody tr")).toHaveCount(2);
  await expect(page.locator("#db-result")).not.toContainText("t-111");
  await pause(page);
  await shot(page, info, "03-notes");

  await runSQL(page, "select body from notes where id = 2");
  await expect(page.locator("#db-result tbody tr")).toHaveCount(1);
  await expect(cell(page, "second note")).toBeVisible();
  await pause(page);
  await shot(page, info, "04-select");

  // SQLite-only words, then a write the read-only file refuses.
  await runSQL(page, "pragma table_info(notes)");
  await expectRefused(page, "harness_db_refused", /PRAGMA/);
  await pause(page);
  await shot(page, info, "05-pragma-refused");

  await runSQL(page, "delete from notes where id = 1");
  await expectRefused(page, "invalid_params", /readonly|read-only/i);
  await pause(page);
  await shot(page, info, "06-delete-read-only");

  // A write as the owner, after the confirm.
  await page.locator("#db-write").check();
  page.once("dialog", async (d) => {
    await pause(page, 1500);
    await d.accept();
  });
  await runSQL(page, "insert into notes (body, api_token) values ('written-by-e2e', 't-333')");
  await expect(page.locator("#db-role")).toHaveText(/owner/);
  await expect(page.locator("#db-write")).not.toBeChecked();
  await pause(page);
  await shot(page, info, "07-written");

  await tables.filter({ hasText: "notes" }).click();
  await expect(page.locator("#db-result tbody tr")).toHaveCount(3);
  await expect(cell(page, "written-by-e2e")).toBeVisible();
  await expect(page.locator("#db-role")).toHaveText(/read-only/);
  await pause(page, 1500);
  await shot(page, info, "08-row-shown");
});
