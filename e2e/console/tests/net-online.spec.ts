import { expect, Page, test } from "@playwright/test";

// Run against `aiman-harness-e2e --fixture net --up --network online`. The
// fixture's app calls example.com (public, through the proxy) and the cloud
// metadata address (link-local, which the edge must refuse) every 3s.
const url = process.env.E2E_CONSOLE_URL;
const pause = (page: Page, ms = 1200) => page.waitForTimeout(ms);

test("online network capture: public host upstream over TLS, private address refused", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  test.skip(!!process.env.E2E_NO_INTERNET, "this host has no internet");
  await page.goto(url!);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await expect(page.locator("#facts")).toContainText("online");
  await pause(page);

  await page.locator("#tab-net").click();
  await expect(page.locator("#net-status")).toContainText("Online: calls go out through the edge proxy", { timeout: 30_000 });

  const rows = page.locator("#net-list > li.net-item");
  const example = rows.filter({ hasText: "https://example.com/" }).first();
  const metadata = rows.filter({ hasText: "169.254.169.254" }).first();
  const stripe = rows.filter({ hasText: "https://api.stripe.com/v1/refunds" }).first();
  await expect(example.locator(".net-badge")).toHaveText("200 upstream", { timeout: 60_000 });
  await expect(metadata.locator(".net-badge")).toContainText("refused");
  // Stubs answer in either mode.
  await expect(stripe.locator(".net-badge")).toHaveText("200 stub");
  // The longest outcome badges stay inside their column.
  for (const r of [example, metadata]) {
    const badge = await r.locator(".net-badge").boundingBox();
    const link = await r.locator(".net-url").boundingBox();
    expect(badge!.x + badge!.width).toBeLessThanOrEqual(link!.x);
  }
  await page.screenshot({ path: test.info().outputPath("01-list.png") });
  await pause(page);

  // Rows are newest first and new calls arrive every 3s: pin flows by id.
  const eid = await example.locator("button.net-row").getAttribute("aria-controls");
  await page.locator(`button[aria-controls="${eid}"]`).click();
  const ed = page.locator("#" + eid);
  await expect(ed).toContainText("TLS (run CA)");
  await expect(ed).toContainText("Example Domain");
  await expect(ed).toContainText("Authorization: [redacted]");
  await ed.scrollIntoViewIfNeeded();
  await page.screenshot({ path: test.info().outputPath("02-upstream.png") });
  await pause(page, 2000);
  await page.locator(`button[aria-controls="${eid}"]`).click();

  const mid = await metadata.locator("button.net-row").getAttribute("aria-controls");
  await page.locator(`button[aria-controls="${mid}"]`).click();
  const md = page.locator("#" + mid);
  await expect(md).toContainText("plain HTTP");
  await md.scrollIntoViewIfNeeded();
  await page.screenshot({ path: test.info().outputPath("03-refused.png") });
  await pause(page, 2000);

  await page.locator("#net-filter").pressSequentially("refused", { delay: 120 });
  for (const t of await rows.locator(".net-badge").allTextContents()) expect(t).toContain("refused");
  await page.screenshot({ path: test.info().outputPath("04-filter-refused.png") });
  await pause(page, 1500);
});
