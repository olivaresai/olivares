#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# race-groups.sh — split the race-full workspace sweep into groups that fit a job.
#
# WHY: `race-workspace` raced every workspace module in ONE step and died on the
# step ceiling at 75 min with 14 packages green and finops/governance never
# started. A sweep that cannot finish is not evidence of anything, and the
# release preflight (release.yml) requires a GREEN race-full on the tagged SHA.
#
# ⛔ THE GROUPS LIVE IN ONE PLACE — scripts/race-groups.json — and the workflow
# matrix is BUILT from it (`groups`). Writing the names in the YAML too is the
# defect this repository keeps paying for: a fact typed twice drifts in silence.
#
# Subcommands:
#   groups              JSON array of group names (for the workflow matrix)
#   packages <group>    the import paths that group owns, one per line
#   check               the union control: every test-bearing package is owned
#                       by exactly one group, no pattern is stale, no duplicates
#   run <group>         go test -race over that group's packages
#
# Assignment rule: LONGEST MATCHING PATTERN WINS. `core/...` and `core/auth/...`
# both match core/auth; the second is longer, so core/auth belongs to whoever
# declared it. That is what lets a broad group exist beside a surgical one
# without either of them listing the other's packages.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

SPEC="${OLIVARES_RACE_GROUPS:-scripts/race-groups.json}"
say() { printf 'race-groups: %s\n' "$*"; }
fail()   { printf 'race-groups: FAIL — %s\n' "$*" >&2; exit 1; }
cannot() { printf 'race-groups: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }

[ -f "${SPEC}" ] || cannot "no encuentro ${SPEC}"
[ -f go.work ]   || cannot "go.work no está en ${ROOT}"
command -v go >/dev/null 2>&1 || cannot "no hay toolchain de Go: no puedo enumerar paquetes"

# ── Enumeración: UNA sola función, la misma para `check` y para `run` ──────────
# Un `go test ./...` en un workspace sólo cubre el módulo ACTUAL (golang/go#50745),
# así que se enumera módulo a módulo. Se listan sólo los paquetes CON tests: un
# paquete sin tests no aporta cobertura de carrera y alarga la línea de comandos.
enumerate() {
  local m
  while IFS= read -r m; do
    [ -n "${m}" ] || continue
    ( cd "${m}" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... ) \
      || cannot "go list falló en ${m}"
  done < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
}

CACHE="${TMPDIR:-/tmp}/race-groups-pkgs.$$"
trap 'rm -f "${CACHE}" "${LIST:-}" "${TESTS_CACHE:-}" "${TLIST:-}"' EXIT
pkgs() {
  [ -s "${CACHE}" ] || enumerate | grep -v '^$' | sort -u > "${CACHE}"
  cat "${CACHE}"
}

# ── Los tests del paquete RAIZ, para el reparto por -run ──────────────────────────
# Se leen del FUENTE (no de un `go test -list`, que compila con -race y cuesta minutos).
# `^func TestX(` es la forma que Go reconoce como test de nivel superior.
TESTS_CACHE="${TMPDIR:-/tmp}/race-root-tests.$$"
root_tests() {
  local dir
  dir="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1],encoding="utf-8"))["root_package"])' "${SPEC}" | sed 's|github.com/olivaresai/olivares/||')"
  [ -d "${dir}" ] || cannot "no encuentro el directorio del paquete raiz: ${dir}"
  if [ ! -s "${TESTS_CACHE}" ]; then
    # ⛔ EL UNIVERSO SALE DE `go list`, NO DE UN GLOB DE `*_test.go`. Medido por
    # sobre la primera version: el glob daba 1827 y `go test -list` 1811. La diferencia
    # son QUINCE `TestE2E*` detras de una etiqueta de build —ficheros que Go NO compila
    # en esta configuracion— mas `TestMain`, que no es un test sino el arranque del
    # binario y `-run` nunca selecciona.
    #
    # Contar tests que la build EXCLUYE no es un detalle de aritmetica: inflaba
    # `tests_now`, desequilibraba el reparto con tests que no existen, y —lo peor— si
    # un turno se quedara SOLO con tests etiquetados, su `-run` casaria CERO y saldria
    # 0 pareciendo verde, que es justo el fallo que este control existe para cortar.
    #
    # `go list` aplica las MISMAS restricciones de build que `go test` y no compila
    # nada, asi que es barato. Se piden los dos conjuntos: los tests del paquete y los
    # del paquete _test externo.
    local files
    files="$(cd "${dir}" && go list -f '{{range .TestGoFiles}}{{.}}
{{end}}{{range .XTestGoFiles}}{{.}}
{{end}}' . 2>/dev/null | grep -v '^$')" || cannot "go list fallo en ${dir}"
    [ -n "${files}" ] || cannot "go list no devolvio ficheros de test en ${dir}"
    ( cd "${dir}" && printf '%s\n' "${files}" | tr '\n' '\0' | xargs -0 grep -hoE '^func Test[A-Za-z0-9_]+' ) \
      | sed 's/^func //' | grep -vx 'TestMain' | sort -u > "${TESTS_CACHE}"
  fi
  cat "${TESTS_CACHE}"
}

case "${1:-}" in
  groups)
    # Un grupo con `shards` se EXPANDE a «grupo#turno»: la matriz saca un job por turno y el
    # nombre del grupo nunca se teclea en el YAML. Sin shards, sale tal cual.
    python3 -c 'import json,sys
