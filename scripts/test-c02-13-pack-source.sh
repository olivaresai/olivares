#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-13-pack-source.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-13-pack-source.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0213.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

good_json() {
	cat >"$TMP/tree/commercial/pack-composition.json" <<'EOF'
{
  "base": {"code": "biz", "product_id": "self_hosted.business"},
  "enterprise": {"code": "ent", "product_id": "self_hosted.enterprise"},
  "addons": [
    {"code": "airs", "addon": "ai-runtime-security", "product_id": "self_hosted.business.addons.ai-runtime-security"},
    {"code": "cp", "addon": "compliance-packs", "product_id": "self_hosted.business.addons.compliance-packs"},
    {"code": "ids", "addon": "identity-scale", "product_id": "self_hosted.business.addons.identity-scale"},
    {"code": "reg", "addon": "regulated", "product_id": "self_hosted.business.addons.regulated"}
  ]
}
EOF
}

good_sets() {
	mkdir -p "$TMP/tree/commercial/license-worker/src/download"
	cat >"$TMP/tree/commercial/license-worker/src/download/sets.ts" <<'EOF'
export const ADDON_CODES = ["airs", "cp", "ids", "reg"] as const;
export const ALLOWED_SET_SLUGS: ReadonlySet<string> = new Set([
  "biz",
  "biz+airs",
  "biz+cp",
  "biz+ids",
  "biz+reg",
  "biz+airs+cp",
  "biz+airs+ids",
  "biz+airs+reg",
  "biz+cp+ids",
  "biz+cp+reg",
  "biz+ids+reg",
  "biz+airs+cp+ids",
  "biz+airs+cp+reg",
  "biz+airs+ids+reg",
  "biz+cp+ids+reg",
  "biz+airs+cp+ids+reg",
  "ent",
]);
  ["self_hosted.business.addons.regulated", "reg"],
  ["self_hosted.business.addons.ai-runtime-security", "airs"],
  ["self_hosted.business.addons.compliance-packs", "cp"],
  ["self_hosted.business.addons.identity-scale", "ids"],
EOF
}

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" "$TMP/tree/commercial"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-13-pack-source.sh"
	chmod +x "$TMP/tree/scripts/check-c02-13-pack-source.sh"
	good_json
	good_sets
	mkdir -p "$TMP/tree/scripts"
	cat >"$TMP/tree/scripts/addon-sets.sh" <<'EOF'
CODES = {
    "regulated": "reg",
    "ai-runtime-security": "airs",
    "compliance-packs": "cp",
    "identity-scale": "ids",
}
EOF
	cat >"$TMP/tree/commercial/module-slug-package.json" <<'EOF'
{"entries":[{"slug":"a","package":"enterprise/a"},{"slug":"b","package":"enterprise/b"},{"slug":"c","package":"enterprise/c"},{"slug":"d","package":"enterprise/d"},{"slug":"e","package":"enterprise/e"},{"slug":"f","package":"enterprise/f"},{"slug":"g","package":"enterprise/g"},{"slug":"h","package":"enterprise/h"},{"slug":"i","package":"enterprise/i"},{"slug":"j","package":"enterprise/j"},{"slug":"k","package":"enterprise/k"},{"slug":"l","package":"enterprise/l"},{"slug":"m","package":"enterprise/m"},{"slug":"n","package":"enterprise/n"},{"slug":"o","package":"enterprise/o"},{"slug":"p","package":"enterprise/p"},{"slug":"q","package":"enterprise/q"},{"slug":"r","package":"enterprise/r"},{"slug":"s","package":"enterprise/s"},{"slug":"t","package":"enterprise/t"}]}
EOF
	cat >"$TMP/tree/design/C02-13-PACK-SOURCE-2026-08-19.md" <<'EOF'
C02-13 slice 1. One source.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-13-pack-source.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: JSON, sets.ts and addon-sets agree is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/pack-composition.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["addons"] = [a for a in d["addons"] if a["code"] != "reg"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping an addon from the source is FAIL"
else
	bad "dropped addon should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/"ent",/d' "$TMP/tree/commercial/license-worker/src/download/sets.ts"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: sets.ts missing ent is FAIL"
else
	bad "missing ent should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/identity-scale/d' "$TMP/tree/scripts/addon-sets.sh"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: addon-sets.sh missing a code is FAIL"
else
	bad "addon-sets drift should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/module-slug-package.json" <<'PY'
import json, sys
json.dump({"entries": [{"slug": "only", "package": "x"}]}, open(sys.argv[1], "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: truncated C13-02 map is FAIL"
else
	bad "short C13 map should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/commercial/pack-composition.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing composition JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire after a firing case still CLEAN"
else
	bad "second untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo
echo "test-c02-13-pack-source: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
