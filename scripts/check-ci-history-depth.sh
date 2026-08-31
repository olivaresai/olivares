#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# check-ci-history-depth.sh — un gate que necesita HISTORIA no puede correr en un clon SUPERFICIAL.
#
# ⛔ POR QUE EXISTE, y es una medida del 2026-08-27, no una precaucion. El job `control-plane` —uno
#    de los CUATRO contextos REQUERIDOS de la proteccion de `main`— salio rojo en el paso
#    `add-on sets still derive from today's canon`. No era el gate ni el codigo:
#
#        check-alc-ver-programs-prep: COULD NOT LOOK — derived baseline is not exactly one commit identity
#        task: Failed to run task "lint:addon-sets": exit status 2
#
#    Ese guion deriva su base con `git log --follow --diff-filter=A --format=%P` y exige UN SHA de
#    40 hex. `actions/checkout` clona SUPERFICIAL por defecto, asi que esa historia no existe, la
#    cadena sale vacia y el gate contesta la TERCERA RESPUESTA: no he podido mirar. Probado
#    clonando el repositorio de las dos formas:
#
#        --depth 1  ->  ''                                          -> exit 2
#        completo   ->  '9c02e0094c3d21fcde626a2f8d3608d92f4afcda'  -> verde
#
# ⛔ Y LO QUE LO HACIA INTERMITENTE, que es la razon de que haga falta un GATE y no solo el arreglo:
#    `actions/checkout` NO borra el clon entre jobs en un runner autoalojado. A veces la historia
#    seguia ahi de una corrida anterior y el gate pasaba. **El veredicto dependia del RESIDUO de la
#    caja**, y un fallo que depende del residuo no se reproduce, no se diagnostica y vuelve.
#
#    Arreglar el checkout de HOY no impide que manana alguien anada otro guion con derivacion
#    historica a un job superficial. Eso es lo que este gate mira.
#
# Que hace, exactamente: para cada guion de scripts/ que derive de historia profunda, busca que
# tarea lo invoca, que job de .github/workflows/ corre esa tarea, y exige que el `checkout` de ese
# job traiga `fetch-depth: 0`.
#
# Salidas: 0 = limpio · 1 = una pareja rota · 2 = NO HE PODIDO MIRAR.
set -uo pipefail

