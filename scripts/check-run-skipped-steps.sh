#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-run-skipped-steps.sh <run-id> — v2.
#
# ⛔ QUE PREGUNTA, y por que no es la que ya hacemos. Antes de nombrar un candidato leemos «14 de
# 14 jobs en verde, por steps». Eso responde CUANTOS pasos fallaron. No responde **cuantos no
# llegaron a correr**, y en GitHub Actions un paso que falla deja los siguientes en `skipped`:
# no dieron veredicto, y un `skipped` se lee igual que un «no hacia falta».
#
# Medido el 2026-08-30, TRES veces en la misma noche y en tres sitios distintos:
#   · `control-plane` de 33284926144: fallo el paso 17 (SPDX) y saltaron 32, **incluido el que
#     INSTALA** las dependencias del paso 50 — que si corria, por llevar guarda, y canto
#     `openapi-typescript: not found`. Un dia entero leido como «deriva del snapshot» sobre un
#     snapshot que no habia derivado.
#   · `web` de 33284926144: fallo el paso 12 (ratchet de formato) y saltaron el 13, 14 y 15. Al
#     curar el 12, el 14 (`vitest`) corrio POR PRIMERA VEZ y destapo un test que llevaba un dia
#     rojo — dentro del lote que ya se habia nombrado candidato.
#   · Y la cura de esa clase, aplicada a medias: se le dio guarda al consumidor para que hablara
#     pese a un rojo ajeno, sin darsela a su proveedor.
#
# Los tres pasos tapados NO estaban rotos: estaban SIN MEDIR. Esta sonda los cuenta.
#
# ═══ QUE CAMBIA EN LA v2, y quien lo encontro ═══
# La v1 (claim `150392175`) la rechazo the reviewer el 2026-08-30T10:52Z con tres bloqueos, y los
# tres eran ciertos. Se cierran asi:
#
# A-01 · LA v1 SOLO MIRABA `fallos AND saltados`, y por eso no contestaba la pregunta que dice
#   contestar. Dos agujeros simetricos: un salto que cuesta cobertura SIN fallo delante salia
#   **0**, y un `Post Run` inocente al lado de un fallo salia **1**. La v2 **clasifica todos los
#   saltos** contra el predicado compartido `scripts/lib/skips-estructurales.txt`, y el hallazgo
#   depende de que queden saltos SUSTANTIVOS — con fallo o sin el. El fallo sigue reportandose
#   porque ayuda a leer, pero ya no es la condicion.
#
# A-02 · LA GUARDA DE «RUN EN VUELO» DE LA v1 MIRABA LOS JOBS Y NUNCA EL RUN, asi que no cerraba
#   el caso que la motivo. Es MI propio hallazgo del 14→15 y lo deje sin cubrir: si todos los jobs
#   VISIBLES estan cerrados pero el 15.º aun no ha nacido, `/jobs` es indistinguible de un run
#   terminado. La v2 lee el objeto del run (`status`/`conclusion`) y solo mira si el RUN esta
#   `completed` con conclusion. Sin ese objeto no hay veredicto: responde 2.
#
# A-03 · LA v1 CONVERTIA `steps` ausente o null EN `[]` y no leia la conclusion del job, asi que
#   un JSON valido pero incompleto salia CLEAN — un cero sobre lo que no habia mirado, que es el
#   defecto exacto que este fichero persigue. La v2 exige FORMA antes de juzgar: `steps` presente
#   y lista, conclusion de cada job no nula, y `total_count` igual a los jobs recibidos (si la API
#   paginó, faltan jobs y no se puede concluir).
#
# ⛔ COMO SE DECIDE QUE UN SALTO ES ESTRUCTURAL, y lo que eso cuesta. La API **no publica el `if:`
# de cada paso** —medido el 2026-08-30: los campos de un paso son exactamente ['completed_at',
# 'conclusion','name','number','started_at','status']—, asi que la separacion es por nombre, en dos
# formas y solo dos:
#   · la lista declarada `scripts/lib/skips-estructurales.txt`, por igualdad EXACTA, compartida con
#     `check-run-table.sh` de para que no haya dos listas que deriven;
#   · el EMPAREJAMIENTO de los pasos que fabrica el runner: `Post Run X` se exime SOLO si existe
#     `Run X` en el MISMO job. La idea es de y sustituye a la regla por prefijo que yo traia.
#     Medido por los dos por separado sobre 33291332689: 3 saltos `Post Run`, 3 emparejados, 0
#     huerfanos, 0 sustantivos absueltos por accidente. Cubre lo mismo, sobrevive al bump de
#     `setup-go` sin tocar la lista, y **no absuelve a un `Post Run` huerfano**, que es lo que un
#     prefijo si habria hecho.
# Un salto sustantivo que ademas se habria saltado por su propio `if:` se cuenta igual. ⇒ el numero
# es un TECHO de lo no medido. Sirve para decidir «este job no ha dado veredicto completo», que es
# la pregunta, y no para afirmar «faltan exactamente N».
#
# ⛔ Y SOBRE `gh`: NO esta instalado en los runners autoalojados
# (`.github/actions/pr-failure-report/action.yml:60`, medido por otro carril: «`gh: command not
# found` on ci-runner-2»). Asi que este guion sirve para el hub y para quien nombre el candidato,
# y si alguna vez lo quiere un job, tendra que alimentarlo por fichero en vez de dar por hecho el
# binario. Sin `gh` responde 2, nunca 0.
#
# Uso:
#   check-run-skipped-steps.sh <run-id>
#   OLIVARES_RUN_JOBS_JSON=<jobs.json> OLIVARES_RUN_JSON=<run.json> check-run-skipped-steps.sh
#     (sin red; lo usa la bateria. HACEN FALTA LOS DOS: sin el objeto del run no se puede saber
#      si el run habia terminado, y eso es A-02.)
#
# Salida: 0 el run midio todo lo que debia · 1 hallazgo · 2 NO HE PODIDO MIRAR.

