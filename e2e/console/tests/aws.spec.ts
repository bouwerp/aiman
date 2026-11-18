import { readFileSync } from "node:fs";
import { type APIRequestContext, type Page, expect, test } from "@playwright/test";

// Run against the driver's aws fixture: --fixture aws --up. The run's app
// answers /upload (PutObject and PutItem), /wrong-region, /foreign-key and
// /real-aws on its web port.
const url = process.env.E2E_CONSOLE_URL;

const pause = (page: Page, ms = 1200) => page.waitForTimeout(ms);
const shot = (page: Page, name: string) => page.screenshot({ path: test.info().outputPath(name + ".png"), fullPage: true });

async function state(request: APIRequestContext) {
  const u = new URL(url!);
  const token = new URLSearchParams(u.hash.slice(1)).get("token") || "";
  const res = await request.get(u.origin + "/api/state", { headers: { "X-Aiman-Console-Token": token } });
  expect(res.status()).toBe(200);
  return res.json();
}

// app calls a path on the run's web port, as a tester's browser would.
async function app(request: APIRequestContext, path: string) {
  const s = await state(request);
  const web = (s.forwards || []).find((f: { name: string }) => f.name === "web");
  expect(web, "web forward").toBeTruthy();
  const res = await request.get(web.url + path);
  expect(res.status()).toBe(200);
  return res.text();
}

async function openAWS(page: Page) {
  await page.goto(url!);
  await expect(page.locator("#state")).toHaveText("ready", { timeout: 5 * 60 * 1000 });
  await pause(page);
  await expect(page.locator("#tab-aws")).toBeVisible();
  await page.locator("#tab-aws").click();
  await expect(page.locator("#aws")).toBeVisible();
}

test.beforeEach(() => {
  test.skip(!url, "set E2E_CONSOLE_URL to the driver's E2E_CONSOLE_URL line");
});

test("resources: manifest, env and the plan's warning", async ({ page }) => {
  await openAWS(page);
  // The read relay is the AWS tab's, not an app link.
  await expect(page.locator("#apps")).not.toContainText("aws");
  const warn = page.locator("#aws-warnings .aws-warn[data-kind=warning]");
  await expect(warn).toHaveCount(1);
  await expect(warn).toContainText("OrdersFunction");
  await expect(warn).toContainText("NotHere");
  const rows = page.locator("#aws-res tbody tr");
  await expect(rows).toHaveCount(3);
  await expect(rows.filter({ hasText: "OrdersTable" })).toContainText("ORDERS_TABLE");
  await expect(rows.filter({ hasText: "OrdersTable" })).toContainText(/orders-OrdersTable-[a-z0-9]{12}/);
  await expect(rows.filter({ hasText: "UploadBucket" })).toContainText("UPLOAD_BUCKET");
  await expect(page.locator("#aws-res")).toContainText("eu-west-1");
  await expect(page.locator("#aws-env")).toContainText("ORDERS_TABLE");
  await pause(page, 2000);
  await shot(page, "01-resources");
});

test("s3: browse by prefix, preview, versions and download", async ({ page }) => {
  await openAWS(page);
  // A resource row opens its bucket.
  await page.locator("#aws-res tbody tr", { hasText: "UploadBucket" }).locator("button").click();
  await expect(page.locator("#aws-s3")).toBeVisible();
  await expect(page.locator("#aws-buckets button[aria-pressed=true]")).toContainText("orders-uploadbucket-");
  await page.locator(".aws-obj[data-kind=prefix]", { hasText: "seed/" }).click();
  await expect(page.locator(".aws-obj[data-kind=object]", { hasText: "readme.txt" })).toBeVisible();
  await expect(page.locator(".aws-obj[data-kind=prefix]", { hasText: "notes/" })).toBeVisible();
  await expect(page.locator("#aws-crumbs")).toContainText("seed");
  await pause(page);
  await page.locator(".aws-obj[data-kind=object]", { hasText: "readme.txt" }).click();
  await expect(page.locator("#aws-preview")).toContainText("hello from the seed");
  await expect(page.locator("#aws-object")).toContainText("text/plain");
  await pause(page, 1500);
  await shot(page, "02-object");

  const [dl] = await Promise.all([page.waitForEvent("download"), page.locator("#aws-download").click()]);
  expect(dl.suggestedFilename()).toBe("readme.txt");
  expect(readFileSync(await dl.path(), "utf8")).toBe("hello from the seed\nsecond line\n");
  await expect(page.locator("#aws-status")).toContainText("Saved readme.txt");

  // The breadcrumb goes back to the bucket root, then into notes.
  await page.locator("#aws-crumbs button").first().click();
  await page.locator(".aws-obj[data-kind=prefix]", { hasText: "seed/" }).click();
  await page.locator(".aws-obj[data-kind=prefix]", { hasText: "notes/" }).click();
  await page.locator(".aws-obj[data-kind=object]", { hasText: "orders.csv" }).click();
  await expect(page.locator("#aws-preview")).toContainText("o2,new");
  await pause(page, 1500);
});

