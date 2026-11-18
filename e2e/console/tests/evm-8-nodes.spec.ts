import { expect, test } from "@playwright/test";
import { consoleAPI, evm2URL, openEVMTab, openReady, pause } from "./evm-helpers";

test("node picker: two anvil nodes, each with its own transactions and traces", async ({ page, request }) => {
  test.skip(!evm2URL, "set E2E_EVM2_CONSOLE_URL to a console of the evm2 fixture");
  await openReady(page, evm2URL!);
  const st = await consoleAPI(request, evm2URL!, "/api/state");
  expect(st.run.evm).toEqual(["alpha", "beta"]);
  // Neither relay is an app link.
  for (const n of ["alpha", "beta"]) await expect(page.locator("#apps a.app-link", { hasText: n })).toHaveCount(0);
  await openEVMTab(page);

  const picker = page.locator("#evm-service");
  const rows = page.locator("#evm-txs .evm-tx");
  const status = page.locator("#evm-status");
  const trace = page.locator("#evm-trace");
  await expect(page.locator("#evm-service-row")).toBeVisible();
  await expect(picker.locator("option")).toHaveText(["alpha", "beta"]);
  await expect(picker).toHaveValue("alpha");

  // alpha: chain 1001, the reverting sequence.
  await expect(status).toContainText("Chain 1001, block 3. 3 transactions");
  await expect(rows).toHaveCount(3);
  await rows.filter({ has: page.locator('.evm-badge[data-status="reverted"]') }).click();
  await expect(trace.locator(".evm-revert")).toHaveText('Reverted: Error("nope")');
  await page.screenshot({ path: test.info().outputPath("alpha.png") });
  await pause(page, 2500);

  // beta: chain 1002, two transfers. Switching clears alpha's trace.
  let req = page.waitForRequest((r) => r.url().includes("/api/evm/txs?") && r.url().includes("service=beta"));
  await picker.selectOption("beta");
  await req;
  await expect(trace).toBeEmpty();
  await expect(status).toContainText("Chain 1002, block 2. 2 transactions");
  await expect(rows).toHaveCount(2);
  await expect(rows.locator(".evm-badge")).toHaveText(["success", "success"]);
  await expect(rows.locator(".evm-meta").first()).toContainText(/0x000000…beef/i);
  const traceReq = page.waitForRequest((r) => r.url().includes("/api/evm/trace?") && r.url().includes("service=beta"));
  await rows.first().click();
  await traceReq;
  await expect(trace.locator(".evm-trace-head .evm-badge")).toHaveText("success");
  await expect(trace.locator(".evm-trace-head .evm-meta")).toContainText("block 2 · gas 21000 · 1 frame");
  await expect(trace.locator(".evm-revert")).toHaveCount(0);
  await expect(trace.locator(":scope > details.evm-frame > summary")).toContainText("2 wei");
  await page.screenshot({ path: test.info().outputPath("beta.png") });
  await pause(page, 2500);

  // The 5s refresh stays on beta.
  const tick = await page.waitForRequest((r) => r.url().includes("/api/evm/txs?"), { timeout: 10 * 1000 });
  expect(tick.url()).toContain("service=beta");

  // Back to alpha.
  req = page.waitForRequest((r) => r.url().includes("/api/evm/txs?") && r.url().includes("service=alpha"));
  await picker.selectOption("alpha");
  await req;
  await expect(status).toContainText("Chain 1001");
  await expect(rows).toHaveCount(3);
  await page.screenshot({ path: test.info().outputPath("alpha-again.png") });
  await pause(page, 2500);
});