d=json.load(open(sys.argv[1],encoding="utf-8"))
out=[]
for g in d["groups"]:
    sh=g.get("shards")
    out.extend(["%s#%s" % (g["name"], s["name"]) for s in sh] if sh else [g["name"]])
print(json.dumps(out))' "${SPEC}"
    ;;

  packages)
    G="${2:?usage: $0 packages <group>}"
    python3 -c 'import json,sys;n=[g["name"] for g in json.load(open(sys.argv[1],encoding="utf-8"))["groups"]];sys.exit(0 if sys.argv[2] in n else 3)' "${SPEC}" "${G}" \
      || fail "grupo desconocido: ${G}"
    LIST="${TMPDIR:-/tmp}/race-groups-list.$$"
    pkgs > "${LIST}"
    python3 - "${SPEC}" "${LIST}" "${G}" <<'PY'
import json, sys
spec = json.load(open(sys.argv[1], encoding="utf-8"))
want = sys.argv[3]

def match(pat, pkg):
    if pat.endswith("/..."):
        base = pat[:-4]
        return pkg == base or pkg.startswith(base + "/")
    return pkg == pat

def weight(pat):
    return len(pat[:-4] if pat.endswith("/...") else pat)

excl = [e["pattern"] for e in spec.get("excluded", [])]
pairs = [(g["name"], p) for g in spec["groups"] for p in g["patterns"]]
for line in open(sys.argv[2], encoding="utf-8"):
    pkg = line.strip()
    if not pkg or any(match(p, pkg) for p in excl):
        continue
    best, bw = None, -1
    for name, pat in pairs:
        if match(pat, pkg) and weight(pat) > bw:
            best, bw = name, weight(pat)
    if best == want:
        print(pkg)
PY
    ;;

  check)
    # ⛔ TODO EL CONTROL EN UNA SOLA PASADA DE PYTHON, y no es estilo: la primera
    # versión encadenaba `awk | python3` con una salida temprana, el lector cerraba
    # la tubería mientras el escritor seguía, y el control moría con rc=141 (SIGPIPE)
    # SIN IMPRIMIR NADA. Un control que muere mudo se lee como un control que pasó.
    # ⛔ Y LA LISTA VIAJA POR FICHERO, NO POR TUBERÍA: un `<<'PY'` YA ocupa el stdin
    # con el PROGRAMA, así que `pkgs | python3 - <<'PY'` deja al python leyendo su
    # propio código como si fueran datos y la lista llega VACÍA.
    LIST="${TMPDIR:-/tmp}/race-groups-list.$$"
    TLIST="${TMPDIR:-/tmp}/race-groups-tests.$$"
    pkgs > "${LIST}"
    root_tests > "${TLIST}"
    python3 - "${SPEC}" "${LIST}" "${TLIST}" <<'PY'
