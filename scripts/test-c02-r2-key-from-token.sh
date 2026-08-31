#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-r2-key-from-token.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-r2-key-from-token.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c02key.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src/download" \
		"$TMP/tree/commercial/license-worker/test"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-r2-key-from-token.sh"
	chmod +x "$TMP/tree/scripts/check-c02-r2-key-from-token.sh"
	cp "$ROOT/design/c02-r2-key-from-token.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C02-R2-KEY-FROM-TOKEN-2026-08-19.md" <<'EOF'
delivery NOT CLOSED. Binary key is four-arg grants.set.
EOF
	cat >"$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'EOF'
export function artifactKey(version: string, os: string, arch: string, set: string): string {
  return `enterprise/${version}/${set}/${version}_${os}_${arch}.tar.gz`;
}
EOF
	cat >"$TMP/tree/commercial/license-worker/src/download/gate.ts" <<'EOF'
if (url.searchParams.has("variant")) {
  return text("Bad Request: variant is not a binary download query", 400);
}
if (url.searchParams.has("set")) {
  return text("Bad Request: set is the manifest axis, not a binary key", 400);
}
EOF
	cat >"$TMP/tree/commercial/license-worker/test/download.test.ts" <<'EOF'
test("engine-shaped query streams the set-keyed artifact from live grants", async () => {});
test("binary path refuses a client-supplied set (manifest axis, not a key)", async () => {
  assert.match(await res.text(), /set is the manifest axis/);
});
test("binary path refuses a client-supplied variant (engine does not send it)", async () => {
  assert.match(await res.text(), /variant is not a binary download query/);
});
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-r2-key-from-token.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: remasured 4-arg grants.set key is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-r2-key-from-token.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["binary_key_includes_set"] = False
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: denying set-in-key is FAIL"
else
	bad "set-in-key false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
import sys
open(sys.argv[1], "w", encoding="utf-8").write(
    'export function artifactKey(version: string, os: string, arch: string): string {\n'
    '  return `enterprise/${version}/${version}_${os}_${arch}.tar.gz`;\n'
    '}\n'
)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: three-arg unscoped key is FAIL"
else
	bad "three-arg key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/gate.ts" <<'PY'
import sys
open(sys.argv[1], "w", encoding="utf-8").write("// no refusals\n")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping the binary refusals is FAIL"
else
	bad "dropped refusals should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-r2-key-from-token.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["delivery_404_closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming delivery closed is FAIL"
else
	bad "delivery closed should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-r2-key-from-token.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["producer_on_overlay_main"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming overlay producer on main is FAIL"
else
	bad "producer on overlay main should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c02-r2-key-from-token.json"
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
echo "test-c02-r2-key-from-token: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
