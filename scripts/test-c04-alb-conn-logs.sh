#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Battery for check-c04-alb-conn-logs.sh. Both firing directions.

set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="$ROOT/scripts/check-c04-alb-conn-logs.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "$_tmp_base"
TMP="$(mktemp -d "$_tmp_base/c04cl.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT
pass=0
fail=0
ok() { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

stage() {
	rm -rf "$TMP/tree"
	mkdir -p "$TMP/tree/scripts" "$TMP/tree/design" \
		"$TMP/tree/deploy/aws/modules/ingress" \
		"$TMP/tree/deploy/aws/modules/data"
	cp "$CHECK" "$TMP/tree/scripts/"
	chmod +x "$TMP/tree/scripts/check-c04-alb-conn-logs.sh"
	cp "$ROOT/design/c04-alb-conn-logs.json" "$TMP/tree/design/"
	cp "$ROOT/design/C04-ALB-CONN-LOGS-2026-08-20.md" "$TMP/tree/design/"
	cp "$ROOT/deploy/aws/modules/ingress/main.tf" \
		"$TMP/tree/deploy/aws/modules/ingress/"
	cp "$ROOT/deploy/aws/modules/data/main.tf" \
		"$TMP/tree/deploy/aws/modules/data/"
	cp "$ROOT/deploy/aws/main.tf" "$TMP/tree/deploy/aws/"
}

run() {
	local rc=0
	OLIVARES_ROOT="$TMP/tree" \
		bash "$TMP/tree/scripts/check-c04-alb-conn-logs.sh" \
		>"$TMP/out" 2>"$TMP/err" || rc=$?
	echo "$rc" >"$TMP/rc"
	return 0
}

stage
run
if [ "$(cat "$TMP/rc")" = 0 ]; then
	ok "no-fire: live connection-log pin is CLEAN"
else
	bad "live should be CLEAN ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
text = p.read_text()
start = text.find("  connection_logs {")
if start < 0:
    raise SystemExit("connection_logs missing to strip")
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
	ok "firing: dropped connection_logs is FAIL"
else
	bad "dropped connection_logs should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/ingress/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    'prefix  = "alb-conn"',
    'prefix  = "AWSLogs"',
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
	ok "firing: user prefix AWSLogs is FAIL"
else
	bad "AWSLogs prefix should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    "connection_logs_bucket = module.data.alb_conn_bucket_id",
    "connection_logs_bucket = module.data.plane_bucket_id",
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
	ok "firing: plane-bucket destination is FAIL"
else
	bad "plane dest should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/deploy/aws/modules/data/main.tf" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
_old = p.read_text()
_new = _old.replace(
    "logdelivery.elasticloadbalancing.amazonaws.com",
    "data.aws_elb_service_account.current.arn",
    1,
)
if _new == _old:
    raise SystemExit(
        "MUTANTE NO INYECTADO: el texto buscado no esta en el arbol. "
        "Un mutante que no se aplica no prueba nada, y sin esta linea "
        "el test lo reporta como 'el gate no lo caza'."
    )
p.write_text(_new)
# Keep the identifier in the data module so the check sees it.
import re

# Inyecta el principal HEREDADO DENTRO de la politica de connection logs, que es
# el sujeto del gate. Antes lo anadia al final del fichero: eso valia mientras el
# gate miraba el fichero entero, y dejo de representar la regresion cuando se
# acoto a su bloque -- los ACCESS logs de #1225 usan esa cuenta legitimamente y
# el propio design dice que este lote no los reapila.
_t = p.read_text()
_m = re.search(r'resource\s+"aws_s3_bucket_policy"\s+"alb_conn"\s*\{', _t)
if not _m:
    raise SystemExit("MUTANTE NO INYECTADO: no encuentro aws_s3_bucket_policy.alb_conn")
_i, _d = _m.end(), 1
while _i < len(_t) and _d:
    if _t[_i] == "{":
        _d += 1
    elif _t[_i] == "}":
        _d -= 1
    _i += 1
_blk = _t[_m.start():_i]
_ancla = 'Service = "logdelivery.elasticloadbalancing.amazonaws.com"'
if _ancla not in _blk:
    raise SystemExit("MUTANTE NO INYECTADO: no encuentro el principal de log-delivery")
_mut = _blk.replace(
    _ancla,
    _ancla + "\n          AWS     = data.aws_elb_service_account.current.arn",
    1,
)
p.write_text(_t[:_m.start()] + _mut + _t[_i:])
PY
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: legacy ELB account principal is FAIL"
else
	bad "legacy principal should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
python3 - "$TMP/tree/design/c04-alb-conn-logs.json" <<'PY'
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
python3 - "$TMP/tree/design/c04-alb-conn-logs.json" <<'PY'
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
echo 'estate applied' >>"$TMP/tree/design/C04-ALB-CONN-LOGS-2026-08-20.md"
run
if [ "$(cat "$TMP/rc")" = 1 ]; then
	ok "firing: doc claims apply is FAIL"
else
	bad "apply claim should FAIL 1 ($(cat "$TMP/rc") $(cat "$TMP/err"))"
fi

stage
rm -f "$TMP/tree/design/c04-alb-conn-logs.json"
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

echo "check-c04-alb-conn-logs selftest: $pass passed, $fail failed"
if [[ "$fail" -ne 0 ]]; then exit 1; fi
exit 0
