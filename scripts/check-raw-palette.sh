#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Enforce `olivares/no-raw-palette` over web/src with the REAL eslint.config.js: no raw
# palette class, arbitrary color value or bare color string outside the design tokens, which
# would paint the same in Light, Dark and Black and escape the contrast tests. The config
# names the files whose colors must stay literal and leaves tests and the dev page out.
#
# This was a vitest case (web/src/features/raw-palette.test.ts) that ran a full
# programmatic ESLint pass over 777 files INSIDE a vitest worker: 148s and 113s alone,
# and 300 892 ms — its own 300s ceiling — under suite load, which is how it failed. Its
# comment records an earlier raise from 60s, so the ceiling had already been bought once.
#
# It is a lint, so it runs in the lint path. The ONLY thing that changes is where it
# runs: same config, same rule, same file set, same verdict.
#
# Why the whole config and not just the rule: measured, a single-rule ESLint run is 5.9x
# cheaper (14.7s vs 86.6s) and reports 58 violations where the real config reports 0,
# because eslint.config.js exempts six files under api-playground/ and backups/.
# Re-declaring those exemptions here would be a second copy of the control.
#
# Why only this rule's messages are fatal: that is exactly what the vitest case asserted.
# The other ESLint rules are NOT enforced by any gate today (`lint:web` is deliberately
# skipped by .githooks/pre-push and no workflow invokes it); widening the verdict here
# would be a different change, made silently, on a contributor nobody has budgeted for.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
raiz="$PWD"

if [ ! -d web/node_modules ]; then
  echo "check-raw-palette: web/node_modules missing — this leg runs after check:web installs deps" >&2
  exit 2
fi

report_file="$(mktemp)"
error_file="$(mktemp)"
trap 'rm -f "$report_file" "$error_file"' EXIT

# ESLint exits non-zero when ANY rule reports; the JSON is still what we judge, so the
# exit code is captured and only used to tell "it ran" from "it could not run".
pnpm --dir web exec eslint 'src/**/*.{ts,tsx}' --format json >"$report_file" 2>"$error_file"
rc=$?

if [ ! -s "$report_file" ]; then
  echo "check-raw-palette: ESLint produced no report (rc=$rc) — NOT a clean verdict" >&2
  head -n 20 "$error_file" >&2
  exit 2
fi

python3 - "$report_file" "$raiz/web" <<'PY'
import json, os, sys

with open(sys.argv[1], encoding='utf-8') as fh:
    try:
        informe = json.load(fh)
    except json.JSONDecodeError as exc:
        print(f'check-raw-palette: unreadable ESLint report: {exc}', file=sys.stderr)
        raise SystemExit(2)

base = sys.argv[2]
faltas = []
ilegibles = []
for r in informe:
    for m in r.get('messages', []):
        donde = f"{os.path.relpath(r['filePath'], base)}:{m.get('line')}:{m.get('column')} {m.get('message')}"
        if m.get('ruleId') == 'olivares/no-raw-palette':
            faltas.append(donde)
        elif m.get('fatal'):
            # A file ESLint could not parse was never checked: not a clean verdict.
            ilegibles.append(donde)

print(f'check-raw-palette: {len(informe)} files linted, {len(faltas)} raw-color violations, {len(ilegibles)} unparsed')
if ilegibles:
    print('Files ESLint could not parse, so the raw color rule never ran on them:', file=sys.stderr)
    for f in ilegibles:
        print(f'  {f}', file=sys.stderr)
    raise SystemExit(2)
if faltas:
    print('Raw colors bypass the semantic design tokens:', file=sys.stderr)
    for f in faltas:
        print(f'  {f}', file=sys.stderr)
    raise SystemExit(1)
PY
