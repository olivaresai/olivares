#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-exec-ecr-encrypt.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-exec-ecr-encrypt.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04exec.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/deploy/aws/modules/compute" \
		"$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-exec-ecr-encrypt.sh"
	chmod +x "$TMP/tree/scripts/check-c04-exec-ecr-encrypt.sh"
	cat >"$TMP/tree/deploy/aws/modules/compute/main.tf" <<'EOF'
resource "aws_ecr_repository" "cp" {
  encryption_configuration {
    encryption_type = "AES256"
  }
}
resource "aws_ecs_service" "cp" {
  enable_execute_command = false
}
resource "aws_ecs_service" "engine" {
  enable_execute_command = false
}
EOF
	cat >"$TMP/tree/design/C04-EXEC-ECR-ENCRYPT-2026-08-20.md" <<'EOF'
NO APLICADO. execute_command off. ECR AES256.
EOF
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-exec-ecr-encrypt.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pins present is CLEAN"
else
	bad "untouched tree should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
grep -v encryption_configuration "$TMP/tree/deploy/aws/modules/compute/main.tf" \
	>"$TMP/o.tmp" && mv "$TMP/o.tmp" "$TMP/tree/deploy/aws/modules/compute/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing encryption_configuration is FAIL"
else
	bad "missing encryption_configuration should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/"AES256"/"KMS"/' "$TMP/tree/deploy/aws/modules/compute/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: KMS without a customer key is FAIL"
else
	bad "KMS without a key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# Turn only the engine pin on. The control-plane stays off.
sed -i '/resource "aws_ecs_service" "engine"/,/^}/ s/enable_execute_command = false/enable_execute_command = true/' \
	"$TMP/tree/deploy/aws/modules/compute/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: engine execute_command on is FAIL"
else
	bad "engine execute_command on should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
# Drop the engine pin entirely (implicit default is not a pin).
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
text = text.replace(
    'resource "aws_ecs_service" "engine" {\n  enable_execute_command = false\n}\n',
    'resource "aws_ecs_service" "engine" {\n}\n',
)
p.write_text(text, encoding="utf-8")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: engine pin missing is FAIL"
else
	bad "missing engine pin should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'applied the estate' >>"$TMP/tree/design/C04-EXEC-ECR-ENCRYPT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claiming apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/deploy/aws/modules/compute/main.tf"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing compute is LOOK (2)"
else
	bad "missing compute should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c04-exec-ecr-encrypt: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
