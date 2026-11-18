import { expect, test } from "@playwright/test";
import { consoleAPI, evmURL, openEVMTab, openReady, pause } from "./evm-helpers";

test("hash search: the reverted tx traces, a malformed hash is refused, an unknown one errors", async ({ page, request }) => {
  test.skip(!evmURL, "set E2E_CONSOLE_URL for the evm fixture");
  await openReady(page, evmURL!);
  const st = await consoleAPI(request, evmURL!, "/api/state");
  const list = await consoleAPI(request, evmURL!, `/api/evm/txs?run=${st.run.id}&service=chain`);
  const reverted = list.evm.txs.find((t: { status: string }) => t.status === "reverted").hash as string;
  await openEVMTab(page);

  const input = page.locator("#evm-hash");
  const trace = page.locator("#evm-trace");
  const traceBtn = page.locator("#evm-find button[type=submit]");

  // The reverted transaction, typed in full.
  const req = page.waitForRequest((r) => r.url().includes("/api/evm/trace?") && r.url().includes("tx=" + reverted));
  await input.pressSequentially(reverted, { delay: 15 });
  await traceBtn.click();
  await req;
  await expect(trace.locator(".evm-trace-head code.evm-hash")).toHaveText(reverted);
  await expect(trace.locator(".evm-revert")).toHaveText('Reverted: Error("nope")');
  // The matching row is marked as the one shown.
  await expect(page.locator(`.evm-tx[title="${reverted}"]`)).toHaveAttribute("aria-pressed", "true");
  await page.screenshot({ path: test.info().outputPath("search-reverted.png") });
  await pause(page, 2500);

  // A malformed hash never leaves the page.
  let traced = false;
  page.on("request", (r) => { if (r.url().includes("/api/evm/trace?")) traced = true; });
  await input.fill("0x1234");
  await traceBtn.click();
  expect(await input.evaluate((e: HTMLInputElement) => e.validationMessage)).toBe("A transaction hash: 0x and 64 hex digits");
  await expect(trace.locator(".evm-revert")).toHaveText('Reverted: Error("nope")'); // the last trace stays
  await page.screenshot({ path: test.info().outputPath("search-malformed.png") });
  await pause(page, 2500);
  expect(traced).toBe(false);

  // A well-formed hash the chain never mined: serve's refusal as a notice.
  const unknown = "0x" + "1".repeat(64);
  await input.fill(unknown);
  await traceBtn.click();
  await expect(page.locator("#notice")).toBeVisible();
  await expect(page.locator("#notice")).toContainText("chain has no mined transaction " + unknown);
  await expect(trace).toBeEmpty();
  await expect(page.locator('.evm-tx[aria-pressed="true"]')).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath("search-unknown.png") });
  await pause(page, 2500);
});