import json, sys

spec = json.load(open(sys.argv[1], encoding="utf-8"))
pkgs = [l.strip() for l in open(sys.argv[2], encoding="utf-8") if l.strip()]

def match(pat, pkg):
    if pat.endswith("/..."):
        base = pat[:-4]
        return pkg == base or pkg.startswith(base + "/")
    return pkg == pat

def weight(pat):
    return len(pat[:-4] if pat.endswith("/...") else pat)

def fail(msg, extra=()):
    print("race-groups: FAIL — " + msg, file=sys.stderr)
    for e in list(extra)[:20]:
        print("    " + e, file=sys.stderr)
    sys.exit(1)

if not pkgs:
    print("race-groups: COULD NOT LOOK — la enumeración no devolvió ni un paquete", file=sys.stderr)
    sys.exit(2)

excl = [e["pattern"] for e in spec.get("excluded", [])]
pairs = [(g["name"], p) for g in spec["groups"] for p in g["patterns"]]

# (1) un patrón declarado en DOS grupos no tiene dueño: el más largo gana y el otro
#     miente en silencio.
seen = {}
for name, pat in pairs:
    if pat in seen:
        fail("patrón declarado en dos grupos (%s y %s): %s" % (seen[pat], name, pat))
    seen[pat] = name

# (2) el reparto
owner, orphans = {}, []
for pkg in pkgs:
    if any(match(p, pkg) for p in excl):
        owner[pkg] = "!"
        continue
    best, bw = None, -1
    for name, pat in pairs:
        if match(pat, pkg) and weight(pat) > bw:
            best, bw = name, weight(pat)
    if best is None:
        orphans.append(pkg)
    owner[pkg] = best or "-"

if orphans:
    fail("%d paquete(s) con tests NO pertenecen a ningún grupo — ese código no se corre "
         "bajo -race y nadie lo dice" % len(orphans), orphans)

# (3) un patrón que ya no casa con NADA es una mentira que sobrevive a su refactor:
#     declara cobertura sobre algo que no existe. Es el mismo defecto que una lista
#     de ranuras que se quedó en seis cuando el módulo tenía ocho.
absent_ok = {e["pattern"] for e in spec.get("excluded", []) if e.get("absent_when_unpublished")}
avisos = []
for pat in [p for _, p in pairs] + excl:
    if not any(match(pat, k) for k in pkgs):
        if pat in absent_ok:
            # ⛔ ESTE CAMINO EXISTE POR UNA MEDIDA, no por comodidad: el export RETIRA
            # `./cloud/control-plane` del go.work («el módulo no viaja en el árbol
            # publicado»), así que su exclusión no casa con nada ALLÍ y el control moriría
            # sobre el árbol exportado — que es justo donde tiene que correr, porque la
            # matriz de race-full se construye desde ahí. Se permite, pero se DICE: una
            # exención que calla es una exención que nadie revisa.
            avisos.append(pat)
            continue
        fail("patrón que no casa con NINGÚN paquete (rancio): " + pat)

# (3-bis) `cloud_task_group` nombra el grupo cuyo job corre `task test:cloud`. Si
#     alguien renombra ese grupo, el `if:` del workflow deja de casar y la pata
#     del control-plane DESAPARECE sin que nada se ponga rojo. Un `if:` que no
#     casa no falla: se salta, y saltarse se lee como verde.
ctg = spec.get("cloud_task_group")
names = [g["name"] for g in spec["groups"]]
if ctg is None:
    fail("falta cloud_task_group: el workflow lo lee para decidir dónde corre `task test:cloud`")
if ctg not in names:
    fail("cloud_task_group nombra un grupo que no existe (%s); el `task test:cloud` del "
         "workflow se saltaría EN SILENCIO. Grupos: %s" % (ctg, ", ".join(names)))

# (4) un grupo sin paquetes es un job que arranca un Postgres para no correr nada
counts = {}
for k, v in owner.items():
    counts[v] = counts.get(v, 0) + 1
