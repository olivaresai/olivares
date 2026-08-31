#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-13-scale-gates.sh — ECO-13 slice 1. Seven SCALE-* HOLDs.
# Sales lane stays closed. Three answers: 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-13-scale-gates: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-13-scale-gates: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO13_JSON:-design/eco-13-scale-gates.json}"
HOLD="${OLIVARES_ECO13_HOLD:-design/HOLD-SCALE-GATES-2026-08-19.md}"
CANON="${OLIVARES_ECO13_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$HOLD" ] || cannot "missing $HOLD"
[ -f "$CANON" ] || cannot "missing $CANON"
command -v python3 >/dev/null 2>&1 || cannot "python3 missing"

python3 - "$JSON" "$CANON" "$HOLD" <<'PY' || fail "JSON/canon/HOLD failed the ECO-13 contract"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
canon = open(sys.argv[2], encoding="utf-8").read()
hold = open(sys.argv[3], encoding="utf-8").read()
want = [
    "SCALE-REPORTING",
    "SCALE-PQCPOSTURE",
    "SCALE-ONBOARDING",
    "SCALE-COMPUTER-USE-GATE",
    "SCALE-RENDER-INSPECTOR",
    "SCALE-CREDENTIAL-MINTER",
    "SCALE-LOGIN-ENFORCEMENT",
]
if data.get("implemented") is not False:
    raise SystemExit("implemented must be false")
if data.get("sales_lane_opened") is not False:
    raise SystemExit("sales_lane_opened must be false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s is %r, want UNKNOWN" % (k, data.get(k)))
rows = data.get("gates") or []
got = [r.get("id") for r in rows]
if got != want:
    raise SystemExit("gate ids %s, want %s" % (got, want))
for r in rows:
    if r.get("status") != "HOLD":
        raise SystemExit("%s status %s, want HOLD" % (r.get("id"), r.get("status")))
    ev = r.get("evidence") or ""
    if ev not in canon:
        raise SystemExit("%s evidence %r missing from canon" % (r.get("id"), ev))
    if "| %s |" % r["id"] not in hold and not re.search(r"^\| %s \|" % r["id"], hold, flags=re.M):
        # HOLD tables use the id in a cell
        if r["id"] not in hold:
            raise SystemExit("HOLD doc lost %s" % r["id"])
for g in want:
    if g not in canon:
        raise SystemExit("canon lost %s" % g)
if "SCALE-ALL-HOLD-GATES" not in canon:
    raise SystemExit("canon lost SCALE-ALL-HOLD-GATES")
# cloud-scale-monthly must stay closed
m = re.search(r"cloud-scale-monthly:\s*\n\s*state:\s*(\S+)", canon)
if not m or m.group(1) != "closed":
    raise SystemExit("cloud-scale-monthly state is %s, want closed" % (m.group(1) if m else "missing"))
print("json-ok", len(rows))
PY

grep -q 'HOLD' "$HOLD" || fail "$HOLD lost HOLD"
grep -q 'NO ABIERTO' "$HOLD" || fail "$HOLD lost NO ABIERTO"
grep -q 'NO IMPLEMENTADO' "$HOLD" || fail "$HOLD lost NO IMPLEMENTADO"
if grep -qiE 'abrimos cloud-scale|sales_lane opened|implemented the seven' "$HOLD"; then
	fail "$HOLD claims an opening this lote does not have"
fi

say "check-eco-13-scale-gates: CLEAN — seven SCALE-* HOLDs; cloud-scale-monthly closed; none implemented."
exit 0
