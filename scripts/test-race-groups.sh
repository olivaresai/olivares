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
if [ "$(rc)" = 0 ] && grep -q 'CLEAN' "${TMP}/out"; then ok "the live partition covers the entire workspace"
else bad "the live partition should be CLEAN (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "belong to NO group"; then ok "mutant (an entire group removed → orphan packages) is killed"
else bad "orphan packages not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "belong to NO group"; then ok "mutant (ONE uncovered package) is killed"
else bad "a single orphan package went undetected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "stale"; then ok "mutant (stale pattern) is killed"
else bad "stale pattern not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "stale"; then ok "mutant (stale exclusion) is killed"
else bad "stale exclusion not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "two groups"; then ok "mutant (pattern duplicated across two groups) is killed"
else bad "duplicate not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "cloud_task_group"; then ok "mutant (renamed cloud group) is killed"
else bad "the rename would disable task test:cloud without a failure (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (g) grupo DESCONOCIDO y grupo VACÍO son causas distintas y tienen mensajes distintos
stage; run run no-existe
if [ "$(rc)" = 1 ] && errhas "unknown group"; then ok "unknown group is reported as such"
else bad "unknown group incorrectly reported (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (h) sin especificación no se inventa un veredicto
stage; rm -f "${SPEC}"; run check
if [ "$(rc)" = 2 ] && errhas "COULD NOT LOOK"; then ok "missing specification reports COULD NOT LOOK, not a pass"
else bad "missing spec should return rc=2 (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (i) el ensamblado de la orden: -race, -count=1 y el -timeout de la especificación
stage
OLIVARES_RACE_GROUPS="${SPEC}" OLIVARES_RACE_DRYRUN=1 bash "${SUT}" run heavy-stores >"${TMP}/dry" 2>&1 || true
if grep -q -- '-race' "${TMP}/dry" && grep -q -- '-count=1' "${TMP}/dry" && grep -qE -- '-timeout [0-9]+m' "${TMP}/dry"; then
  ok "the command includes -race, -count=1 and -timeout in minutes"
else bad "incomplete command assembly: $(tail -1 "${TMP}/dry" | cut -c1-90)"; fi

# (j) y el -timeout de Go va POR DEBAJO del techo del paso, que es justamente lo
#     que mató a race-root: su reloj arranca DESPUÉS de compilar con -race.
ceil="$(python3 -c 'import json;print(json.load(open("scripts/race-groups.json"))["step_ceiling_minutes"])')"
gto="$(python3 -c 'import json;print(json.load(open("scripts/race-groups.json"))["go_timeout_minutes"])')"
if [ "${gto}" -lt "${ceil}" ]; then ok "go_timeout (${gto}m) < step ceiling (${ceil}m): leaves time to compile"
else bad "go_timeout ${gto}m leaves no margin below ceiling ${ceil}m — this is the race-root defect"; fi

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
if [ "$(rc)" = 1 ] && errhas "belong to no shard"; then ok "mutant (removed family) is killed by ORPHANS, not the count"
else bad "removed family not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "its families are stale"; then ok "mutant (shard matching no tests) is killed"
else bad "phantom shard not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (m) A family prefix in another shard must cause duplicate test ownership.
#     Construct the overlap; a valid partition need not contain redundant prefixes.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for s in d["root_shards"]:
    for f in s["families"]:
        if len(f) > 1:
            other = next(x for x in d["root_shards"] if x["name"] != s["name"])
            other["families"].append(f[:-1])
            for x in d["root_shards"]:
                x["tests_now"] = -1
            json.dump(d, open(p, "w", encoding="utf-8"))
            sys.exit(0)
raise SystemExit("no root family can form a nonempty proper prefix")
PY
run check
# Require the duplicate-ownership diagnosis, not an unrelated count failure.
if [ "$(rc)" = 1 ] && errhas "belong to TWO shards"; then ok "mutant (overlapping prefix in another root shard) rejected for duplicate ownership"
else bad "duplicate ownership passed or failed for another reason (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "leaves no headroom"; then ok "mutant (Go timeout equal to the ceiling) is killed"
else bad "equal timeouts passed (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ]; then ok "mutant (one shard takes half the root package) is killed"
else bad "the imbalance passed (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "stale"; then ok "a stale exclusion WITHOUT the marker still fails"
else bad "the marker became a wildcard (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "no longer exists"; then ok "mutant (duration bound to a removed group) is killed"
else bad "the stale measurement passed (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
if [ "$(rc)" = 1 ] && errhas "belong to no shard"; then ok "mutant (family removed from a group shard) is killed"
else bad "group orphans not detected (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (s) Construct the same prefix overlap between shards of a workspace group.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for g in d["groups"]:
    sh = g.get("shards")
    if not sh or len(sh) < 2:
        continue
    for f in sh[0]["families"]:
        if len(f) > 1:
            sh[1]["families"].append(f[:-1])
            json.dump(d, open(p, "w", encoding="utf-8"))
            sys.exit(0)
raise SystemExit("no group family can form a nonempty proper prefix")
PY
run check
if [ "$(rc)" = 1 ] && errhas "belong to TWO shards"; then ok "mutant (overlapping prefix in another group shard) rejected for duplicate ownership"
else bad "group duplicate ownership passed (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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
raise SystemExit("no group declares go_timeout_minutes: the case cannot construct its mutant")
PYT
run check
if [ "$(rc)" = 1 ] && errhas "no headroom below the step limit"; then ok "mutant (group timeout with no margin below the ceiling) is killed"
else bad "a group go_timeout without margin passed (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

# (split) what race-full races a turn at a time: the root package and the packages of a group
#     with shards, never one of a group without them; a group that loses its shards leaves it.
stage; run split
if [ "$(rc)" = 0 ] && grep -qx 'github.com/olivaresai/olivares/cmd/olivares' "${TMP}/out" \
  && grep -qx 'github.com/olivaresai/olivares/core/auth' "${TMP}/out" \
  && ! grep -q '^github.com/olivaresai/olivares/modules/health' "${TMP}/out"; then
  ok "split names the root package and the packages of groups with shards"
else bad "split (rc=$(rc): $(head -1 "${TMP}/err"))"; fi
stage
python3 - "${SPEC}" <<'PYT'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
for g in d["groups"]:
    if g["name"] == "core-hot":
        del g["shards"]
json.dump(d, open(p, "w", encoding="utf-8"))
PYT
run split
if [ "$(rc)" = 0 ] && ! grep -q '^github.com/olivaresai/olivares/core/auth$' "${TMP}/out"; then
  ok "mutant (core-hot without shards) leaves core/auth out of split"
else bad "split ignores a group's shards (rc=$(rc))"; fi

echo "race-groups selftest: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
