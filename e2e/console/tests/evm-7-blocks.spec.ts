import { expect, test } from "@playwright/test";
import { DEV0, evmURL, openEVMTab, openReady, pause, rpcFromApp, runID, sendFromApp } from "./evm-helpers";

// Spread the chain from inside the run: the fixture's 3 transactions in
// blocks 1-3, 10 empty blocks, a transfer (block 14), 10 empty blocks, a
// transfer (block 25). The last 10 blocks then hold 1 transaction, the last
// 20 hold 2 and the last 64 all 5.
test("blocks selector: the request and the list follow it", async ({ page }) => {
  test.skip(!evmURL || !runID, "set E2E_CONSOLE_URL and E2E_RUN for a fresh evm fixture run");
  const transfer = { from: DEV0, to: "0x000000000000000000000000000000000000dEaD", value: "0x1" };
  expect(rpcFromApp(runID!, "anvil_mine", ["0xa"])).toContain('"result"');
  sendFromApp(runID!, transfer);
  expect(rpcFromApp(runID!, "anvil_mine", ["0xa"])).toContain('"result"');
  sendFromApp(runID!, transfer);

  await openReady(page, evmURL!);
  await openEVMTab(page);
  const rows = page.locator("#evm-txs .evm-tx");
  const status = page.locator("#evm-status");
  const blocks = page.locator("#evm-blocks");

  // Default 20.
  await expect(blocks).toHaveValue("20");
  await expect(status).toHaveText("Chain 31337, block 25. 2 transactions in the last 20 blocks.");
  await expect(rows).toHaveCount(2);
  await expect(rows.locator(".evm-meta").nth(0)).toContainText("#25 ");
  await expect(rows.locator(".evm-meta").nth(1)).toContainText("#14 ");
  await page.screenshot({ path: test.info().outputPath("blocks-20.png") });
  await pause(page, 2500);

  for (const [value, count, first, last] of [["10", 1, "#25 ", "#25 "], ["64", 5, "#25 ", "#1 "], ["20", 2, "#25 ", "#14 "]] as const) {
    const req = page.waitForRequest((r) => r.url().includes("/api/evm/txs?") && r.url().includes("blocks=" + value));
    await blocks.selectOption(value);
    await req;
    const shown = value === "64" ? 26 : Number(value); // the chain has only 26 blocks, genesis included
    await expect(status).toHaveText(`Chain 31337, block 25. ${count} transaction${count === 1 ? "" : "s"} in the last ${shown} blocks.`);
    await expect(rows).toHaveCount(count);
    await expect(rows.locator(".evm-meta").first()).toContainText(first);
    await expect(rows.locator(".evm-meta").last()).toContainText(last);
    await page.screenshot({ path: test.info().outputPath(`blocks-${value}.png`) });
    await pause(page, 2500);
  }

  // The 5s refresh keeps the chosen window.
  await blocks.selectOption("10");
  const tick = await page.waitForRequest((r) => r.url().includes("/api/evm/txs?"), { timeout: 10 * 1000 });
  expect(tick.url()).toContain("blocks=10");
  await expect(rows).toHaveCount(1);
  await pause(page);
});
