// aiman artifacts Worker (PoC of design rev 4, piece B).
// Serves https://<ARTIFACT_HOST>/a/<id>/... from the private R2 bucket BUCKET,
// only to a viewer whose Cloudflare Access JWT verifies and whose email is in
// OWNERS or the artifact's share list (a/<id>/meta.json).

const ID_RE = /^[a-z2-7]{20}$/;
const CDNS = "https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://unpkg.com";
const CSP = [
  "sandbox allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads",
  "default-src 'self' 'unsafe-inline' data: blob:",
  `script-src 'self' 'unsafe-inline' ${CDNS}`,
  `style-src 'self' 'unsafe-inline' ${CDNS} https://fonts.googleapis.com`,
  "font-src 'self' data: https://cdnjs.cloudflare.com https://cdn.jsdelivr.net https://fonts.gstatic.com",
  "connect-src 'self'",
  "form-action 'none'",
  "base-uri 'none'",
  "object-src 'none'",
  "worker-src 'none'",
  "frame-ancestors 'none'",
].join("; ");

const BASE_HEADERS = {
  "Content-Security-Policy": CSP,
  "X-Content-Type-Options": "nosniff",
  "Referrer-Policy": "no-referrer",
  "Cache-Control": "private, no-store",
};

const TYPES = {
  html: "text/html; charset=utf-8", htm: "text/html; charset=utf-8",
  css: "text/css; charset=utf-8", js: "text/javascript; charset=utf-8",
  mjs: "text/javascript; charset=utf-8", json: "application/json",
  svg: "image/svg+xml", png: "image/png", jpg: "image/jpeg", jpeg: "image/jpeg",
  gif: "image/gif", webp: "image/webp", ico: "image/x-icon",
  woff: "font/woff", woff2: "font/woff2", txt: "text/plain; charset=utf-8",
  md: "text/plain; charset=utf-8", csv: "text/csv; charset=utf-8", pdf: "application/pdf",
};

function deny(status, text) {
  const h = { ...BASE_HEADERS, "Content-Type": "text/plain; charset=utf-8" };
  if (status === 403) h["Clear-Site-Data"] = '"storage", "cache"';
  return new Response(text + "\n", { status, headers: h });
}

// ---- Access JWT ----

let jwks = null;
let jwksAt = 0;

function b64urlBytes(s) {
  s = s.replace(/-/g, "+").replace(/_/g, "/");
  while (s.length % 4) s += "=";
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function b64urlJSON(s) {
  return JSON.parse(new TextDecoder().decode(b64urlBytes(s)));
}

async function keys(team, force) {
  if (!force && jwks && Date.now() - jwksAt < 3600_000) return jwks;
  const r = await fetch(`https://${team}/cdn-cgi/access/certs`);
  if (!r.ok) throw new Error("certs " + r.status);
  jwks = (await r.json()).keys || [];
  jwksAt = Date.now();
  return jwks;
}

// verify returns the email of a valid Access token, else null.
export async function verify(token, env, now = Date.now() / 1000) {
  if (!token) return null;
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  let header, claims;
  try {
    header = b64urlJSON(parts[0]);
    claims = b64urlJSON(parts[1]);
  } catch {
    return null;
  }
  if (header.alg !== "RS256" || !header.kid) return null;
  let jwk = (await keys(env.TEAM_DOMAIN, false)).find((k) => k.kid === header.kid);
  if (!jwk) jwk = (await keys(env.TEAM_DOMAIN, true)).find((k) => k.kid === header.kid);
  if (!jwk) return null;
  const key = await crypto.subtle.importKey("jwk", jwk, { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" }, false, ["verify"]);
  const ok = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", key, b64urlBytes(parts[2]),
    new TextEncoder().encode(parts[0] + "." + parts[1]));
  if (!ok) return null;
  const aud = Array.isArray(claims.aud) ? claims.aud : [claims.aud];
  if (!aud.includes(env.POLICY_AUD)) return null;
  if (claims.iss !== `https://${env.TEAM_DOMAIN}`) return null;
  if (typeof claims.exp !== "number" || claims.exp < now) return null;
  if (typeof claims.nbf === "number" && claims.nbf > now + 60) return null;
  return typeof claims.email === "string" ? claims.email.toLowerCase() : null;
}

// ---- authorisation ----

export function allowed(email, owners, share) {
  if (!email) return false;
  const list = (owners || "").split(",").map((s) => s.trim().toLowerCase()).filter(Boolean);
  if (list.includes(email)) return true;
  const emails = (share?.emails || []).map((s) => s.toLowerCase());
  if (emails.includes(email)) return true;
  const domain = email.slice(email.lastIndexOf("@") + 1);
  return (share?.domains || []).map((s) => s.toLowerCase()).includes(domain);
}

// route maps a path to {id, version|null, file} or null.
export function route(pathname) {
  const m = pathname.match(/^\/a\/([^/]+)(\/.*)?$/);
  if (!m || !ID_RE.test(m[1])) return null;
  let rest = m[2];
  if (rest === undefined) return { id: m[1], redirect: true };
  let version = null;
  const v = rest.match(/^\/v\/([1-9][0-9]{0,8})(\/.*)?$/);
  if (v) {
    version = Number(v[1]);
    if (v[2] === undefined) return { id: m[1], redirect: true, version };
    rest = v[2];
  }
  let file = decodeURIComponent(rest.slice(1));
  if (file === "" || file.endsWith("/")) file += "index.html";
  const segs = file.split("/");
  if (segs.some((s) => s === "" || s === "." || s === ".." || /[\x00-\x1f]/.test(s))) return null;
  return { id: m[1], version, file };
}

export default {
  async fetch(request, env) {
    if (request.method !== "GET" && request.method !== "HEAD") return deny(405, "method not allowed");
    const url = new URL(request.url);
    if (url.hostname !== env.ARTIFACT_HOST) return deny(404, "not found");
    const r = route(url.pathname);
    if (!r) return deny(404, "not found");

    let email;
    try {
      email = await verify(request.headers.get("Cf-Access-Jwt-Assertion"), env);
    } catch {
      return deny(403, "forbidden");
    }
    if (!email) return deny(403, "forbidden");

    const metaObj = await env.BUCKET.get(`a/${r.id}/meta.json`);
    if (!metaObj) return deny(403, "forbidden");
    let meta;
    try {
      meta = await metaObj.json();
    } catch {
      return deny(403, "forbidden");
    }
    if (meta.deleted || !allowed(email, env.OWNERS, meta.share)) return deny(403, "forbidden");

    if (r.redirect) {
      const loc = r.version ? `/a/${r.id}/v/${r.version}/` : `/a/${r.id}/`;
      return new Response(null, { status: 301, headers: { ...BASE_HEADERS, Location: loc } });
    }
    const version = r.version ?? meta.latest;
    if (!Number.isInteger(version) || version < 1 || version > meta.latest) return deny(404, "not found");
    const obj = await env.BUCKET.get(`a/${r.id}/v/${version}/${r.file}`);
    if (!obj) return deny(404, "not found");
    const ext = r.file.slice(r.file.lastIndexOf(".") + 1).toLowerCase();
    const headers = { ...BASE_HEADERS, "Content-Type": TYPES[ext] || "application/octet-stream", ETag: obj.httpEtag };
    return new Response(request.method === "HEAD" ? null : obj.body, { headers });
  },
};
