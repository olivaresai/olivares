#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-run-table.sh <run-id> — la TABLA de una corrida: por job, veredicto, duracion, paso mas
# caro, y los pasos saltados que SI cuestan cobertura.
#
# Es la implementacion del modo `--tabla` que pidio para `check-run-skipped-steps.sh`.
#
# ⛔ VIVE APARTE PORQUE SU HUESPED NO EXISTE. Medido dos veces —07:5xZ y 10:1xZ—:
# `scripts/check-run-skipped-steps.sh` NO esta en `origin/main` NI en ninguno de los claims
# publicados de (`git ls-tree` sobre cada uno: cero las dos veces). Injertar un modo en un
# fichero que no puedo leer seria escribir contra un contrato imaginado. Esto es la MISMA logica,
# con su bateria, lista para pegarse dentro cuando el huesped aparezca — y mientras tanto sirve
# sola, que es mejor que esperar: su primera corrida ya encontro dos saltos reales.
#
# QUE IMPRIME, por job: veredicto · duracion · paso mas caro; y para los jobs con pasos SALTADOS,
# cuales. Todo derivado de la API (`steps[].started_at/completed_at`), nunca de la duracion del job.
#
# ⛔ rc 2 MIENTRAS EL RUN ESTE EN VUELO, y no es prudencia: Midio **14 jobs a las 06:12Z y 15
# al cerrar**. Una tabla tomada en vuelo enseña un job de menos y se lee igual que una completa.
# Un `skipped` a mitad de corrida tampoco es un `skipped` final: puede no haber arrancado aun.
#
# 0 tabla completa y ningun job con saltados · 1 hay jobs con pasos saltados · 2 NO HE PODIDO MIRAR.
set -u -o pipefail
RUN="${1:-}"
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
	REPO="${OLIVARES_REPO:-${GITHUB_REPOSITORY:-}}"
	[ -n "$(repo_slug)" ] || REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) || REPO=""
	[ -n "$(repo_slug)" ] || { echo "check-run-table: 2 COULD NOT CHECK: cannot determine the repository (set OLIVARES_REPO)" >&2; exit 2; }
	printf '%s' "$(repo_slug)"
}
[ -n "$RUN" ] || { echo "usage: $0 <run-id>" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "check-run-table: ⛔ COULD NOT CHECK: jq is not installed" >&2; exit 2; }

if [ -n "${OLIVARES_TABLA_JSON:-}" ]; then
  [ -r "$OLIVARES_TABLA_JSON" ] || { echo "check-run-table: ⛔ COULD NOT CHECK: unreadable fixture" >&2; exit 2; }
  J=$(cat "$OLIVARES_TABLA_JSON")
else
  command -v gh >/dev/null 2>&1 || { echo "check-run-table: ⛔ COULD NOT CHECK: gh is not installed" >&2; exit 2; }
  J=$(gh api "repos/$(repo_slug)/actions/runs/${RUN}/jobs" --paginate 2>/dev/null) \
    || { echo "check-run-table: ⛔ COULD NOT CHECK: the API did not return jobs for ${RUN}" >&2; exit 2; }
fi
printf '%s' "$J" | jq -e . >/dev/null 2>&1 || { echo "check-run-table: ⛔ COULD NOT CHECK: unreadable JSON" >&2; exit 2; }

# ⛔ EL RUN SE PREGUNTA AL RUN, NO A SUS JOBS. Mirar solo `/jobs` confunde «todos los jobs que hay
# AHORA han terminado» con «el run ha CERRADO», y son cosas distintas: GitHub materializa los jobs
# por tandas. Lo midio — **14 jobs a las 06:12Z y 15 al cerrar** — y una tabla tomada en esa
# ventana enseña un job de menos y **se lee igual que una completa**. Mi guarda de «en vuelo»
# tenia dentro el agujero que esa guarda existe para tapar. Lo cazo the reviewer (A-01).
if [ -n "${OLIVARES_TABLA_JSON:-}" ]; then
  EST=$(printf '%s' "$J" | jq -r '.run.status // empty')
  [ -n "$EST" ] || { echo "check-run-table: ⛔ COULD NOT CHECK: fixture has no \`run.status\`." >&2
                     echo "  Without the run status, finished jobs cannot prove the run is complete." >&2; exit 2; }