for g in spec["groups"]:
    if counts.get(g["name"], 0) == 0:
        fail("el grupo %s se queda sin paquetes: sobra, o sus patrones son rancios" % g["name"])

# (5) EL REPARTO DEL PAQUETE RAIZ, por `-run`. Aqui el fallo silencioso es peor que
#     en los paquetes: **un `-run` que no casa con nada sale 0 y parece verde**, asi
#     que un turno con familias rancias publicaria un exito sin ejecutar un test.
# El universo lo produce root_tests() —una sola funcion, la misma que usa `root-run`—
# y llega por FICHERO (sys.argv[3]): dos enumeraciones distintas para el mismo conjunto
# es como se cuelan los 16 tests que midio de diferencia.
tests = set(l.strip() for l in open(sys.argv[3], encoding="utf-8") if l.strip())
if not tests:
    print("race-groups: COULD NOT LOOK — el universo de tests de la raiz vino vacio", file=sys.stderr)
    sys.exit(2)

# ⛔ LA MISMA GUARDA, POR GRUPO. El override de `go_timeout_minutes` de un grupo puede pasarse del
# techo del paso igual que el de la raiz, y entonces el reloj del paso mata al job ANTES de que el
# de Go pueda dar su diagnostico: se pierde el motivo, que es lo caro. La de la raiz esta doce
# lineas mas abajo desde el 2026-08-29; esta la exige el mismo argumento.
for _g in spec.get("groups", []):
    _to = _g.get("go_timeout_minutes")
    if _to is None:
        continue
    _techo = _g.get("step_ceiling_minutes", spec.get("step_ceiling_minutes", 45))
    if _to >= _techo:
        fail("el grupo %s pide go_timeout %dm y no deja margen bajo el techo del paso (%dm): "
             "el reloj del PASO mataria al job antes de que Go dijera por que"
             % (_g["name"], _to, _techo))

shards = spec["root_shards"]
if spec["root_go_timeout_minutes"] >= spec["root_step_ceiling_minutes"]:
    fail("root_go_timeout (%dm) no deja margen bajo el techo (%dm): es el defecto que mato "
         "a race-root, el reloj de Go arranca DESPUES de compilar con -race"
         % (spec["root_go_timeout_minutes"], spec["root_step_ceiling_minutes"]))

hits = {}
for s in shards:
    fams = s["families"]
    if not fams:
        fail("el turno %s no declara familias: su -run casaria con NADA y saldria 0" % s["name"])
    own = [t for t in tests if any(t.startswith("Test" + f) for f in fams)]
    if not own:
        fail("el turno %s no casa con NINGUN test: sus familias son rancias y `-run` saldria "
             "0 sin ejecutar nada" % s["name"])
    for t_ in own:
        hits.setdefault(t_, []).append(s["name"])

huerf = sorted(t_ for t_ in tests if t_ not in hits)
if huerf:
    fail("%d test(s) del paquete raiz no caen en ningun turno: no se correrian y nadie lo "
         "diria" % len(huerf), huerf)
dobles = sorted(t_ for t_, v in hits.items() if len(v) > 1)
if dobles:
    fail("%d test(s) caen en DOS turnos (una familia es prefijo de otra en otro turno): "
         "correrian dos veces" % len(dobles), ["%s → %s" % (t_, ",".join(hits[t_])) for t_ in dobles])

# ⛔ LA CIFRA DECLARADA ES INFORMATIVA, Y ESO TAMBIEN SE MIDIO. Empezo siendo una igualdad
# dura y el arbol EXPORTADO la rompe: alli hay 355 ficheros de test en la raiz y no 356, o
# sea 1808 tests y no 1811. Una spec que solo vale en el arbol del hub no sirve, porque la
# matriz de race-full se construye TAMBIEN sobre el export y sobre el repositorio publico.
# `tests_now` queda como la foto con la que se calibra, y se imprime junto a la cuenta VIVA
# para que la deriva se vea; lo que decide es una propiedad que no depende del arbol.
#
# Y lo que se comprueba de verdad es el EQUILIBRIO, que es la razon de existir del reparto:
# ningun turno puede llevarse mas de `max_shard_share` del universo, porque un turno gordo
# es exactamente el paso que no cabe en el techo del job.
# Las duraciones MEDIDAS que se guardan para calibrar tienen que nombrar grupos que existan:
# una cifra atada a un grupo que ya no esta es la misma rancidez que un patron muerto, y encima
# es la que se lee para decidir el proximo reparto.
mr = spec.get("measured_run") or {}
nombres_g = {g["name"] for g in spec["groups"]}
for k in (mr.get("minutes") or {}):
    if k not in nombres_g:
        fail("measured_run cita el grupo %s, que ya no existe: la medida con la que se "
             "reequilibra estaria atada a un grupo muerto" % k)