ROOT="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || echo "")}"
[ -n "$ROOT" ] || { echo "check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
cd "$ROOT" || { echo "check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: no entro en $ROOT." >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: no hay python3." >&2; exit 2; }

TASKFILE="${OLIVARES_CIHD_TASKFILE:-Taskfile.yml}"
WFDIR="${OLIVARES_CIHD_WFDIR:-.github/workflows}"
SCRIPTSDIR="${OLIVARES_CIHD_SCRIPTS:-scripts}"
[ -r "$TASKFILE" ] || { echo "check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: no leo $TASKFILE." >&2; exit 2; }
[ -d "$WFDIR" ]    || { echo "check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: no hay $WFDIR." >&2; exit 2; }
[ -d "$SCRIPTSDIR" ] || { echo "check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: no hay $SCRIPTSDIR." >&2; exit 2; }

python3 - "$TASKFILE" "$WFDIR" "$SCRIPTSDIR" <<'PY'
import os, re, sys, glob
taskfile, wfdir, scriptsdir = sys.argv[1], sys.argv[2], sys.argv[3]

def cannot(msg):
    print(f"check-ci-history-depth: ⛔ NO HE PODIDO MIRAR: {msg}", file=sys.stderr); sys.exit(2)

try:
    import yaml
except Exception:
    cannot("no puedo importar yaml para leer los workflows")

# 1 · guiones que derivan de historia profunda
#
# ⛔ ENSANCHADO EL 2026-08-29 (a repository gate), Y NO POR PRECAUCION: UN MUTANTE MIO SOBREVIVIO A ESTE
#    PATRON. Al anadir el job `hook-only-legs` a mainline-ci quite su `fetch-depth: 0` para ver
#    a este gate ponerse rojo — y salio CLEAN. `check-int-12-no-land.sh:115` deriva con
#    `git("rev-list", "--count", f"{pin58}..{ovl_pin}", cwd=hub_dir)`: un RANGO de dos puntos
#    entre dos objetos que un clon superficial no tiene. En `--depth 1` ese conteo falla y el
#    guion contesta la tercera respuesta (exit 2), que es exactamente el fallo del 08-27 con
#    otro comando.
#
#    El patron viejo solo conocia `git log` con `--follow` o `--diff-filter=A`, que son las dos
#    formas del caso que lo motivo. Un gate dice lo que su MECANISMO DE DESCUBRIMIENTO alcanza,
#    no lo que su nombre promete: este descubria por UNA forma y certificaba sobre todas.
#
#    LA ASIMETRIA QUE HACE ESTE ENSANCHADO BARATO, declarada porque es la que decide si un falso
#    positivo importa: este gate solo puede pedir MAS historia, nunca menos. Un falso positivo
#    —clasificar como «profunda» una derivacion que no lo es— cuesta que un job traiga el
#    historial entero en su checkout: segundos de red. Un falso NEGATIVO cuesta un job entero
#    muerto con exit 2 y un diagnostico que apunta al gate en vez de al clon. ⇒ ante la duda, el
#    patron se ensancha, y por eso la forma correcta de equivocarse aqui es hacia el exceso.
#    Lo que NO se puede hacer es ensancharlo sin medir: eso convierte una precaucion en rojos
#    ajenos, y por eso la medida de abajo va ANTES del cambio y no despues.
#
# ⛔ LO QUE ESTE DETECTOR **NO** VE, declarado tras el contraste `sol max` del 2026-08-29 (H-01),
#    porque el nombre del gate promete una clase mayor que su mecanismo. Un gate dice lo que su
#    MECANISMO DE DESCUBRIMIENTO alcanza, y el de aqui es una REGEX sobre el cuerpo de cada `.sh`
#    mas una busqueda del basename en el Taskfile mas una invocacion textual `task X` en el
#    workflow. De ahi salen las dos listas de abajo. Ninguna esta cerrada por este cambio.
#
#    FALSOS POSITIVOS (clasifica de mas; el coste es un checkout completo de mas):
#      · comentarios, heredocs y fixtures cuentan como ejecucion — este propio guion y su bateria
#        entran en los 13 por los EJEMPLOS de sus comentarios;
#      · se pierde la PROCEDENCIA del repositorio: check-hub-web-fidelity.sh cuenta en un clon
#        hermano y check-submodule-pin-distance.sh en `$HUB`, y la profundidad del checkout de
#        ESTE repositorio no arregla la de aquellos;
#      · basta que el basename aparezca en el `desc:` de una tarea que ejecuta OTRA cosa;
#      · `..` cuenta aunque este en prosa, si cae tras `log` o `rev-list` en la misma linea.
#
#    FALSOS NEGATIVOS (los caros — un job muere con exit 2 y el diagnostico apunta aqui):
#      · solo recorre `*.sh`: Python, Go, JS, acciones compuestas y comandos inline del workflow
#        quedan fuera;
#      · solo conoce `--follow`, `--diff-filter=A` y `..`: `merge-base`, `cat-file`, `show`,
#        `diff BASE...HEAD`, `blame`, `describe`, `rev-parse HEAD~N` y `rev-list --all` no se ven
#        (ejemplo real que combina cuatro de ellas: scripts/check-baseline-shrink.sh:38-66);
#      · la regex no cruza saltos de linea, asi que un comando partido escapa;
#      · el cruce con el workflow solo reconoce `task X` DIRECTO: ni `deps:`, ni `cmds: - task:`,
#        ni `task -x X`, ni un `bash scripts/foo.sh` a pelo;
#      · cualquier checkout con `fetch-depth: 0` en el job satisface el gate, aunque sea de OTRO
#        repositorio, viva en otro `path` o ocurra DESPUES del comando que necesita la historia.
#
#    Y la BATERIA cubre `log --follow/--diff-filter=A`, `rev-list A..B`, sin profundidad,
#    profundidad 1, profundidad 0 y los fallos de lectura. NO cubre ninguna de las clases de
#    arriba. ⇒ «ya esta corregido» es cierto para el mutante `rev-list A..B` y **NO** para la
#    clase que el nombre del gate sugiere. Se dice aqui en vez de dejarlo implicito, porque un
#    gate que certifica mas de lo que mira es la forma exacta de fallo que este fichero existe
#    para corregir.
#
#    MEDIDO ANTES DE CAMBIARLO, porque ensanchar una sonda puede fabricar rojos ajenos:
#      patron viejo ....  4 guiones ·  5 tareas ·  1 job de CI · CLEAN
#      patron nuevo .... 13 guiones · 16 tareas ·  2 jobs de CI · CLEAN
#    Los nueve que entran (check-int-12-no-land, check-c02-70-no-land-snapshot,
#    check-c02-hold-key-until-producer, check-c03-76-no-land-snapshot, check-hub-web-fidelity,
#    check-submodule-pin-distance, hub-hygiene, rebase-web-branch, report-orphan-branches) NO
#    producen ni un hallazgo hoy: el arbol ya estaba bien, lo que faltaba era la guarda.
DEEP = re.compile(r"git\b[^\n]*\b(log|rev-list)\b[^\n]*(--follow|--diff-filter=A|\.\.)")
deep_scripts = set()
for path in sorted(glob.glob(os.path.join(scriptsdir, "**", "*.sh"), recursive=True)):
    try:
        body = open(path, encoding="utf-8", errors="replace").read()
    except OSError as e:
        cannot(f"no puedo leer {path}: {e}")
    if DEEP.search(body):
        deep_scripts.add(os.path.basename(path))
if not deep_scripts:
    cannot("cero guiones con derivacion historica; el patron cambio y esto seria limpio por vacuidad")

# 2 · tareas que invocan cada uno
try:
    tf = open(taskfile, encoding="utf-8", errors="replace").read().split("\n")
except OSError as e:
    cannot(f"no puedo leer {taskfile}: {e}")
task_re = re.compile(r"^  ([A-Za-z0-9:_.-]+):\s*$")
tasks_needing = {}
cur = None
for line in tf:
    m = task_re.match(line)
    if m:
        cur = m.group(1)
    elif cur:
        for s in deep_scripts:
            if s in line:
                tasks_needing.setdefault(cur, set()).add(s)
if not tasks_needing:
    cannot("ninguna tarea invoca esos guiones; el Taskfile cambio de forma")

# 3 · jobs que corren esas tareas, y su fetch-depth
findings = []
checked = 0
for wf in sorted(glob.glob(os.path.join(wfdir, "*.yml")) + glob.glob(os.path.join(wfdir, "*.yaml"))):
    try:
        doc = yaml.safe_load(open(wf, encoding="utf-8", errors="replace"))
    except Exception as e:
        cannot(f"{wf} no parsea como YAML: {e}")
    if not isinstance(doc, dict):
        continue
    for jobname, job in (doc.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps") or []
        if not isinstance(steps, list):
            continue
        runs = " \n".join(str(s.get("run", "")) for s in steps if isinstance(s, dict))
        needed = {t: v for t, v in tasks_needing.items()
                  if re.search(r"(?<![A-Za-z0-9:_.-])task\s+" + re.escape(t) + r"(?![A-Za-z0-9:_.-])", runs)}
        if not needed:
            continue
        checked += 1
        depths = [s.get("with", {}).get("fetch-depth")
                  for s in steps
                  if isinstance(s, dict) and "checkout" in str(s.get("uses", ""))]
        if not depths:
            findings.append((wf, jobname, sorted(needed), "no hace checkout"))
        elif not any(str(d) == "0" for d in depths):
            findings.append((wf, jobname, sorted(needed), f"fetch-depth={depths}"))

if checked == 0:
    cannot("ningun job de CI corre esas tareas; el cruce no midio nada y 'limpio' seria mentira")

if findings:
    for wf, job, tasks, why in findings:
        print(f"check-ci-history-depth: ⛔ {wf} job '{job}' corre {', '.join(tasks)} —que deriva de "
              f"historia profunda— y su checkout {why}.")
        print( "             En un clon superficial esa derivacion sale VACIA y el gate contesta")
        print( "             exit 2 («no he podido mirar»), que tumba el job entero. Anade")
        print( "             `fetch-depth: 0` al checkout de ese job.")
    sys.exit(1)

print(f"check-ci-history-depth: CLEAN — {len(deep_scripts)} guion(es) con derivacion historica, "
      f"{len(tasks_needing)} tarea(s), {checked} job(s) de CI que las corren, todos con fetch-depth: 0.")
PY
