import { defineConfig, devices } from "@playwright/test";

// Each spec reads the console URL from E2E_CONSOLE_URL, printed by
// `go run -tags e2e ./cmd/aiman-harness-e2e`. E2E_OUT puts the videos,
// screenshots and traces somewhere other than ./test-results.
export default defineConfig({
  testDir: "./tests",
  outputDir: process.env.E2E_OUT || "test-results",
  // Harness runs build images and start containers: minutes, not seconds.
  timeout: 10 * 60 * 1000,
  expect: { timeout: 30 * 1000 },
  fullyParallel: false,
  workers: 1,
  reporter: [["list"]],
  use: {
    ...devices["Desktop Chrome"],
    viewport: { width: 1440, height: 900 },
    video: { mode: "on", size: { width: 1440, height: 900 } },
    screenshot: "on",
    trace: "retain-on-failure",
    launchOptions: { slowMo: Number(process.env.E2E_SLOWMO || 250) },
  },
});
