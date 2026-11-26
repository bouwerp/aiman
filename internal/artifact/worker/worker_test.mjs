// Acceptance tests for docs/specs/artifacts.md piece B (Worker). Do not edit.
import test from "node:test";
import assert from "node:assert/strict";
import worker, { route, allowed, verify } from "./worker.js";

const ID = "abcdefghijklmnopqrst";
const env0 = { TEAM_DOMAIN: "t.cloudflareaccess.com", POLICY_AUD: "aud1", ARTIFACT_HOST: "aiman.fyi", OWNERS: "me@x.com" };

const { publicKey, privateKey } = await crypto.subtle.generateKey(
  { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" }, true, ["sign", "verify"]);
const jwk = { ...(await crypto.subtle.exportKey("jwk", publicKey)), kid: "k1" };
globalThis.fetch = async () => new Response(JSON.stringify({ keys: [jwk] }));
const b64 = (o) => Buffer.from(typeof o === "string" ? o : JSON.stringify(o)).toString("base64url");
async function token(claims, kid = "k1") {
  const h = b64({ alg: "RS256", kid }), c = b64(claims);
  const sig = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", privateKey, new TextEncoder().encode(h + "." + c));
  return h + "." + c + "." + Buffer.from(sig).toString("base64url");
}
const now = Math.floor(Date.now() / 1000);
const good = { aud: ["aud1"], iss: "https://t.cloudflareaccess.com", exp: now + 600, email: "Bob@Y.com" };

test("route", () => {
  assert.deepEqual(route(`/a/${ID}/`), { id: ID, version: null, file: "index.html" });
  assert.deepEqual(route(`/a/${ID}/v/3/img/a.png`), { id: ID, version: 3, file: "img/a.png" });
  assert.equal(route(`/a/${ID}`).redirect, true);
  assert.equal(route(`/a/${ID}/v/2`).redirect, true);
  assert.equal(route(`/a/${ID}/../x`), null);
  assert.equal(route(`/a/${ID}/a/%2e%2e/meta.json`), null);
  assert.equal(route(`/a/BAD/`), null);
  assert.equal(route(`/x`), null);
});

test("allowed", () => {
  assert.ok(allowed("me@x.com", "me@x.com", {}));
  assert.ok(allowed("bob@y.com", "me@x.com", { emails: ["Bob@y.com"] }));
  assert.ok(allowed("z@p.com", "me@x.com", { domains: ["p.com"] }));
  assert.ok(!allowed("z@evilp.com", "me@x.com", { domains: ["p.com"] }));
  assert.ok(!allowed(null, "me@x.com", {}));
});

test("verify", async () => {
  assert.equal(await verify(await token(good), env0), "bob@y.com");
  assert.equal(await verify(await token({ ...good, aud: ["other"] }), env0), null);
  assert.equal(await verify(await token({ ...good, iss: "https://evil" }), env0), null);
  assert.equal(await verify(await token({ ...good, exp: now - 1 }), env0), null);
  assert.equal(await verify(await token(good, "nokid"), env0), null);
  const t = await token(good);
  assert.equal(await verify(t.slice(0, -4) + "AAAA", env0), null);
  assert.equal(await verify("", env0), null);
});

function bucket(objs) {
  return { async get(k) { if (!(k in objs)) return null; const v = objs[k];
    return { json: async () => JSON.parse(v), body: v, httpEtag: '"e"' }; } };
}

test("fetch", async () => {
  const env = { ...env0, BUCKET: bucket({
    [`a/${ID}/meta.json`]: JSON.stringify({ latest: 2, share: { emails: ["bob@y.com"] } }),
    [`a/${ID}/v/2/index.html`]: "<p>v2</p>", [`a/${ID}/v/1/index.html`]: "<p>v1</p>" }) };
  const req = async (path, claims = good, method = "GET") => worker.fetch(new Request("https://aiman.fyi" + path,
    { method, headers: claims ? { "Cf-Access-Jwt-Assertion": await token(claims) } : {} }), env);
  let r = await req(`/a/${ID}/`);
  assert.equal(r.status, 200); assert.equal(await r.text(), "<p>v2</p>");
  assert.match(r.headers.get("Content-Security-Policy"), /^sandbox allow-scripts/);
  assert.ok(!r.headers.get("Access-Control-Allow-Origin"));
  assert.equal(await (await req(`/a/${ID}/v/1/`)).text(), "<p>v1</p>");
  assert.equal((await req(`/a/${ID}/v/3/`)).status, 404);
  assert.equal((await req(`/a/${ID}`)).status, 301);
  assert.equal((await req(`/a/${ID}/`, null)).status, 403);
  r = await req(`/a/${ID}/`, { ...good, email: "eve@z.com" });
  assert.equal(r.status, 403); assert.ok(r.headers.get("Clear-Site-Data"));
  assert.equal((await req(`/a/${ID}/meta.json`)).status, 404);
  assert.equal((await req(`/a/zzzzzzzzzzzzzzzzzzzz/`)).status, 403);
  assert.equal((await req(`/a/${ID}/`, good, "POST")).status, 405);
  r = await worker.fetch(new Request(`https://evil.com/a/${ID}/`, { headers: { "Cf-Access-Jwt-Assertion": await token(good) } }), env);
  assert.equal(r.status, 404);
});
