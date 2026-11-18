import { expect, test } from "@playwright/test";
import { cell, expectRefused, openDB, pause, runSQL, shot } from "./db-helpers";

// Run against the driver's db fixture: --fixture db --up.
const url = process.env.E2E_CONSOLE_URL;

test("postgres: tables, rows, redaction, reads and refusals", async ({ page }, info) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  await openDB(page, url!);

  // The postgres relay is a psql address, not an HTTP link.
  await expect(page.locator("#apps .db-link")).toContainText(/db\s*postgres 127\.0\.0\.1:\d+/);
  await expect(page.locator("#apps a.app-link")).toHaveCount(0);

  // The tables, read as the read-only role.
  const tables = page.locator("#db-tables button");
  await expect(tables).toHaveText(["public.customers", "public.orders"]);
  await expect(page.locator("#db-role")).toHaveText(/aiman_ro/);
  await pause(page);
  await shot(page, info, "01-tables");

  // A table's rows; the password column is redacted.
  await tables.filter({ hasText: "public.customers" }).click();
  await expect(cell(page, "Ada")).toBeVisible();
  await expect(page.locator("#db-result th.redacted")).toHaveText("password");
  await expect(page.locator("#db-result td.redacted")).toHaveText("[redacted]");
  await expect(page.locator("#db-result")).not.toContainText("hunter2");
  await expect(page.locator("#db-status")).toContainText("hidden columns: password");
  await pause(page);
  await shot(page, info, "02-customers-redacted");

  await tables.filter({ hasText: "public.orders" }).click();
  await expect(cell(page, "paid")).toBeVisible();
  await expect(page.locator("#db-result td.redacted")).toHaveText(["[redacted]", "[redacted]"]);
  await expect(page.locator("#db-result")).not.toContainText("k-123");
  await pause(page);
  await shot(page, info, "03-orders-redacted");

  // A SELECT.
  await runSQL(page, "select id, status from orders where status = 'paid'");
  await expect(page.locator("#db-result tbody tr")).toHaveCount(1);
  await expect(cell(page, "42")).toBeVisible();
  await expect(page.locator("#db-status")).toContainText("1 rows");
  await pause(page);
  await shot(page, info, "04-select");

  // Refused before the database: two statements, SET, COPY.
  await runSQL(page, "select 1; select 2");
  await expectRefused(page, "harness_db_refused", /one statement/i);
  // The SELECT's rows no longer show under the refusal.
  await expect(page.locator("#db-result")).toBeEmpty();
  await pause(page);
  await shot(page, info, "05-multi-refused");

  await runSQL(page, "set statement_timeout = 0");
  await expectRefused(page, "harness_db_refused", /SET/);
  await pause(page);
  await shot(page, info, "06-set-refused");

  await runSQL(page, "copy orders to stdout");
  await expectRefused(page, "harness_db_refused", /COPY/);
  await pause(page);
  await shot(page, info, "07-copy-refused");

  // Writes without the write box. An INSERT names INTO, which serve's read
  // check refuses before the database sees it.
  await runSQL(page, "insert into orders values (44, 'new', 'k-789')");
  await expectRefused(page, "harness_db_refused", /may not use INTO/);
  await pause(page);
  await shot(page, info, "08-insert-refused");

  // An UPDATE names SET, refused the same way.
  await runSQL(page, "update orders set status = 'void' where id = 42");
  await expectRefused(page, "harness_db_refused", /may not use SET/);
  await pause(page);
  await shot(page, info, "09-update-refused");

  // A DELETE passes that check; the read-only transaction refuses it.
  await runSQL(page, "delete from orders where id = 42");
  await expectRefused(page, "invalid_params", /read-only transaction/);
  await pause(page);
  await shot(page, info, "10-delete-read-only");

  // Nothing was written.
  await tables.filter({ hasText: "public.orders" }).click();
  await expect(page.locator("#db-result tbody tr")).toHaveCount(2);
  await pause(page, 1500);
});
