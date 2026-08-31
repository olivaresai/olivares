#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-s3-access-logging.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-s3-access-logging.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04sl.XXXXXX")"
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
	chmod +x "$TMP/tree/scripts/check-c04-s3-access-logging.sh"
	cp "$ROOT/design/c04-s3-access-logging.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-S3-ACCESS-LOGGING-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/data/main.tf" \
		"$TMP/tree/deploy/aws/modules/data/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-s3-access-logging.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live S3 logging pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text()
start = text.find('resource "aws_s3_bucket_logging" "plane"')
if start < 0:
    raise SystemExit("logging resource missing to strip")
i = text.find("{", start)
j = i + 1
depth = 1
while j < len(text) and depth:
    if text[j] == "{":
        depth += 1
    elif text[j] == "}":
        depth -= 1
    j += 1
p.write_text(text[:start] + text[j:])
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped bucket_logging is FAIL"
else
	bad "dropped logging should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    "target_bucket = aws_s3_bucket.plane_logs.id",
    "target_bucket = aws_s3_bucket.plane.id",
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
	ok "firing: self-target logging is FAIL"
else
	bad "self-target should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    "logging.s3.amazonaws.com",
    "s3.amazonaws.com",
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
	ok "firing: wrong delivery principal is FAIL"
else
	bad "wrong principal should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-s3-access-logging.json" <<'PY'
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
python3 - "$TMP/tree/design/c04-s3-access-logging.json" <<'PY'
import json, sys
from pathlib import Path
p = Path(sys.argv[1])
d = json.loads(p.read_text())
d["u_f"] = "0"
p.write_text(json.dumps(d))
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: filled U_f is FAIL"
else
	bad "U_f fill should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
echo 'estate applied' >>"$TMP/tree/design/C04-S3-ACCESS-LOGGING-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c04-s3-access-logging.json"
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

echo "check-c04-s3-access-logging selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
