import { expect, test } from "@playwright/test";
import { evmURL, openEVMTab, openReady, pause, runID, sendFromApp } from "./evm-helpers";

test("transaction list: status, gas, selector, and a new transaction appears", async ({ page }) => {
  test.skip(!evmURL || !runID, "set E2E_CONSOLE_URL and E2E_RUN for the evm fixture");
  await openReady(page, evmURL!);
  await openEVMTab(page);

  const rows = page.locator("#evm-txs .evm-tx");
  await expect(page.locator("#evm-status")).toContainText("3 transactions");
  await expect(rows).toHaveCount(3);
  // Newest first: the call that reverts, then the two creates.
  await expect(rows.nth(0).locator(".evm-badge")).toHaveText("reverted");
  await expect(rows.nth(0).locator(".evm-meta")).toContainText("0x12345678");
  await expect(rows.nth(0).locator(".evm-meta")).toContainText(/gas \d+/);
  for (const i of [1, 2]) {
    await expect(rows.nth(i).locator(".evm-badge")).toHaveText("success");
    await expect(rows.nth(i).locator(".evm-meta")).toContainText(/create 0x.*gas \d+/);
  }
  await rows.nth(0).hover();
  await page.screenshot({ path: test.info().outputPath("three-txs.png") });
  await pause(page, 2500);

  // A transfer sent from inside the run shows on the next 5s refresh.
  const out = sendFromApp(runID!, {
    from: "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266",
    to: "0x000000000000000000000000000000000000dEaD",
    value: "0x1",
  });
  expect(out).toContain('"result":"0x');
  await expect(rows).toHaveCount(4, { timeout: 15 * 1000 });
  await expect(page.locator("#evm-status")).toContainText("4 transactions");
  await expect(rows.nth(0).locator(".evm-badge")).toHaveText("success");
  await expect(rows.nth(0).locator(".evm-meta")).toContainText(/0x000000…dead/i);
  await page.screenshot({ path: test.info().outputPath("four-txs.png") });
  await pause(page, 2500);
});
