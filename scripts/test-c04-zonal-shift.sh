#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-zonal-shift.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-zonal-shift.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04zs.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/ingress"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c04-zonal-shift.sh"
	cp "$ROOT/design/c04-zonal-shift.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-ZONAL-SHIFT-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/ingress/main.tf" \
		"$TMP/tree/deploy/aws/modules/ingress/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-zonal-shift.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live zonal-shift pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
import re


def _mutar_alb(p, viejo, nuevo):
    """Muta DENTRO del bloque aws_lb.alb, que es el unico que este gate vigila.

    Antes se hacia replace(..., 1) sobre el fichero entero. Desde que #1240 puso
    enable_zonal_shift tambien en aws_lb.nlb -- que aparece ANTES -- la primera
    aparicion era la del NLB, asi que el mutante tocaba un recurso ajeno: el
    fichero cambiaba, el gate salia 0 CON RAZON, y el test lo reportaba como que
    el gate no caza su mutante. El gate estaba bien; apuntaba mal el mutante.
    """
    t = p.read_text()
    m = re.search(r'resource\s+"aws_lb"\s+"alb"\s*\{', t)
    if not m:
        raise SystemExit("MUTANTE NO INYECTADO: no encuentro aws_lb.alb")
    i, depth = m.end(), 1
    while i < len(t) and depth:
        if t[i] == "{":
            depth += 1
        elif t[i] == "}":
            depth -= 1
        i += 1
    bloque = t[m.start():i]
    if viejo not in bloque:
        raise SystemExit(
            "MUTANTE NO INYECTADO: %r no esta en el bloque aws_lb.alb. "
            "Un mutante que no se aplica no prueba nada." % viejo
        )
    p.write_text(t[:m.start()] + bloque.replace(viejo, nuevo, 1) + t[i:])


p = Path(sys.argv[1])
_mutar_alb(p, "  enable_zonal_shift         = true\n", "  enable_zonal_shift         = false\n")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: enable_zonal_shift false is FAIL"
else
	bad "false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
import re


def _mutar_alb(p, viejo, nuevo):
    """Muta DENTRO del bloque aws_lb.alb, que es el unico que este gate vigila.

    Antes se hacia replace(..., 1) sobre el fichero entero. Desde que #1240 puso
    enable_zonal_shift tambien en aws_lb.nlb -- que aparece ANTES -- la primera
    aparicion era la del NLB, asi que el mutante tocaba un recurso ajeno: el
    fichero cambiaba, el gate salia 0 CON RAZON, y el test lo reportaba como que
    el gate no caza su mutante. El gate estaba bien; apuntaba mal el mutante.
    """
    t = p.read_text()
    m = re.search(r'resource\s+"aws_lb"\s+"alb"\s*\{', t)
    if not m:
        raise SystemExit("MUTANTE NO INYECTADO: no encuentro aws_lb.alb")
    i, depth = m.end(), 1
    while i < len(t) and depth:
        if t[i] == "{":
            depth += 1
        elif t[i] == "}":
            depth -= 1
        i += 1
    bloque = t[m.start():i]
    if viejo not in bloque:
        raise SystemExit(
            "MUTANTE NO INYECTADO: %r no esta en el bloque aws_lb.alb. "
            "Un mutante que no se aplica no prueba nada." % viejo
        )
    p.write_text(t[:m.start()] + bloque.replace(viejo, nuevo, 1) + t[i:])


p = Path(sys.argv[1])
_mutar_alb(p, "  enable_zonal_shift         = true\n", "")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped enable_zonal_shift is FAIL"
else
	bad "dropped flag should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-zonal-shift.json" <<'PY'
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
python3 - "$TMP/tree/design/c04-zonal-shift.json" <<'PY'
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
echo 'estate applied' >>"$TMP/tree/design/C04-ZONAL-SHIFT-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c04-zonal-shift.json"
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

echo "check-c04-zonal-shift selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
