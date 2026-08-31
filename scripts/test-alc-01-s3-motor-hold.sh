#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-alc-01-s3-motor-hold.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-alc-01-s3-motor-hold.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alc01s3.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree" "$TMP/ent"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/cmd/olivares" \
		"$TMP/ent/enterprise/activation"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-alc-01-s3-motor-hold.sh"
	cp "$ROOT/design/alc-01-s3-motor-hold.json" "$TMP/tree/design/"
	cp "$ROOT/design/ALC-01-S3-MOTOR-HOLD-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/cmd/olivares/wire_noenterprise.go" "$TMP/tree/cmd/olivares/"
	cat >"$TMP/ent/enterprise/activation/catalog.go" <<'EOF'
package activation

var Catalog = []struct {
	Key string
}{
	{Key: "reporting"},
}
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" OLIVARES_ENT_DIR="$TMP/ent" \
		bash "$TMP/tree/scripts/check-alc-01-s3-motor-hold.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live S3 HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
mkdir -p "$TMP/ent/enterprise/managedscim"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted overlay package is FAIL"
else
	bad "package dir should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
printf '\n\t{Key: "managed-scim"},\n' >>"$TMP/ent/enterprise/activation/catalog.go"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted catalog key is FAIL"
else
	bad "catalog key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-01-s3-motor-hold.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["motor_implemented"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: motor_implemented true is FAIL"
else
	bad "motor flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'managed SCIM shipped' >>"$TMP/tree/design/ALC-01-S3-MOTOR-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims shipped is FAIL"
else
	bad "shipped claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ALC-01-S3-MOTOR-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
unset OLIVARES_ENT_DIR || true
rc=0
OLIVARES_ROOT="$TMP/tree" \
	bash "$TMP/tree/scripts/check-alc-01-s3-motor-hold.sh" \
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

echo "check-alc-01-s3-motor-hold selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
