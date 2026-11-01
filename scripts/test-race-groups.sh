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

# The public workflow reads the same spec, but runs on the export's file set.
# Use the curator's manifest, never a second list of private modules.
# export-closure: hub-only scripts/export-public.sh — the curator exists only in the development tree
if [ -f "${ROOT}/scripts/export-public.sh" ]; then
  bash "${ROOT}/scripts/export-public.sh" --manifest > "${TMP}/manifest"
  python3 - "${ROOT}" "${TMP}/public" "${TMP}/manifest" <<'PY'
import json, shutil, subprocess, sys
from pathlib import Path

root, public, manifest = map(Path, sys.argv[1:])
for name in manifest.read_text().splitlines():
    target = public / name
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(root / name, target)
# Like the export, retain only workspace members present in its file set.
# Let Go parse and edit its own format.
work = json.loads(subprocess.check_output(['go', 'work', 'edit', '-json'], cwd=public))
drop = ['-dropuse=' + item['DiskPath'] for item in work['Use']
        if not (public / item['DiskPath']).is_dir()]
if drop:
    subprocess.run(['go', 'work', 'edit', *drop], cwd=public, check=True)
PY
  public_rc=0
  OLIVARES_RACE_GROUPS="${TMP}/public/scripts/race-groups.json" \
    bash "${TMP}/public/scripts/race-groups.sh" check \
    > "${TMP}/public-out" 2> "${TMP}/public-err" || public_rc=$?
  if [ "${public_rc}" = 0 ] && grep -q 'CLEAN' "${TMP}/public-out"; then
    ok "the public export partition covers the curator's complete file set"
  else
    bad "public export partition (rc=${public_rc}: $(head -1 "${TMP}/public-err"))"
  fi
elif [ -f "${ROOT}/.olivares-public-export" ]; then
  echo "SKIP export comparison: curation tooling is absent from the public tree"
else
  bad "cannot compare the public partition: source curation tooling is missing"
fi

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
# A `-run` that matches nothing EXITS 0. An empty generated shard would report success
# without running a test; scripts/test-generated-shards.sh proves that refusal and exact coverage.

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

# (o) With three generated shards, at least one must exceed the 30% share limit.
#     This keeps the balance guard itself under test after the hand-kept lists are gone.
stage
python3 - "${SPEC}" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p, encoding="utf-8"))
d["root_shards"] = d["root_shards"][:3]
json.dump(d, open(p, "w", encoding="utf-8"))
PY
run check
if [ "$(rc)" = 1 ] && errhas "an oversized shard"; then ok "mutant (three generated root shards) is killed by max_shard_share"
else bad "the oversized generated shard passed (rc=$(rc): $(head -1 "${TMP}/err"))"; fi

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

# (pr-ci) The union check must run on the pull request that adds a test. It ran only after the
#     merge until 2026-10-13, so tests added by two pull requests reached main in no shard and
#     the next public race-full failed. Removing the step has to turn this bench red.
if grep -qE '^[[:space:]]+run: task lint:race-groups$' "${ROOT}/.github/workflows/pr-ci.yml"; then
  ok "pr-ci runs the race-groups union check on every pull request"
else bad "pr-ci does not run task lint:race-groups: a new test could reach main in no race shard"; fi

# main-suite is omitted from the public export. Development runs the union control
# through lint:pr-fast; keep this bench tied to that existing job and task.
# export-closure: hub-only .github/workflows/main-suite.yml — guarded by its presence below
if [ -f "${ROOT}/.github/workflows/main-suite.yml" ]; then
  rg_job="$(awk '/^  pr-lint-fast:$/{on=1;next} on && /^  [A-Za-z0-9_-]+:$/{on=0} on' "${ROOT}/.github/workflows/main-suite.yml")"
  rg_task="$(awk '/^  lint:pr-fast:$/{on=1;next} on && /^  [A-Za-z0-9_:-]+:$/{on=0} on' "${ROOT}/Taskfile.yml")"
  if printf '%s\n' "${rg_job}" | grep -qE '^[[:space:]]+run: task lint:pr-fast$' \
     && printf '%s\n' "${rg_job}" | grep -qE "^    if: .+ && github.event_name == 'pull_request'\$" \
     && ! printf '%s\n' "${rg_job}" | grep -q 'continue-on-error' \
     && printf '%s\n' "${rg_task}" | grep -qE '^[[:space:]]+- task: lint:race-groups$'; then
    ok "main-suite runs the race-groups union check on every development pull request"
  else bad "main-suite's race-groups wiring failed"; fi
fi

echo "race-groups selftest: ${pass} passed, ${fail} failed"
[ "${fail}" -eq 0 ]
