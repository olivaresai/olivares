#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C05-23: grant FSM + scheduled refresh handler still absent.
# 0 CLEAN · 1 finding · 2 LOOK.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-23-grant-fsm: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-23-grant-fsm: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0523_JSON:-design/c05-23-grant-fsm.json}"
DOC="${OLIVARES_C0523_DOC:-design/C05-23-GRANT-FSM-HOLD-2026-08-20.md}"
IDX="${OLIVARES_C0523_IDX:-commercial/license-worker/src/index.ts}"
WF="${OLIVARES_C0523_WF:-commercial/license-worker/wrangler.jsonc}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$IDX" ] || cannot "missing worker entrypoint"
[ -f "$WF" ] || cannot "missing wrangler config"

grep -q 'HOLD' "$DOC" || fail "$DOC lost HOLD"
grep -q 'NO IMPLEMENTADO' "$DOC" || fail "$DOC lost NO IMPLEMENTADO"
if grep -qiE 'scheduled handler shipped|grant FSM live|FIRMA A claimed' "$DOC"; then
	fail "$DOC claims a motor this batch does not have"
fi

python3 - "$JSON" <<'PY' || fail "JSON flags drifted"
import json, re, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
# Since 2026-08-31 this field names the handler instead of using a boolean: false became
# incorrect when C05-36 added retention purge (B/90 decision). The HOLD protects
# grants from cron mutation; it does not forbid all cron handlers.
if data.get("scheduled_handler") != "retention-purge-only":
    raise SystemExit("scheduled_handler must be 'retention-purge-only'")
if data.get("grant_fsm") is not False:
    raise SystemExit("grant_fsm must stay false")
for key in ("hub", "overlay"):
    val = data.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("%s is not a 40-hex object id" % key)
PY

# Forbidding every `scheduled` handler confused the grant HOLD with a ban on cron.
# That proxy worked only until order 367, option B/90, added C05-36 retention
# purge. The resulting fleet failure came from a stale assertion, not a broken HOLD.
# Check the actual property: scheduled handlers may exist, but none may touch grants.
n_sched="$(grep -cE 'async[[:space:]]+scheduled[[:space:]]*\(' "$IDX" || true)"
if [ "${n_sched}" -gt 1 ]; then
	fail "worker entrypoint has ${n_sched} scheduled handlers; the HOLD allows exactly one (retention purge)"
fi

if [ "${n_sched}" -eq 1 ]; then
	# El CUERPO del handler, acotado a su propio tramo: desde su firma hasta el cierre del
	# objeto exportado. Mirar el fichero entero diría que «toca grants» por cualquier otra ruta.
	body="$(awk '/async[[:space:]]+scheduled[[:space:]]*\(/{f=1} f{print} f&&/^  \},?$/{exit}' "$IDX")"
	# ⛔ SIN TUBERIA HACIA grep -q: bajo `set -o pipefail` un `productor | grep -q X` devuelve **141
	#    CUANDO ACIERTA** —grep sale al primer acierto y el productor muere con SIGPIPE—, asi que el
	#    `if` toma la rama FALSA justo cuando debia tomar la verdadera. Aqui eso significaria APROBAR
	#    un cuerpo que SI menciona grant/paid_through/entitlement, o sea el defecto que este check
	#    existe para cazar. Se captura y se prueba sobre la variable, que no depende de ninguna rc.
	if grep -qiE 'grant|paid_through|entitlement' <<<"$body"; then
		fail "the scheduled handler touches grants — that is exactly what this HOLD forbids"
	fi
	# Y que sea el que decimos que es, no uno cualquiera que además no toque grants.
	# ⛔ Misma razon que arriba, y aqui la polaridad la hace peor: con la negacion, un 141 en exito
	#    convierte «SI esta la purga» en «NO esta», y el check FALLA sobre un arbol correcto.
	if ! grep -q 'purgeExpiredCustodyBodies' <<<"$body"; then
		fail "the single scheduled handler is not the retention purge the HOLD declares"
	fi
fi

# ⛔ LOS CRONS: se permite EL de retención y NINGUNO más — y hay que contarlos, no sólo
# comprobar que el bueno está. La primera versión de esta guarda sólo exigía la PRESENCIA de
# `17 3 * * *`, así que un cron ADICIONAL se colaba: la batería lo cazó (el caso «wrangler
# crons is FAIL» salía CLEAN). Comprobar que lo esperado está no es comprobar que no hay más.
bad_cron="$(python3 -c '
import json, re, sys
raw = re.sub(r"^\s*//.*$", "", open(sys.argv[1], encoding="utf-8").read(), flags=re.M)
doc = json.loads(raw)
seen = []
def walk(n):
    # CUALQUIER clave `crons`, esté donde esté — no solo bajo `triggers`. La primera version
    # solo miraba ahi, y la bateria lo cazo: un `crons` de primer nivel era INVISIBLE. Un
    # cable-trampa que solo mira un sitio no cubre el sitio siguiente que alguien use.
    if isinstance(n, dict):
        for k, v in n.items():
            if k == "crons" and isinstance(v, list):
                seen.extend(v)
            walk(v)
    elif isinstance(n, list):
        for x in n:
            walk(x)
walk(doc)
print(" ".join(sorted({c for c in seen if c != "17 3 * * *"})))
' "$WF" 2>/dev/null || echo PARSE)"
if [ "$bad_cron" = "PARSE" ]; then
	cannot "cannot parse $WF to enumerate crons"
elif [ -n "$bad_cron" ]; then
	fail "wrangler declares cron(s) that are not the declared retention purge: $bad_cron"
fi

say "check-c05-23-grant-fsm: CLEAN — ${n_sched} scheduled handler (retention purge, touches no grants); grant FSM unbuilt."
exit 0
