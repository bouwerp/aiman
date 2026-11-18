import { expect, test } from "@playwright/test";
import { evmURL, openEVMTab, openReady, pause } from "./evm-helpers";

// Error("nope") ABI-encoded, as the reverter returns it.
const NOPE = "0x08c379a0" + "20".padStart(64, "0") + "4".padStart(64, "0") + "6e6f7065".padEnd(64, "0");

test("reverted transaction: call tree with the failing path open and the revert reason", async ({ page }) => {
  test.skip(!evmURL, "set E2E_CONSOLE_URL for the evm fixture");
  await openReady(page, evmURL!);
  await openEVMTab(page);

  const reverted = page.locator("#evm-txs .evm-tx").filter({ has: page.locator('.evm-badge[data-status="reverted"]') }).first();
  await reverted.click();
  await expect(reverted).toHaveAttribute("aria-pressed", "true");

  const trace = page.locator("#evm-trace");
  await expect(trace.locator(".evm-trace-head .evm-badge")).toHaveText("reverted");
  await expect(trace.locator(".evm-trace-head .evm-meta")).toContainText("2 frames");
  await expect(trace.locator(".evm-revert")).toHaveText('Reverted: Error("nope")');
  await pause(page, 2500);

  // Root: the caller, called with 0x12345678; child: the reverter. Both
  // failed, so both are open.
  const root = trace.locator(":scope > details.evm-frame");
  const child = root.locator(".evm-calls details.evm-frame");
  await expect(root).toHaveAttribute("open", "");
  await expect(child).toHaveAttribute("open", "");
  await expect(root.locator(":scope > summary")).toHaveAttribute("data-failed", "true");
  await expect(child.locator(":scope > summary")).toHaveAttribute("data-failed", "true");
  await expect(child.locator(":scope > summary .evm-err")).toContainText('Error("nope")');

  const rootIO = root.locator(":scope > dl.evm-io");
  await expect(rootIO.locator("dt", { hasText: "input" })).toBeVisible();
  await expect(rootIO.locator("dd code").filter({ hasText: /^0x12345678$/ })).toBeVisible();
  await expect(rootIO.locator("dd code").filter({ hasText: NOPE })).toBeVisible();
  await expect(child.locator(":scope > dl.evm-io dd code").filter({ hasText: NOPE })).toBeVisible();
  await page.screenshot({ path: test.info().outputPath("trace-open.png"), fullPage: true });
  await pause(page, 2500);

  // Collapse the child, then the root, then expand both again.
  await child.locator(":scope > summary").click();
  await expect(child).not.toHaveAttribute("open", "");
  await expect(child.locator(":scope > dl.evm-io")).toBeHidden();
  await pause(page);
  await root.locator(":scope > summary").click();
  await expect(root).not.toHaveAttribute("open", "");
  await expect(child).toBeHidden();
  await page.screenshot({ path: test.info().outputPath("trace-collapsed.png") });
  await pause(page);
  await root.locator(":scope > summary").click();
  await child.locator(":scope > summary").click();
  await expect(root).toHaveAttribute("open", "");
  await expect(child).toHaveAttribute("open", "");
  await expect(child.locator(":scope > dl.evm-io")).toBeVisible();
  await pause(page, 2500);
});
