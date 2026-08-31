#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-interface-endpoints.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-interface-endpoints.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04ie.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/network"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c04-interface-endpoints.sh"
	cp "$ROOT/design/c04-interface-endpoints.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-INTERFACE-ENDPOINTS-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/network/interface-endpoints.tf" \
		"$TMP/tree/deploy/aws/modules/network/"
	cp "$ROOT/deploy/aws/modules/network/main.tf" \
		"$TMP/tree/deploy/aws/modules/network/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-interface-endpoints.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: pinned interface endpoints are CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/deploy/aws/modules/network/interface-endpoints.tf"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "firing: missing interface file is LOOK (2)"
else
	bad "missing file should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/network/interface-endpoints.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text().replace('"ecr.api",\n', "", 1))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping ecr.api is FAIL"
else
	bad "dropped ecr.api should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/network/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text()
start = text.find('resource "aws_nat_gateway"')
if start < 0:
    raise SystemExit("nat missing")
i = text.find("{", start) + 1
depth = 1
while i < len(text) and depth:
    if text[i] == "{":
        depth += 1
    elif text[i] == "}":
        depth -= 1
    i += 1
p.write_text(text[:start] + text[i:])
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropping NAT is FAIL"
else
	bad "dropped NAT should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-interface-endpoints.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["applied"] = True
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: claiming applied is FAIL"
else
	bad "applied true should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/network/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
p.write_text(p.read_text() + '\n  vpc_endpoint_type = "Interface"\n')
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: Interface leaked into main.tf is FAIL"
else
	bad "Interface in main.tf should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

if OLIVARES_ROOT="$ROOT" bash "$CHECK" >/dev/null 2>"$TMP/err"; then
	ok "no-fire: live checkout stays CLEAN"
else
	bad "no-fire live went RED ($(cat "$TMP/err"))"
fi

echo
echo "test-c04-interface-endpoints: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
