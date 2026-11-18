import { expect, test } from "@playwright/test";

// The console URL carries its token in the fragment.
const url = process.env.E2E_CONSOLE_URL;

test("run reaches ready, a step takes a verdict, logs stream", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  await page.goto(url!);

  // The driver's --up starts the run; building it takes up to a few minutes.
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await expect(page.locator("#meta")).toContainText("run ");

  const steps = page.locator("#steps > li.step");
  await expect(steps.first()).toBeVisible();
  expect(await steps.count()).toBeGreaterThan(0);

  const first = steps.first();
  await first.getByRole("textbox").fill("seen in the e2e smoke test");
  await first.locator("button.v-pass").click();
  await expect(page.locator("#notice")).toContainText("Recorded pass for step 1");
  await expect(steps.first().locator(".step-recorded .badge")).toHaveText("pass");
  await expect(steps.first().locator("button.v-pass")).toHaveAttribute("aria-pressed", "true");

  await page.locator("#tab-logs").click();
  await expect(page.locator("#panel-logs")).toBeVisible();
  await expect(page.locator("#log-service")).not.toHaveValue("");
  // A poll answered: "<n> lines, updated <time>". Apps that print nothing
  // (python, db, evm fixtures) show 0 lines.
  await expect(page.locator("#log-status")).toContainText(/^\d+ lines/, { timeout: 60 * 1000 });
  if (process.env.E2E_EXPECT_LOG) {
    await expect(page.locator("#log")).toContainText(process.env.E2E_EXPECT_LOG, { timeout: 60 * 1000 });
  }
  await page.waitForTimeout(1500);
});