else
  EST=$(gh api "repos/$(repo_slug)/actions/runs/${RUN}" --jq '.status' 2>/dev/null)     || { echo "check-run-table: ⛔ COULD NOT CHECK: the API did not return run ${RUN}." >&2; exit 2; }
fi
if [ "$EST" != "completed" ]; then
  echo "check-run-table: ⛔ COULD NOT CHECK: run ${RUN} is '${EST}', not 'completed'." >&2
  echo "  GitHub creates jobs in batches: all existing jobs may be finished while others" >&2
  echo "  have not been created. Rerun this check after the run finishes." >&2
  exit 2
fi

TOT=$(printf '%s' "$J" | jq '.jobs|length')
[ "${TOT:-0}" -gt 0 ] || { echo "check-run-table: ⛔ COULD NOT CHECK: no jobs in ${RUN}" >&2; exit 2; }
VUELO=$(printf '%s' "$J" | jq '[.jobs[]|select(.status!="completed")]|length')
if [ "${VUELO:-0}" -gt 0 ]; then
  echo "check-run-table: ⛔ COULD NOT CHECK: ${VUELO} of ${TOT} job(s) are still running." >&2
  echo "  A table captured during a run can look complete while omitting jobs." >&2
  echo "  One run had 14 jobs at 06:12Z and 15 when finished. Rerun this check after completion." >&2
  exit 2
fi

# Use one source for the table and verdict. Independent jq expressions previously
# disagreed when the structural-skip filter was mutated: a displayed skip still
# returned 0. Write the table to a file and count the verdict from that same file.
# Check jq's rc: a valid job without steps previously failed to build a table but
# still returned CLEAN rc 0. Load the shared predicate file; absence returns 2,
# matching rather than treating all skips as substantive (9 of 11 jobs).
# Distinguish three steps states: missing and null broke jq with its own
# diagnostic; [] falsely printed CLEAN with null duration and a dash for the most
# expensive step. Report each explicitly. Keep `or` inside select: `select(A) or (B)`
# is a selection followed by disjunction, not select(A or B), and fails before the
# probe with jq's diagnostic.
SIN=$(printf '%s' "$J" | jq -r '[.jobs[]|select((has("steps")|not) or (.steps==null))|.name]|join(", ")' 2>/dev/null)
if [ -n "${SIN:-}" ]; then
  echo "check-run-table: ⛔ COULD NOT CHECK: job(s) with missing or null \`steps\` list: ${SIN}." >&2
  echo "  The JSON is incomplete; this does not prove the jobs have no steps." >&2; exit 2
fi
VACIO=$(printf '%s' "$J" | jq -r '[.jobs[]|select((.steps|type=="array") and (.steps|length==0))|.name]|join(", ")' 2>/dev/null)
if [ -n "${VACIO:-}" ]; then
  echo "check-run-table: ⛔ COULD NOT CHECK: job(s) with no steps: ${VACIO}." >&2
  echo "  An empty list leaves nothing to inspect; it does not prove the job needs no work." >&2; exit 2
fi

REGLAS="${OLIVARES_SKIPS_FILE:-scripts/lib/skips-estructurales.txt}"
[ -r "$REGLAS" ] || { echo "check-run-table: ⛔ COULD NOT CHECK: cannot read shared predicate $REGLAS." >&2
                      echo "  The predicate's maintainer publishes it. Without it, every skip would be" >&2
                      echo "  treated as substantive, making the table noisy." >&2; exit 2; }
EXENTOS=$(sed -n 's/^exacto:"\(.*\)"$/\1/p' "$REGLAS" | jq -R . | jq -s .) || {
  echo "check-run-table: ⛔ COULD NOT CHECK: could not read rules from $REGLAS." >&2; exit 2; }
