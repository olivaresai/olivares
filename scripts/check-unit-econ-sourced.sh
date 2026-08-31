#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-unit-econ-sourced.sh — ECO-05.
# Sourced +/− figures may be numeric. A price may not. U_f / U_d may be
# numeric only with an account-panel source. Public marketing is not that.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-unit-econ-sourced: FAIL — $*" >&2; exit 1; }
cannot() { say "check-unit-econ-sourced: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
LEDGER=design/unit-econ-sourced.json
CANON=design/PRICING-CANON.md
SNAP=design/UNIT-ECONOMICS-SOURCED-2026-08-18.md
[ -r "$LEDGER" ] || cannot "missing $LEDGER"
[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$SNAP" ] || cannot "missing $SNAP"

grep -q 'refund_fee_adder: UNKNOWN' "$CANON" || fail "canon lost refund_fee_adder: UNKNOWN"
grep -q 'dispute_fee_adder: UNKNOWN' "$CANON" || fail "canon lost dispute_fee_adder: UNKNOWN"
# Positive control: the snapshot still names the live public headline.
grep -q '4% + 40¢' "$SNAP" || fail "snapshot lost the remasured 4% + 40¢ headline"
grep -q '4% + 15¢' "$SNAP" || fail "snapshot lost the remasured India INR 4% + 15¢"
# Negative control: the snapshot must not present a signed price.
if grep -qiE 'price.*(signed|final|\$[0-9]+/mo)|precio firmado' "$SNAP"; then
  fail "snapshot reads as a signed price — this lane does not decide price"
fi

python3 - "$LEDGER" <<'PY' || exit $?
import json, sys
from decimal import Decimal, InvalidOperation

path = sys.argv[1]
def fail(msg):
    print(f"check-unit-econ-sourced: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)
def cannot(msg):
    print(f"check-unit-econ-sourced: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    doc = json.loads(open(path, encoding="utf-8").read())
except Exception as e:
    cannot(f"ledger is not JSON: {e}")

if doc.get("schema") != "olivares.unit-econ.sourced.v1":
    fail(f"unknown schema {doc.get('schema')!r}")

price = doc.get("price")
if not isinstance(price, dict):
    fail("price object missing")
if price.get("status") != "unsigned":
    fail(f"price.status is {price.get('status')!r}, must stay unsigned")
if price.get("value") not in (None, "",):
    fail(f"price.value is {price.get('value')!r} — this lane does not sign a price")

figures = doc.get("figures")
if not isinstance(figures, list) or not figures:
    cannot("ledger has no figures")

by_id = {}
for fig in figures:
    if not isinstance(fig, dict) or not fig.get("id"):
        fail("figure missing id")
    if fig["id"] in by_id:
        fail(f"duplicate figure id {fig['id']}")
    by_id[fig["id"]] = fig
    sign = fig.get("sign")
    if sign not in ("+", "-"):
        fail(f"{fig['id']}: sign must be + or −, got {sign!r}")
    val = fig.get("value")
    status = fig.get("status")
    source = fig.get("source")
    kind = fig.get("source_kind")
    closes = fig.get("closes")
    if val not in (None, ""):
        try:
            Decimal(str(val))
        except InvalidOperation:
            fail(f"{fig['id']}: value {val!r} is not a number")
        if not source:
            fail(f"{fig['id']}: numeric value without a source")
        if kind not in ("public_pricing", "msa", "account_panel_or_statement", "canon_decided"):
            fail(f"{fig['id']}: numeric value with unknown source_kind {kind!r}")
        if closes in ("U_f", "U_d") and kind != "account_panel_or_statement":
            fail(f"{fig['id']}: {closes} filled from {kind} — only an account panel may close it")
        if status == "UNKNOWN":
            fail(f"{fig['id']}: status UNKNOWN but value is {val!r}")
    else:
        if closes in ("U_f", "U_d") and status != "UNKNOWN":
            fail(f"{fig['id']}: empty {closes} must stay UNKNOWN until the panel")

required = {
    "processing_us_percent": "0.04",
    "processing_us_fixed_usd": "0.40",
    "processing_in_inr_percent": "0.04",
    "processing_in_inr_fixed_usd": "0.15",
    "byop_percent": "0.005",
    "subscription_adder_percent": "0.005",
    "international_card_percent": "0.015",
    "recovery_percent": "0.05",
    "swift_payout_usd": "25.00",
    "payout_below_1000_usd": "5.00",
}
for rid, expect in required.items():
    fig = by_id.get(rid)
    if fig is None:
        fail(f"required sourced + figure {rid} is missing")
    if fig.get("sign") != "+":
        fail(f"{rid} must be a + cost")
    try:
        got = Decimal(str(fig.get("value")))
        want = Decimal(expect)
    except InvalidOperation:
        fail(f"{rid} value {fig.get('value')!r} is not numeric")
    if got != want:
        fail(f"{rid} = {got} want {want} (public Dodo pricing / canon)")
    if not fig.get("source"):
        fail(f"{rid} lost its source")

for uid in ("refund_fee_adder", "dispute_fee_adder"):
    fig = by_id.get(uid)
    if fig is None:
        fail(f"{uid} row missing")
    if fig.get("value") not in (None, "") or fig.get("status") != "UNKNOWN":
        if fig.get("source_kind") != "account_panel_or_statement":
            fail(f"{uid} filled without an account-panel source")

conflict = by_id.get("public_refund_fee_usd")
if conflict is None:
    fail("public_refund_fee_usd row missing (the $1 that must not become U_f)")
if conflict.get("status") != "sourced_conflicts":
    fail("public $1 refund fee is not marked sourced_conflicts — copying it into U_f is the lie")
if Decimal(str(conflict.get("value"))) != Decimal("1.00"):
    fail("public refund fee drifted from the fetched $1")

neg = [f for f in figures if f.get("sign") == "-"]
if not neg:
    fail("ledger has no − figure (the fee-return side of DODO-4 must stay named)")
if all(f.get("value") not in (None, "") for f in neg):
    # A filled − without a panel source is the same lie as filling U_f.
    for f in neg:
        if f.get("source_kind") != "account_panel_or_statement" and f.get("closes"):
            fail(f"{f['id']}: filled − closer without an account-panel source")

print("check-unit-econ-sourced: CLEAN — sourced +/− present; price unsigned; U_f/U_d UNKNOWN.")
PY
