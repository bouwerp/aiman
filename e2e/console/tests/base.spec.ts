import { expect, Page, test } from "@playwright/test";

// Run against `aiman-harness-e2e --fixture go --up`: a Go app with a web
// port, three steps and two checks (binary passes, deliberate-failure exits 3).
const url = process.env.E2E_CONSOLE_URL;
const pause = (page: Page, ms = 1200) => page.waitForTimeout(ms);

test("base console: header, steps, verdicts, checks, logs, down and up", async ({ page }) => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
  await page.goto(url!);

  // Run header.
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await expect(page.locator("#scenario")).toHaveText("Go app answers on its web port");
  await expect(page.locator("#meta")).toContainText("scenario smoke");
  await expect(page.locator("#meta")).toContainText("clean tree");
  const runId = (await page.locator("#meta code").nth(1).textContent())!;
  expect(runId).toMatch(/^[0-9a-f]{12}$/);
  await expect(page.locator("#apps a.app-link")).toContainText("web");
  await expect(page.locator("#facts")).toContainText(runId);
  await page.screenshot({ path: test.info().outputPath("01-header.png") });
  await pause(page);

  // Step list.
  const steps = page.locator("#steps > li.step");
  await expect(steps).toHaveCount(3);
  await expect(steps.nth(0)).toContainText("Open the web link");
  await expect(steps.nth(1)).toContainText("Reload the page");

  // Pass with a note on step 1.
  await steps.nth(0).getByRole("textbox").pressSequentially("page says hello", { delay: 40 });
  await steps.nth(0).locator("button.v-pass").click();
  await expect(page.locator("#notice")).toContainText("Recorded pass for step 1");
  await expect(steps.nth(0).locator(".step-recorded")).toContainText("pass");
  await expect(steps.nth(0).locator(".step-recorded")).toContainText("page says hello");
  await pause(page);

  // Fail with a note on step 2: serve adds a to-do on the session.
  await steps.nth(1).getByRole("textbox").pressSequentially("hit count stayed at 1", { delay: 40 });
  await steps.nth(1).locator("button.v-fail").click();
  await expect(page.locator("#notice")).toContainText("Recorded fail for step 2");
  await expect(page.locator("#notice")).toContainText("added to-do");
  await expect(steps.nth(1).locator(".step-recorded .badge")).toHaveText("fail");
  await expect(steps.nth(1).locator(".step-recorded")).toContainText("hit count stayed at 1");
  await page.screenshot({ path: test.info().outputPath("02-step-verdicts.png") });
  await pause(page);

  // Whole scenario: fail with a note, then pass.
  const scen = page.locator("#scenario-row");
  await scen.getByRole("textbox").pressSequentially("step 2 broken", { delay: 40 });
  await scen.locator("button.v-fail").click();
  await expect(page.locator("#notice")).toContainText("Recorded fail for the scenario");
  await expect(page.locator("#scenario-row button.v-fail")).toHaveAttribute("aria-pressed", "true");
  await pause(page);
  await page.locator("#scenario-row").getByRole("textbox").fill("rechecked, fine");
  await page.locator("#scenario-row button.v-pass").click();
  await expect(page.locator("#notice")).toContainText("Recorded pass for the scenario");
  await expect(page.locator("#scenario-row button.v-pass")).toHaveAttribute("aria-pressed", "true");
  await page.screenshot({ path: test.info().outputPath("03-scenario-verdict.png") });
  await pause(page);

  // Run one check, then all.
  await page.locator("#tab-checks").click();
  const checks = page.locator("#checks > li.check-item");
  await expect(checks).toHaveCount(2);
  const binary = checks.filter({ hasText: "binary" }).first();
  await binary.getByRole("button", { name: "Run", exact: true }).click();
  await expect(binary.locator(".badge")).toHaveText("exit 0", { timeout: 5 * 60 * 1000 });
  await expect(binary).toContainText("binary ok");
  await pause(page);
  await page.locator("#check-all").click();
  const failing = page.locator("#checks > li.check-item").filter({ hasText: "deliberate-failure" }).first();
  await expect(failing.locator(".badge")).toHaveText("exit 3", { timeout: 5 * 60 * 1000 });
  await expect(failing).toContainText("this check fails on purpose");
  await page.screenshot({ path: test.info().outputPath("04-checks.png") });
  await pause(page);

  // Logs per container, with pause.
  await page.locator("#tab-logs").click();
  await expect(page.locator("#log-service option")).toHaveText(["app", "edge"]);
  await expect(page.locator("#log-status")).toContainText(/^\d+ lines/, { timeout: 60_000 });
  await pause(page, 2500);
  await page.locator("#log-service").selectOption("edge");
  await expect(page.locator("#log-status")).toContainText(/^\d+ lines/, { timeout: 60_000 });
  await page.screenshot({ path: test.info().outputPath("05-logs-edge.png") });
  await pause(page, 2000);
  await page.locator("#log-service").selectOption("app");
  await expect(page.locator("#log-status")).toContainText(/^\d+ lines/);
  await page.locator("#log-pause").click();
  await expect(page.locator("#log-pause")).toHaveText("Resume");
  await page.waitForTimeout(500);
  const frozen = await page.locator("#log-status").textContent();
  await page.waitForTimeout(5000);
  expect(await page.locator("#log-status").textContent()).toBe(frozen);
  await page.locator("#log-pause").click();
  await expect(page.locator("#log-pause")).toHaveText("Pause");
  await expect(page.locator("#log-status")).not.toHaveText(frozen!, { timeout: 10_000 });
  await pause(page);

  // Down from the page.
  page.once("dialog", (d) => d.accept());
  await page.locator("#down").click();
  await expect(page.locator("#notice")).toContainText(`Run ${runId} stopped`, { timeout: 3 * 60 * 1000 });
  await expect(page.locator("#state")).toHaveText("stopped");
  await expect(page.locator("#down")).toBeHidden();
  await page.locator("#tab-run").click();
  await expect(page.locator("#facts")).toContainText("stopped (down)");
  await page.screenshot({ path: test.info().outputPath("06-down.png") });
  await pause(page, 2000);

  // Up from the page: a new run of the same scenario reaches ready.
  await page.locator("#up-scenario").selectOption("smoke");
  await page.locator("#up-go").click();
  await expect(page.locator("#notice")).toContainText("is starting");
  await expect(page.locator("#meta code").nth(1)).not.toHaveText(runId, { timeout: 60_000 });
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await expect(page.locator("#steps .step-recorded")).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath("07-up-again.png") });
  await pause(page, 2000);
});
