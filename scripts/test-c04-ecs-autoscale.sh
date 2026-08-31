#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-ecs-autoscale.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-ecs-autoscale.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04as.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/compute"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c04-ecs-autoscale.sh"
	cp "$ROOT/design/c04-ecs-autoscale.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-ECS-AUTOSCALE-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/compute/main.tf" \
		"$TMP/tree/deploy/aws/modules/compute/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-ecs-autoscale.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live ECS autoscale pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/compute/main.tf" <<'PY'
from pathlib import Path
import sys, re
p = Path(sys.argv[1])
t = p.read_text()
t = re.sub(
    r'\nresource "aws_appautoscaling_target" "cp" \{.*?\n\}\n',
    '\n',
    t,
    count=1,
    flags=re.S,
)
p.write_text(t)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped cp target is FAIL"
else
	bad "dropped cp target should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-ecs-autoscale.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["applied"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: applied true is FAIL"
else
	bad "applied flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'estate applied' >>"$TMP/tree/design/C04-ECS-AUTOSCALE-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/C04-ECS-AUTOSCALE-2026-08-20.md"
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

echo "check-c04-ecs-autoscale selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