share = spec.get("max_shard_share", 0.30)
live = {}
for s in shards:
    live[s["name"]] = len([t_ for t_ in tests if any(t_.startswith("Test" + f) for f in s["families"])])
for s in shards:
    frac = live[s["name"]] / float(len(tests))
    if frac > share:
        fail("el turno %s se lleva el %.1f%% de los tests (tope %.1f%%): un turno gordo es "
             "el paso que no cabe en el techo del job" % (s["name"], frac * 100, share * 100))

# (6) LOS SHARDS DE UN GRUPO parten sus tests por `-run`, y ahi el fallo silencioso es el mismo
#     que en la raiz: un `-run` que no casa con nada SALE 0 Y PARECE VERDE. Se exige lo mismo:
#     todo test del paquete en EXACTAMENTE un turno, ninguno en dos, ningun turno vacio.
import os as _os, re as _re, glob as _glob
for g in spec["groups"]:
    sh = g.get("shards")
    if not sh:
        continue
    pkgs_g = [k for k, v in owner.items() if v == g["name"]]
    dirs = [k.replace("github.com/olivaresai/olivares/", "") for k in pkgs_g]
    tg = set()
    for dd in dirs:
        for f in _glob.glob(_os.path.join(dd, "*_test.go")):
            for line in open(f, encoding="utf-8", errors="replace"):
                m = _re.match(r"^func (Test[A-Za-z0-9_]+)", line)
                if m and m.group(1) != "TestMain":
                    tg.add(m.group(1))
    if not tg:
        fail("el grupo %s declara turnos pero no encuentro sus tests" % g["name"])
    hits_g = {}
    for s in sh:
        own = [t_ for t_ in tg if any(t_.startswith("Test" + f) for f in s["families"])]
        if not own:
            fail("el turno %s de %s no casa con NINGUN test: su -run saldria 0 sin correr nada"
                 % (s["name"], g["name"]))
        for t_ in own:
            hits_g.setdefault(t_, []).append(s["name"])
    huer = sorted(t_ for t_ in tg if t_ not in hits_g)
    if huer:
        fail("%d test(s) de %s no caen en ningun turno" % (len(huer), g["name"]), huer)
    dob = sorted(t_ for t_, v in hits_g.items() if len(v) > 1)
    if dob:
        fail("%d test(s) de %s caen en DOS turnos (familia prefijo de otra)" % (len(dob), g["name"]),
             ["%s -> %s" % (t_, ",".join(hits_g[t_])) for t_ in dob])
    print("race-groups: grupo %s — %d test(s) en %d turno(s), 0 huerfanos, 0 dobles"
          % (g["name"], len(tg), len(sh)))

root_reparto = " ".join("%s=%d(decl %d)" % (s["name"], live[s["name"]], s["tests_now"]) for s in shards)

reparto = " ".join("%s=%d" % (g["name"], counts.get(g["name"], 0)) for g in spec["groups"])
print("race-groups: CLEAN — %d paquete(s) con tests, 0 huérfanos, 0 patrones rancios, "
      "%d excluido(s) con motivo escrito. Reparto: %s"
      % (len(pkgs), counts.get("!", 0), reparto))
for a in avisos:
    print("race-groups: aviso — la exclusión %s no casa con nada en ESTE árbol (declarada "
          "como ausente en el publicado)" % a)
