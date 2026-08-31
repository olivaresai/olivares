#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-eco-10-payout-evidence.sh — ECO-10. Class C payout evidence absent.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-eco-10-payout-evidence: FAIL — $*" >&2; exit 1; }
cannot() { say "check-eco-10-payout-evidence: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ECO10_JSON:-design/eco-10-payout-evidence.json}"
DOC="${OLIVARES_ECO10_DOC:-design/ECO-10-PAYOUT-EVIDENCE-HOLD-2026-08-19.md}"
CANON="${OLIVARES_ECO10_CANON:-design/PRICING-CANON.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CANON" ] || cannot "missing $CANON"

grep -q 'NOT EVIDENCED' "$DOC" || fail "$DOC lost NOT EVIDENCED"
if grep -qiE 'DODO-2 closed|DODO-3 closed|account panel read|intermediary capped' "$DOC"; then
	fail "$DOC claims account evidence this lote does not have"
fi
grep -q 'unbounded-until-account-evidence' "$CANON" || \
	fail "canon lost unbounded-until-account-evidence"
grep -q 'payout-route-currency-fees-and-intermediary-maximum' "$CANON" || \
	fail "canon lost DODO-2 subject"
grep -q 'threshold-scope-and-annual-payout-allocation' "$CANON" || \
	fail "canon lost DODO-3 subject"

python3 - "$JSON" <<'PY' || fail "JSON failed the ECO-10 contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "eco-10-payout-evidence/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("account_evidence") is not False:
    raise SystemExit("account_evidence must stay false")
if data.get("intermediary_bank_charges") != "unbounded-until-account-evidence":
    raise SystemExit("intermediary_bank_charges must stay unbounded")
if data.get("annual_lane_closed") is not True:
    raise SystemExit("annual_lane_closed must stay true")
d2 = data.get("dodo2") or {}
d3 = data.get("dodo3") or {}
if d2.get("class") != "C" or d3.get("class") != "C":
    raise SystemExit("DODO-2 and DODO-3 must stay class C")
if d2.get("closed") is not False or d3.get("closed") is not False:
    raise SystemExit("DODO-2 and DODO-3 must stay open")
if d2.get("subject") != "payout-route-currency-fees-and-intermediary-maximum":
    raise SystemExit("DODO-2 subject drifted")
if d3.get("subject") != "threshold-scope-and-annual-payout-allocation":
    raise SystemExit("DODO-3 subject drifted")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
PY

say "check-eco-10-payout-evidence: CLEAN — class C absent; annual lane closed."
exit 0
