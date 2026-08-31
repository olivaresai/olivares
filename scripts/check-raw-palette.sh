#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Enforce `olivares/no-raw-palette` over web/src/features with the REAL eslint.config.js.
#
# This was a vitest case (web/src/features/raw-palette.test.ts) that ran a full
# programmatic ESLint pass over 777 files INSIDE a vitest worker: 148s and 113s alone,
# and 300 892 ms — its own 300s ceiling — under suite load, which is how it failed. Its
# comment records an earlier raise from 60s, so the ceiling had already been bought once.
#
# It is a lint, so it runs in the lint lane. The ONLY thing that changes is where it
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
# would be a different change, made silently, on a lane nobody has budgeted for.
set -uo pipefail

cd "$(dirname "$0")/.." || exit 2
raiz="$PWD"

if [ ! -d web/node_modules ]; then
  echo "check-raw-palette: web/node_modules missing — this leg runs after check:web installs deps" >&2
  exit 2
fi

salida="$(mktemp)"
trap 'rm -f "$salida"' EXIT

# ESLint exits non-zero when ANY rule reports; the JSON is still what we judge, so the
# exit code is captured and only used to tell "it ran" from "it could not run".
pnpm --dir web exec eslint 'src/features/**/*.{ts,tsx}' --format json >"$salida" 2>/dev/null
rc=$?

if [ ! -s "$salida" ]; then
  echo "check-raw-palette: ESLint produced no report (rc=$rc) — NOT a clean verdict" >&2
  exit 2
fi

python3 - "$salida" "$raiz/web" <<'PY'
import json, os, sys

with open(sys.argv[1], encoding='utf-8') as fh:
    try:
        informe = json.load(fh)
    except json.JSONDecodeError as exc:
        print(f'check-raw-palette: unreadable ESLint report: {exc}', file=sys.stderr)
        raise SystemExit(2)

base = sys.argv[2]
faltas = []
for r in informe:
    for m in r.get('messages', []):
        if m.get('ruleId') == 'olivares/no-raw-palette':
            faltas.append(
                f"{os.path.relpath(r['filePath'], base)}:{m.get('line')}:{m.get('column')} {m.get('message')}"
            )

print(f'check-raw-palette: {len(informe)} files linted, {len(faltas)} raw-palette violations')
if faltas:
    print('Raw Tailwind palette classes bypass the semantic design tokens:', file=sys.stderr)
    for f in faltas:
        print(f'  {f}', file=sys.stderr)
    raise SystemExit(1)
PY
