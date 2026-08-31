#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-ecr-lifecycle-rds-logs.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-ecr-lifecycle-rds-logs.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04lc.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" \
		"$TMP/tree/deploy/aws/modules/compute" \
		"$TMP/tree/deploy/aws/modules/data" \
		"$TMP/tree/design"
	cp "$CHECK" "$TMP/tree/scripts/check-c04-ecr-lifecycle-rds-logs.sh"
	chmod +x "$TMP/tree/scripts/check-c04-ecr-lifecycle-rds-logs.sh"
	cp "$ROOT/deploy/aws/modules/compute/main.tf" \
		"$TMP/tree/deploy/aws/modules/compute/main.tf"
	cp "$ROOT/deploy/aws/modules/data/main.tf" \
		"$TMP/tree/deploy/aws/modules/data/main.tf"
	cp "$ROOT/design/C04-ECR-LIFECYCLE-RDS-LOGS-2026-08-20.md" \
		"$TMP/tree/design/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" bash "$TMP/tree/scripts/check-c04-ecr-lifecycle-rds-logs.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live pins are CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
start = s.find('resource "aws_ecr_lifecycle_policy"')
if start < 0:
    raise SystemExit('lifecycle missing')
end = s.find('\nresource "', start + 1)
p.write_text(s[:start] + (s[end+1:] if end >= 0 else ''))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing ECR lifecycle is FAIL"
else
	bad "missing lifecycle should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/tagStatus   = "untagged"/tagStatus   = "tagged"/' \
	"$TMP/tree/deploy/aws/modules/compute/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: untagged expire dropped is FAIL"
else
	bad "tagged-only lifecycle should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/health_check_grace_period_seconds/d' \
	"$TMP/tree/deploy/aws/modules/compute/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing ECS grace is FAIL"
else
	bad "missing grace should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i '/enabled_cloudwatch_logs_exports/d' \
	"$TMP/tree/deploy/aws/modules/data/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: missing RDS log export is FAIL"
else
	bad "missing RDS exports should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
sed -i 's/skip_final_snapshot         = false/skip_final_snapshot         = true/' \
	"$TMP/tree/deploy/aws/modules/data/main.tf"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: skip_final_snapshot true is FAIL"
else
	bad "snapshot skip should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C04-ECR-LIFECYCLE-RDS-LOGS-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing prep doc is COULD NOT LOOK"
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

echo "check-c04-ecr-lifecycle-rds-logs selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
