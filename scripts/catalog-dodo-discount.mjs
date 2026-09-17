#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

// Put ONE discount rule into a `DODO_CATALOG` block of wrangler.jsonc, and refuse to write
// anything the real parser would not accept.
//
// WHY A SCRIPT AND NOT AN EDIT. The catalogue is a JSON document escaped INSIDE a JSON string
// inside a JSONC file. Editing it by hand means counting backslashes on a line that decides what
// a paying customer is charged, and getting it subtly wrong is silent: the file still parses as
// JSONC, the worker still boots, and a rule authorises something nobody wrote. I hand-edited that
// line once today for sandbox and the only reason it was right is that I verified it afterwards
// with a separate probe. This does the verification BEFORE writing, with the catalogue parser the
// worker itself uses — so a rule that would throw at boot cannot reach the file.
//
// ⛔ IT REFUSES A 100 % RULE, because the parser does: a free purchase is a different KIND of
// consideration (Fase B), not another percentage. And it requires `subscription_cycles`, because
// the provider's own default is FOR EVER — a coupon created without it is a free subscription,
// invisible until the second charge (measured 2026-09-01; the contrast caught it in our own
// runbook before it reached a customer).
//
//   node scripts/catalog-dodo-discount.mjs --env production --id dsc_… --bp 9900 \
//        --product pdt_… --cycles 1 [--addons] [--check] [--remove]
//
// Exit codes, the house's three: 0 written (or already correct) · 1 refused · 2 could not look.

import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const HERE = dirname(fileURLToPath(import.meta.url));
const WRANGLER = resolve(HERE, "..", "commercial", "license-worker", "wrangler.jsonc");

function die(code, msg) {
  console.error(`catalog-dodo-discount: ${msg}`);
  process.exit(code);
}

function arg(name, fallback = undefined) {
  const i = process.argv.indexOf(`--${name}`);
  if (i === -1) return fallback;
  const v = process.argv[i + 1];
  if (v === undefined || v.startsWith("--")) die(1, `--${name} needs a value`);
  return v;
}
const flag = (name) => process.argv.includes(`--${name}`);

const env = arg("env");
const id = arg("id");
const check = flag("check");
const remove = flag("remove");
if (!env || !["sandbox", "production"].includes(env)) die(1, "--env must be sandbox or production");
if (!id) die(1, "--id is required (the dsc_… the provider minted)");

// The three env blocks share the key, so the block is chosen by its CONTENT, never by line
// number: a line number drifts with every edit above it, and picking the wrong one here means
// writing a production rule into sandbox or the reverse.
const SANDBOX_MARKER = "pdt_0NkL6fPms1DwlDsUUcawf";
const PRODUCTION_MARKER = "pdt_0NlE6WlUBAFO7F2uZLbHE";

let raw;
try {
  raw = readFileSync(WRANGLER, "utf8");
} catch (e) {
  die(2, `cannot read ${WRANGLER}: ${e.message}`);
}
const lines = raw.split("\n");

const { parseDodoCatalog } = await import(
  resolve(HERE, "..", "commercial", "license-worker", "src", "dodo", "catalog.ts")
).catch((e) => die(2, `cannot load the catalogue parser: ${e.message}`));

const hits = [];
lines.forEach((line, idx) => {
  const m = line.match(/^(\s*"DODO_CATALOG": ")(.*)("\s*,?\s*)$/);
  if (!m) return;
  let doc;
  try {
    doc = JSON.parse(JSON.parse(`"${m[2]}"`));
  } catch {
    return; // an empty or unparseable block is not a candidate; it is also not an error here
  }
  const isProd = Object.keys(doc.products ?? {}).includes(PRODUCTION_MARKER);
  const isSandbox = Object.keys(doc.products ?? {}).includes(SANDBOX_MARKER);
  if ((env === "production" && isProd) || (env === "sandbox" && isSandbox && !isProd)) {
    hits.push({ idx, m, doc });
  }
});

if (hits.length !== 1) {
  die(2, `expected exactly ONE ${env} DODO_CATALOG block, found ${hits.length}`);
}
const { idx, m, doc } = hits[0];

const before = JSON.stringify(doc.discounts ?? {});
if (remove) {
  if (!doc.discounts || !(id in doc.discounts)) {
    console.log(`catalog-dodo-discount: ${id} is not in the ${env} catalogue; nothing to remove`);
    process.exit(0);
  }
  delete doc.discounts[id];
  if (Object.keys(doc.discounts).length === 0) delete doc.discounts;
} else {
  const bp = Number(arg("bp"));
  const cycles = Number(arg("cycles"));
  const products = process.argv.flatMap((a, i) => (a === "--product" ? [process.argv[i + 1]] : []));
  if (!Number.isSafeInteger(bp)) die(1, "--bp must be an integer of basis points");
  if (!Number.isSafeInteger(cycles)) die(1, "--cycles must be an integer");
  if (products.length === 0) die(1, "--product is required, at least once");
  doc.discounts = {
    ...(doc.discounts ?? {}),
    [id]: {
      type: "percentage",
      basis_points: bp,
      restricted_to: products,
      subscription_cycles: cycles,
      applies_to_addons: flag("addons"),
    },
  };
}

// THE GATE: the worker's own parser decides. It throws on a 100 % rule, on a missing cycle
// limit, on a product this catalogue does not price, and on every other shape outside Fase A.
let parsed;
try {
  parsed = parseDodoCatalog(JSON.stringify(doc));
} catch (e) {
  die(1, `the resulting catalogue is REFUSED by the worker's own parser: ${e.message}`);
}

const after = JSON.stringify(doc.discounts ?? {});
if (before === after) {
  console.log(`catalog-dodo-discount: ${env} already says exactly this; nothing to write`);
  process.exit(0);
}

const encoded = JSON.stringify(doc)
  .replace(/\\/g, "\\\\")
  .replace(/"/g, '\\"');
lines[idx] = `${m[1]}${encoded}${m[3]}`;
const out = lines.join("\n");

// Belt: re-read the line we just built, the way the file will be read, before trusting it.
const rebuilt = out.split("\n")[idx].match(/^(\s*"DODO_CATALOG": ")(.*)("\s*,?\s*)$/);
if (!rebuilt) die(2, "the rewritten line no longer matches the expected shape");
try {
  parseDodoCatalog(JSON.parse(`"${rebuilt[2]}"`));
} catch (e) {
  die(2, `the rewritten line does not round-trip through the parser: ${e.message}`);
}

console.log(
  `catalog-dodo-discount: ${env} line ${idx + 1} · products ${parsed.products.size} · ` +
    `discounts ${parsed.discounts.size} · ${before} -> ${after}`,
);
if (check) {
  console.log("catalog-dodo-discount: --check, nothing written");
  process.exit(0);
}
writeFileSync(WRANGLER, out);
console.log("catalog-dodo-discount: written. Review the ONE-line diff before committing.");