test("calls: the app's calls live, filters, detail and hints", async ({ page, request }) => {
  await openAWS(page);
  await page.locator("#aws-v-calls").click();
  const list = page.locator("#aws-call-list .aws-call");
  // The applier's calls come first, from client aiman.
  await expect(list.filter({ hasText: "CreateTable" }).locator(".aws-call-client")).toHaveText("aiman");
  await pause(page);

  expect(await app(request, "/upload")).toContain("PutObject 200, PutItem 200");
  // Newest first: PutItem, then PutObject, both from the app, both 200.
  await expect(list.nth(0)).toContainText("dynamodb PutItem", { timeout: 10 * 1000 });
  // The app may take the applier's address; it is still named app.
  await expect(list.nth(0).locator(".aws-call-client")).toHaveText("app");
  await expect(list.nth(0).locator(".aws-badge")).toHaveText("200");
  await expect(list.nth(1)).toContainText("s3 PutObject");
  await expect(list.nth(1)).toContainText(/orders-uploadbucket-[a-z0-9]+\/uploads\/order-u\d+\.txt/);
  await pause(page, 1500);

  await list.nth(0).click();
  const detail = page.locator(".aws-call-detail").first();
  await expect(detail).toContainText("Params");
  await expect(detail).toContainText("TableName");
  // Item values are kept only as types.
  await expect(detail).not.toContainText("uploads/order-u");
  await pause(page, 1500);
  await shot(page, "03-calls");
  await list.nth(0).click();

  await page.locator("#aws-f-service").selectOption("s3");
  await expect(list.filter({ hasText: "dynamodb" })).toHaveCount(0);
  await page.locator("#aws-f-service").selectOption("");
  await pause(page);

  expect(await app(request, "/wrong-region")).toContain("301");
  expect(await app(request, "/foreign-key")).toContain("403");
  expect(await app(request, "/real-aws")).toContain("refused");
  const hints = page.locator("#aws-hints .aws-hint");
  await expect(hints.and(page.locator("[data-hint=region]"))).toContainText("us-east-1", { timeout: 10 * 1000 });
  await expect(hints.and(page.locator("[data-hint=region]"))).toContainText("eu-west-1");
  await expect(hints.and(page.locator("[data-hint=region]"))).toContainText("ListObjectsV2 from app");
  await expect(hints.and(page.locator("[data-hint=foreign_key]"))).toContainText("access key other than test");
  await expect(hints.and(page.locator("[data-hint=real_aws]"))).toContainText("s3.amazonaws.com");
  await page.locator("#aws-f-errors").check();
  await expect(list.filter({ hasText: "PermanentRedirect" })).toHaveCount(1);
  await expect(list.filter({ hasText: "InvalidAccessKeyId" })).toHaveCount(1);
  await expect(list.filter({ hasText: "PutItem" })).toHaveCount(0);
  await pause(page, 2000);
  await shot(page, "04-hints");
  await page.locator("#aws-f-errors").uncheck();
});

test("dynamodb: describe, scan, index query, item JSON and stream", async ({ page, request }) => {
  const up = await app(request, "/upload");
  const pk = /order-(u\d+)\.txt/.exec(up)![1];
  await openAWS(page);
  await page.locator("#aws-v-ddb").click();
  await page.locator("#aws-tables button", { hasText: "orders-OrdersTable-" }).click();
  await expect(page.locator("#aws-describe")).toContainText("pk (HASH)");
  await expect(page.locator("#aws-describe")).toContainText("GSI by-status");
  await expect(page.locator("#aws-describe")).toContainText("NEW_AND_OLD_IMAGES");
  // Opening a table scans it.
  await expect(page.locator("#aws-items tbody tr").filter({ hasText: "received" })).toHaveCount(1);
  await expect(page.locator("#aws-items th.redacted")).toHaveText("api_key");
  await expect(page.locator("#aws-items")).not.toContainText("k-123");
  await pause(page, 1500);

  await page.locator("#aws-q-index").selectOption("by-status");
  await expect(page.locator("#aws-q-pk-label")).toHaveText("Partition key status");
  await page.locator("#aws-q-pk").fill("new");
  await pause(page, 600);
  await page.locator("#aws-q-run").click();
  await expect(page.locator("#aws-status")).toContainText("Query:");
  const row = page.locator("#aws-items tbody tr", { hasText: pk });
  await expect(row).toHaveCount(1);
  await expect(page.locator("#aws-items tbody tr", { hasText: "received" })).toHaveCount(0);
  await row.click();
  await expect(page.locator("#aws-item-json")).toContainText(`"pk": "${pk}"`);
  await expect(page.locator("#aws-item-json")).toContainText(`"status": "new"`);
  await pause(page, 1500);
  await shot(page, "05-query");

  await page.locator("#aws-stream-load").click();
  const recs = page.locator("#aws-stream .aws-rec");
  await expect(recs.first()).toContainText("INSERT");
  await expect(recs.first()).toContainText(pk);
  await recs.first().locator("summary").click();
  await expect(recs.first()).toContainText("New image");
  await pause(page, 2000);
  await shot(page, "06-stream");
});

