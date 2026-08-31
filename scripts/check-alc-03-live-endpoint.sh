#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ALC-03 live: W-1..W-4 not run. Stays open.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-03-live-endpoint: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-03-live-endpoint: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_ALC03_JSON:-design/alc-03-live-endpoint.json}"
DOC="${OLIVARES_ALC03_DOC:-design/ALC-03-LIVE-ENDPOINT-HOLD-2026-08-20.md}"
CRIT="${OLIVARES_ALC03_CRIT:-design/ALC-03-WORM-LIVE-CRITERIOS-2026-08-18.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$CRIT" ] || cannot "missing $CRIT"

grep -q 'NOT LOOKED' "$DOC" || fail "$DOC lost NOT LOOKED"
# Las formas prohibidas viven en el JSON —que este guion ya lee y que el export NO
# publica— y no en este fichero, que SI viaja. Escribirlas aqui metia un token de
# sesion en el arbol publico; leerlas de alli no debilita nada: es el mismo patron,
# en el unico sitio donde su literal no es una fuga.
_pats="$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1],encoding="utf-8"));print("|".join(d["doc_must_not_claim"]))' "$JSON")" \
  || cannot "$JSON lost doc_must_not_claim"
[ -n "$_pats" ] || cannot "$JSON has an empty doc_must_not_claim"
if grep -qiE "$_pats" "$DOC"; then
  fail "$DOC claims a live lock this lote does not have"
fi
grep -q 'NO HE PODIDO MIRAR el live' "$CRIT" || fail "$CRIT lost the live cannot-look"

python3 - "$JSON" <<'PY' || fail "JSON failed the ALC-03 live contract"
import json, sys

data = json.load(open(sys.argv[1], encoding="utf-8"))
if data.get("schema") != "alc-03-live-endpoint/v1":
    raise SystemExit("unknown schema %r" % data.get("schema"))
if data.get("live_looked") is not False:
    raise SystemExit("live_looked must stay false")
if data.get("s248_closed") is not False:
    raise SystemExit("s248_closed must stay false")
if data.get("unit_tests_close_s248") is not False:
    raise SystemExit("unit_tests_close_s248 must stay false")
if data.get("azure_credentials") is not False:
    raise SystemExit("azure_credentials must stay false")
if data.get("gcs_credentials") is not False:
    raise SystemExit("gcs_credentials must stay false")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN" % k)
crit = data.get("criteria")
if not isinstance(crit, list):
    raise SystemExit("criteria missing")
ids = []
for row in crit:
    i = row.get("id")
    if i in ids:
        raise SystemExit("duplicate criterion %s" % i)
    ids.append(i)
    if row.get("status") != "NOT_RUN":
        raise SystemExit("%s must stay NOT_RUN" % i)
if set(ids) != {"W-1", "W-2", "W-3", "W-4"}:
    raise SystemExit("criteria must be W-1..W-4, not a substitute")
PY

say "check-alc-03-live-endpoint: CLEAN — W-1..W-4 not run; s248_closed false."
exit 0
