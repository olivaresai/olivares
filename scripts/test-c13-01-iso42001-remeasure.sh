#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c13-01-iso42001-remeasure.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c13-01-iso42001-remeasure.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c1301r.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/commercial" \
		"$TMP/ent/enterprise/activation"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c13-01-iso42001-remeasure.sh"
	cp "$ROOT/design/c13-01-iso42001-remeasure.json" "$TMP/tree/design/"
	cp "$ROOT/design/C13-01-ISO42001-REMEASURE-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/commercial/module-slug-package.json" "$TMP/tree/commercial/"
	cat >"$TMP/ent/enterprise/activation/catalog.go" <<'EOF'
package activation
Key: "reporting"
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-c13-01-iso42001-remeasure.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live remasure is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'Key: "iso42001"' >>"$TMP/ent/enterprise/activation/catalog.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted catalog key is FAIL"
else
	bad "catalog key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c13-01-iso42001-remeasure.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["iso42001_in_catalog"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: iso42001_in_catalog true is FAIL"
else
	bad "flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'iso42001 catalog landed' >>"$TMP/tree/design/C13-01-ISO42001-REMEASURE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims landed is FAIL"
else
	bad "landed claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C13-01-ISO42001-REMEASURE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing remasure doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
unset OLIVARES_ENT_DIR || true
rc=0
OLIVARES_ROOT="$TMP/tree" \
	bash "$TMP/tree/scripts/check-c13-01-iso42001-remeasure.sh" \
	>"$TMP/out" 2>"$TMP/err" || rc=$?
if [ "$rc" = 2 ]; then
	ok "unset overlay dir is COULD NOT LOOK"
else
	bad "unset overlay dir should be 2 ($rc $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c13-01-iso42001-remeasure selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