test("writes: confirm first, upload, delete, put item, send, reset", async ({ page }) => {
  // Each write asks; the confirm text is checked and accepted, except
  // where the test dismisses it.
  const asked: string[] = [];
  let answer = true;
  page.on("dialog", (d) => { asked.push(d.message()); void (answer ? d.accept() : d.dismiss()); });
  await openAWS(page);
  const status = page.locator("#aws-w-status");

  // A message to the plan's queue, from the Resources view.
  await expect(page.locator("#aws-w-sqs")).toBeVisible();
  await expect(page.locator("#aws-w-queue")).toHaveValue(/orders-Events-/);
  await page.locator("#aws-w-body").fill("hello from the console");
  await page.locator("#aws-w-send").click();
  await expect(status).toContainText(/Sent message .* to orders-Events-/);
  expect(asked.pop()).toContain("hello from the console");
  await pause(page);

  // Upload into the bucket, then delete it; a dismissed confirm writes nothing.
  await page.locator("#aws-v-s3").click();
  await page.locator("#aws-buckets button", { hasText: "orders-uploadbucket-" }).click();
  await expect(page.locator("#aws-w-s3")).toBeVisible();
  await page.locator("#aws-w-file").setInputFiles({ name: "console.txt", mimeType: "text/plain", buffer: Buffer.from("written from the console\n") });
  await page.locator("#aws-w-key").fill("uploads/console.txt");
  await pause(page, 600);
  await page.locator("#aws-w-upload").click();
  await expect(status).toContainText("Uploaded orders-uploadbucket-");
  expect(asked.pop()).toContain("uploads/console.txt");
  await expect(page.locator("#aws-preview")).toContainText("written from the console");
  await pause(page, 1500);
  await shot(page, "07-uploaded");

  answer = false;
  await page.locator("#aws-w-delobj").click();
  await expect.poll(() => asked.length).toBe(1);
  expect(asked.pop()).toContain("delete marker");
  await expect(page.locator("#aws-preview")).toContainText("written from the console");
  answer = true;
  await page.locator("#aws-w-delobj").click();
  await expect(status).toContainText("Deleted orders-uploadbucket-");
  await expect(page.locator("#aws-object")).toBeEmpty();
  await page.locator(".aws-obj[data-kind=prefix]", { hasText: "uploads/" }).click();
  await expect(page.locator(".aws-obj[data-kind=object]", { hasText: "console.txt" })).toHaveCount(0);
  await pause(page);

  // Edit a seeded item, then delete it.
  await page.locator("#aws-v-ddb").click();
  await page.locator("#aws-tables button", { hasText: "orders-OrdersTable-" }).click();
  await page.locator("#aws-items tbody tr", { hasText: "received" }).click();
  await expect(page.locator("#aws-w-item")).toHaveValue(/"pk": "o1"/);
  await page.locator("#aws-w-item").fill('{"pk": "o1", "status": "edited", "total": 13}');
  await pause(page, 600);
  await page.locator("#aws-w-put").click();
  await expect(status).toContainText("Put an item into orders-OrdersTable-");
  await expect(page.locator("#aws-items tbody tr", { hasText: "edited" })).toHaveCount(1);
  await page.locator("#aws-items tbody tr", { hasText: "edited" }).click();
  await page.locator("#aws-w-delitem").click();
  await expect(status).toContainText('Deleted {"pk":"o1"}');
  expect(asked.pop()).toContain('{"pk":"o1"}');
  await expect(page.locator("#aws-items tbody tr", { hasText: "edited" })).toHaveCount(0);
  await pause(page, 1500);
  await shot(page, "08-item-deleted");

  // Reset wipes the emulator and applies the plan and seed again.
  await page.locator("#aws-w-reset").click();
  await expect(status).toContainText(/Reset aws: \d+ resources, \d+ calls applied again\./, { timeout: 2 * 60 * 1000 });
  expect(asked.pop()).toContain("wipes every bucket");
  await expect(status).not.toContainText("now have other values");
  await page.locator("#aws-tables button", { hasText: "orders-OrdersTable-" }).click();
  await expect(page.locator("#aws-items tbody tr", { hasText: "received" })).toHaveCount(1);
  await expect(page.locator("#aws-items tbody tr", { hasText: "uploads/" })).toHaveCount(0);
  await pause(page, 2000);
  await shot(page, "09-reset");
});
