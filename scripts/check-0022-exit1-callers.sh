#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-0022-exit1-callers.sh — 0022 §5 exit 1. Published 0016/0018 win.
# Callers write those columns. No 0022 CREATE of the colliding tables.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-0022-exit1-callers: FAIL — $*" >&2; exit 1; }
cannot() { say "check-0022-exit1-callers: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0022_JSON:-design/c0022-exit1-callers.json}"
DOC="${OLIVARES_C0022_DOC:-design/ADJUDICACION-0022-SALIDA-1-CALLERS-2026-08-19.md}"
M16="${OLIVARES_C0022_M16:-commercial/license-worker/migrations/0016_dodo_cohort_barrier.sql}"
M18="${OLIVARES_C0022_M18:-commercial/license-worker/migrations/0018_dodo_atomic_issuance.sql}"
DB="${OLIVARES_C0022_DB:-commercial/license-worker/src/store/db.ts}"
MIGDIR="${OLIVARES_C0022_MIGDIR:-commercial/license-worker/migrations}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$M16" ] || cannot "missing $M16"
[ -f "$M18" ] || cannot "missing $M18"
[ -f "$DB" ] || cannot "missing $DB"
[ -d "$MIGDIR" ] || cannot "missing $MIGDIR"

grep -q 'SALIDA 1' "$DOC" || fail "$DOC lost SALIDA 1"
if grep -qiE 'CREATE TABLE IF NOT EXISTS dodo_cohort_fragments|0022 creates the tables' "$DOC"; then
	fail "$DOC claims a CREATE this lote forbids"
fi
if ls "$MIGDIR"/0022* >/dev/null 2>&1; then
	fail "a 0022_*.sql exists — exit 1 forbids creating the colliding tables"
fi
grep -q 'CREATE TABLE IF NOT EXISTS dodo_cohort_fragments' "$M16" \
	|| fail "0016 lost dodo_cohort_fragments"
grep -q 'CREATE TABLE dodo_line_grants' "$M18" \
	|| fail "0018 lost dodo_line_grants"
grep -q 'INSERT INTO dodo_line_grants' "$DB" \
	|| fail "db.ts lost the dodo_line_grants writer"
grep -q 'INSERT OR IGNORE INTO dodo_cohort_fragments' "$DB" \
	|| fail "db.ts lost the dodo_cohort_fragments writer"
if grep -q 'cohort barrier (migrations/0006)' "$DB"; then
	fail "db.ts still attributes the barrier to slot 0006"
fi
grep -q 'line_key, grant_id, order_line_id, product_id' "$DB" \
	|| fail "dodo_line_grants INSERT lost the published 0018 columns"
grep -q 'business_id, subscription_id, event_timestamp, kind, webhook_id, payload, received_at' "$DB" \
	|| fail "dodo_cohort_fragments INSERT lost the published 0016 columns"

python3 - "$JSON" <<'PY' || fail "JSON failed the 0022 exit-1 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c0022-exit1-callers/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("exit") != 1:
    raise SystemExit("exit must stay 1")
if data.get("creates_0022_tables") is not False:
    raise SystemExit("creates_0022_tables must stay false")
if data.get("callers_same_concept") is not True:
    raise SystemExit("callers_same_concept must stay true (exit 3 rejected)")
if data.get("exit2_rejected") is not True or data.get("exit3_rejected") is not True:
    raise SystemExit("exit 2 and 3 must stay rejected")
pub = data.get("published") or []
names = {row.get("table"): row.get("slot") for row in pub if isinstance(row, dict)}
if names.get("dodo_cohort_fragments") != "0016":
    raise SystemExit("fragments must stay slot 0016")
if names.get("dodo_line_grants") != "0018":
    raise SystemExit("line grants must stay slot 0018")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-0022-exit1-callers: CLEAN — exit 1; callers write 0016/0018; no 0022 CREATE."
exit 0
