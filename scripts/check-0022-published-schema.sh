#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-0022-published-schema.sh — slot 0022 is Exit 1: published 0016/0018 win.
#
# Three answers: 0 clean · 1 finding · 2 could not look.
# It does not apply migrations and it never writes D1.

set -euo pipefail

say() { printf '%s\n' "$*"; }
fail() { say "check-0022: FAIL — $*" >&2; exit 1; }
cannot() { say "check-0022: COULD NOT LOOK — $*" >&2; exit 2; }

# Script dir, not `git rev-parse`. Measured 2026-08-19: from another
# work tree the cwd root has no verdict and this gate reported FAIL
# for a written Exit 1.
ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
[ -n "$ROOT" ] || cannot "cannot resolve repository root"
cd "$ROOT" || cannot "cannot enter $ROOT"

VERDICT="$ROOT/design/ADJUDICACION-0022-SALIDA-1-2026-08-18.md"
[ -f "$VERDICT" ] || fail "verdict file missing: design/ADJUDICACION-0022-SALIDA-1-2026-08-18.md"
grep -q 'Salida 1' "$VERDICT" || fail "verdict does not name Exit 1"
grep -q '0022-exit: 1' "$VERDICT" || fail "verdict lost the machine Exit 1 marker"
if grep -qE '0022-exit: [23]' "$VERDICT"; then
  fail "verdict names Exit 2 or 3 as the choice — only Exit 1 is written"
fi

MIG="$ROOT/commercial/license-worker/migrations"
[ -d "$MIG" ] || cannot "no commercial/license-worker/migrations"

# A 0022_* that CREATEs the published tables is the bomb §5 exists to stop.
shopt -s nullglob
for f in "$MIG"/0022_*.sql; do
  if grep -Eiq 'CREATE[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+NOT[[:space:]]+EXISTS[[:space:]]+)?dodo_line_grants' "$f"; then
    fail "0022 recreates dodo_line_grants ($(basename "$f")) — published 0018 wins"
  fi
  if grep -Eiq 'CREATE[[:space:]]+TABLE[[:space:]]+(IF[[:space:]]+NOT[[:space:]]+EXISTS[[:space:]]+)?dodo_cohort_fragments' "$f"; then
    fail "0022 recreates dodo_cohort_fragments ($(basename "$f")) — published 0016 wins"
  fi
done
shopt -u nullglob

DB="$ROOT/commercial/license-worker/src/store/db.ts"
[ -f "$DB" ] || cannot "no store/db.ts"

# Live writers must name the published column lists. A writer that adds a
# flight-only column is choosing Exit 2 without saying so.
if ! grep -q 'INSERT INTO dodo_line_grants' "$DB"; then
  fail "no live INSERT INTO dodo_line_grants — the published table has no writer"
fi
if ! grep -q 'INSERT OR IGNORE INTO dodo_cohort_fragments' "$DB"; then
  fail "no live INSERT OR IGNORE INTO dodo_cohort_fragments — the published table has no writer"
fi

flight='body_sha256|billing_period|fragment_kind|normalized_json|cohort_hash|settlement_amount'
# Only flag those names when they sit in an INSERT that names the published tables.
# Sin la tuberia final a grep: bajo pipefail devuelve 141 CUANDO ACIERTA, porque grep
# cierra el tubo en la primera coincidencia y le manda SIGPIPE al awk.
_insert_lines="$(awk '
  BEGIN { ign=0 }
  /INSERT INTO dodo_line_grants|INSERT OR IGNORE INTO dodo_cohort_fragments/ { ign=1 }
  ign && /VALUES/ { ign=0 }
  ign { print }
' "$DB" || true)"
if grep -Eiq "$flight" <<<"$_insert_lines"; then
  fail "live INSERT writes a flight-only column — that is Exit 2, not Exit 1"
fi

say "check-0022: CLEAN — Exit 1, published 0016/0018 win, no 0022 CREATE of those tables."
exit 0
