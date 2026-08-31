#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-10-payout-evidence.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-10-payout-evidence.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco10.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-10-payout-evidence.sh"
	chmod +x "$TMP/tree/scripts/check-eco-10-payout-evidence.sh"
	cp "$ROOT/design/eco-10-payout-evidence.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/ECO-10-PAYOUT-EVIDENCE-HOLD-2026-08-19.md" <<'EOF'
NOT EVIDENCED. DODO-2 and DODO-3 stay open. Annual lane closed.
EOF
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
  intermediary_bank_charges: unbounded-until-account-evidence
    DODO-2:  { class: C, subject: payout-route-currency-fees-and-intermediary-maximum }
    DODO-3:  { class: C, subject: threshold-scope-and-annual-payout-allocation }
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-10-payout-evidence.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: payout HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-10-payout-evidence.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["account_evidence"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: account_evidence true is FAIL"
else
	bad "account_evidence true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-10-payout-evidence.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["dodo2"]["closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: DODO-2 closed is FAIL"
else
	bad "DODO-2 closed should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-10-payout-evidence.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["annual_lane_closed"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: annual lane opened is FAIL"
else
	bad "annual open should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'DODO-2 closed' >>"$TMP/tree/design/ECO-10-PAYOUT-EVIDENCE-HOLD-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming DODO-2 closed is FAIL"
else
	bad "DODO-2 claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/PRICING-CANON.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing canon is LOOK (2)"
else
	bad "missing canon should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-eco-10-payout-evidence: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
