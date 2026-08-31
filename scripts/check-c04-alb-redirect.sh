#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: ALB :80 redirects to HTTPS. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-alb-redirect: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-alb-redirect: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04RD_JSON:-design/c04-alb-redirect.json}"
DOC="${OLIVARES_C04RD_DOC:-design/C04-ALB-REDIRECT-2026-08-20.md}"
ING="${OLIVARES_C04RD_ING:-deploy/aws/modules/ingress/main.tf}"
NET="${OLIVARES_C04RD_NET:-deploy/aws/modules/network/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$ING" ] || cannot "missing ingress terraform"
[ -f "$NET" ] || cannot "missing network terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$ING" || fail "ingress module lost NEVER APPLIED"
grep -q 'HTTP_301' "$DOC" || fail "$DOC lost HTTP_301"
if grep -qiE 'estate applied|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims an apply this lote does not have"
fi

python3 - "$JSON" "$ING" "$NET" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-alb-redirect/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("http_redirect") is not True:
    raise SystemExit("http_redirect must be true")
if data.get("status_code") != "HTTP_301":
    raise SystemExit("status_code must stay HTTP_301")
if data.get("count_follows_cert") is not True:
    raise SystemExit("count_follows_cert must stay true")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

ing = open(sys.argv[2], encoding="utf-8").read()
net = open(sys.argv[3], encoding="utf-8").read()
if not re.search(r'resource\s+"aws_lb_listener"\s+"http_redirect"', ing):
    raise SystemExit("http_redirect listener missing")
if not re.search(r'status_code\s*=\s*"HTTP_301"', ing):
    raise SystemExit("status_code lost HTTP_301")
if not re.search(r'protocol\s*=\s*"HTTPS"', ing):
    raise SystemExit("redirect protocol lost HTTPS")
# ⛔ SE COMPARAN LOS DOS `count` ENTRE SÍ, NO CONTRA UN TEXTO. Esta línea contaba
# apariciones de la expresión literal `local.cert_arn == "" ? 0 : 1`, y eso **certifica la
# deriva que existe para cazar**: el día que la condición se reescriba —como pasó el
# 2026-08-29, cuando hubo que sacarla de un valor que el apply produce para que el plan
# fuese posible— el gate se pone rojo aunque la propiedad se conserve intacta, y el arreglo
# «obvio» es retorcer el código para que vuelva a decir lo que el gate espera leer.
#
# La propiedad de verdad es otra y no depende de cómo se escriba: **el redirect a :80 y el
# listener HTTPS existen bajo LA MISMA condición**, para que nunca haya un redirect
# apuntando a un listener que no está. Eso se comprueba comparándolos, y así el gate
# sobrevive a cualquier reescritura que conserve la propiedad.
def _count_of(name):
    m = re.search(r'resource\s+"aws_lb_listener"\s+"%s"\s*\{(.*?)\n\}' % re.escape(name),
                  ing, re.S)
    if not m:
        raise SystemExit("listener %s not found" % name)
    c = re.search(r"^\s*count\s*=\s*(.+?)\s*$", m.group(1), re.M)
    if not c:
        raise SystemExit("listener %s has no count" % name)
    return " ".join(c.group(1).split())

_https, _redir = _count_of("https"), _count_of("http_redirect")
if _https != _redir:
    raise SystemExit("redirect count must follow the cert the same way as HTTPS: "
                     "https has %r and http_redirect has %r" % (_https, _redir))
if not re.search(r'resource\s+"aws_vpc_security_group_ingress_rule"\s+"alb_http"', net):
    raise SystemExit("ALB :80 security-group rule missing")
if not re.search(r"from_port\s*=\s*80", net):
    raise SystemExit("ALB :80 from_port lost")
PY

say "check-c04-alb-redirect: CLEAN — HTTP:80 redirects to HTTPS; estate unapplied."
exit 0
