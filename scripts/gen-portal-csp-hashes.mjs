#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Generate the portal stylesheet hash from the rendered layout bytes. Scripts use a
// per-response nonce. --check verifies the committed stylesheet hash without writing.
// Updates replace only the generated region in csp.ts.

import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const CSP_FILE = join(HERE, "..", "commercial", "license-worker", "src", "portal", "csp.ts");
const LAYOUT = join(HERE, "..", "commercial", "license-worker", "src", "portal", "layout.ts");

const BEGIN = "// >>> GENERATED: portal CSP hashes";
const END = "// <<< GENERATED";

/**
 * The bytes the browser will hash.
 *
 * ⛔ THE SOURCE OF TRUTH IS THE MODULE, NOT A COPY. The functions are imported and CALLED, so what
 * is hashed is exactly what `renderLayout` interpolates. Reading the file and extracting the
 * template literal with a regular expression would be a second implementation of the template
 * language, and it would diverge on the first backtick somebody adds.
 *
 * The `\n` handling matters and is deliberate: a browser hashes the element's text content
 * VERBATIM, so the string is used exactly as returned, with no trimming.
 */
async function inlineBodies() {
  const layout = await import(`file://${LAYOUT}`);
  if (typeof layout.portalStyles !== "function" || typeof layout.portalScript !== "function") {
    throw new Error("gen-portal-csp-hashes: layout.ts must export portalStyles and portalScript");
  }
  return { style: layout.portalStyles(), script: layout.portalScript() };
}

function sha256Base64(text) {
  return createHash("sha256").update(text, "utf8").digest("base64");
}

function replaceBetweenSentinels(source, generated) {
  const begin = source.indexOf(BEGIN);
  const end = source.indexOf(END);
  if (begin < 0 || end < 0 || end < begin) {
    throw new Error(`gen-portal-csp-hashes: sentinels not found in ${CSP_FILE}`);
  }
  const head = source.slice(0, begin + BEGIN.length);
  const tail = source.slice(end);
  return `${head}\n${generated}${tail}`;
}

async function main() {
  const check = process.argv.includes("--check");
  const { style, script } = await inlineBodies();
  // A positive control on the instrument: an empty body would hash to a stable value and every
  // comparison below would agree with itself while authorising nothing. The SCRIPT is still read
  // and still checked, even though no hash is emitted for it: an extractor that silently returns
  // nothing for one of the two bodies is broken for both, and this is where that shows.
  if (style.length < 100 || script.length < 100) {
    console.error("gen-portal-csp-hashes: NO HE PODIDO MIRAR: an inline body came back empty");
    process.exit(2);
  }

  // ⛔ ONLY THE STYLESHEET IS HASHED. `script-src` is a per-response nonce, stamped by `html()`, so
  // a script hash would be a constant with no consumer — and a constant with no consumer is one
  // nobody re-derives when the bytes move.
  const generated =
    `export const PORTAL_STYLE_HASH = "sha256-${sha256Base64(style)}";\n`;

  const current = readFileSync(CSP_FILE, "utf8");
  const next = replaceBetweenSentinels(current, generated);

  if (check) {
    if (next !== current) {
      console.error(
        "gen-portal-csp-hashes: the committed stylesheet hash does not match the rendered bytes.\n" +
        "  The policy would refuse the portal stylesheet, and the page would\n" +
        "  render without its stylesheet.\n" +
        "  Run: node scripts/gen-portal-csp-hashes.mjs",
      );
      process.exit(1);
    }
    console.log("gen-portal-csp-hashes: OK — the stylesheet hash matches the rendered bytes.");
    return;
  }

  writeFileSync(CSP_FILE, next);
  console.log(`gen-portal-csp-hashes: wrote ${CSP_FILE}`);
}

main().catch((error) => {
  console.error("gen-portal-csp-hashes:", error?.message ?? error);
  process.exit(2);
});
