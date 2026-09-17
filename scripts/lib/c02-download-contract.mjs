// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import assert from "node:assert/strict";
import { resolve, dirname } from "node:path";
import { pathToFileURL } from "node:url";

// Import the actual module graph: no rewritten imports or replacement entitlement predicate.
// Missing runtime inputs are unobserved, while syntax/export/behaviour regressions are findings.
if (Number(process.versions.node.split(".")[0]) < 24) {
  console.error("c02-download-contract: Node 24 or newer is required");
  process.exit(2);
}
async function load(path) {
  try {
    return await import(pathToFileURL(resolve(path)).href);
  } catch (error) {
    if (["ERR_MODULE_NOT_FOUND", "ENOENT"].includes(error.code)) {
      console.error(`c02-download-contract: missing runtime input: ${error.message}`);
      process.exit(2);
    }
    throw error;
  }
}
const { artifactKey } = await load(process.argv[2]);
const { ALLOWED_SET_SLUGS, isAllowedSetSlug } = await load(process.argv[3]);
const { handleDownload } = await load(process.argv[4]);
const { mintDownloadToken, verifyDownloadToken } = await load(resolve(dirname(process.argv[4]), "tokens.ts"));
assert.equal(typeof handleDownload, "function", "handleDownload must be exported");
// The handler only uses its supplied Store and bucket. Any accidental egress is a finding.
globalThis.fetch = async () => { throw new Error("download contract attempted network access"); };
if (typeof artifactKey !== "function") {
  throw new Error("artifactKey is not exported as a function");
}
if (artifactKey.length !== 4) {
  throw new Error(`artifactKey arity is ${artifactKey.length}, want 4`);
}
// This is an independent contract oracle on purpose. Deriving the expected
// universe from ALLOWED_SET_SLUGS would let removal of one paid set make both
// the implementation and this check agree on the same regression.
const expected = [
  "biz",
  "biz+airs",
  "biz+cp",
  "biz+ids",
  "biz+reg",
  "biz+airs+cp",
  "biz+airs+ids",
  "biz+airs+reg",
  "biz+cp+ids",
  "biz+cp+reg",
  "biz+ids+reg",
  "biz+airs+cp+ids",
  "biz+airs+cp+reg",
  "biz+airs+ids+reg",
  "biz+cp+ids+reg",
  "biz+airs+cp+ids+reg",
  "ent",
];
const allowed = [...ALLOWED_SET_SLUGS];
if (
  allowed.length !== expected.length ||
  expected.some((set) => !ALLOWED_SET_SLUGS.has(set)) ||
  allowed.some((set) => !expected.includes(set))
) {
  throw new Error(
    `ALLOWED_SET_SLUGS=${JSON.stringify(allowed)}, want ${JSON.stringify(expected)}`,
  );
}
for (const set of expected) {
  if (!isAllowedSetSlug(set)) {
    throw new Error(`isAllowedSetSlug rejected paid set ${JSON.stringify(set)}`);
  }
  const got = artifactKey("v26.8.0", "linux", "amd64", set);
  const want = `enterprise/v26.8.0/${set}/olivares_v26.8.0_linux_amd64.tar.gz`;
  if (got !== want) {
    throw new Error(`artifactKey(${set})=${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
}
for (const invalid of [
  "",
  "not-a-set",
  "attacker",
  "all",
  "biz+unknown",
  "reg+biz",
  "biz+reg+reg",
  "ent+biz",
]) {
  if (isAllowedSetSlug(invalid)) {
    throw new Error(`isAllowedSetSlug accepted ${JSON.stringify(invalid)}`);
  }
  let rejected = false;
  try {
    artifactKey("v26.8.0", "linux", "amd64", invalid);
  } catch {
    rejected = true;
  }
  if (!rejected) {
    throw new Error(`artifactKey accepted non-allowlisted set ${JSON.stringify(invalid)}`);
  }
}
// The same correctly signed token remains valid while its holder's live grants change.
// Expected paths and audit labels are independent of artifactKey/downloadAuditLabel.
const secret = "local-c02-r2-contract-fixture-only";
const now = Math.floor(Date.parse("2026-09-05T12:00:00Z") / 1000);
const version = "v26.8.0";
const holder = "contract-holder-a";
const other = "contract-holder-b";
const token = await mintDownloadToken({ secret, holderId: holder, version, nowSec: now, ttlSec: 3600 });
const otherToken = await mintDownloadToken({ secret, holderId: other, version, nowSec: now, ttlSec: 3600 });
const verified = await verifyDownloadToken(secret, token, now);
assert.equal(verified.ok, true, "official mint/verify must compose");
assert.ok(verified.jti);
let currentHolder = holder;
let currentCodes = ["biz"];
let hasLicense = true;
let tick = now;
let calls = [];
const license = {
  id: "contract-license", holderId: holder, licensee: "Local contract", plan: "commercial",
  edition: "Business", maxUsers: 25, features: [], serial: "contract-serial",
  issuedAt: "2026-09-01T00:00:00Z", expiresAt: "2027-09-01T00:00:00Z", blob: "fixture",
  status: "active", provider: "polar", polarOrderId: "", polarSubscriptionId: "",
  polarCustomerId: "", productId: "", priceId: "", amount: null, currency: "",
  customerEmail: "", sourceEventType: "", sourceEndpoint: "", sourceWebhookId: "",
  createdAt: "2026-09-01T00:00:00Z", revokedAt: null, revokedReason: null,
  terminatedAt: null, terminatedReason: null,
};
function observedRead(method, id, date) {
  assert.equal(id, currentHolder, `${method} must read the authenticated holder`);
  assert.equal(date, new Date(tick * 1000).toISOString().replace(".000Z", "Z"), `${method} must use this request's time`);
  calls.push([method, id, date]);
}
function strict(object, name) {
  return new Proxy(object, { get(target, key) {
    assert.ok(Object.hasOwn(target, key), `unexpected ${name} operation ${String(key)}`);
    return target[key];
  } });
}
const store = strict({
  async getActiveLicenseByHolder(id, date) {
    observedRead("license", id, date);
    return hasLicense ? { ...license, holderId: id } : null;
  },
  async listActiveGrantCodes(id, date) {
    observedRead("grants", id, date);
    return [...currentCodes];
  },
  async explainDownloadRefusal(id, date) {
    observedRead("refusal", id, date);
    return { kind: "none" };
  },
  async recordDownload(...args) { calls.push(["audit", ...args]); },
}, "Store");
const env = {
  DOWNLOAD_TOKEN_SECRET: secret,
  ALLOWED_ARTIFACTS: "linux/amd64",
  BUCKET: strict({ async get(key) {
    calls.push(["bucket", key]);
    return { body: key, size: key.length, httpEtag: '"contract"', writeHttpMetadata() {} };
  } }, "bucket"),
};
async function request(label, { bearer = token, query = "", status = 200, set = null } = {}) {
  calls = [];
  tick++;
  const response = await handleDownload(env, new Request(
    `https://fixture.invalid/download?os=linux&arch=amd64${query}`,
    { headers: { authorization: `Bearer ${bearer}` } },
  ), store, tick);
  assert.equal(response.status, status, label);
  const bytes = await response.text();
  if (status !== 200) {
    assert.deepEqual(calls.filter(([op]) => op === "bucket" || op === "audit"), [], `${label}: no bucket or audit before refusal`);
    return;
  }
  const key = `enterprise/${version}/${set}/olivares_${version}_linux_amd64.tar.gz`;
  assert.deepEqual(calls.filter(([op]) => op === "bucket"), [["bucket", key]], `${label}: exact grant-derived key`);
  assert.equal(bytes, key, `${label}: response streams the selected object's bytes`);
  const claims = bearer === token ? verified : await verifyDownloadToken(secret, bearer, tick);
  assert.deepEqual(calls.filter(([op]) => op === "audit"), [[
    "audit", currentHolder, `${version} ${set} linux/amd64`, claims.jti,
    new Date(tick * 1000).toISOString().replace(".000Z", "Z"),
  ]], `${label}: audit must name the actual holder, set and token`);
  const operations = calls.map(([op]) => op);
  assert.ok(operations.includes("license") && operations.includes("grants"), `${label}: re-read both live predicates`);
  assert.ok(operations.indexOf("license") < operations.indexOf("bucket") && operations.indexOf("grants") < operations.indexOf("bucket"), `${label}: authorize before bucket`);
}
for (const set of expected) {
  currentCodes = set.split("+").reverse(); // ordering must not supply the answer
  await request(`live ${set}`, { set });
}
// Reverse an earlier transition with the SAME token/store/module; caches cannot hide here.
currentCodes = ["biz"];
await request("same token after grants contract", { set: "biz" });
currentHolder = other;
currentCodes = ["ent"];
await request("second authenticated holder", { bearer: otherToken, set: "ent" });
currentHolder = holder;
for (const codes of [[], ["unknown"], ["reg"], ["biz", "biz"]]) {
  currentCodes = codes;
  await request(`no valid live grants ${JSON.stringify(codes)}`, { status: 403 });
}
currentCodes = ["biz", "reg"];
hasLicense = false;
await request("license removed while token remains valid", { status: 403 });
hasLicense = true;
await request("same token after license restored", { set: "biz+reg" });
for (const field of ["set", "variant"]) {
  for (const value of ["", "biz", "ent", "biz+reg", "attacker"]) {
    await request(`${field} query ${JSON.stringify(value)}`, {
      query: `&${field}=${encodeURIComponent(value)}`, status: 400,
    });
  }
  await request(`duplicate ${field} query`, { query: `&${field}=biz&${field}=ent`, status: 400 });
}
await request("invalid HMAC", { bearer: await mintDownloadToken({ secret: "other-fixture-key", holderId: holder, version, nowSec: now, ttlSec: 3600 }), status: 403 });
console.log("download-contract: 17 sets; live holder/grant transitions, audit, query and no-bucket refusals verified");
