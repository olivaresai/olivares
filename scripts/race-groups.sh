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
# release preflight requires a green race-full on the tagged SHA or identical tree.
#
# ⛔ THE GROUPS LIVE IN ONE PLACE — scripts/race-groups.json — and the workflow
# matrix is BUILT from it (`groups`). Writing the names in the YAML too is the
# defect this repository keeps paying for: a fact typed twice drifts in silence.
#
# Subcommands:
#   groups              JSON array of group names (for the workflow matrix)
#   packages <group>    the import paths that group owns, one per line
#   split               the import paths raced by test name, a turn at a time
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
SHARDER="${ROOT}/scripts/lib/test_name_shard.py"
RUN_EXPR_LIMIT=100000
say() { printf 'race-groups: %s\n' "$*"; }
fail()   { printf 'race-groups: FAIL — %s\n' "$*" >&2; exit 1; }
cannot() { printf 'race-groups: COULD NOT LOOK — %s\n' "$*" >&2; exit 2; }
check_run_expression_size() {
  local owner="$1" expression="$2" bytes
  bytes="$(printf '%s' "$expression" | wc -c)"
  [ "$bytes" -le "$RUN_EXPR_LIMIT" ] && return 0
  fail "the -run expression of ${owner} is ${bytes} bytes, over the ${RUN_EXPR_LIMIT}-byte limit this reader declares (the kernel refuses a single argument over MAX_ARG_STRLEN, 131072 bytes). Declare more shards for this package."
}
[ -r "${SHARDER}" ] || cannot "cannot read ${SHARDER}"
export SHARDER

[ -f "${SPEC}" ] || cannot "cannot find ${SPEC}"
[ -f go.work ]   || cannot "go.work is absent from ${ROOT}"
command -v go >/dev/null 2>&1 || cannot "no Go toolchain: cannot enumerate packages"

# ── Enumeración: UNA sola función, la misma para `check` y para `run` ──────────
# Un `go test ./...` en un workspace sólo cubre el módulo ACTUAL (golang/go#50745),
# así que se enumera módulo a módulo. Se listan sólo los paquetes CON tests: un
# paquete sin tests no aporta cobertura de carrera y alarga la línea de comandos.
enumerate() {
  local m
  while IFS= read -r m; do
    [ -n "${m}" ] || continue
    ( cd "${m}" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... ) \
      || cannot "go list failed in ${m}"
  done < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
}

CACHE="$(mktemp "${TMPDIR:-/tmp}/race-groups-pkgs.XXXXXX")" || cannot "cannot create the package inventory"
trap 'rm -f "${CACHE}" "${LIST:-}" "${TESTS_CACHE:-}" "${TLIST:-}"' EXIT
pkgs() {
  [ -s "${CACHE}" ] || enumerate | grep -v '^$' | sort -u > "${CACHE}"
  cat "${CACHE}"
}

# ── Test names come through the one source enumerator ─────────────────────────────
# `go list` applies the build constraints, and pr-suite-shards.sh reads complete Test/Fuzz/
# Example declarations without compiling. The race sweep keeps its existing Test* universe.
TESTS_CACHE="$(mktemp "${TMPDIR:-/tmp}/race-root-tests.XXXXXX")" || cannot "cannot create the root test inventory"
root_tests() {
  local pkg
  pkg="$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1],encoding="utf-8"))["root_package"])' "${SPEC}")"
  if [ ! -s "${TESTS_CACHE}" ]; then
    bash "${ROOT}/scripts/pr-suite-shards.sh" tests "${pkg}" | sed -n '/^Test/p' > "${TESTS_CACHE}" \
      || cannot "cannot enumerate the tests of ${pkg}"
  fi
  cat "${TESTS_CACHE}"
}

group_tests() {
  local group="$1" packages pkg tests
  packages="$("$0" packages "${group}")" || return $?
  tests="$(
    while IFS= read -r pkg; do
      [ -n "${pkg}" ] || continue
      bash "${ROOT}/scripts/pr-suite-shards.sh" tests "${pkg}" || exit $?
    done <<< "${packages}"
  )" || return $?
  printf '%s\n' "${tests}" | sed -n '/^Test/p' | sort -u
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

  packages|split)
    # split: the import paths race-full races a turn at a time, by test name: the root package
    # and every package a group with shards owns. One `go test -race` run does not finish them.
    if [ "$1" = split ]; then
      G=""
    else
      G="${2:?usage: $0 packages <group>}"
      python3 -c 'import json,sys;n=[g["name"] for g in json.load(open(sys.argv[1],encoding="utf-8"))["groups"]];sys.exit(0 if sys.argv[2] in n else 3)' "${SPEC}" "${G}" \
        || fail "unknown group: ${G}"
    fi
    LIST="$(mktemp "${TMPDIR:-/tmp}/race-groups-list.XXXXXX")" || cannot "cannot create the package list"
    pkgs > "${LIST}"
    python3 - "${SPEC}" "${LIST}" "${G}" <<'PY'