set -uo pipefail

AQUI="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)" || exit 2
# ⛔ EL REPOSITORIO NO SE FIJA EN EL CODIGO, Y SE RESUELVE TARDE. El valor por defecto era el slug
# del repositorio privado escrito a mano, y este guion VIAJA en el export: el arbol publico nombraba
# ese repositorio como su destino por defecto. `lint:export` lo caza en la clase «founder bare name /
# private org-or-domain» y tenia razon. El slug NO se repite en este comentario, porque una
# explicacion que cita la fuga la vuelve a filtrar.
#
# ⛔ Y SE RESUELVE AL USARLO, NO AL ARRANCAR, y eso lo dicto una prueba: resolverlo arriba llamaba a
# `gh repo view` en cada invocacion y rompio el caso `hermetico` de la bateria —un guion que no va a
# tocar la red no debe preguntarle a nadie quien es—. Orden de menos a mas suposicion: la variable
# explicita, `GITHUB_REPOSITORY` (existe en toda corrida de Actions) y el remoto del directorio. Si
# ninguna contesta NO se adivina: 2 «no he podido mirar», la tercera respuesta de siempre.
REPO=""
# ⛔ LAS CUATRO REFERENCIAS DE DENTRO SON `$REPO`, NO `$(repo_slug)`, Y LA DIFERENCIA TIRO LA CAJA
# CUATRO VECES. Al retirar el slug escrito a mano (5ad5548751) se sustituyo `$REPO` por
# `$(repo_slug)` en todo el fichero, incluidas las lineas de DENTRO de la propia funcion: cada una
# se llamaba a si misma en un `$( )`, o sea un fork por vuelta y sin caso base. Medido en vivo:
# 1 837 bash bloqueados en `anon_pipe_read`, ~14 MB cada uno, creciendo ~10/s. Y no paraba solo,
# por dos motivos que conviene saber separados: el `exit 2` de aqui abajo vive en una subshell y
# solo mata a la subshell; y un `timeout` sobre el guion mata al hijo directo, no a los nietos.
# La memoizacion es lo que la funcion queria hacer, y `$REPO` es lo unico que la hace.
repo_slug() {
	[ -n "$REPO" ] && { printf '%s' "$REPO"; return 0; }
	REPO="${OLIVARES_RUN_REPO:-${GITHUB_REPOSITORY:-}}"
	[ -n "$REPO" ] || REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) || REPO=""
	[ -n "$REPO" ] || { echo "check-run-skipped-steps: 2 COULD NOT CHECK: cannot determine the repository (set OLIVARES_RUN_REPO)" >&2; exit 2; }
	printf '%s' "$REPO"
}
JSON="${OLIVARES_RUN_JOBS_JSON:-}"
RUNJSON="${OLIVARES_RUN_JSON:-}"
REGLAS="${OLIVARES_SKIPS_ESTRUCTURALES:-$AQUI/lib/skips-estructurales.txt}"
RUN="${1:-}"

