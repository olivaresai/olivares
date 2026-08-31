#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-eco-02-eleven-rows.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-eco-02-eleven-rows.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/eco02.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/commercial"
	cp "$CHECK" "$TMP/tree/scripts/check-eco-02-eleven-rows.sh"
	chmod +x "$TMP/tree/scripts/check-eco-02-eleven-rows.sh"
	cp "$ROOT/design/eco-02-eleven-rows.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/ECO-02-ELEVEN-ROWS-2026-08-19.md" <<'EOF'
NOT EXECUTED. Eleven-row work list pinned. Overlay main still 20.
EOF
	cat >"$TMP/tree/design/DIFERENCIA-CANON-CATALOGO-2026-08-08.md" <<'EOF'
= 11 filas de diferencia.
once filas dejan de ser un desajuste
retrieval-scan compliancedepth doraregister iso42001 oscalingest
federation-multi-idp group-mapping durablebus connectors-conjur
credential-minter circuit-breaker
EOF
	cat >"$TMP/tree/design/PRICING-CANON.md" <<'EOF'
    incomplete_is_worklist: true
EOF
	cat >"$TMP/tree/commercial/module-slug-package.json" <<'EOF'
{
  "entries": [
    {"slug": "retrieval-scan", "package": "enterprise/retrievalscan"},
    {"slug": "compliancedepth", "package": "enterprise/compliancedepth"},
    {"slug": "doraregister", "package": "enterprise/doraregister"},
    {"slug": "iso42001", "package": "enterprise/iso42001"},
    {"slug": "oscalingest", "package": "enterprise/oscalingest"},
    {"slug": "federation-multi-idp", "package": "enterprise/federation"},
    {"slug": "group-mapping", "package": "enterprise/federation"},
    {"slug": "durablebus", "package": "enterprise/durablebus"},
    {"slug": "connectors-conjur", "package": "enterprise/connectors/conjur"}
  ]
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-eco-02-eleven-rows.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned open list is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-02-eleven-rows.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["executed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: executed true is FAIL"
else
	bad "executed true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-02-eleven-rows.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["rows"] = [r for r in d["rows"] if r["slug"] != "retrieval-scan"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drop retrieval-scan is FAIL"
else
	bad "dropped slug should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'catalog closed' >>"$TMP/tree/design/ECO-02-ELEVEN-ROWS-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming catalog closed is FAIL"
else
	bad "close claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/eco-02-eleven-rows.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["u_f"] = "0.15"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: filling U_f is FAIL"
else
	bad "U_f fill should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/eco-02-eleven-rows.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-eco-02-eleven-rows: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
