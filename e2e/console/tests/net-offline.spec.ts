import { expect, Page, test } from "@playwright/test";

// Run against `aiman-harness-e2e --fixture net --up` (offline). The fixture's
// app calls a stubbed Stripe refund, a stubbed ping and example.com every 3s.
const url = process.env.E2E_CONSOLE_URL;
const pause = (page: Page, ms = 1200) => page.waitForTimeout(ms);

test("offline network capture: stubs, 502, detail, filter, pause, clear", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  await page.goto(url!);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await pause(page);

  await page.locator("#tab-net").click();
  await expect(page.locator("#panel-net")).toBeVisible();
  await expect(page.locator("#net-status")).toContainText("Offline", { timeout: 30_000 });
  // Every call carries an Authorization header the edge redacts.
  await expect(page.locator("#net-status")).toContainText(/\d+ values redacted/);

  const rows = page.locator("#net-list > li.net-item");
  const stripe = rows.filter({ hasText: "https://api.stripe.com/v1/refunds" }).first();
  const ping = rows.filter({ hasText: "https://api.example.com/ping" }).first();
  const example = rows.filter({ hasText: "https://example.com/" }).first();
  await expect(stripe.locator(".net-badge")).toHaveText("200 stub");
  await expect(stripe.locator(".net-method")).toHaveText("POST");
  await expect(ping.locator(".net-badge")).toHaveText("200 stub");
  await expect(example.locator(".net-badge")).toHaveText("502 offline");
  await page.screenshot({ path: test.info().outputPath("01-list.png") });
  await pause(page);

  // Detail of the stubbed refund: Authorization redacted, both bodies shown.
  // Rows are newest first and new calls arrive every 3s: pin the flow by id.
  const sid = await stripe.locator("button.net-row").getAttribute("aria-controls");
  await page.locator(`button[aria-controls="${sid}"]`).click();
  const d = page.locator("#" + sid);
  await expect(d).toBeVisible();
  await expect(d).toContainText("TLS (run CA)");
  await expect(d).toContainText("Authorization: [redacted]");
  await expect(d).not.toContainText("sk_test_fixture_secret_value");
  await expect(d).toContainText("amount=100");
  await expect(d).toContainText('"status":"succeeded"');
  await d.scrollIntoViewIfNeeded();
  await page.screenshot({ path: test.info().outputPath("02-detail.png") });
  await pause(page, 2000);

  // Detail of the offline 502: the edge's own message.
  const eid = await example.locator("button.net-row").getAttribute("aria-controls");
  await page.locator(`button[aria-controls="${eid}"]`).click();
  await expect(page.locator("#" + eid)).toContainText("offline sandbox, no stub for example.com");
  await pause(page, 1500);

  // Filter narrows the list to the stripe calls.
  await page.locator("#net-filter").pressSequentially("stripe", { delay: 120 });
  await expect(rows.first()).toBeVisible();
  for (const t of await rows.locator(".net-url").allTextContents()) expect(t).toContain("api.stripe.com");
  await page.screenshot({ path: test.info().outputPath("03-filter.png") });
  await pause(page);
  await page.locator("#net-filter").fill("no-such-host");
  await expect(page.locator("#net-empty")).toHaveText("No call matches the filter.");
  await pause(page);
  await page.locator("#net-filter").fill("");
  await expect(rows.first()).toBeVisible();

  // The outcome badge stays inside its column.
  const badgeBox = await example.locator(".net-badge").boundingBox();
  const urlBox = await example.locator(".net-url").boundingBox();
  expect(badgeBox!.x + badgeBox!.width).toBeLessThanOrEqual(urlBox!.x);

  // Pause: no new rows across two 3s app cycles.
  await page.locator("#net-pause").click();
  await expect(page.locator("#net-pause")).toHaveText("Resume");
  await expect(page.locator("#net-pause")).toHaveAttribute("aria-pressed", "true");
  await page.waitForTimeout(500);
  const status = await page.locator("#net-status").textContent();
  const before = await rows.count();
  await page.waitForTimeout(7000);
  expect(await rows.count()).toBe(before);
  expect(await page.locator("#net-status").textContent()).toBe(status);
  await page.screenshot({ path: test.info().outputPath("04-paused.png") });
  await page.locator("#net-pause").click();
  await expect(page.locator("#net-pause")).toHaveText("Pause");
  await expect.poll(() => rows.count(), { timeout: 15_000 }).toBeGreaterThan(before);
  await pause(page);

  // Clear empties the list; the poll brings back only newer calls.
  await page.locator("#net-pause").click();
  await page.locator("#net-clear").click();
  await expect(rows).toHaveCount(0);
  await expect(page.locator("#net-empty")).toBeVisible();
  await page.screenshot({ path: test.info().outputPath("05-cleared.png") });
  await pause(page, 1500);
  const total = await rows.count();
  await page.locator("#net-pause").click();
  await expect.poll(() => rows.count(), { timeout: 15_000 }).toBeGreaterThan(total);
  expect(await rows.count()).toBeLessThan(before);
  await pause(page, 1500);
});