cannot() {
	printf 'check-run-skipped-steps: COULD NOT CHECK: %s\n' "$1" >&2
	exit 2
}

[ -r "$REGLAS" ] || cannot "cannot read the structural-skip predicate in $REGLAS"

if [ -z "$JSON" ] || [ -z "$RUNJSON" ]; then
	[ -z "$JSON" ] && [ -z "$RUNJSON" ] ||
		cannot "set OLIVARES_RUN_JOBS_JSON and OLIVARES_RUN_JSON together, or neither"
	[ -n "$RUN" ] || cannot "missing <run-id> (neither OLIVARES_RUN_JOBS_JSON nor OLIVARES_RUN_JSON is set)"
	case "$RUN" in *[!0-9]* | '') cannot "run-id '$RUN' is not a number" ;; esac
	command -v gh >/dev/null 2>&1 || cannot "gh is not in PATH; supply both JSON inputs as files"
	tmpj="$(mktemp "${TMPDIR:-/tmp}/runjobs.XXXXXX")" || cannot "mktemp failed"
	tmpr="$(mktemp "${TMPDIR:-/tmp}/run.XXXXXX")" || cannot "mktemp failed"
	trap 'rm -f "$tmpj" "$tmpr"' EXIT
	gh api "repos/$(repo_slug)/actions/runs/$RUN/jobs?per_page=100" >"$tmpj" 2>/dev/null ||
		cannot "the API did not return jobs for run $RUN in $(repo_slug)"
	# A-02: el objeto del RUN, que es el unico que sabe si nacieron todos sus jobs.
	gh api "repos/$(repo_slug)/actions/runs/$RUN" >"$tmpr" 2>/dev/null ||
		cannot "the API did not return run $RUN in $(repo_slug)"
	JSON="$tmpj"
	RUNJSON="$tmpr"
fi
[ -r "$JSON" ] || cannot "cannot read $JSON"
[ -r "$RUNJSON" ] || cannot "cannot read $RUNJSON"

command -v python3 >/dev/null 2>&1 || cannot "python3 is not installed; cannot read the JSON"

python3 - "$JSON" "$RUNJSON" "$REGLAS" <<'PY'
import json, sys

RUTA_JOBS, RUTA_RUN, RUTA_REGLAS = sys.argv[1], sys.argv[2], sys.argv[3]


def cannot_check(msg):
    sys.stderr.write(f"check-run-skipped-steps: COULD NOT CHECK: {msg}\n")
    raise SystemExit(2)


def carga(ruta, que):
    try:
        with open(ruta, encoding="utf-8") as fh:
            return json.load(fh)
    except Exception as exc:                                # JSON roto, fichero vacio, lo que sea
        cannot_check(f"{que} is unreadable ({exc})")


# Shared predicate with. An unreadable rules file returns 2, not “no rules”:
# otherwise every skip becomes substantive noise, inviting callers to ignore the check.
exactos = set()
try:
    with open(RUTA_REGLAS, encoding="utf-8") as fh:
        for n, cruda in enumerate(fh, 1):
            linea = cruda.rstrip("\n")
            if not linea.strip() or linea.lstrip().startswith("#"):
                continue
            if linea.startswith("prefijo:"):
                # Se rechaza CON SU MOTIVO, para que quien lo reintroduzca sepa contra que discute.
                cannot_check(
                    f"{RUTA_REGLAS}:{n}: prefix rules were deliberately removed; runner-generated "
                    f"steps are exempted by pairing ('Post Run X' only "
                    f"when 'Run X' exists in the same job). This avoids exempting unrelated "
                    f"steps with similar names. Use 'exacto:' or fix the pairing.")
            if not linea.startswith("exacto:"):
                cannot_check(f"{RUTA_REGLAS}:{n}: line is not an 'exacto:' rule: {linea!r}")
            val = linea[len("exacto:"):]
            if len(val) < 2 or val[0] != '"' or val[-1] != '"':
                cannot_check(
                    f"{RUTA_REGLAS}:{n}: 'exacto:' value must be quoted "
                    f"(leading and trailing spaces matter), got: {val!r}")
            val = val[1:-1]
            if not val:
                cannot_check(f"{RUTA_REGLAS}:{n}: empty rule")
            exactos.add(val)
