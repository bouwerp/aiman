import { expect, test } from "@playwright/test";
import { consoleAPI, evmURL, openReady, pause } from "./evm-helpers";

test("app links leave out the EVM relay port", async ({ page, request }) => {
  test.skip(!evmURL, "set E2E_CONSOLE_URL for the evm fixture");
  await openReady(page, evmURL!);

  // The relay is forwarded (the EVM tab reads through it) ...
  const st = await consoleAPI(request, evmURL!, "/api/state");
  const relay = (st.forwards || []).find((f: { name: string }) => f.name === "chain");
  expect(relay, "the chain relay is a forward").toBeTruthy();

  // ... but never offered as an app link.
  const apps = page.locator("#apps");
  await expect(apps.locator("a.app-link", { hasText: "chain" })).toHaveCount(0);
  await expect(apps.locator(`a[href="${relay.url}"]`)).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(`host port ${relay.host_port}`);
  // The evm fixture's app publishes no ports, so the list is empty and hidden.
  await expect(apps).toBeHidden();
  await page.screenshot({ path: test.info().outputPath("no-relay-link.png") });
  await pause(page, 2500);
});
