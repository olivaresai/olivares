#!/usr/bin/env node
// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
//
// check-wrangler-pin.mjs — the wrangler that is ABOUT TO RUN must be the pinned one.
//
// ⛔ WHY THIS EXISTS, AND IT IS NOT HYPOTHETICAL. On 2026-08-29 the hub's docs deploy carried a
// guard meant to stop an unpinned wrangler running with a production credential:
//   npm --prefix docs-site exec --no-install -- wrangler --version
// `--no-install` is an npx flag, NOT an `npm exec` flag. npm says so itself — `npm warn Unknown
// cli config "--install"` — and ignores it. Both of that day's production docs deploys therefore
// FETCHED wrangler from the registry at deploy time and ran it with CLOUDFLARE_API_TOKEN in the
// environment. The workflow's comment asserted the protection existed, which is worse than having
// none: a reader stops looking.
//
// THIS repository is where that happened. `docs-site/package.json` declared no wrangler at ALL,
// so every documentation deploy resolved one from the registry at deploy time. The fix is the
// same one the web repository now carries: an EXACT version, which neither `npm ci` nor
// `npm install` can move, plus this check on the binary that will actually run.
//
// ⛔ AND THIS CHECK DOES NOT TRUST A FLAG. Flags get renamed, deprecated, and — as above —
// silently ignored. It RUNS the binary that would run and compares what it reports against the
// exact version this repository pins. A guard that asks "is the flag right?" answers a different
// question from "is the code that will hold my production token the code I chose?".
//
// Exit 0 = the binary about to run is the pinned one.
// Exit 1 = it is not; both versions are named.
// Exit 2 = could not look (no manifest, no binary, unreadable output). NEVER reported as clean.

import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

// In this repository the wrangler that deploys belongs to docs-site/, not to the repo root:
// docs-site is its own npm project and the deploy workflow runs `npm --prefix docs-site`.
// OLIVARES_WRANGLER_PROJECT lets the same script serve another project without a second copy.
//
// And a second workspace DOES use that variable today: its own package.json passes it and wires
// this check into its `test` and into BOTH `predeploy:*` scripts, so the binary is verified
// immediately before it runs with a deploy credential — which is the only moment that matters.
//
// That workspace is deliberately NOT named here. This file SHIPS in the public export and that
// one does not, so naming it would leave, in the published tree, a path that resolves to nothing
// — and no gate would say so, because the closure check skips references into the part of the
// tree the export removes. This repository has already paid for one of those: a parity check
// comparing against a file that the export drops, found by a reading and not by a gate.
// The variable is the contract; the caller supplies the path.
const REPO = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ROOT = path.resolve(REPO, process.env.OLIVARES_WRANGLER_PROJECT || 'docs-site');

function couldNotLook(why) {
  console.error(`check-wrangler-pin: NO HE PODIDO MIRAR: ${why}`);
  process.exit(2);
}

let pinned;
try {
  const pkg = JSON.parse(readFileSync(path.join(ROOT, 'package.json'), 'utf8'));
  pinned = { ...pkg.dependencies, ...pkg.devDependencies }.wrangler;
} catch (err) {
  couldNotLook(`package.json is unreadable (${err.message})`);
}
if (!pinned) couldNotLook('package.json declares no wrangler at all');

// A range is not a pin. "^4.90.1" satisfies 4.90.1 and 4.999.0 alike, and which one you get
// depends on when somebody last ran npm install — that is the decision this file exists to take
// away from the calendar.
if (!/^\d+\.\d+\.\d+$/.test(pinned)) {
  console.error(
    `check-wrangler-pin: package.json declares wrangler "${pinned}", which is a RANGE, not a pin.\n` +
      '    A range lets an install move the binary that will hold the production token.\n' +
      '    Declare the exact version the lockfile resolves.',
  );
  process.exit(1);
}

let reported;
try {
  // The LOCAL binary — the one `npx wrangler` and `npm exec` would resolve first. If it is
  // absent this throws, and absence is exactly the case the old guard was supposed to catch.
  const out = execFileSync(path.join(ROOT, 'node_modules', '.bin', 'wrangler'), ['--version'], {
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  const m = out.match(/(\d+\.\d+\.\d+)/);
  if (!m) couldNotLook(`could not read a version out of wrangler's output: ${JSON.stringify(out.slice(0, 120))}`);
  reported = m[1];
} catch (err) {
  couldNotLook(
    `the local wrangler did not run (${err.message.split('\n')[0]}). ` +
      'Run `npm ci` first — this check must never pass by falling back to a downloaded copy.',
  );
}

if (reported !== pinned) {
  console.error(
    `check-wrangler-pin: the wrangler that would run is ${reported}, but this repository pins ${pinned}.\n` +
      '    Refusing: a deploy runs this binary with CLOUDFLARE_API_TOKEN in its environment.\n' +
      '    Fix by running `npm ci` so node_modules matches the lockfile, or by changing the pin\n' +
      '    deliberately in package.json AND package-lock.json together.',
  );
  process.exit(1);
}

console.log(`check-wrangler-pin: OK — the wrangler that will run is ${reported}, the pinned version.`);