except OSError as exc:
    cannot_check(f"cannot read the predicate ({exc})")

if not exactos:
    cannot_check(f"{RUTA_REGLAS} declares no rules")


def clasifica(nombre, nombres_del_job):
    """Devuelve la REGLA que descarta este salto, o None si cuesta cobertura.

    Dos formas, y solo dos:
      · la lista declarada, por igualdad EXACTA;
      · el EMPAREJAMIENTO de los pasos que fabrica el runner. Idea de otro carril, medida por los dos
        por separado sobre 33291332689: 3 saltos `Post Run`, 3 emparejados, 0 huerfanos, y 0
        sustantivos absueltos por accidente. No mira el sha, mira la pareja — asi que un bump de
        `setup-go` no toca nada, y un `Post Run` HUERFANO no se exime, que es justo lo que una
        regla por prefijo si habria absuelto.
    """
    if nombre in exactos:
        return f'exacto:"{nombre}"'
    # ⛔ `Post Run X` ↔ `Run X`, y NADA MAS ANCHO. La v2 emparejaba `Post <lo que sea>` con
    # `<lo que sea>`, y the reviewer lo bloqueo con razon: un paso del repo llamado `Post foo`
    # junto a otro llamado `foo` quedaba eximido y producia un CLEAN falso. Mi fixture del
    # huerfano no lo veia porque probaba la AUSENCIA del par, no su FORMA. Lo que el runner
    # fabrica se llama literalmente `Post Run <action>@<sha>` y su hermano `Run <action>@<sha>`
    # —medido sobre 33291332689: 3 de 3 emparejados asi— de modo que exigir el prefijo `Post Run `
    # y el hermano `Run …` no pierde ni uno de los reales y cierra el hueco.
    if nombre.startswith("Post Run ") and nombre[len("Post "):] in nombres_del_job:
        return "paired with its 'Run' step (runner-generated)"
    return None


# ══ A-02 ══ El objeto del RUN. La v1 miraba los jobs y nunca el run, y por eso no cerraba el caso
# que la motivo: si todos los jobs VISIBLES estan cerrados pero aun falta nacer uno, `/jobs` es
# indistinguible de un run terminado. MEDIDO sobre 33291332689: 14 jobs al consultarlo y 15 al
# cerrar — `race-hot` nace de los dos `-race`. El conjunto de jobs NO es estable mientras el run
# esta abierto, asi que preguntarle a los jobs si el run acabo es preguntarle al testigo equivocado.
run = carga(RUTA_RUN, "the run object")
if not isinstance(run, dict):
    cannot_check("run input is not an object")
if "status" not in run:
    cannot_check("run object has no 'status'")
if run.get("status") != "completed":
    cannot_check(
        f"run {run.get('id', '?')} has not finished (status={run.get('status')!r}); "
        f"its jobs may not have been created yet")
if run.get("conclusion") is None:
    cannot_check(f"run {run.get('id', '?')} is 'completed' but has no conclusion")

datos = carga(RUTA_JOBS, "the jobs JSON")
if not isinstance(datos, dict) or "jobs" not in datos or not isinstance(datos["jobs"], list):
    cannot_check("JSON has no 'jobs' list")

jobs = datos["jobs"]
# Un run sin jobs no es un run limpio: es un run que no existe, o cuyos jobs no arrancaron. Decirlo
# 0 seria justo el defecto que este guion persigue — dar por medido lo que no se ha mirado.
if not jobs:
    cannot_check("run has no jobs")

# ══ A-03 ══ FORMA antes de juzgar. Un JSON valido puede estar incompleto, y la v1 lo daba por
# bueno: convertia `steps` ausente en `[]` y no leia la conclusion del job. Un cero sobre lo que no
# se ha mirado es peor que un 2.
total = datos.get("total_count")
if not isinstance(total, int):
    cannot_check("JSON has no integer 'total_count'; cannot determine whether jobs are missing")
