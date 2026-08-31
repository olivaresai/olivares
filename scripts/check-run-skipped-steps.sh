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
repo_slug() {
	[ -n "$(repo_slug)" ] && { printf '%s' "$(repo_slug)"; return 0; }
	REPO="${OLIVARES_RUN_REPO:-${GITHUB_REPOSITORY:-}}"
	[ -n "$(repo_slug)" ] || REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) || REPO=""
	[ -n "$(repo_slug)" ] || { echo "check-run-skipped-steps: 2 NO PUDE MIRAR: no se que repositorio consultar (fija OLIVARES_RUN_REPO)" >&2; exit 2; }
	printf '%s' "$(repo_slug)"
}
JSON="${OLIVARES_RUN_JOBS_JSON:-}"
RUNJSON="${OLIVARES_RUN_JSON:-}"
REGLAS="${OLIVARES_SKIPS_ESTRUCTURALES:-$AQUI/lib/skips-estructurales.txt}"
RUN="${1:-}"

cannot() {
	printf 'check-run-skipped-steps: NO HE PODIDO MIRAR: %s\n' "$1" >&2
	exit 2
}

[ -r "$REGLAS" ] || cannot "no puedo leer el predicado de saltos estructurales en $REGLAS"

if [ -z "$JSON" ] || [ -z "$RUNJSON" ]; then
	[ -z "$JSON" ] && [ -z "$RUNJSON" ] ||
		cannot "alimenta OLIVARES_RUN_JOBS_JSON y OLIVARES_RUN_JSON juntos, o ninguno"
	[ -n "$RUN" ] || cannot "falta el <run-id> (y no hay OLIVARES_RUN_JOBS_JSON/OLIVARES_RUN_JSON)"
	case "$RUN" in *[!0-9]* | '') cannot "el run-id '$RUN' no es un numero" ;; esac
	command -v gh >/dev/null 2>&1 || cannot "gh no esta en el PATH; alimenta los dos JSON por fichero"
	tmpj="$(mktemp "${TMPDIR:-/tmp}/runjobs.XXXXXX")" || cannot "mktemp fallo"
	tmpr="$(mktemp "${TMPDIR:-/tmp}/run.XXXXXX")" || cannot "mktemp fallo"
	trap 'rm -f "$tmpj" "$tmpr"' EXIT
	gh api "repos/$(repo_slug)/actions/runs/$RUN/jobs?per_page=100" >"$tmpj" 2>/dev/null ||
		cannot "la API no contesto los jobs del run $RUN en $(repo_slug)"
	# A-02: el objeto del RUN, que es el unico que sabe si nacieron todos sus jobs.
	gh api "repos/$(repo_slug)/actions/runs/$RUN" >"$tmpr" 2>/dev/null ||
		cannot "la API no contesto el run $RUN en $(repo_slug)"
	JSON="$tmpj"
	RUNJSON="$tmpr"
fi
[ -r "$JSON" ] || cannot "no puedo leer $JSON"
[ -r "$RUNJSON" ] || cannot "no puedo leer $RUNJSON"

command -v python3 >/dev/null 2>&1 || cannot "sin python3 no puedo leer el JSON"

python3 - "$JSON" "$RUNJSON" "$REGLAS" <<'PY'
import json, sys

RUTA_JOBS, RUTA_RUN, RUTA_REGLAS = sys.argv[1], sys.argv[2], sys.argv[3]


def no_puedo(msg):
    sys.stderr.write(f"check-run-skipped-steps: NO HE PODIDO MIRAR: {msg}\n")
    raise SystemExit(2)


def carga(ruta, que):
    try:
        with open(ruta, encoding="utf-8") as fh:
            return json.load(fh)
    except Exception as exc:                                # JSON roto, fichero vacio, lo que sea
        no_puedo(f"{que} ilegible ({exc})")


# ── El predicado compartido con. Un fichero ilegible NO se degrada a «sin reglas»: sin
#    reglas todo salto seria sustantivo y la sonda se volveria ruido, o —peor— alguien la
#    "arreglaria" ignorandolas. Se responde 2.
exactos = set()
try:
    with open(RUTA_REGLAS, encoding="utf-8") as fh:
        for n, cruda in enumerate(fh, 1):
            linea = cruda.rstrip("\n")
            if not linea.strip() or linea.lstrip().startswith("#"):
                continue
            if linea.startswith("prefijo:"):
                # Se rechaza CON SU MOTIVO, para que quien lo reintroduzca sepa contra que discute.
                no_puedo(
                    f"{RUTA_REGLAS}:{n}: las reglas por prefijo se retiraron a proposito — los "
                    f"pasos que fabrica el runner se eximen por EMPAREJAMIENTO ('Post Run X' solo "
                    f"si existe 'Run X' en el mismo job), que cubre lo mismo sin absolver por "
                    f"parecido. Usa 'exacto:' o arregla el emparejamiento.")
            if not linea.startswith("exacto:"):
                no_puedo(f"{RUTA_REGLAS}:{n}: linea que no es 'exacto:': {linea!r}")
            val = linea[len("exacto:"):]
            if len(val) < 2 or val[0] != '"' or val[-1] != '"':
                no_puedo(
                    f"{RUTA_REGLAS}:{n}: el valor de 'exacto:' debe ir entre comillas "
                    f"(los espacios de los extremos son significativos), y trae: {val!r}")
            val = val[1:-1]
            if not val:
                no_puedo(f"{RUTA_REGLAS}:{n}: regla vacia")
            exactos.add(val)