NREG=$(printf '%s' "$EXENTOS" | jq 'length')
[ "${NREG:-0}" -gt 0 ] || { echo "check-run-table: ⛔ COULD NOT CHECK: $REGLAS declares no rules using \`exacto:\`." >&2; exit 2; }

FILA=$(mktemp "${TMPDIR:-/tmp}/tabla.XXXXXX") || exit 2
trap 'rm -f "$FILA"' EXIT
printf '%s' "$J" | jq -r --argjson exentos "$EXENTOS" '
  def dur(s): if s.started_at and s.completed_at
              then ((s.completed_at|fromdateiso8601) - (s.started_at|fromdateiso8601)) else 0 end;
  .jobs[] |
  . as $j |
  ([.steps[] | {n: .name, d: dur(.), c: .conclusion}]) as $st |
  ($st | map(select(.c=="skipped") | .n)) as $todos |
  # Warnings that fire everywhere hide lost coverage: run 33291332689 marked 9 of 11
  # jobs, usually because `report failure` under if: failure() and action Post Run
  # steps were structurally skipped on success. Separate these from substantive skips.
  # Declared heuristic: the API omits the if: condition for each step, so classification uses names.
  # Renaming the reporter can cause a false positive, the safe direction of error.
  # Use the shared data file from alone: the v2 embedded list omitted the exempt
  # `NOT APPLICABLE notice…` and drifted from that file, as both readers found.
  # The Post Run X ↔ Run X pairing stays in code because it is Actions-generated
  # structure, not an enumerated list; names change together across setup-go bumps.
  # Measured: all three Post Run skips paired, zero orphans.
  ([$st[].n] | map(select(startswith("Run ")))) as $runs |
  ($todos | map(select(
      . as $n
      | ($exentos | index($n) != null)
        or (($n | startswith("Post Run ")) and (($runs | index($n | sub("^Post ";""))) != null))
      | not))) as $skip |
  ($todos | length) as $nskiptot |
  ($st | max_by(.d)) as $caro |
  ([$st[].d] | add) as $suma |
  "\(.name)\t\(.conclusion // "-")\t\($suma)\t\($caro.n // "-")\t\($caro.d // 0)\t\($skip|length)\t\($nskiptot)\t\($skip|join(" · "))"
# ⛔ EL CAMPO DE TEXTO VA EL ULTIMO, y no es estetica: el TABULADOR es «IFS whitespace», asi que
# bash COLAPSA las secuencias de tabuladores en un solo delimitador. Con la lista de saltos vacia
# —el caso normal— el campo desaparecia y el contador de la derecha se leia en su sitio: el
# recuento estructural salia siempre 0 y su linea no se imprimia nunca. El sintoma parecia del
# sujeto y era del formato.
' > "$FILA" || { echo "check-run-table: ⛔ COULD NOT CHECK: jq expression failed on jobs for ${RUN}." >&2; exit 2; }
[ -s "$FILA" ] || { echo "check-run-table: ⛔ COULD NOT CHECK: table is empty despite ${TOT} job(s)." >&2; exit 2; }
CON=0
while IFS=$'\t' read -r nombre veredicto suma caro cd nskip ntot skips; do
  printf '  %-18s %-9s %5ss   slowest: %-46s %4ss\n' "$nombre" "$veredicto" "$suma" "${caro:0:46}" "$cd"
  [ "${nskip:-0}" -gt 0 ] && CON=$((CON+1))
  [ "${nskip:-0}" -gt 0 ] && printf '  %-18s   ⚠ %s NONSTRUCTURAL skipped step(s): %s\n' "" "$nskip" "$skips"
  est=$(( ${ntot:-0} - ${nskip:-0} ))
  [ "$est" -gt 0 ] && printf '  %-18s     (%s structural skipped step(s): reporter/Post Run)\n' "" "$est"
done < "$FILA"
echo "  ── ${TOT} job(s); ${CON} with nonstructural skips"
[ "${CON:-0}" -eq 0 ]
