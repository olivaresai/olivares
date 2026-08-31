#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-c0022-exit1-remeasure.sh — 0022 Exit 1. No new SQL. Callers match 0016/0018.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c0022-exit1-remeasure: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c0022-exit1-remeasure: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_0022_JSON:-design/c0022-exit1-remeasure.json}"
DOC="${OLIVARES_0022_DOC:-design/C0022-EXIT1-REMEASURE-2026-08-20.md}"
DB="${OLIVARES_0022_DB:-commercial/license-worker/src/store/db.ts}"
MIG="${OLIVARES_0022_MIG:-commercial/license-worker/migrations}"
ADJ="${OLIVARES_0022_ADJ:-design/ADJUDICACION-SLOTS-D1-LICENSE-WORKER-2026-08-17.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$DB" ] || cannot "missing $DB"
[ -d "$MIG" ] || cannot "missing $MIG"
[ -f "$ADJ" ] || cannot "missing $ADJ"

grep -q 'schema NOT TOUCHED' "$DOC" || fail "$DOC lost schema NOT TOUCHED"
if grep -qiE 'salida 2 wins|CREATE TABLE IF NOT EXISTS dodo_cohort_fragments in 0022|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a close or schema this lote does not have"
fi
grep -q 'Salida 1' "$DOC" || fail "$DOC lost Salida 1"
grep -q 'salida 1' "$ADJ" || grep -q 'Salida 1' "$ADJ" || fail "$ADJ lost the Exit 1 record"

shopt -s nullglob
hits=("$MIG"/0022_*)
shopt -u nullglob
if [ "${#hits[@]}" -gt 0 ]; then
	fail "slot 0022 SQL exists: ${hits[*]}"
fi

python3 - "$JSON" "$DB" "$MIG" <<'PY' || fail "JSON/db failed the 0022 Exit 1 contract"
import json, os, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
db = open(sys.argv[2], encoding="utf-8").read()
mig = sys.argv[3]

if data.get("schema") != "c0022-exit1-remeasure/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("exit") != 1:
    raise SystemExit("exit must stay 1")
if data.get("slot_0022_sql") is not False:
    raise SystemExit("slot_0022_sql must stay false")
if data.get("schema_touched") is not False:
    raise SystemExit("schema_touched must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)

# ⛔ THE LOCATION IS DERIVED, NOT PINNED. This block used to require the JSON to carry
# line_grants_insert_line == 733 and fragments_insert_line == 1179 and then slice db.ts at
# those offsets. Any edit ANYWHERE ABOVE those lines moved the statement and the gate accused
# the mover of breaking a contract about COLUMNS - which is the class "a hardcoded expectation
# certifies the drift it exists to catch". It fired for real on 2026-08-27 (split a
# validator ~70 lines up; nothing about either INSERT changed). Two numbers are pinned instead,
# and they are the ones the lote is actually about: HOW MANY COLUMNS each writer names.
for k in ("line_grants_insert", "fragments_insert"):
    if data.get(k) != "derived":
        raise SystemExit(
            "%s must stay \"derived\": the statement is found by searching db.ts, "
            "never by a stored line number" % k
        )
if data.get("line_grants_columns") != 8:
    raise SystemExit("line_grants_columns must stay 8")
if data.get("fragments_columns") != 7:
    raise SystemExit("fragments_columns must stay 7")


def sole_statement(needle, label):
    """The whole template literal that opens with `needle`, wherever it now lives."""
    n = db.count(needle)
    if n != 1:
        raise SystemExit(
            "%s: db.ts must contain exactly one %r, found %d "
            "(two writers of one table is itself the finding)" % (label, needle, n)
        )
    i = db.index(needle)
    j = db.find("`", i)
    if j == -1:
        raise SystemExit("%s: statement is not a closed template literal" % label)
    return db[i:j], db.count("\n", 0, i) + 1


def columns_of(stmt, label, line):
    m = re.search(r"\(([^()]*)\)\s*VALUES\s*\(([^()]*)\)", stmt, re.S)
    if not m:
        raise SystemExit("%s at db.ts:%d has no column list + VALUES" % (label, line))
    cols = [c.strip() for c in m.group(1).split(",") if c.strip()]
    ph = [c.strip() for c in m.group(2).split(",") if c.strip()]
    if any(c != "?" for c in ph):
        raise SystemExit("%s at db.ts:%d binds something that is not a placeholder" % (label, line))
    if len(cols) != len(ph):
        raise SystemExit(
            "%s at db.ts:%d names %d columns and binds %d values"
            % (label, line, len(cols), len(ph))
        )
    return cols


lg, lg_i = sole_statement("INSERT INTO dodo_line_grants", "line_grants")
lg_cols = columns_of(lg, "line_grants", lg_i)
for forbidden in ("license_id", "paid_through"):
    if forbidden in lg_cols:
        raise SystemExit("line_grants INSERT grew the in-flight 0006 column %s" % forbidden)
if len(lg_cols) != data["line_grants_columns"]:
    raise SystemExit(
        "line_grants INSERT at db.ts:%d names %d columns, the lote pinned %d"
        % (lg_i, len(lg_cols), data["line_grants_columns"])
    )

fr, fr_i = sole_statement("INSERT OR IGNORE INTO dodo_cohort_fragments", "fragments")
fr_cols = columns_of(fr, "fragments", fr_i)
for forbidden in ("billing_period", "normalized_json"):
    if forbidden in fr_cols:
        raise SystemExit("fragments INSERT grew the in-flight 0006 column %s" % forbidden)
if len(fr_cols) != data["fragments_columns"]:
    raise SystemExit(
        "fragments INSERT at db.ts:%d names %d columns, the lote pinned %d"
        % (fr_i, len(fr_cols), data["fragments_columns"])
    )

names = os.listdir(mig)
if any(n.startswith("0022_") for n in names):
    raise SystemExit("migrations/ contains a 0022_ file")
if "0029_dodo_serial_purpose.sql" not in names:
    raise SystemExit("next registered slot 0029 is missing")
PY

say "check-c0022-exit1-remeasure: CLEAN — Exit 1; callers match 0016/0018; no 0022 SQL."
exit 0