print("race-groups: raiz — %d test(s) de nivel superior en %d turno(s), 0 huerfanos, "
      "0 dobles. Reparto: %s" % (len(tests), len(shards), root_reparto))
PY
    ;;

  run)
    G="${2:?usage: $0 run <group>}"
    # «grupo#turno»: el turno aporta su `-run`; el grupo, sus paquetes. La union la comprueba
    # `check`, igual que en la raiz: ningun test sin turno y ninguno en dos.
    SHARD=""
    case "${G}" in *"#"*) SHARD="${G#*#}"; G="${G%%#*}" ;; esac
    # ⛔ `-timeout` POR GRUPO, con el mismo patron de override que `parallel` doce lineas mas
    # abajo. Existe porque el reparto en turnos reparte el TRABAJO y no el RELOJ: `modules-sessions`
    # se estimo en ~17 min por mitad a 4 plazas, pero a 2 plazas son ~39 — y con el global de 35m
    # el `go test` se corta EL SOLO antes de terminar, matando el turno por la variable que los
    # turnos existian para eliminar. Las plazas del runner publico son el dato que NADIE ha medido,
    # asi que el timeout se pone donde cubre los dos mundos en vez de apostar por uno.
    TO="$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1],encoding="utf-8"))
g=[x for x in d["groups"] if x["name"]==sys.argv[2]]
print((g[0].get("go_timeout_minutes") if g else None) or d.get("go_timeout_minutes",35))' "${SPEC}" "${G}")"
    # ⛔ `-parallel` POR GRUPO. Lo hereda de GOMAXPROCS si no se pasa, y GOMAXPROCS sale hoy de la
    # CUOTA del cgroup (Go 1.25+), no de las CPU visibles: un runner de «4 vCPU» con cuota 2 corre
    # DOS tests a la vez por muchos que esperen. Para una suite que espera temporizadores e I/O,
    # sobre-suscribir es correcto y barato. 0 = no pasar el flag (comportamiento de siempre).
    PAR="$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1],encoding="utf-8"))
g=[x for x in d["groups"] if x["name"]==sys.argv[2]]
print((g[0].get("parallel") if g else None) or d.get("default_parallel",0))' "${SPEC}" "${G}")"
    PFLAG=""
    [ "${PAR}" -gt 0 ] 2>/dev/null && PFLAG="-parallel ${PAR}"
    # ⛔ El nombre se valida AQUÍ y no dentro de la sustitución de proceso: el fallo de
    # `$0 packages` dentro de `< <(...)` NO se propaga, así que un grupo INEXISTENTE
    # llegaba con la lista vacía y se reportaba como «grupo sin paquetes». Dos causas
    # distintas con el mismo mensaje es una causa que nadie va a encontrar.
    python3 -c 'import json,sys;n=[g["name"] for g in json.load(open(sys.argv[1],encoding="utf-8"))["groups"]];sys.exit(0 if sys.argv[2] in n else 3)' "${SPEC}" "${G}" \
      || fail "grupo desconocido: ${G} (los declarados: $("$0" groups))"
    mapfile -t LIST < <("$0" packages "${G}")
    [ "${#LIST[@]}" -gt 0 ] || fail "el grupo ${G} está declarado pero no casa con ningún paquete con tests: sus patrones son rancios"
    say "grupo ${G}: ${#LIST[@]} paquete(s), go test -timeout ${TO}m"
    # ⛔ -timeout de Go POR DEBAJO del techo del paso, y con margen para compilar:
    # el reloj de `-timeout` arranca DESPUÉS de compilar con -race, así que un
    # -timeout igual al techo del paso garantiza que gane el techo y el volcado de
    JS=""
    [ "${3:-}" = "--json" ] && JS="-json"
    RFLAG=""
    if [ -n "${SHARD}" ]; then
      RE="$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1],encoding="utf-8"))
