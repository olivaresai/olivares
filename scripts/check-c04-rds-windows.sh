#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C04 remainder: RDS backup/maintenance windows named. Unapplied.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-rds-windows: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-rds-windows: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C04WIN_JSON:-design/c04-rds-windows.json}"
DOC="${OLIVARES_C04WIN_DOC:-design/C04-RDS-WINDOWS-2026-08-20.md}"
TF="${OLIVARES_C04WIN_TF:-deploy/aws/modules/data/main.tf}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$TF" ] || cannot "missing data terraform"

grep -q 'NEVER APPLIED' "$DOC" || fail "$DOC lost NEVER APPLIED"
grep -q 'NEVER APPLIED' "$TF" || fail "data module lost NEVER APPLIED"
grep -q 'random hour' "$DOC" || fail "$DOC lost random-hour fact"
if grep -qiE 'estate applied|FIRMA A claimed|F1 restore closed' "$DOC"; then
	fail "$DOC claims an apply or restore this lote does not have"
fi

python3 - "$JSON" "$TF" <<'PY' || fail "JSON flags or terraform drifted"
import json, re, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "c04-rds-windows/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("applied") is not False:
    raise SystemExit("applied must stay false")
if data.get("backup_window") != "03:00-04:00":
    raise SystemExit("backup_window must stay 03:00-04:00")
if data.get("maintenance_window") != "sun:05:00-sun:06:00":
    raise SystemExit("maintenance_window must stay sun:05:00-sun:06:00")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)

tf = open(sys.argv[2], encoding="utf-8").read()
# ⛔ `backup_window`, NO `preferred_backup_window` — Y ESTE GATE SE CONTRADECIA A SI MISMO.
#
# Arriba, sobre el JSON de spec, ya comprueba `backup_window` y `maintenance_window` (los
# nombres correctos). Aqui abajo, sobre el terraform, exigia `preferred_*`. La spec tenia
# razon y la comprobacion del arbol no: `aws_db_instance` NO tiene esos atributos —
# preguntado al esquema del proveedor FIJADO (hashicorp/aws 5.100.0) tiene `backup_window`
# y `maintenance_window` entre sus 81, y ninguno de los dos `preferred_*`. Los `preferred_*`
# son los nombres de la API de AWS y de `aws_rds_cluster`, no de este recurso.
#
# ⇒ El gate estaba escrito para casar con el terraform, y el terraform estaba mal. Verificaba
# lo que el codigo CREIA, no lo que el proveedor acepta: un verificador construido sobre la
# misma suposicion que el codigo confirma la suposicion. Y no podia cazarlo porque
# `aws-terraform` no llegaba nunca a `tofu validate` — 0 exitos en 40 corridas.
#
# Control de que este arreglo NO afloja nada: los valores exigidos son los mismos, y el
# `backup_retention_period = 7` de debajo no se toca.
if not re.search(r'\bbackup_window\s*=\s*"03:00-04:00"', tf):
    raise SystemExit("backup_window lost 03:00-04:00")
if not re.search(r'\bmaintenance_window\s*=\s*"sun:05:00-sun:06:00"', tf):
    raise SystemExit("maintenance_window lost sun:05:00-sun:06:00")
if not re.search(r"backup_retention_period\s*=\s*7", tf):
    raise SystemExit("backup_retention_period lost 7")
PY

say "check-c04-rds-windows: CLEAN — backup/maintenance windows named; estate unapplied."
exit 0
