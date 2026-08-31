#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ECO-05 unique leftover unique vs check-unit-econ-sourced.sh
# (named on main, CHECK not in lint:addon-sets) and unique leftover
# unique vs #1387 / #1402. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-unit-econ-sourced-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-unit-econ-sourced-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_UECONP_JSON:-design/unit-econ-sourced-prep-2026-08-20.json}"
DOC="${OLIVARES_UECONP_DOC:-design/UNIT-ECONOMICS-SOURCED-PREP-2026-08-20.md}"
LEDGER="${OLIVARES_UECONP_LEDGER:-design/unit-econ-sourced.json}"
CANON="${OLIVARES_UECONP_CANON:-design/PRICING-CANON.md}"
SNAP="${OLIVARES_UECONP_SNAP:-design/UNIT-ECONOMICS-SOURCED-2026-08-18.md}"

for f in "$JSON" "$DOC" "$LEDGER" "$CANON" "$SNAP"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-unit-econ-sourced.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original sourced check"
grep -F -q 'Unique leftover unique vs `#1387`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1387"
grep -F -q 'Unique leftover unique vs `#1402`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1402"
grep -F -q 'Does not restack `#961`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #961"
grep -q 'This lane does not decide price' "$DOC" || fail "prepare doc lost price HOLD"
grep -q 'U_f / U_d UNKNOWN' "$DOC" || fail "prepare doc lost U_f/U_d HOLD"
if grep -qiE 'price.*(signed|final|\$[0-9]+/mo)|precio firmado|FIRMA A claimed' "$DOC"; then
  fail "prepare doc reads as a signed price"
fi

grep -q 'refund_fee_adder: UNKNOWN' "$CANON" || fail "canon lost refund_fee_adder: UNKNOWN"
grep -q 'dispute_fee_adder: UNKNOWN' "$CANON" || fail "canon lost dispute_fee_adder: UNKNOWN"
grep -q '4% + 40¢' "$SNAP" || fail "snapshot lost the remasured 4% + 40¢ headline"
grep -q '4% + 15¢' "$SNAP" || fail "snapshot lost the remasured India INR 4% + 15¢"

python3 - "$JSON" "$LEDGER" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-unit-econ-sourced-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-unit-econ-sourced-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    ledger = json.load(open(sys.argv[2], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "unit-econ-sourced-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("price_unsigned") is not True:
    fail("price_unsigned must stay true")
if data.get("u_f") != "UNKNOWN":
    fail("u_f must stay UNKNOWN")
if data.get("u_d") != "UNKNOWN":
    fail("u_d must stay UNKNOWN")
if data.get("public_refund_fee_sourced_conflicts") is not True:
    fail("public_refund_fee_sourced_conflicts must stay true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")

price = ledger.get("price") or {}
if price.get("status") != "unsigned":
    fail("ledger price.status is %r, must stay unsigned" % price.get("status"))
if price.get("value") not in (None, ""):
    fail("ledger price.value is %r — this lane does not sign a price" % price.get("value"))
by_id = {}
for fig in ledger.get("figures") or []:
    by_id[fig.get("id")] = fig
for uid in ("refund_fee_adder", "dispute_fee_adder"):
    fig = by_id.get(uid) or {}
    if fig.get("value") not in (None, "") or fig.get("status") != "UNKNOWN":
        fail("%s filled without this lote" % uid)
conflict = by_id.get("public_refund_fee_usd") or {}
if conflict.get("status") != "sourced_conflicts":
    fail("public $1 refund fee is not marked sourced_conflicts")
print("json-ok")
PY

say "check-unit-econ-sourced-prep: CLEAN — sourced +/− present; price unsigned; U_f/U_d UNKNOWN."
exit 0
