#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-unit-econ-sourced.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/unit-econ-src.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass+1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail+1)); }

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/design" "$TMP/tree/scripts"
  cp "$ROOT/design/PRICING-CANON.md" \
     "$ROOT/design/UNIT-ECONOMICS-SOURCED-2026-08-18.md" \
     "$ROOT/design/unit-econ-sourced.json" \
     "$TMP/tree/design/"
  cp "$CHECK" "$TMP/tree/scripts/"
  chmod +x "$TMP/tree/scripts/check-unit-econ-sourced.sh"
}
run() {
  local rc=0
  OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-unit-econ-sourced.sh" >/dev/null 2>"$TMP/err" || rc=$?
  echo "$rc" >"$TMP/rc"
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "live ledger is CLEAN"; else bad "live should be CLEAN ($(cat "$TMP/err"))"; fi

# − direction: remasured headline must stay in the snapshot.
stage
sed -i 's/4% + 40¢/FEE_REDACTED/g' "$TMP/tree/design/UNIT-ECONOMICS-SOURCED-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop remasured 4%+40¢) is killed"
else bad "dropped headline stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# − direction: signing a price is forbidden.
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
d["price"]={"status":"signed","value":"99.00"}
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (signed price) is killed"; else bad "signed price stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# − direction: numeric U_f with no source.
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
for f in d["figures"]:
    if f["id"]=="refund_fee_adder":
        f["value"]="1.00"
        f["status"]="sourced"
        f["source"]=None
        f["source_kind"]=None
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (U_f filled, no source) is killed"; else bad "U_f no-source stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# − direction: numeric U_f copied from the public $1 page.
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
for f in d["figures"]:
    if f["id"]=="refund_fee_adder":
        f["value"]="1.00"
        f["status"]="sourced"
        f["source"]="https://dodopayments.com/pricing"
        f["source_kind"]="public_pricing"
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (U_f from marketing page) is killed"; else bad "U_f from marketing stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# + direction: a panel-sourced adder is admitted (we do not refuse every number).
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
for f in d["figures"]:
    if f["id"]=="refund_fee_adder":
        f["value"]="2.50"
        f["status"]="sourced"
        f["source"]="design/audits/fixture-account-statement.md"
        f["source_kind"]="account_panel_or_statement"
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
# Canon still says UNKNOWN, so this fixture is only about the ledger half.
# The check also requires the canon line. Leave canon as UNKNOWN — the
# ledger may record a panel number once one exists; today it does not.
# To isolate the admit path, keep canon UNKNOWN and only change the ledger:
# the check refuses a filled closer unless source_kind is the panel, and
# does not also demand the canon flip. That is the admit we want.
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "panel-sourced U_f is admitted"; else bad "panel-sourced U_f refused ($(cat "$TMP/err"))"; fi

# − direction: drop a required sourced + figure.
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
d["figures"]=[f for f in d["figures"] if f["id"]!="processing_us_percent"]
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop sourced + fee) is killed"; else bad "dropped + fee stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# − direction: drop the newly remasured India INR fixed fee.
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
d["figures"]=[f for f in d["figures"] if f["id"]!="processing_in_inr_fixed_usd"]
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop India 15¢) is killed"; else bad "dropped India 15¢ stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# − direction: snapshot loses the India headline while the ledger keeps it.
stage
sed -i 's/4% + 15¢/INR_REDACTED/g' "$TMP/tree/design/UNIT-ECONOMICS-SOURCED-2026-08-18.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok "mutant (drop India headline) is killed"
else bad "dropped India headline stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

# − direction: treat the public $1 as a decided U_f source.
stage
python3 - "$TMP/tree/design/unit-econ-sourced.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p,encoding="utf-8"))
for f in d["figures"]:
    if f["id"]=="public_refund_fee_usd":
        f["status"]="sourced"
json.dump(d,open(p,"w",encoding="utf-8"),indent=2)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then ok 'mutant ($1 marked decided) is killed'; else bad "public refund marked decided stayed rc=$(cat "$TMP/rc") ($(cat "$TMP/err"))"; fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then ok "no-fire: live unsigned ledger stays CLEAN"; else bad "no-fire failed ($(cat "$TMP/err"))"; fi

# cannot-look: missing ledger.
stage
rm -f "$TMP/tree/design/unit-econ-sourced.json"
run
if [ "$(cat "$TMP/rc")" = 2 ] && grep -q 'COULD NOT LOOK' "$TMP/err"; then
  ok "missing ledger is COULD NOT LOOK"
else
  bad "missing ledger should be exit 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

printf 'check-unit-econ-sourced selftest: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