import json, sys
spec = json.load(open(sys.argv[1], encoding="utf-8"))
want = sys.argv[3]
split = {g["name"] for g in spec["groups"] if g.get("shards")}
if not want:
    print(spec["root_package"])

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
    if best == want or (not want and best in split):
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
    LIST="$(mktemp "${TMPDIR:-/tmp}/race-groups-list.XXXXXX")" || cannot "cannot create the package list"
    TLIST="$(mktemp "${TMPDIR:-/tmp}/race-groups-tests.XXXXXX")" || cannot "cannot create the root test list"
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
    print("race-groups: COULD NOT LOOK — enumeration returned no packages", file=sys.stderr)
    sys.exit(2)

excl = [e["pattern"] for e in spec.get("excluded", [])]
pairs = [(g["name"], p) for g in spec["groups"] for p in g["patterns"]]

# (1) un patrón declarado en DOS grupos no tiene dueño: el más largo gana y el otro
#     miente en silencio.
seen = {}
for name, pat in pairs:
    if pat in seen:
        fail("pattern declared in two groups (%s and %s): %s" % (seen[pat], name, pat))
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
    fail("%d package(s) with tests belong to NO group — that code is not tested "
         "under -race, without any warning" % len(orphans), orphans)

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
        fail("pattern matching NO package (stale): " + pat)

# (3-bis) `cloud_task_group` nombra el grupo cuyo job corre `task test:cloud`. Si
#     alguien renombra ese grupo, el `if:` del workflow deja de casar y la pata
#     del control-plane DESAPARECE sin que nada se ponga rojo. Un `if:` que no
#     casa no falla: se salta, y saltarse se lee como verde.
ctg = spec.get("cloud_task_group")
names = [g["name"] for g in spec["groups"]]
if ctg is None:
    fail("missing cloud_task_group: the workflow uses it to decide where to run `task test:cloud`")
if ctg not in names:
    fail("cloud_task_group names a nonexistent group (%s); the workflow "
         "would SILENTLY skip `task test:cloud`. Groups: %s" % (ctg, ", ".join(names)))

# (4) un grupo sin paquetes es un job que arranca un Postgres para no correr nada
counts = {}
for k, v in owner.items():
    counts[v] = counts.get(v, 0) + 1
for g in spec["groups"]:
    if counts.get(g["name"], 0) == 0:
        fail("group %s has no packages: it is unnecessary, or its patterns are stale" % g["name"])

# (5) GENERATED ASSIGNMENT. Shard names and clocks stay declared; every test is assigned
# exactly once by the shared SHA-256 implementation. Empty selections remain findings because
# `go test -run` over an empty selection exits 0.
import importlib.util as _importlib
import os as _os
import subprocess as _subprocess

_helper_spec = _importlib.spec_from_file_location("test_name_shard", _os.environ["SHARDER"])
_sharder = _importlib.module_from_spec(_helper_spec)
_helper_spec.loader.exec_module(_sharder)

def assign(domain, shards, tests):
    try:
        return _sharder.assign(domain, [s["name"] for s in shards], sorted(tests))
    except ValueError as exc:
        fail(str(exc))

tests = set(l.strip() for l in open(sys.argv[3], encoding="utf-8") if l.strip())
if not tests:
    print("race-groups: COULD NOT LOOK — the root test inventory was empty", file=sys.stderr)
    sys.exit(2)

for _g in spec.get("groups", []):
    _to = _g.get("go_timeout_minutes")
    if _to is None:
        continue
    _techo = _g.get("step_ceiling_minutes", spec.get("step_ceiling_minutes", 45))
    if _to >= _techo:
        fail("group %s requests go_timeout %dm with no headroom below the step limit (%dm): "
             "the STEP timer would stop the job before Go could explain why"
             % (_g["name"], _to, _techo))

shards = spec["root_shards"]
if spec["root_go_timeout_minutes"] >= spec["root_step_ceiling_minutes"]:
    fail("root_go_timeout (%dm) leaves no headroom below the limit (%dm): this is the defect that stopped "
         "race-root; the Go timer starts AFTER compilation with -race"
         % (spec["root_go_timeout_minutes"], spec["root_step_ceiling_minutes"]))

