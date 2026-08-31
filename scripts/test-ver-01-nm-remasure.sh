#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-ver-01-nm-remasure.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-ver-01-nm-remasure.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/ver01.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-ver-01-nm-remasure.sh"
	chmod +x "$TMP/tree/scripts/check-ver-01-nm-remasure.sh"
	cp "$ROOT/design/ver-01-nm-remasure.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/VER-01-NM-REMEASURE-2026-08-19.md" <<'EOF'
NOT CLOSED. Overlay main not assembled. credminter stays in BASE.
EOF
	cat >"$TMP/tree/design/VER-ALC-MEDIDO-2026-08-18.md" <<'EOF'
credminter viaja en el BASE. VER-01 no lo inventa.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-ver-01-nm-remasure.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: remasure HOLD is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/ver-01-nm-remasure.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["b02_closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: b02_closed true is FAIL"
else
	bad "b02_closed true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/ver-01-nm-remasure.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["origin_main_assembled"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: origin_main_assembled true is FAIL"
else
	bad "assembled true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/ver-01-nm-remasure.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["base"]["contentfirewall"] = 26
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: BASE contentfirewall 26 is FAIL"
else
	bad "BASE leak should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'B-02 closed on overlay main' >>"$TMP/tree/design/VER-01-NM-REMEASURE-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming B-02 closed is FAIL"
else
	bad "B-02 claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ver-01-nm-remasure.json"
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
echo "test-ver-01-nm-remasure: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
