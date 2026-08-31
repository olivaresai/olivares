#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-rds-pi.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-rds-pi.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04pi.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/data"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c04-rds-pi.sh"
	cp "$ROOT/design/c04-rds-pi.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-RDS-PI-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/data/main.tf" \
		"$TMP/tree/deploy/aws/modules/data/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-rds-pi.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live Insights pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    "  performance_insights_enabled          = true\n",
    "  performance_insights_enabled          = false\n",
    1,
)
if _new == _old:
    raise SystemExit(
        "MUTANTE NO INYECTADO: el texto buscado no esta en el arbol. "
        "Un mutante que no se aplica no prueba nada, y sin esta linea "
        "el test lo reporta como 'el gate no lo caza'."
    )
p.write_text(_new)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: Insights disabled is FAIL"
else
	bad "disabled Insights should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    "  performance_insights_kms_key_id       = aws_kms_key.rds.arn\n",
    "",
    1,
)
if _new == _old:
    raise SystemExit(
        "MUTANTE NO INYECTADO: el texto buscado no esta en el arbol. "
        "Un mutante que no se aplica no prueba nada, y sin esta linea "
        "el test lo reporta como 'el gate no lo caza'."
    )
p.write_text(_new)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped Insights CMK bind is FAIL"
else
	bad "dropped Insights CMK should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text()
# plant a second customer key — F4 remainder forbids it
plant = '''
resource "aws_kms_key" "tenant" {
  enable_key_rotation = true
}
'''
p.write_text(text + plant)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: planted second KMS key is FAIL"
else
	bad "second KMS key should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-rds-pi.json" <<'PY'
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
echo 'estate applied' >>"$TMP/tree/design/C04-RDS-PI-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'F1 restore closed' >>"$TMP/tree/design/C04-RDS-PI-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims F1 closed is FAIL"
else
	bad "F1 claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c04-rds-pi.json"
run
if [ "$(cat "$TMP/rc")" = 2 ]; then
	ok "missing JSON is LOOK (2)"
else
	bad "missing JSON should LOOK 2 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: restored live stays CLEAN"
else
	bad "restored live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

echo "check-c04-rds-pi selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
