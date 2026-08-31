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
	[ -n "$(repo_slug)" ] || { echo "check-run-table: 2 NO PUDE MIRAR: no se que repositorio consultar (fija OLIVARES_REPO)" >&2; exit 2; }
	printf '%s' "$(repo_slug)"
}
[ -n "$RUN" ] || { echo "uso: $0 <run-id>" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: sin jq" >&2; exit 2; }

if [ -n "${OLIVARES_TABLA_JSON:-}" ]; then
  [ -r "$OLIVARES_TABLA_JSON" ] || { echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: fixture ilegible" >&2; exit 2; }
  J=$(cat "$OLIVARES_TABLA_JSON")
else
  command -v gh >/dev/null 2>&1 || { echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: sin gh" >&2; exit 2; }
  J=$(gh api "repos/$(repo_slug)/actions/runs/${RUN}/jobs" --paginate 2>/dev/null) \
    || { echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: la API no devolvio los jobs de ${RUN}" >&2; exit 2; }
fi
printf '%s' "$J" | jq -e . >/dev/null 2>&1 || { echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: JSON ilegible" >&2; exit 2; }

# ⛔ EL RUN SE PREGUNTA AL RUN, NO A SUS JOBS. Mirar solo `/jobs` confunde «todos los jobs que hay
# AHORA han terminado» con «el run ha CERRADO», y son cosas distintas: GitHub materializa los jobs
# por tandas. Lo midio — **14 jobs a las 06:12Z y 15 al cerrar** — y una tabla tomada en esa
# ventana enseña un job de menos y **se lee igual que una completa**. Mi guarda de «en vuelo»
# tenia dentro el agujero que esa guarda existe para tapar. Lo cazo the reviewer (A-01).
if [ -n "${OLIVARES_TABLA_JSON:-}" ]; then
  EST=$(printf '%s' "$J" | jq -r '.run.status // empty')
  [ -n "$EST" ] || { echo "check-run-table: ⛔ NO HE PODIDO MIRAR: el fixture no trae \`run.status\`." >&2
                     echo "  Sin el estado del RUN no se puede distinguir «cerrado» de «los jobs de esta tanda»." >&2; exit 2; }
else
  EST=$(gh api "repos/$(repo_slug)/actions/runs/${RUN}" --jq '.status' 2>/dev/null)     || { echo "check-run-table: ⛔ NO HE PODIDO MIRAR: la API no devolvio el run ${RUN}." >&2; exit 2; }
fi
if [ "$EST" != "completed" ]; then
  echo "check-run-table: ⛔ NO HE PODIDO MIRAR: el run ${RUN} esta en '${EST}', no 'completed'." >&2
  echo "  GitHub materializa los jobs por tandas: los que ya existen pueden estar todos terminados" >&2
  echo "  y faltar otros. Re-corre cuando el RUN cierre." >&2
  exit 2
fi

TOT=$(printf '%s' "$J" | jq '.jobs|length')
[ "${TOT:-0}" -gt 0 ] || { echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: cero jobs en ${RUN}" >&2; exit 2; }
VUELO=$(printf '%s' "$J" | jq '[.jobs[]|select(.status!="completed")]|length')
if [ "${VUELO:-0}" -gt 0 ]; then
  echo "modo-tabla: ⛔ NO HE PODIDO MIRAR: ${VUELO} de ${TOT} job(s) siguen en vuelo." >&2
  echo "  Una tabla tomada en vuelo enseña menos jobs y se lee igual que una completa" >&2
  echo "  (una medida en vuelo dio 14 jobs a las 06:12Z y 15 al cerrar). Re-corre cuando el run cierre." >&2
  exit 2
fi

# ⛔ UNA SOLA FUENTE PARA LA TABLA Y PARA EL VEREDICTO. La version anterior imprimia la tabla con
# una expresion jq y contaba los jobs con saltos con OTRA, independiente. Las dos podian discrepar
# —y discrepaban: al mutar el filtro de saltos estructurales, la tabla marcaba el salto y el
# veredicto seguia saliendo 0, o sea **el rc no venia de lo que se habia impreso**. Ahora la tabla
# se escribe a un fichero y el veredicto se CUENTA DE ESE MISMO FICHERO: lo que se ve es lo que se
# juzga. Lo destapo su propio mutante, que es para lo que estan.
# ⛔ EL rc DE `jq` SE COMPRUEBA. Un job valido SIN `steps` hacia fallar la expresion, su rc se
# perdia y el guion terminaba «CLEAN rc 0» sobre una tabla que no se habia podido construir: un
# «no pude mirar» disfrazado de limpio, la familia que este carril lleva toda la noche cazando.
# ⛔ EL PREDICADO COMPARTIDO SE CARGA DE SU FICHERO, Y SU AUSENCIA ES rc 2 — NO «sin reglas».
# Sin reglas, TODO salto seria sustantivo y esta tabla volveria a ser el ruido que quitamos: nueve
# de once jobs marcados. Y alguien la «arreglaria» ignorandolos. Misma decision que tomo en
# su consumidor, para que los dos fallen igual ante el mismo fichero roto.
# ⛔ TRES ESTADOS DE `steps`, NO DOS. Me lo enseño midiendo el suyo, y al medir el mio el
# tercero era el que se colaba: `steps` AUSENTE y `steps: null` reventaban `jq` —rc 2 correcto pero
# con el error de `jq` como mensaje, no con un diagnostico mio— y `steps: []` salia **CLEAN con una
# fila basura**: `nulls` de duracion y `-` de paso mas caro, o sea un job que se lee como barato y
# sano. Colapsar los tres en dos es donde se pierde el aviso.
# ⛔ EL `or` DENTRO DEL `select`, no fuera: `select(A) or (B)` no es «A o B» — es un `select(A)`
# seguido de una disyuncion, la expresion no hace lo que parece y `jq` revienta ANTES de mi sonda,
# devolviendo su propio error como mensaje. Sintoma: rc 2 correcto con diagnostico ajeno.
SIN=$(printf '%s' "$J" | jq -r '[.jobs[]|select((has("steps")|not) or (.steps==null))|.name]|join(", ")' 2>/dev/null)
if [ -n "${SIN:-}" ]; then
  echo "check-run-table: ⛔ NO HE PODIDO MIRAR: job(s) sin lista \`steps\` (ausente o null): ${SIN}." >&2
  echo "  El JSON esta incompleto; no es que el job no tenga pasos." >&2; exit 2
fi
VACIO=$(printf '%s' "$J" | jq -r '[.jobs[]|select((.steps|type=="array") and (.steps|length==0))|.name]|join(", ")' 2>/dev/null)
if [ -n "${VACIO:-}" ]; then
  echo "check-run-table: ⛔ NO HE PODIDO MIRAR: job(s) con CERO pasos: ${VACIO}." >&2
  echo "  Una lista vacia no es un job barato: es un job del que no hay nada que leer." >&2; exit 2
fi

REGLAS="${OLIVARES_SKIPS_FILE:-scripts/lib/skips-estructurales.txt}"
[ -r "$REGLAS" ] || { echo "check-run-table: ⛔ NO HE PODIDO MIRAR: no leo el predicado compartido $REGLAS." >&2
                      echo "  Lo publica el carril que mantiene el predicado. Sin el, todo salto seria" >&2
                      echo "  sustantivo y la tabla volveria a ser ruido." >&2; exit 2; }
EXENTOS=$(sed -n 's/^exacto:"\(.*\)"$/\1/p' "$REGLAS" | jq -R . | jq -s .) || {
  echo "check-run-table: ⛔ NO HE PODIDO MIRAR: no pude leer las reglas de $REGLAS." >&2; exit 2; }
NREG=$(printf '%s' "$EXENTOS" | jq 'length')
[ "${NREG:-0}" -gt 0 ] || { echo "check-run-table: ⛔ NO HE PODIDO MIRAR: $REGLAS no trae ninguna regla \`exacto:\`." >&2; exit 2; }

FILA=$(mktemp "${TMPDIR:-/tmp}/tabla.XXXXXX") || exit 2
trap 'rm -f "$FILA"' EXIT
printf '%s' "$J" | jq -r --argjson exentos "$EXENTOS" '
  def dur(s): if s.started_at and s.completed_at
              then ((s.completed_at|fromdateiso8601) - (s.started_at|fromdateiso8601)) else 0 end;
  .jobs[] |
  . as $j |
  ([.steps[] | {n: .name, d: dur(.), c: .conclusion}]) as $st |
  ($st | map(select(.c=="skipped") | .n)) as $todos |
  # ⛔ UN AVISO QUE SE ENCIENDE EN TODO NO INFORMA. Medido sobre la corrida 33291332689: el aviso
  # de «pasos saltados» salta en **9 de 11 jobs**, y casi siempre por `report failure` (guardado
  # con `if: failure()`, o sea saltado JUSTAMENTE porque el job fue bien) y por los `Post Run` de
  # las actions. Esos saltos son ESTRUCTURALES: su presencia es la prueba de que todo fue bien.
  # Mezclarlos con los que sí cuestan cobertura convierte la senal en ruido — la misma clase que
  # las 376 issues abiertas que nadie lee.
  #
  # ⚠ HEURISTICA DECLARADA: la API no devuelve el `if:` de cada paso, asi que la separacion es POR
  # NOMBRE. Si alguien renombra el informador, su salto pasara a contarse como perdida de
  # cobertura — falso positivo, no falso negativo, que es el lado correcto para equivocarse.
  # ⛔ UNA SOLA FUENTE PARA EL PREDICADO COMPARTIDO. La v2 mantenia un literal EMBEBIDO ademas del
  # fichero de datos de y dos fuentes derivan: el falso positivo reproducible era
  # `NOT APPLICABLE notice…`, que su fichero exime y mi lista no tenia. Lo cazaron los dos lectores
  # a la vez, cada uno desde su lado. Aqui no queda NINGUN nombre embebido.
  #
  # El emparejamiento `Post Run X` ↔ `Run X` SI vive en codigo, y no es una excepcion a lo anterior:
  # no es una lista de nombres, es una ESTRUCTURA que Actions genera. Una lista tendria que crecer
  # con cada bump de `setup-go`; el emparejamiento sobrevive al bump porque los dos nombres cambian
  # juntos. Medido: 3 de 3 saltos `Post Run` emparejados, 0 huerfanos.
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
' > "$FILA" || { echo "check-run-table: ⛔ NO HE PODIDO MIRAR: la expresión jq falló sobre los jobs de ${RUN}." >&2; exit 2; }
[ -s "$FILA" ] || { echo "check-run-table: ⛔ NO HE PODIDO MIRAR: la tabla salió vacía con ${TOT} job(s)." >&2; exit 2; }
CON=0
while IFS=$'\t' read -r nombre veredicto suma caro cd nskip ntot skips; do
  printf '  %-18s %-9s %5ss   más caro: %-46s %4ss\n' "$nombre" "$veredicto" "$suma" "${caro:0:46}" "$cd"
  [ "${nskip:-0}" -gt 0 ] && CON=$((CON+1))
  [ "${nskip:-0}" -gt 0 ] && printf '  %-18s   ⚠ %s salto(s) NO estructural(es): %s\n' "" "$nskip" "$skips"
  est=$(( ${ntot:-0} - ${nskip:-0} ))
  [ "$est" -gt 0 ] && printf '  %-18s     (%s salto(s) estructural(es): informador/Post Run)\n' "" "$est"
done < "$FILA"
echo "  ── ${TOT} job(s); ${CON} con saltos NO estructurales"
[ "${CON:-0}" -eq 0 ]
