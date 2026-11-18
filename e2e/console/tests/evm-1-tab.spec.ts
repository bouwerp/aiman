import { expect, test } from "@playwright/test";
import { consoleAPI, evmURL, openEVMTab, openReady, otherURL, pause } from "./evm-helpers";

test("EVM tab shows only for a ready run with EVM services", async ({ page, request }) => {
  test.skip(!evmURL || !otherURL, "set E2E_CONSOLE_URL (evm fixture) and E2E_OTHER_CONSOLE_URL (a fixture without EVM)");

  // A run without EVM services: the tab stays hidden.
  await openReady(page, otherURL!);
  const other = await consoleAPI(request, otherURL!, "/api/state");
  expect(other.run.evm || []).toHaveLength(0);
  await expect(page.locator("#tab-evm")).toBeHidden();
  await expect(page.locator("#tab-db")).toBeVisible();
  await page.screenshot({ path: test.info().outputPath("no-evm-run.png") });
  await pause(page, 2500);

  // The evm fixture's ready run: the tab shows and opens on its node.
  await openReady(page, evmURL!);
  const st = await consoleAPI(request, evmURL!, "/api/state");
  expect(st.run.evm).toEqual(["chain"]);
  await openEVMTab(page);
  await expect(page.locator("#evm-empty")).toBeHidden();
  await expect(page.locator("#evm-service-row")).toBeHidden(); // one node: no picker
  await page.screenshot({ path: test.info().outputPath("evm-run.png") });
  await pause(page, 2500);
});