if total != len(jobs):
    cannot_check(
        f"the API declares {total} job(s), but only {len(jobs)} were received: pagination is incomplete "
        f"and cannot verify the missing jobs")

for j in jobs:
    nombre = str(j.get("name", "?"))
    if j.get("status") != "completed":
        cannot_check(f"job '{nombre}' has not finished (status={j.get('status')!r})")
    if j.get("conclusion") is None:
        cannot_check(f"job '{nombre}' is finished but has no conclusion")
    if "steps" not in j or not isinstance(j["steps"], list):
        cannot_check(f"job '{nombre}' has no 'steps' list; the JSON is incomplete")
    if not j["steps"]:
        cannot_check(f"job '{nombre}' has no steps")
    for p in j["steps"]:
        if not isinstance(p, dict) or "name" not in p or "conclusion" not in p:
            cannot_check(f"unreadable step in '{nombre}': {p!r}")
        if p.get("conclusion") is None:
            cannot_check(
                f"step {p.get('number', '?')} '{p.get('name', '?')}' in '{nombre}' has no "
                f"conclusion, although the job is marked finished")

# ══ A-01 ══ Clasificar TODOS los saltos, no solo los que van detras de un fallo.
incompletos, descartados_total = [], {}
for j in jobs:
    nombre = str(j.get("name", "?"))
    pasos = j["steps"]
    fallos = [p for p in pasos if p.get("conclusion") == "failure"]
    saltados = [p for p in pasos if p.get("conclusion") == "skipped"]
    sustantivos, descartados = [], []
    nombres_del_job = {str(q.get("name", "")) for q in pasos}
    for p in saltados:
        regla = clasifica(str(p.get("name", "")), nombres_del_job)
        (descartados if regla else sustantivos).append((p, regla))
        if regla:
            descartados_total[regla] = descartados_total.get(regla, 0) + 1
    primero = ""
    if fallos:
        # El PRIMER fallo, por numero de paso: es el que causa los saltos. El ultimo suele ser la
        # consecuencia, y leerlo a el es como se pierde una noche.
        p0 = min(fallos, key=lambda p: p.get("number", 0))
        primero = f"step {p0.get('number', '?')} {p0.get('name', '?')}"
    linea = (f"{nombre} · failure:{len(fallos)} · skipped:{len(saltados)} "
             f"(substantive:{len(sustantivos)} · structural:{len(descartados)})")
    if primero:
        linea += f" · first failure: {primero}"
    print(linea)
    if sustantivos:
        incompletos.append((nombre, len(fallos), sustantivos, primero))

print()
if descartados_total:
    # La heuristica se AUDITA en la salida. Un predicado por nombre escondido en el codigo es el
    # que deriva; uno que dice a quien descarto y por que regla, se corrige al leerlo.
    print("Skips excluded by the declared predicate "
          "(scripts/lib/skips-estructurales.txt) — their presence is normal:")
    for regla, n in sorted(descartados_total.items(), key=lambda kv: -kv[1]):
        print(f"  {n} × {regla}")
    print()

if incompletos:
    print("FINDING — INCOMPLETE result: some steps were not measured and are not structural skips.")
    print("A `skipped` step is unmeasured; it does not prove the step was unnecessary.")
    for nombre, nf, sustantivos, primero in incompletos:
        detalle = ", ".join(
            f"{p.get('number', '?')} {p.get('name', '?')}" for p, _ in sustantivos[:5])
        mas = "" if len(sustantivos) <= 5 else f" (+{len(sustantivos) - 5} more)"
        cola = f" — after {nf} failure(s), {primero}" if nf else " — no preceding failure"
        print(f"  {nombre}: {len(sustantivos)} unmeasured{cola}")
        print(f"      {detalle}{mas}")
    print("Run the step following the failure before designating a candidate.")
    raise SystemExit(1)

print(f"CLEAN — {len(jobs)} job(s) in run {run.get('id', '?')} "
      f"({run.get('conclusion')}); no step remained unmeasured for a nonstructural reason.")
raise SystemExit(0)
PY