assigned = assign("race:" + spec["root_package"], shards, tests)
for s in shards:
    if not assigned[s["name"]]:
        fail("shard %s selects no test: its generated -run would exit 0 without executing anything" % s["name"])

# Measurements may inform future shard-count changes, but they cannot name a removed group.
mr = spec.get("measured_run") or {}
nombres_g = {g["name"] for g in spec["groups"]}
for k in (mr.get("minutes") or {}):
    if k not in nombres_g:
        fail("measured_run references group %s, which no longer exists: the measurement used for "
             "rebalancing would be tied to a removed group" % k)

share = spec.get("max_shard_share", 0.30)
for s in shards:
    frac = len(assigned[s["name"]]) / float(len(tests))
    if frac > share:
        fail("shard %s contains %.1f%% of the tests (limit %.1f%%): an oversized shard is "
             "a step that cannot fit within the job limit" % (s["name"], frac * 100, share * 100))

# (6) Group shards use the same generated assignment and the same source enumerator as the
# runner. A package read failure is COULD NOT LOOK, never an empty test inventory.
for g in spec["groups"]:
    sh = g.get("shards")
    if not sh:
        continue
    pkgs_g = [k for k, v in owner.items() if v == g["name"]]
    tg = set()
    for pkg in pkgs_g:
        out = _subprocess.run(["bash", "scripts/pr-suite-shards.sh", "tests", pkg],
                              capture_output=True, text=True)
        if out.returncode != 0:
            print("race-groups: COULD NOT LOOK — cannot enumerate the tests of %s: %s"
                  % (pkg, out.stderr.strip().splitlines()[-1] if out.stderr.strip() else "no reason given"),
                  file=sys.stderr)
            sys.exit(2)
        tg.update(line for line in out.stdout.splitlines() if line.startswith("Test"))
    if not tg:
        fail("group %s declares shards, but its tests could not be found" % g["name"])
    assigned_g = assign("race-group:" + g["name"], sh, tg)
    for s in sh:
        if not assigned_g[s["name"]]:
            fail("shard %s of %s selects no test: its generated -run would exit 0 without running anything"
                 % (s["name"], g["name"]))
    print("race-groups: group %s — %d test(s) in %d shard(s), 0 orphans, 0 duplicates"
          % (g["name"], len(tg), len(sh)))

root_reparto = " ".join("%s=%d" % (s["name"], len(assigned[s["name"]])) for s in shards)

reparto = " ".join("%s=%d" % (g["name"], counts.get(g["name"], 0)) for g in spec["groups"])
print("race-groups: CLEAN — %d package(s) with tests, 0 orphans, 0 stale patterns, "
      "%d excluded with documented reasons. Distribution: %s"
      % (len(pkgs), counts.get("!", 0), reparto))
for a in avisos:
    print("race-groups: warning — exclusion %s matches nothing in THIS tree (declared "
          "absent from the published tree)" % a)
print("race-groups: root — %d top-level test(s) in %d shard(s), 0 orphans, "
      "0 duplicates. Distribution: %s" % (len(tests), len(shards), root_reparto))
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
      || fail "unknown group: ${G} (declared groups: $("$0" groups))"
    mapfile -t LIST < <("$0" packages "${G}")
    [ "${#LIST[@]}" -gt 0 ] || fail "group ${G} is declared but matches no package with tests: its patterns are stale"
    say "group ${G}: ${#LIST[@]} package(s), go test -timeout ${TO}m"
    # ⛔ -timeout de Go POR DEBAJO del techo del paso, y con margen para compilar:
    # el reloj de `-timeout` arranca DESPUÉS de compilar con -race, así que un
    # -timeout igual al techo del paso garantiza que gane el techo y el volcado de
    JS=""
    [ "${3:-}" = "--json" ] && JS="-json"
    RFLAG=""
    if [ -n "${SHARD}" ]; then
      TLIST="$(mktemp "${TMPDIR:-/tmp}/race-group-tests.XXXXXX")" || cannot "cannot create the group test list"
      group_tests "${G}" > "${TLIST}" || exit $?
      RE_RAW="$(python3 - "${SPEC}" "${G}" "${SHARD}" "${TLIST}" <<'PY'
import importlib.util
import json
import os
import sys

spec = json.load(open(sys.argv[1], encoding="utf-8"))
group_name, want = sys.argv[2:4]
group = [g for g in spec["groups"] if g["name"] == group_name][0]
shards = group.get("shards", [])
if want not in [s["name"] for s in shards]:
    print(0)
    sys.exit(0)
