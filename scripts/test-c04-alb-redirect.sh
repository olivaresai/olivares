#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-alb-redirect.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-alb-redirect.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04rd.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/ingress" \
		"$TMP/tree/deploy/aws/modules/network"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c04-alb-redirect.sh"
	cp "$ROOT/design/c04-alb-redirect.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-ALB-REDIRECT-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/ingress/main.tf" \
		"$TMP/tree/deploy/aws/modules/ingress/"
	cp "$ROOT/deploy/aws/modules/network/main.tf" \
		"$TMP/tree/deploy/aws/modules/network/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-alb-redirect.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live redirect pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    '      status_code = "HTTP_301"',
    '      status_code = "HTTP_302"',
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
	ok "firing: HTTP_302 is FAIL"
else
	bad "HTTP_302 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text()
# Se le quita el `count` SOLO al redirect y se deja el de HTTPS: la propiedad es que los
# dos vayan juntos, así que quitarle el suyo a uno tiene que caer.
#
# ⛔ El ancla se deriva del fichero, NO se teclea. La versión anterior llevaba escrita la
# expresión literal `local.cert_arn == "" ? 0 : 1`, y el 2026-08-29 —cuando la condición
# tuvo que cambiar para que el plan fuese posible— el mutante dejó de aplicarse: el caso
# reportó «should FAIL 1 (0)» y acusó al gate de ciego estando sano. Un mutante que no muta
# es un falso hallazgo, y este `assert` lo convierte en un fallo ruidoso.
import re
m = re.search(r'(resource\s+"aws_lb_listener"\s+"http_redirect"\s*\{\n\s*count\s*=[^\n]*\n)', text)
assert m, "no encuentro el count del redirect: el mutante no se aplicaria"
text = text.replace(m.group(1), 'resource "aws_lb_listener" "http_redirect" {\n', 1)
p.write_text(text)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: redirect without cert count is FAIL"
else
	bad "ungated redirect should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/network/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text()
start = text.index('resource "aws_vpc_security_group_ingress_rule" "alb_http"')
i = text.index("{", start) + 1
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
	ok "firing: dropped :80 SG rule is FAIL"
else
	bad "dropped SG should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-alb-redirect.json" <<'PY'
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
echo 'estate applied' >>"$TMP/tree/design/C04-ALB-REDIRECT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c04-alb-redirect.json"
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

echo "check-c04-alb-redirect selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
