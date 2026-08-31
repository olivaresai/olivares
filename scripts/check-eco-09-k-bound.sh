#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-09-k-bound.sh — ECO-09. K stays UNKNOWN. Bytes/event do not close it.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-09-k-bound: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-09-k-bound: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO09_JSON:-design/eco-09-attack-bound.json}"
DOC="${OLIVARES_ECO09_DOC:-design/ECO-09-K-BOUND-HOLD-2026-08-19.md}"
CANON="${OLIVARES_ECO09_CANON:-design/PRICING-CANON.md}"
DRILL="${OLIVARES_ECO09_DRILL:-design/COMMERCE-FASE2-COST-DRILL-2026-08-01.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"
[ -f "$DRILL" ] || cannot "missing $DRILL"

grep -q 'NOT BOUNDED' "$DOC" || fail "$DOC lost NOT BOUNDED"
if grep -qiE 'K closed|K is [0-9]|bounded_attack_cost is [0-9]' "$DOC"; then
	fail "$DOC claims a K this lote does not have"
fi
grep -q 'bounded_attack_cost: UNKNOWN' "$CANON" || fail "canon lost bounded_attack_cost UNKNOWN"
grep -q '604,6' "$DRILL" || fail "drill lost the export bytes/event"
grep -q '541,8' "$DRILL" || fail "drill lost the read bytes/event"
grep -q '519,6' "$DRILL" || fail "drill lost the storage bytes/event"

python3 - "$JSON" <<'PY' || fail "JSON failed the ECO-09 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "eco-09-k-bound/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("k_bounded") is not False:
    raise SystemExit("k_bounded must stay false")
if data.get("bounded_attack_cost") != "UNKNOWN":
    raise SystemExit("bounded_attack_cost must stay UNKNOWN")
if data.get("dedicated_machine") != "required":
    raise SystemExit("dedicated_machine must stay required")
if data.get("cloud_lanes_closed") is not True:
    raise SystemExit("cloud_lanes_closed must stay true")
partial = data.get("partial_bytes_per_event") or {}
if partial.get("export") != 604.6:
    raise SystemExit("export bytes/event must stay 604.6")
if partial.get("read") != 541.8:
    raise SystemExit("read bytes/event must stay 541.8")
if partial.get("storage") != 519.6:
    raise SystemExit("storage bytes/event must stay 519.6")
if partial.get("closes_k") is not False:
    raise SystemExit("bytes/event must not close K")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-eco-09-k-bound: CLEAN — K UNKNOWN; bytes/event do not close it."
exit 0
