#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# Banco de scripts/race-groups.sh. Cada caso MUTA la especificación en una copia y
# exige que el control lo mate. Un control de cobertura que nunca ha visto un
# paquete descubierto no ha demostrado que sepa verlo.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
SUT="${ROOT}/scripts/race-groups.sh"
_tmp_base="${TMPDIR:-/workspace/.olivares-tmptest}"
mkdir -p "${_tmp_base}"
TMP="$(mktemp -d "${_tmp_base}/racegroups.XXXXXX")"
trap 'rm -rf "${TMP}"' EXIT
pass=0; fail=0
ok()  { printf 'ok   %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=$((fail + 1)); }

SPEC="${TMP}/spec.json"
stage() { cp "${ROOT}/scripts/race-groups.json" "${SPEC}"; }
run() {   # $1 = subcomando(s); deja rc en $TMP/rc y stderr en $TMP/err
  local rc=0
  OLIVARES_RACE_GROUPS="${SPEC}" bash "${SUT}" "$@" >"${TMP}/out" 2>"${TMP}/err" || rc=$?
  echo "${rc}" >"${TMP}/rc"
}
rc() { cat "${TMP}/rc"; }
errhas() { grep -qF "$1" "${TMP}/err"; }

# (a) el reparto vivo está completo — si esto falla, todo lo demás miente
stage; run check
if [ "$(rc)" = 0 ] && grep -q 'CLEAN' "${TMP}/out"; then ok "el reparto vivo cubre el workspace entero"
else bad "el vivo debería ser CLEAN (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (b) ⛔ EL CASO QUE PIDE EL ENCARGO: un paquete fuera de todos los grupos es rojo.
#     Se retira el grupo de connectors entero — 178 paquetes quedan sin dueño.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["groups"] = [g for g in d["groups"] if g["name"] != "connectors"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "NO pertenecen a ningún grupo"; then ok "mutante (un grupo entero retirado → paquetes huérfanos) muere"
else bad "huérfanos no detectados (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (c) y con UN SOLO paquete descubierto, no 178: el control no puede necesitar un
#     agujero grande para verlo.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for g in d["groups"]:
    if g["name"] == "sdk-and-tools":
        # sdk/plugin es un módulo propio: sin su patrón queda UN paquete sin dueño
        g["patterns"] = [x for x in g["patterns"] if x != "github.com/olivaresai/olivares/sdk/..."]
        g["patterns"] += ["github.com/olivaresai/olivares/sdk/event/...",
                          "github.com/olivaresai/olivares/sdk/model/...",
                          "github.com/olivaresai/olivares/sdk/netbind/...",
                          "github.com/olivaresai/olivares/sdk/siemwire/...",
                          "github.com/olivaresai/olivares/sdk/plugin/...",
                          "github.com/olivaresai/olivares/sdk/scaffold/..."]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "NO pertenecen a ningún grupo"; then ok "mutante (UN paquete descubierto) muere"
else bad "un solo huérfano pasó desapercibido (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (d) un patrón que ya no casa con nada declara cobertura que no existe
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["groups"][0]["patterns"].append("github.com/olivaresai/olivares/core/ya-no-existe/...")
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "rancio"; then ok "mutante (patrón rancio) muere"
else bad "patrón rancio no detectado (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (e) y la MISMA rancidez en la lista de EXCLUSIONES: una exclusión que ya no
#     excluye nada es una excusa escrita para un problema que se fue.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["excluded"].append({"pattern": "github.com/olivaresai/olivares/no/existe/...", "reason": "prueba"})
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "rancio"; then ok "mutante (exclusión rancia) muere"
else bad "exclusión rancia no detectada (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (f) el mismo patrón en dos grupos: gana el más largo y el otro miente
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["groups"][1]["patterns"].append(d["groups"][0]["patterns"][0])
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "dos grupos"; then ok "mutante (patrón duplicado en dos grupos) muere"
else bad "duplicado no detectado (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (f-bis) renombrar un grupo NO puede desactivar `task test:cloud` en silencio:
#     el `if:` del workflow compara con cloud_task_group, y un `if:` que no casa
#     se SALTA — y saltarse se lee como verde.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for g in d["groups"]:
    if g["name"] == d["cloud_task_group"]:
        g["name"] = g["name"] + "-renombrado"
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "cloud_task_group"; then ok "mutante (grupo del cloud renombrado) muere"
else bad "el renombrado desactivaría task test:cloud sin rojo (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (g) grupo DESCONOCIDO y grupo VACÍO son causas distintas y tienen mensajes distintos
stage; run run no-existe
if [ "$(rc)" = 1 ] && errhas "grupo desconocido"; then ok "grupo desconocido se nombra como tal"
else bad "grupo desconocido mal reportado (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (h) sin especificación no se inventa un veredicto
stage; rm -f "${SPEC}"; run check
if [ "$(rc)" = 2 ] && errhas "COULD NOT LOOK"; then ok "sin especificación es COULD NOT LOOK, no verde"
else bad "spec ausente debería ser rc=2 (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (i) el ensamblado de la orden: -race, -count=1 y el -timeout de la especificación
stage
OLIVARES_RACE_GROUPS="${SPEC}" OLIVARES_RACE_DRYRUN=1 bash "${SUT}" run heavy-stores >"${TMP}/dry" 2>&1 || true
if grep -q -- '-race' "${TMP}/dry" && grep -q -- '-count=1' "${TMP}/dry" && grep -qE -- '-timeout [0-9]+m' "${TMP}/dry"; then
  ok "la orden lleva -race, -count=1 y un -timeout con minutos"
else bad "ensamblado incompleto: $(tail -1 "${TMP}/dry" | cut -c1-90)"; fi

# (j) y el -timeout de Go va POR DEBAJO del techo del paso, que es justamente lo
#     que mató a race-root: su reloj arranca DESPUÉS de compilar con -race.
ceil="$(python3 -c 'import json;print(json.load(open("scripts/race-groups.json"))["step_ceiling_minutes"])')"
gto="$(python3 -c 'import json;print(json.load(open("scripts/race-groups.json"))["go_timeout_minutes"])')"
if [ "${gto}" -lt "${ceil}" ]; then ok "go_timeout (${gto}m) < techo del paso (${ceil}m): queda margen para compilar"
else bad "go_timeout ${gto}m no deja margen bajo el techo ${ceil}m — es el defecto de race-root"; fi

# ── EL REPARTO DEL PAQUETE RAIZ (`-run`) ─────────────────────────────────────────
# Aqui el fallo silencioso es peor: un `-run` que no casa con nada SALE 0. Un turno
# con familias rancias publicaria exito sin ejecutar un test.

# (k) una familia retirada deja tests sin turno
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
s = d["root_shards"][0]
s["families"] = s["families"][1:]
s["tests_now"] = -1          # la cifra se recalcula, no se ajusta a ojo
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "no caen en ningun turno"; then ok "mutante (familia retirada) muere por HUERFANOS, no por la cifra"
else bad "familia retirada no detectada (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (l) un turno cuyas familias no existen: su -run saldria 0 sin correr nada
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["root_shards"].append({"name": "root-fantasma", "tests_now": 1,
                         "families": ["NoExisteEstaFamilia"]})
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "sus familias son rancias"; then ok "mutante (turno que no casa con ningun test) muere"
else bad "turno fantasma no detectado (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (m) ⛔ EL DE VERDAD: una familia que es PREFIJO de otra en OTRO turno hace que sus
#     tests corran DOS veces. Se midieron 53 pares al escribir esto.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
own = {f: s["name"] for s in d["root_shards"] for f in s["families"]}
# se busca un par prefijo dentro del MISMO turno y se separa: eso crea el doble
for s in d["root_shards"]:
    for f in list(s["families"]):
        for g in s["families"]:
            if g != f and g.startswith(f):
                s["families"].remove(g)
                other = [x for x in d["root_shards"] if x["name"] != s["name"]][0]
                other["families"].append(g)
                for x in d["root_shards"]:
                    x["tests_now"] = -1
                json.dump(d, open(p, "w", encoding="utf-8"))
                sys.exit(0)
raise SystemExit("no encontre un par prefijo que separar: el fixture cambio")
PY
run check
# ⛔ Y MUERE POR EL MOTIVO CORRECTO, que es la mitad que faltaba: si el control de la
# cifra fuese lo primero, este mutante moriria por «cifra rancia» y el de DOBLES no se
# habria ejecutado nunca. Se exige el mensaje.
if [ "$(rc)" = 1 ] && errhas "caen en DOS turnos"; then ok "mutante (familia prefijo separada en otro turno) muere por DOBLE, no por la cifra"
else bad "el doble conteo pasó o murió por otro motivo (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (n) el -timeout de la raiz tambien va POR DEBAJO de su techo
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["root_go_timeout_minutes"] = d["root_step_ceiling_minutes"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "no deja margen"; then ok "mutante (reloj de Go igual al techo) muere"
else bad "el empate de relojes paso (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (o) un turno que se lleva media raiz es el paso que no cabe en el techo del job. Este es
#     el control que SUSTITUYE a la igualdad de cifras, asi que tiene que morir de verdad.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
a, b = d["root_shards"][0], d["root_shards"][1]
a["families"] = sorted(set(a["families"]) | set(b["families"]))
b["families"] = ["ZzzNoExisteNadaAsi"]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ]; then ok "mutante (un turno se lleva media raiz) muere"
else bad "el desequilibrio pasó (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (p) y una exclusión rancia SIN la marca de «ausente en el publicado» sigue siendo roja:
#     la marca no puede convertirse en el camino cómodo para callar cualquier rancidez.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["excluded"].append({"pattern": "github.com/olivaresai/olivares/no/existe/...", "reason": "prueba"})
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "rancio"; then ok "una exclusión rancia SIN la marca sigue muriendo"
else bad "la marca se convirtió en comodín (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (q) una duracion guardada contra un grupo que ya no existe es la cifra con la que se
#     reequilibraria el proximo reparto: atarla a un muerto es peor que no tenerla.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d.setdefault("measured_run", {}).setdefault("minutes", {})["grupo-que-ya-no-existe"] = 12.3
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "ya no existe"; then ok "mutante (duracion atada a un grupo muerto) muere"
else bad "la medida rancia paso (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (r) los TURNOS DE GRUPO: una familia retirada deja tests sin turno
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for g in d["groups"]:
    if g.get("shards"):
        g["shards"][0]["families"] = g["shards"][0]["families"][1:]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "no caen en ningun turno"; then ok "mutante (familia retirada de un turno de grupo) muere"
else bad "huerfanos de grupo no detectados (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (s) y una familia PREFIJO separada en el otro turno: sus tests correrian DOS veces
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for g in d["groups"]:
    sh = g.get("shards")
    if not sh:
        continue
    for f in list(sh[0]["families"]):
        for h in sh[0]["families"]:
            if h != f and h.startswith(f):
                sh[0]["families"].remove(h)
                sh[1]["families"].append(h)
                json.dump(d, open(p, "w", encoding="utf-8"))
                sys.exit(0)
raise SystemExit("no encontre un par prefijo en un turno de grupo")
PY
run check
if [ "$(rc)" = 1 ] && errhas "caen en DOS turnos"; then ok "mutante (prefijo separado entre turnos de grupo) muere"
else bad "el doble conteo de grupo pasó (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (t) un grupo cuyo `go_timeout_minutes` se pasa del techo del PASO. Sin la guarda por grupo el
#     campo seria decoracion: el reloj del paso mata al job antes de que Go diga por que, y se
#     pierde el diagnostico, que es lo caro. Espejo del caso que ya cubre la raiz.
stage
python3 - "${SPEC}" <<'PYT'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
techo = d.get("step_ceiling_minutes", 45)
for g in d["groups"]:
    if g.get("go_timeout_minutes") is not None:
        g["go_timeout_minutes"] = techo          # >= techo: margen cero
        json.dump(d, open(p, "w", encoding="utf-8"))
        sys.exit(0)
raise SystemExit("ningun grupo declara go_timeout_minutes: el caso no puede fabricar su mutante")
PYT
run check
if [ "$(rc)" = 1 ] && errhas "no deja margen bajo el techo del paso"; then ok "mutante (timeout de grupo sin margen bajo el techo) muere"
else bad "un go_timeout de grupo sin margen paso (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

echo "race-groups selftest: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
