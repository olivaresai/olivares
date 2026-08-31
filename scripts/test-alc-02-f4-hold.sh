#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-alc-02-f4-hold.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-alc-02-f4-hold.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/alc02f4.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/data" \
		"$TMP/tree/deploy/aws/modules/observability" \
		"$TMP/tree/deploy/aws/modules/compute" \
		"$TMP/tree/deploy/aws/modules/secrets"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-alc-02-f4-hold.sh"
	cp "$ROOT/design/alc-02-f4-hold.json" "$TMP/tree/design/"
	cp "$ROOT/design/ALC-02-F4-HOLD-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/data/main.tf" \
		"$TMP/tree/deploy/aws/modules/data/"
	# C04 plane keys live in these modules. The fixture must carry them
	# or the census would pass on a tree that is not the live one.
	for m in observability compute secrets; do
		src="$ROOT/deploy/aws/modules/$m/main.tf"
		if [ -f "$src" ]; then
			cp "$src" "$TMP/tree/deploy/aws/modules/$m/"
		fi
	done
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-alc-02-f4-hold.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live F4 HOLD is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
cat >>"$TMP/tree/deploy/aws/modules/data/main.tf" <<'EOF'
resource "aws_kms_key" "tenant" {
  description = "per-tenant"
}
EOF
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted tenant KMS key is FAIL"
else
	bad "tenant key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/alc-02-f4-hold.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["per_tenant_cmek"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: per_tenant_cmek true is FAIL"
else
	bad "cmek flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'CMEK applied' >>"$TMP/tree/design/ALC-02-F4-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims CMEK applied is FAIL"
else
	bad "applied claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/ALC-02-F4-HOLD-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing HOLD doc is COULD NOT LOOK"
else
	bad "missing doc should be 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-alc-02-f4-hold selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
