#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-alb-http2.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-alb-http2.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04h2.XXXXXX")"
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
	chmod +x "$TMP/tree/scripts/check-c04-alb-http2.sh"
	cp "$ROOT/design/c04-alb-http2.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-ALB-HTTP2-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/ingress/main.tf" \
		"$TMP/tree/deploy/aws/modules/ingress/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-alb-http2.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live HTTP/2 pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
import re


def _alb(t):
    m = re.search(r'resource\s+"aws_lb"\s+"alb"\s*\{', t)
    if not m:
        raise SystemExit("MUTANTE NO INYECTADO: no encuentro aws_lb.alb")
    i, d = m.end(), 1
    while i < len(t) and d:
        if t[i] == "{":
            d += 1
        elif t[i] == "}":
            d -= 1
        i += 1
    return m.start(), i


def _mutar_attr(p, attr, nuevo):
    """Sustituye `attr = ...` DENTRO de aws_lb.alb, sin depender de la alineacion.

    El mutante original buscaba la linea con un numero EXACTO de espacios. Cuando
    el bloque se realineo al entrar atributos de nombre mas largo, el texto dejo de
    existir, el mutante no se aplicaba, y el test lo reportaba como que el gate no
    lo cazaba. `nuevo` a None borra la linea entera.
    """
    t = p.read_text()
    a, b = _alb(t)
    blk = t[a:b]
    rx = re.compile(r'^([ \t]*)' + re.escape(attr) + r'([ \t]*)=([ \t]*).*$', re.M)
    m = rx.search(blk)
    if not m:
        raise SystemExit(
            "MUTANTE NO INYECTADO: %s no esta en el bloque aws_lb.alb" % attr
        )
    rep = "" if nuevo is None else "%s%s%s=%s%s" % (
        m.group(1), attr, m.group(2), m.group(3), nuevo,
    )
    blk2 = blk[:m.start()] + rep + blk[m.end():]
    if nuevo is None:
        blk2 = blk2.replace("\n\n", "\n", 1)
    p.write_text(t[:a] + blk2 + t[b:])


p = Path(sys.argv[1])
_mutar_attr(p, "enable_http2", "false")
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: enable_http2 false is FAIL"
else
	bad "http2 false should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
import re


def _alb(t):
    m = re.search(r'resource\s+"aws_lb"\s+"alb"\s*\{', t)
    if not m:
        raise SystemExit("MUTANTE NO INYECTADO: no encuentro aws_lb.alb")
    i, d = m.end(), 1
    while i < len(t) and d:
        if t[i] == "{":
            d += 1
        elif t[i] == "}":
            d -= 1
        i += 1
    return m.start(), i


def _mutar_attr(p, attr, nuevo):
    """Sustituye `attr = ...` DENTRO de aws_lb.alb, sin depender de la alineacion.

    El mutante original buscaba la linea con un numero EXACTO de espacios. Cuando
    el bloque se realineo al entrar atributos de nombre mas largo, el texto dejo de
    existir, el mutante no se aplicaba, y el test lo reportaba como que el gate no
    lo cazaba. `nuevo` a None borra la linea entera.
    """
    t = p.read_text()
    a, b = _alb(t)
    blk = t[a:b]
    rx = re.compile(r'^([ \t]*)' + re.escape(attr) + r'([ \t]*)=([ \t]*).*$', re.M)
    m = rx.search(blk)
    if not m:
        raise SystemExit(
            "MUTANTE NO INYECTADO: %s no esta en el bloque aws_lb.alb" % attr
        )
    rep = "" if nuevo is None else "%s%s%s=%s%s" % (
        m.group(1), attr, m.group(2), m.group(3), nuevo,
    )
    blk2 = blk[:m.start()] + rep + blk[m.end():]
    if nuevo is None:
        blk2 = blk2.replace("\n\n", "\n", 1)
    p.write_text(t[:a] + blk2 + t[b:])


p = Path(sys.argv[1])
_mutar_attr(p, "enable_http2", None)
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: dropped enable_http2 is FAIL"
else
	bad "dropped http2 should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-alb-http2.json" <<'PY'
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
echo 'estate applied' >>"$TMP/tree/design/C04-ALB-HTTP2-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c04-alb-http2.json"
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

echo "check-c04-alb-http2 selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
