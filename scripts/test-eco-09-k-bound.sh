#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-09-k-bound.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-09-k-bound.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco09.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-09-k-bound.sh"
	chmod +x "$TMP/tree/scripts/check-eco-09-k-bound.sh"
	cp "$ROOT/design/eco-09-attack-bound.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/ECO-09-K-BOUND-HOLD-2026-08-19.md" <<'EOF'
NOT BOUNDED. K stays UNKNOWN. Bytes/event do not close it.
EOF
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
    bounded_attack_cost: UNKNOWN
EOF
	cat >"$TMP/tree/design/COMMERCE-FASE2-COST-DRILL-2026-08-01.md" <<'EOF'
export 604,6 · lectura 541,8 · almacenamiento 519,6 B/evento
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-09-k-bound.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: K HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-09-attack-bound.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["k_bounded"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: k_bounded true is FAIL"
else
	bad "k_bounded true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-09-attack-bound.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["bounded_attack_cost"] = 604.6
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: numeric K is FAIL"
else
	bad "numeric K should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-09-attack-bound.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["partial_bytes_per_event"]["closes_k"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: bytes/event closing K is FAIL"
else
	bad "closes_k true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'K closed' >>"$TMP/tree/design/ECO-09-K-BOUND-HOLD-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming K closed is FAIL"
else
	bad "K closed claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/COMMERCE-FASE2-COST-DRILL-2026-08-01.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing drill is LOOK (2)"
else
	bad "missing drill should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-eco-09-k-bound: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