except OSError as exc:
    no_puedo(f"no puedo leer el predicado ({exc})")

if not exactos:
    no_puedo(f"{RUTA_REGLAS} no declara ninguna regla")


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
        return "emparejado con su paso 'Run' (generado por el runner)"
    return None


# ══ A-02 ══ El objeto del RUN. La v1 miraba los jobs y nunca el run, y por eso no cerraba el caso
# que la motivo: si todos los jobs VISIBLES estan cerrados pero aun falta nacer uno, `/jobs` es
# indistinguible de un run terminado. MEDIDO sobre 33291332689: 14 jobs al consultarlo y 15 al
# cerrar — `race-hot` nace de los dos `-race`. El conjunto de jobs NO es estable mientras el run
# esta abierto, asi que preguntarle a los jobs si el run acabo es preguntarle al testigo equivocado.
run = carga(RUTA_RUN, "el objeto del run")
if not isinstance(run, dict):
    no_puedo("el objeto del run no es un objeto")
if "status" not in run:
    no_puedo("el objeto del run no trae 'status'")
if run.get("status") != "completed":
    no_puedo(
        f"el run {run.get('id', '?')} no ha terminado (status={run.get('status')!r}); "
        f"sus jobs pueden no haber nacido todavia")
if run.get("conclusion") is None:
    no_puedo(f"el run {run.get('id', '?')} figura 'completed' pero sin conclusion")

datos = carga(RUTA_JOBS, "el JSON de jobs")
if not isinstance(datos, dict) or "jobs" not in datos or not isinstance(datos["jobs"], list):
    no_puedo("el JSON no trae una lista 'jobs'")

jobs = datos["jobs"]
# Un run sin jobs no es un run limpio: es un run que no existe, o cuyos jobs no arrancaron. Decirlo
# 0 seria justo el defecto que este guion persigue — dar por medido lo que no se ha mirado.
if not jobs:
    no_puedo("el run no trae jobs")

# ══ A-03 ══ FORMA antes de juzgar. Un JSON valido puede estar incompleto, y la v1 lo daba por
# bueno: convertia `steps` ausente en `[]` y no leia la conclusion del job. Un cero sobre lo que no
# se ha mirado es peor que un 2.
total = datos.get("total_count")
if not isinstance(total, int):
    no_puedo("el JSON no trae un 'total_count' entero: no se si faltan jobs")
if total != len(jobs):
    no_puedo(
        f"la API declara {total} job(s) y he recibido {len(jobs)}: hay paginacion sin recorrer "
        f"y no puedo concluir sobre los que faltan")

for j in jobs:
    nombre = str(j.get("name", "?"))
    if j.get("status") != "completed":
        no_puedo(f"el job '{nombre}' no esta cerrado (status={j.get('status')!r})")
    if j.get("conclusion") is None:
        no_puedo(f"el job '{nombre}' figura cerrado y sin conclusion")
    if "steps" not in j or not isinstance(j["steps"], list):
        no_puedo(f"el job '{nombre}' no trae una lista 'steps': el JSON esta incompleto")
    if not j["steps"]:
        no_puedo(f"el job '{nombre}' no trae ningun paso")
    for p in j["steps"]:
        if not isinstance(p, dict) or "name" not in p or "conclusion" not in p:
            no_puedo(f"paso ilegible en '{nombre}': {p!r}")
        if p.get("conclusion") is None:
            no_puedo(
                f"el paso {p.get('number', '?')} '{p.get('name', '?')}' de '{nombre}' no tiene "
                f"conclusion pese a que el job figura cerrado")

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
        primero = f"paso {p0.get('number', '?')} {p0.get('name', '?')}"
    linea = (f"{nombre} · failure:{len(fallos)} · skipped:{len(saltados)} "
             f"(sustantivos:{len(sustantivos)} · estructurales:{len(descartados)})")
    if primero:
        linea += f" · primer failure: {primero}"
    print(linea)
    if sustantivos:
        incompletos.append((nombre, len(fallos), sustantivos, primero))

print()
if descartados_total:
    # La heuristica se AUDITA en la salida. Un predicado por nombre escondido en el codigo es el
    # que deriva; uno que dice a quien descarto y por que regla, se corrige al leerlo.
    print("Saltos descartados por el predicado declarado "
          "(scripts/lib/skips-estructurales.txt) — su presencia es normal:")
    for regla, n in sorted(descartados_total.items(), key=lambda kv: -kv[1]):
        print(f"  {n} × {regla}")
    print()

if incompletos:
    print("HALLAZGO — veredicto INCOMPLETO: hay pasos que NO se midieron y no son estructurales.")
    print("Un `skipped` no es un «no hacia falta»: es un «no se midio».")
    for nombre, nf, sustantivos, primero in incompletos:
        detalle = ", ".join(
            f"{p.get('number', '?')} {p.get('name', '?')}" for p, _ in sustantivos[:5])
        mas = "" if len(sustantivos) <= 5 else f" (+{len(sustantivos) - 5} mas)"
        cola = f" — tras {nf} fallo(s), {primero}" if nf else " — SIN fallo delante"
        print(f"  {nombre}: {len(sustantivos)} sin medir{cola}")
        print(f"      {detalle}{mas}")
    print("Corre la pata siguiente a donde murio antes de nombrar candidato.")
    raise SystemExit(1)

print(f"CLEAN — {len(jobs)} job(s) del run {run.get('id', '?')} "
      f"({run.get('conclusion')}); ningun paso quedo sin medir por causa no estructural.")
raise SystemExit(0)
PY