g=[x for x in d["groups"] if x["name"]==sys.argv[2]][0]
s=[x for x in g.get("shards",[]) if x["name"]==sys.argv[3]]
print("^Test(" + "|".join(sorted(s[0]["families"], key=len, reverse=True)) + ")" if s else "")' "${SPEC}" "${G}" "${SHARD}")"
      [ -n "${RE}" ] || fail "turno desconocido en ${G}: ${SHARD}"
      RFLAG="-run ${RE}"
      say "turno ${SHARD} del grupo ${G}" >&2
    fi
    # goroutines —lo único que dice DÓNDE colgó— no llegue a imprimirse.
    # OLIVARES_RACE_DRYRUN=1 imprime la orden en vez de correrla: es lo que permite
    # que el banco compruebe el ENSAMBLADO sin pagar una compilación con -race.
    if [ -n "${OLIVARES_RACE_DRYRUN:-}" ]; then
      printf 'go test -race -count=1 %s%s -timeout %sm' "${PFLAG}" "${RFLAG:+ $RFLAG}" "${TO}"
      printf ' %s' "${LIST[@]}"
      printf '\n'
      exit 0
    fi
    # shellcheck disable=SC2086 — PFLAG es "" o «-parallel N»: dos palabras a proposito.
    # --json: el workflow lo canaliza a un artefacto por grupo. Sin el, un rojo de grupo solo
    # deja el volcado del panic, y de un volcado se lee de mas (medido: acuse a dos tests sanos).
    # shellcheck disable=SC2086 — los tres flags son "" o varias palabras a proposito.
    go test -race -count=1 ${JS} ${PFLAG} ${RFLAG} -timeout "${TO}m" "${LIST[@]}"
    ;;

  root-shards)
    python3 -c 'import json,sys;print(json.dumps([s["name"] for s in json.load(open(sys.argv[1],encoding="utf-8"))["root_shards"]]))' "${SPEC}"
    ;;

  root-run)
    S="${2:?usage: $0 root-run <turno>}"
    TLIST="${TMPDIR:-/tmp}/race-root-tests-list.$$"
    root_tests > "${TLIST}"
    read -r RE TO CEIL < <(python3 - "${SPEC}" "${S}" <<'PY'
import json, sys
spec = json.load(open(sys.argv[1], encoding="utf-8"))
want = sys.argv[2]
sh = [s for s in spec["root_shards"] if s["name"] == want]
if not sh:
    print("", 0, 0); sys.exit(0)
fams = sorted(sh[0]["families"], key=len, reverse=True)
print("^Test(" + "|".join(fams) + ")",
      spec["root_go_timeout_minutes"], spec["root_step_ceiling_minutes"])
PY
    )
    [ -n "${RE}" ] || fail "turno desconocido: ${S} (los declarados: $("$0" root-shards))"
    # ⛔ UN `-run` QUE NO CASA CON NADA SALE 0 Y PARECE VERDE. Se cuenta ANTES.
    n="$(grep -cE "${RE}" "${TLIST}" || true)"
    [ "${n}" -gt 0 ] || fail "el turno ${S} no casa con ningun test: sus familias son rancias"
    # ⛔ A STDERR A PROPOSITO: en el workflow esta salida va POR UNA TUBERIA al `tee` que
    # escribe el .jsonl y al awk del progreso. Por stdout, esta linea ensuciaria el .jsonl
    # con texto que no es JSON —el fichero con el que luego se recalibra el reparto— y
    # ademas desapareceria del log, porque el awk solo imprime lo que casa. Por stderr
    # sobrevive intacta, que es donde tiene que estar: es la prueba de que el turno casó
    # tests y no corrio en vacio.
    say "turno ${S}: ${n} test(s) de nivel superior, go test -timeout ${TO}m (techo del paso ${CEIL}m)" >&2
    if [ -n "${OLIVARES_RACE_DRYRUN:-}" ]; then
      printf 'cd cmd/olivares && go test -json -race -count=1 -timeout %sm -run %s .\n' "${TO}" "${RE}"
      exit 0
    fi
    cd cmd/olivares && go test -json -race -count=1 -timeout "${TO}m" -run "${RE}" .
    ;;

  *)
    cannot "uso: $0 {groups|packages <grupo>|check|run <grupo>|root-shards|root-run <turno>}"
    ;;
esac
