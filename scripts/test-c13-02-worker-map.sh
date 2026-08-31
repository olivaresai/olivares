#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-02-worker-map.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-02-worker-map.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1302w.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/commercial/license-worker/src/catalog"
	cp "$CHECK" "$TMP/tree/scripts/check-c13-02-worker-map.sh"
	chmod +x "$TMP/tree/scripts/check-c13-02-worker-map.sh"
	cat >"$TMP/tree/commercial/module-slug-package.json" <<'EOF'
{"entries":[{"slug":"iso42001","package":"enterprise/iso42001"},{"slug":"reporting","package":"enterprise/reporting"},{"slug":"a","package":"enterprise/a"},{"slug":"b","package":"enterprise/b"},{"slug":"c","package":"enterprise/c"},{"slug":"d","package":"enterprise/d"},{"slug":"e","package":"enterprise/e"},{"slug":"f","package":"enterprise/f"},{"slug":"g","package":"enterprise/g"},{"slug":"h","package":"enterprise/h"},{"slug":"i","package":"enterprise/i"},{"slug":"j","package":"enterprise/j"},{"slug":"k","package":"enterprise/k"},{"slug":"l","package":"enterprise/l"},{"slug":"m","package":"enterprise/m"},{"slug":"n","package":"enterprise/n"},{"slug":"o","package":"enterprise/o"},{"slug":"p","package":"enterprise/p"},{"slug":"q","package":"enterprise/q"},{"slug":"r","package":"enterprise/r"}]}
EOF
	cp "$TMP/tree/commercial/module-slug-package.json" \
		"$TMP/tree/commercial/license-worker/src/catalog/module-slug-package.json"
	cat >"$TMP/tree/commercial/license-worker/src/catalog/slug-package.ts" <<'EOF'
import map from "./module-slug-package.json" with { type: "json" };
export function packageForSlug(slug: string): string | null { return null; }
void map;
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c13-02-worker-map.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: identical maps is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/catalog/module-slug-package.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
d["entries"][0]["package"] = "enterprise/WRONG"
json.dump(d, open(sys.argv[1], "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: drifted Worker copy is FAIL"
else
	bad "drift should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/module-slug-package.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing source is LOOK (2)"
else
	bad "missing source should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-c13-02-worker-map: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
