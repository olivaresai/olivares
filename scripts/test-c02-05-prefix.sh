#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c02-05-prefix.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c02-05-prefix.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c0205.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial/license-worker/src/download" \
		"$TMP/tree/core/release"
	cp "$CHECK" "$TMP/tree/scripts/check-c02-05-prefix.sh"
	chmod +x "$TMP/tree/scripts/check-c02-05-prefix.sh"
	cp "$ROOT/design/c02-05-prefix.json" "$TMP/tree/design/"
	cat >"$TMP/tree/design/C02-05-PREFIX-2026-08-19.md" <<'EOF'
PREFIX ALIGNED. delivery NOT CLOSED.
EOF
	cat >"$TMP/tree/design/BACKLOG-COMPLETITUD-2026-08-16.md" <<'EOF'
| C02-05 | Arreglar el 404 al 100 % de la puerta de descarga
EOF
	cat >"$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'EOF'
const ARTIFACT_BASENAME_PREFIX = "olivares";
export function artifactFilename(version: string, os: string, arch: string): string {
  return `${ARTIFACT_BASENAME_PREFIX}_${version}_${os}_${arch}.tar.gz`;
}
EOF
	cat >"$TMP/tree/core/release/manifest.go" <<'EOF'
return fmt.Sprintf("olivares_%s_%s_%s.tar.gz", version, goos, goarch)
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c02-05-prefix.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned prefix is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-05-prefix.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["delivery_404_closed"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: delivery_404_closed true is FAIL"
else
	bad "delivery closed should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/commercial/license-worker/src/download/artifacts.ts" <<'PY'
import sys
p = sys.argv[1]
t = open(p, encoding="utf-8").read().replace(
    'ARTIFACT_BASENAME_PREFIX = "olivares"',
    'ARTIFACT_BASENAME_PREFIX = "olivares-enterprise"',
)
open(p, "w", encoding="utf-8").write(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: wrong prefix assignment is FAIL"
else
	bad "wrong prefix should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c02-05-prefix.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["prefix_mismatch_on_hub_main"] = True
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming prefix still mismatches is FAIL"
else
	bad "prefix mismatch true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'delivery closed' >>"$TMP/tree/design/C02-05-PREFIX-2026-08-19.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming delivery closed is FAIL"
else
	bad "false close should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c02-05-prefix.json"
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
echo "test-c02-05-prefix: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
