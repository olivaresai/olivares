#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# 0022 §5 exit 1 remasured. Published 0016/0018 win. No 0022 CREATE.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-0022-exit1-remeasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-0022-exit1-remeasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0022R_JSON:-design/c0022-exit1-remeasure-20260820.json}"
DOC="${OLIVARES_C0022R_DOC:-design/VEREDICTO-0022-SALIDA-1-2026-08-20.md}"
M16="${OLIVARES_C0022R_M16:-commercial/license-worker/migrations/0016_dodo_cohort_barrier.sql}"
M18="${OLIVARES_C0022R_M18:-commercial/license-worker/migrations/0018_dodo_atomic_issuance.sql}"
MIGDIR="${OLIVARES_C0022R_MIGDIR:-commercial/license-worker/migrations}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$M16" ] || cannot "missing 0016"
[ -f "$M18" ] || cannot "missing 0018"
[ -d "$MIGDIR" ] || cannot "missing migrations dir"

grep -q 'SALIDA 1' "$DOC" || fail "$DOC lost SALIDA 1"
grep -q 'dodo_cohort_fragments' "$DOC" || fail "$DOC lost colliding fragments table"
grep -q 'dodo_line_grants' "$DOC" || fail "$DOC lost colliding grants table"
grep -q '0016_dodo_cohort_barrier.sql' "$DOC" || fail "$DOC lost published 0016"
grep -q '0018_dodo_atomic_issuance.sql' "$DOC" || fail "$DOC lost published 0018"
if grep -qiE '0022 creates the tables|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a CREATE this lote forbids"
fi
if ls "$MIGDIR"/0022* >/dev/null 2>&1; then
	fail "a 0022_*.sql exists — exit 1 forbids creating the colliding tables"
fi
grep -q 'CREATE TABLE IF NOT EXISTS dodo_cohort_fragments' "$M16" \
	|| fail "0016 lost dodo_cohort_fragments"
grep -q 'CREATE TABLE dodo_line_grants' "$M18" \
	|| fail "0018 lost dodo_line_grants"

python3 - "$JSON" <<'PY' || fail "JSON failed the 0022 exit-1 remasure"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("exit") != 1:
    raise SystemExit("exit must stay 1")
if data.get("creates_0022_tables") is not False:
    raise SystemExit("creates_0022_tables must stay false")
if data.get("exit2_rejected") is not True or data.get("exit3_rejected") is not True:
    raise SystemExit("exit 2 and 3 must stay rejected")
if data.get("u_f") != "UNKNOWN" or data.get("u_d") != "UNKNOWN":
    raise SystemExit("U_f/U_d must stay UNKNOWN")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
rows = {r.get("table"): r.get("published_slot") for r in (data.get("colliding") or [])}
if rows.get("dodo_cohort_fragments") != "0016":
    raise SystemExit("fragments must stay slot 0016")
if rows.get("dodo_line_grants") != "0018":
    raise SystemExit("line grants must stay slot 0018")
PY

say "check-0022-exit1-remeasure: CLEAN — exit 1; colliding names 0016/0018; no 0022 CREATE."
exit 0