helper_spec = importlib.util.spec_from_file_location("test_name_shard", os.environ["SHARDER"])
sharder = importlib.util.module_from_spec(helper_spec)
helper_spec.loader.exec_module(sharder)
tests = [line.strip() for line in open(sys.argv[4], encoding="utf-8") if line.strip()]
try:
    assigned = sharder.assign("race-group:" + group_name, [s["name"] for s in shards], tests)
    print(1)
    print(sharder.expression(assigned[want]))
except ValueError as exc:
    print("race-groups: FAIL — " + str(exc), file=sys.stderr)
    sys.exit(1)
PY
      )" || exit $?
      mapfile -t RE_META <<< "${RE_RAW}"
      KNOWN="${RE_META[0]}"
      RE="${RE_META[1]:-}"
      [ "${KNOWN}" = 1 ] || fail "unknown shard in ${G}: ${SHARD}"
      [ -n "${RE}" ] || fail "shard ${SHARD} of group ${G} selects no test"
      check_run_expression_size "shard ${SHARD} of group ${G}" "${RE}"
      RFLAG="-run ${RE}"
      say "shard ${SHARD} of group ${G}" >&2
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
    S="${2:?usage: $0 root-run <shard>}"
    TLIST="$(mktemp "${TMPDIR:-/tmp}/race-root-tests-list.XXXXXX")" || cannot "cannot create the root test list"
    root_tests > "${TLIST}"
    RE_RAW="$(python3 - "${SPEC}" "${S}" "${TLIST}" <<'PY'
import importlib.util
import json
import os
import sys

spec = json.load(open(sys.argv[1], encoding="utf-8"))
want = sys.argv[2]
shards = spec["root_shards"]
if want not in [s["name"] for s in shards]:
    print(0)
    sys.exit(0)
helper_spec = importlib.util.spec_from_file_location("test_name_shard", os.environ["SHARDER"])
sharder = importlib.util.module_from_spec(helper_spec)
helper_spec.loader.exec_module(sharder)
tests = [line.strip() for line in open(sys.argv[3], encoding="utf-8") if line.strip()]
try:
    assigned = sharder.assign("race:" + spec["root_package"], [s["name"] for s in shards], tests)
    print(1)
    print(sharder.expression(assigned[want]))
    print(spec["root_go_timeout_minutes"])
    print(spec["root_step_ceiling_minutes"])
except ValueError as exc:
    print("race-groups: FAIL — " + str(exc), file=sys.stderr)
    sys.exit(1)
PY
    )" || exit $?
    mapfile -t RE_META <<< "${RE_RAW}"
    KNOWN="${RE_META[0]}"
    RE="${RE_META[1]:-}"
    TO="${RE_META[2]:-}"
    CEIL="${RE_META[3]:-}"
    [ "${KNOWN}" = 1 ] || fail "unknown shard: ${S} (declared shards: $("$0" root-shards))"
    [ -n "${RE}" ] || fail "shard ${S} selects no test"
    check_run_expression_size "root shard ${S}" "${RE}"
    # ⛔ UN `-run` QUE NO CASA CON NADA SALE 0 Y PARECE VERDE. Se cuenta ANTES.
    GREP_RC=0
    n="$(grep -cE "${RE}" "${TLIST}")" || GREP_RC=$?
    case "${GREP_RC}" in
      0|1) ;;
      *) cannot "grep exited ${GREP_RC} counting the tests selected by root shard ${S}" ;;
    esac
    [ "${n}" -gt 0 ] || fail "shard ${S} selects no test"
    # ⛔ A STDERR A PROPOSITO: en el workflow esta salida va POR UNA TUBERIA al `tee` que
    # escribe el .jsonl y al awk del progreso. Por stdout, esta linea ensuciaria el .jsonl
    # con texto que no es JSON —el fichero con el que luego se recalibra el reparto— y
    # ademas desapareceria del log, porque el awk solo imprime lo que casa. Por stderr
    # sobrevive intacta, que es donde tiene que estar: es la prueba de que el turno casó
    # tests y no corrio en vacio.
    say "shard ${S}: ${n} top-level test(s), go test -timeout ${TO}m (step limit ${CEIL}m)" >&2
    if [ -n "${OLIVARES_RACE_DRYRUN:-}" ]; then
      printf 'cd cmd/olivares && go test -json -race -count=1 -timeout %sm -run %s .\n' "${TO}" "${RE}"
      exit 0
    fi
    cd cmd/olivares && go test -json -race -count=1 -timeout "${TO}m" -run "${RE}" .
    ;;

  *)
    cannot "usage: $0 {groups|packages <group>|check|run <group>|root-shards|root-run <shard>}"
    ;;
esac
