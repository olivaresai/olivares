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

# Use the actual deriver, not a success stub or a hand-copied test roster.
DERIVER="${OLIVARES_MODULE_CATALOG_BIN:-$TMP/deriver}"
if [ -z "${OLIVARES_MODULE_CATALOG_BIN:-}" ]; then
  ( cd "$ROOT/commercial/commerce-lint" && GOWORK=off go build -p 2 -o "$DERIVER" . )
fi
export OLIVARES_MODULE_CATALOG_BIN="$DERIVER"

stage() {
  rm -rf "$TMP/tree"
  mkdir -p "$TMP/tree/commercial/commerce-lint"
  local rel
  for rel in \
    scripts/check-c02-13-pack-source.sh \
    scripts/module-catalog-go.sh \
    scripts/lib/build-bin.sh \
    commercial/pack-composition.json \
    commercial/commerce-lint/addonsets.go \
    cmd/olivares/cmd_license.go \
    commercial/license-worker/src/download/sets.ts \
    scripts/addon-sets.sh \
    commercial/dodo-sandbox/render-sandbox-config.py \
    commercial/dodo-sandbox/render-production-config.py \
    commercial/module-slug-package.json \
    design/C02-13-PACK-SOURCE-2026-08-19.md; do
    mkdir -p "$TMP/tree/$(dirname "$rel")"
    cp "$ROOT/$rel" "$TMP/tree/$rel"
  done
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

stage
sed -i 's/"regulated"/"invented"/' "$TMP/tree/cmd/olivares/cmd_license.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: CLI pack-code drift is FAIL"
else
	bad "CLI pack-code drift should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

rc=0
( . "$TMP/tree/scripts/lib/build-bin.sh" && build_olivares_bin "$TMP/olivares" ) \
  >"$TMP/build.out" 2>"$TMP/build.err" || rc=$?
if [ "$rc" = 1 ] && [ ! -e "$TMP/olivares" ] && \
  grep -q 'pack composition drift' "$TMP/build.err" && \
  grep -q 'cmd/olivares/cmd_license.go' "$TMP/build.err"; then
  ok "real build helper refuses planted CLI drift before compilation"
else
  bad "build helper did not refuse the named CLI drift ($rc $(cat "$TMP/build.err"))"
fi

mkdir -p "$TMP/public"
rc=0
( export OLIVARES_ROOT="$TMP/public"; . "$TMP/tree/scripts/lib/build-bin.sh"; build_olivares_bin "$TMP/olivares" ) \
  >"$TMP/build.out" 2>"$TMP/build.err" || rc=$?
if [ "$rc" = 1 ] && [ ! -e "$TMP/olivares" ] && \
  grep -q 'pack composition drift' "$TMP/build.err" && \
  grep -q 'cmd/olivares/cmd_license.go' "$TMP/build.err"; then
  ok "build validates its own drifted tree despite an inherited alternate root"
else
  bad "alternate root redirected build validation ($rc $(cat "$TMP/build.err"))"
fi

echo
echo "test-c02-13-pack-source: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
