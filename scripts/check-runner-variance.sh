#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# ¿La duración de los paquetes caros depende de QUÉ MÁQUINA cogió el job?
#
# Contexto: `an internal design note (not shipped)` deja una hipótesis con n=3 y pide
# más muestras. Este guion las acumula solo: las corridas nuevas entran sin tocar nada.
#
# ⛔ LO QUE ESTE GUION NO HACE, Y ES DELIBERADO: no dictamina. Imprime la tabla y el CONTEO DE
# MUESTRAS INDEPENDIENTES, porque el error que estuvo a punto de colarse en la hipótesis fue contar
# cinco observaciones donde había tres — `core/api`, `core/auth` y `sqlstore` salen del MISMO job y
# comparten máquina, disco y Postgres, así que se mueven juntos. Un tamaño de muestra inflado hace
# que un patrón de azar parezca un hallazgo. Aquí las dos cifras van SIEMPRE juntas.
#
# Veredictos: 0 = tabla impresa · 2 = NO HE PODIDO MIRAR (nunca 0 por silencio).
# Nunca sale 1: no es un gate, no hay nada que rehusar.
set -u
[ -n "${OLIVARES_RV_DEBUG:-}" ] && set -x

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
	REPO="${OLIVARES_RV_REPO:-${GITHUB_REPOSITORY:-}}"
	[ -n "$(repo_slug)" ] || REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null) || REPO=""
	[ -n "$(repo_slug)" ] || { echo "runner-variance: 2 NO PUDE MIRAR: no se que repositorio consultar (fija OLIVARES_RV_REPO)" >&2; exit 2; }
	printf '%s' "$(repo_slug)"
}
N="${OLIVARES_RV_RUNS:-14}"
PAQ="${OLIVARES_RV_PKGS:-modules/governance core/api core/auth core/internal/store/sqlstore}"
# MODO. `pkg` (por defecto) mide paquetes dentro de los logs; `job` mide la duracion del JOB entero
# por sus sellos de la API. El segundo existe porque la corroboracion mas fuerte de la hipotesis
# vino de `secrets` —otro trabajo, otra herramienta, sin lineas `ok pkg Ns`— y sin este modo habria
# que rehacerla a mano cada vez, que es justo lo que este guion existe para evitar.
MODO="${OLIVARES_RV_MODE:-pkg}"
# ⛔ CORTE. Sin el, la ventana esta VIVA: una corrida que termina despues de que escribas el numero
# lo cambia, y el lector reproduce otra cosa que la tuya sin que ninguno de los dos se equivoque.
# Medido el 2026-08-30: entre mi commit (14:50Z) y su lectura (15:25Z) entro `33310902759` y el
# universo paso de 18 utiles a 19. Con `OLIVARES_RV_UNTIL` el universo queda FIJO y citable.
HASTA="${OLIVARES_RV_UNTIL:-}"
JOBS_RE="${OLIVARES_RV_JOBNAMES:-race-rest race-modules}"

command -v jq >/dev/null 2>&1 || { echo "runner-variance: NO HE PODIDO MIRAR: sin jq" >&2; exit 2; }

if [ -n "${OLIVARES_RV_JOBS:-}" ]; then
  [ -r "$OLIVARES_RV_JOBS" ] || { echo "runner-variance: NO HE PODIDO MIRAR: fixture de jobs ilegible" >&2; exit 2; }
  J=$(cat "$OLIVARES_RV_JOBS")
else
  command -v gh >/dev/null 2>&1 || { echo "runner-variance: NO HE PODIDO MIRAR: sin gh y sin fixture" >&2; exit 2; }
  FILTRO='.workflow_runs[].id'
  if [ -n "$HASTA" ]; then
    date -u -d "$HASTA" +%s >/dev/null 2>&1 \
      || { echo "runner-variance: NO HE PODIDO MIRAR: OLIVARES_RV_UNTIL='${HASTA}' no es una fecha que date entienda." >&2; exit 2; }
    FILTRO=".workflow_runs[]|select(.created_at < \"${HASTA}\")|.id"
  fi
  IDS=$(gh api "repos/$(repo_slug)/actions/workflows/mainline-ci.yml/runs?per_page=${N}" --jq "$FILTRO" 2>/dev/null) \
    || { echo "runner-variance: NO HE PODIDO MIRAR: la API no devolvio las corridas" >&2; exit 2; }
  [ -n "$IDS" ] || { echo "runner-variance: NO HE PODIDO MIRAR: cero corridas en $(repo_slug)" >&2; exit 2; }
  # gh api --jq NO acepta `--args` (es de jq, no de gh): el filtro se construye aqui.
  SEL=""
  while IFS= read -r jn; do
    [ -n "$jn" ] || continue
    [ -n "$SEL" ] && SEL="$SEL or "
    SEL="${SEL}.name==\"${jn}\""
  done <<EOF
$(printf '%s\n' ${JOBS_RE})
EOF
  [ -n "$SEL" ] || { echo "runner-variance: NO HE PODIDO MIRAR: OLIVARES_RV_JOBNAMES vacio" >&2; exit 2; }
  J='{"jobs":[]}'
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    p=$(gh api "repos/$(repo_slug)/actions/runs/${id}/jobs?per_page=100" \
         --jq "[.jobs[]|select((${SEL}) and .status==\"completed\")|{run:\"${id}\",job:.name,id:.id,runner:(.runner_name // \"?\"),ini:.started_at,fin:.completed_at,conc:(.conclusion // \"?\"),rojo:([.steps[]?|select(.conclusion==\"failure\")|((.completed_at|fromdateiso8601)-(.started_at|fromdateiso8601))]|first // 0)}]" 2>/dev/null) || continue
    J=$(printf '%s' "$J" | jq --argjson p "$p" '.jobs += $p') \
      || { echo "runner-variance: NO HE PODIDO MIRAR: jq fallo al acumular" >&2; exit 2; }
  done <<EOF
$IDS
EOF
fi
printf '%s' "$J" | jq -e . >/dev/null 2>&1 || { echo "runner-variance: NO HE PODIDO MIRAR: JSON ilegible" >&2; exit 2; }
NJ=$(printf '%s' "$J" | jq '.jobs|length')
[ "${NJ:-0}" -gt 0 ] || { echo "runner-variance: NO HE PODIDO MIRAR: cero jobs cerrados de: '${JOBS_RE}'" >&2; exit 2; }

TMP=$(mktemp -d "${TMPDIR:-/tmp}/rv.XXXXXX") || exit 2
trap 'rm -rf "$TMP"' EXIT

: > "$TMP/filas"
printf '%s' "$J" | jq -r '.jobs[]|"\(.run)\t\(.job)\t\(.id)\t\(.runner)"' > "$TMP/idx"
while IFS=$'\t' read -r run job jid runner; do
  [ -n "$run" ] || continue
  if [ "$MODO" = "job" ]; then
    ini=$(printf '%s' "$J" | jq -r --arg r "$run" --arg j "$job" '.jobs[]|select(.run==$r and .job==$j)|.ini // ""')
    fin=$(printf '%s' "$J" | jq -r --arg r "$run" --arg j "$job" '.jobs[]|select(.run==$r and .job==$j)|.fin // ""')
    if [ -z "$ini" ] || [ -z "$fin" ]; then
      echo "runner-variance: NO HE PODIDO MIRAR: modo job y el job ${job} de ${run} no trae sellos." >&2; exit 2
    fi
    a=$(date -u -d "$ini" +%s 2>/dev/null) && b=$(date -u -d "$fin" +%s 2>/dev/null) \
      || { echo "runner-variance: NO HE PODIDO MIRAR: sellos ilegibles en ${job} de ${run}." >&2; exit 2; }
    cc=$(printf '%s' "$J" | jq -r --arg r "$run" --arg j "$job" '.jobs[]|select(.run==$r and .job==$j)|.conc // "?"')
    # ⛔ UN JOB QUE MUERE PRONTO PARECE LA MAQUINA MAS RAPIDA. En modo job la duracion es el reloj de
    # pared, asi que un fallo de arranque a los 30 s se ordena por delante de una corrida buena de
    # 1 500 s. Se marca el veredicto y el estadistico REHUSA si alguno de los primeros no es verde:
    # «rapido» y «murio antes de empezar» se escriben igual en una columna de segundos.
    rj=$(printf '%s' "$J" | jq -r --arg r "$run" --arg j "$job" '.jobs[]|select(.run==$r and .job==$j)|.rojo // 0')
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$job" "$runner" "$run" "$((b-a))" "$cc" "$job" "${rj:-0}" >> "$TMP/filas"
    continue
  fi
  log="$TMP/$run-$job.log"
  if [ -n "${OLIVARES_RV_LOGDIR:-}" ]; then
    [ -r "$OLIVARES_RV_LOGDIR/$run-$job.log" ] || continue
    cp "$OLIVARES_RV_LOGDIR/$run-$job.log" "$log"
  else
    gh run view --repo "$(repo_slug)" --job "$jid" --log > "$log" 2>/dev/null || continue
  fi
  # LA LINEA DE `go test` LLEVA TABS PROPIOS: el paquete cae en el CUARTO campo del log, no en el
  # tercero. Leer `[2]` da CERO coincidencias con entrada no vacia — medido, y se escribe igual que
  # «no hay datos». Por eso se recorta la cabecera job/paso y se lee lo que queda.
  awk -v run="$run" -v runner="$runner" -v job="$job" -v paq="$PAQ" '
    BEGIN{ n=split(paq,P," "); for(i=1;i<=n;i++) Q[P[i]]=1 }
    { linea=$0; sub(/^[^\t]*\t[^\t]*\t/,"",linea)
      if (linea ~ /panic: test timed out after/) { corte=1 }
      if (match(linea,/(ok|FAIL)[ \t]+[^ \t]+[ \t]+[0-9.]+s/)) {
        s=substr(linea,RSTART,RLENGTH); split(s,F,/[ \t]+/)
        p=F[2]; sub(/^github\.com\/olivaresai\/olivares\//,"",p)
        d=F[3]; sub(/s$/,"",d)
        if (p in Q) printf "%s\t%s\t%s\t%s\t%s\t%s\n", p, runner, run, d, (corte?"CORTE":"ok"), job
        corte=0
      } }' "$log" >> "$TMP/filas"
done < "$TMP/idx"

# ⛔ `cancelled` NO ES UNA MEDIDA: a ese job lo mataron, su duracion no dice cuanto tarda el trabajo,
# y por ser CORTA se ordena delante de las buenas. Medido el 2026-08-30 sobre mi propia publicacion:
# el mejor tiempo de `srv17` (1 127 s) estaba `cancelled`, y sostenia una «separacion completa» que
# yo ya habia publicado. Se excluye, y se dice cuantos se excluyeron.
#
# `failure` SI se conserva, pero ⛔ NO ES UNA SOLA CLASE, y afirmar que lo era fue un error mio.
# Medido el 2026-08-30 sobre los 11 `failure` de `secrets`, con la duracion del PASO rojo contra su
# techo de 60 min: NUEVE acabaron el barrido en 2 122-3 100 s y salieron rojos por hallazgo
# (`check-secrets: DIRTY — N finding(s)`, exit 1 -> 201), o sea son medidas COMPLETAS y exactas; solo
# DOS se quedaron clavados en 3 613 s, el techo del paso, y esos si estan censurados por arriba.
# Yo habia escrito que los once eran «reloj agotado»: falso para nueve de once.
#
# Por eso este guion NO clasifica los `failure` por su cuenta —para distinguirlos hace falta mirar el
# paso rojo, y una regla automatica se equivocaria en la direccion comoda— y en su lugar IMPRIME la
# duracion del paso rojo al lado, que es lo que permite clasificarlos a la vista.
NCANC=$(awk -F'\t' '$5=="cancelled"' "$TMP/filas" | wc -l)
if [ "${NCANC:-0}" -gt 0 ]; then
  awk -F'\t' '$5!="cancelled"' "$TMP/filas" > "$TMP/filas.f" && mv "$TMP/filas.f" "$TMP/filas"
fi
[ -s "$TMP/filas" ] || { echo "runner-variance: NO HE PODIDO MIRAR: ningun log dio duraciones para los paquetes pedidos." >&2
                         echo "  Paquetes buscados: ${PAQ}" >&2; exit 2; }

# El rotulo NOMBRA lo que se ha medido: decia «race-*» tambien cuando el job era `secrets`, y una
# tabla rotulada con un job que no es el suyo se cita como si lo fuera.
echo "runner-variance — duracion por ${MODO} y maquina (${NJ} job(s) cerrados de: ${JOBS_RE})"
[ -n "$HASTA" ] && echo "  corte: solo corridas creadas ANTES de ${HASTA} · ventana ${N}" \
                || echo "  ⚠ SIN CORTE (ventana ${N}, VIVA): el universo cambia con cada corrida nueva."
echo
sort -t"$(printf '\t')" -k1,1 -k4,4n "$TMP/filas" | awk -F'\t' '
  { if ($1!=p) { if(p!="") print ""; print "  " $1; p=$1 }
    { est=($5=="ok"||$5=="success") ? "" : "[" $5 ($7>0 ? sprintf(" paso rojo %ds", $7) : "") "]" }
    printf "      %-14s %s%9.1fs   corrida %-12s %s\n", $2, ($5=="CORTE"?">":" "), $4, $3, est }
  END{ print "" }'

OBS=$(wc -l < "$TMP/filas")
# LA UNIDAD DE ASIGNACION ES EL JOB, NO LA CORRIDA. Dos jobs distintos de la MISMA corrida en la
# MISMA maquina son DOS asignaciones: se planifican por separado y pueden coger maquinas distintas.
# Con la clave (maquina, corrida) se colapsaban en una, y en los datos de hoy coincidia por azar
# porque `srv17` no repitio corrida — lo caza la bateria, no la vista.
IND=$(cut -f3,6 "$TMP/filas" | sort -u | wc -l)
MAQ=$(cut -f2 "$TMP/filas" | sort -u | wc -l)
[ "${NCANC:-0}" -gt 0 ] && echo "  -- ${NCANC} job(s) 'cancelled' EXCLUIDOS: los mataron, su duracion no mide el trabajo."
echo "  -- ${OBS} observacion(es) sobre ${IND} asignacion(es) INDEPENDIENTE(S) (maquina x job), ${MAQ} maquina(s)"
echo "     Varios paquetes del MISMO job comparten maquina, disco y Postgres: cuentan como UNA."

# EL CONTEO QUE DECIDE LA POTENCIA NO ES EL TOTAL: es cuantas asignaciones tiene CADA maquina.
# Con 10 asignaciones repartidas 3/2/2/1/1/1 no se puede afirmar nada de la que tiene 1 — y el
# total de 10 invita a creer que si. Por eso se imprimen las dos cosas, y el aviso mira el MINIMO
# de la maquina mas observada, que es la unica sobre la que alguien va a querer concluir.
echo "  -- asignaciones independientes POR maquina:"
cut -f2,3,6 "$TMP/filas" | sort -u | cut -f1 | sort | uniq -c | sort -rn \
  | while read -r c m; do printf "       %-14s %s\n" "$m" "$c"; done

# ⛔ AQUI HABIA UN AVISO POR TAMANO DE MUESTRA («con menos de 8 no hay potencia») Y ERA DEMASIADO
# ROMO: no miraba el EFECTO. Medido el 2026-08-30 sobre `secrets`, 20 asignaciones y 7 maquinas:
# `srv17` ocupaba los rangos 1-3 con separacion COMPLETA (su peor tiempo por debajo del mejor de
# todas las demas), y con n=3 eso sale a 1/C(20,3). El aviso decia «no hay potencia» sobre un dato
# que ya separaba. Un umbral sobre n contesta «cuantas muestras hay», no «que dicen».
#
# Lo que se imprime ahora es el numero exacto, y SOLO en el caso estrecho en que se puede afirmar:
# una maquina que ocupa los K primeros puestos SIN que nadie se le cuele. Si no hay separacion
# limpia, no se imprime probabilidad ninguna — que es lo correcto, no lo comodo.
# ⛔ LA CONDICION ES «UN SOLO GRUPO», NO «MODO JOB». Con dos nombres de job, `race-rest` y
# `race-modules` se ordenarian en la MISMA lista pese a durar cosas distintas — el mismo error que
# comparar paquetes entre si, que este guion ya evitaba en modo `pkg`. Lo destapo ir a medir los
# `race-*` con la version anterior: habria dado una cifra, y falsa.
if [ "$(cut -f1 "$TMP/filas" | sort -u | wc -l)" = "1" ]; then
  # ⛔ EL ESTADISTICO QUE CONTESTA SIEMPRE, y el que de verdad decide la palanca: ¿la variacion vive
  # ENTRE maquinas o DENTRO de una? Si entre, la palanca es la flota; si dentro, la maquina no es la
  # causa y etiquetarla no arregla nada. La separacion de mas abajo solo dispara en un caso estrecho;
  # esto habla aunque no haya separacion. Medido el 2026-08-30: en `secrets` manda ENTRE (2,51x
  # frente a 1,40x) y en los dos `race-*` manda DENTRO (1,36x/1,12x frente a 2,00x/1,72x) — o sea la
  # hipotesis de flota valia para uno de los tres trabajos, y NO para los dos que cortan.
  sort -t"$(printf '\t')" -k2,2 -k4,4n "$TMP/filas" | awk -F'\t' '
    { d[$2]=d[$2]" "$4; n[$2]++ }
    END{ entre_min=1e18; entre_max=0; dentro=0; dentro_m=""
         for (m in d) {
           k=split(d[m],a," "); mid=(k%2)?a[(k+1)/2]:(a[k/2]+a[k/2+1])/2
           if (n[m]>=2) { if(mid<entre_min) entre_min=mid; if(mid>entre_max) entre_max=mid }
           if (n[m]>=3) { r=a[k]/a[1]; if(r>dentro){dentro=r; dentro_m=m} }
         }
         if (entre_max>0 && entre_min<1e18 && dentro>0) {
           e=entre_max/entre_min
           printf "  -- ENTRE maquinas (medianas, >=2 asignaciones): %.2fx  ·  DENTRO de una (>=3): %.2fx (%s)\n", e, dentro, dentro_m
           if (e > dentro) print  "     Manda la MAQUINA: la palanca es la flota."
           else            print  "     Manda la CORRIDA: la variacion vive DENTRO de la misma maquina, asi que"
           if (e <= dentro) print "     etiquetar o retirar maquinas NO arreglaria esto."
         } else print "  -- sin suficientes repeticiones por maquina para separar ENTRE de DENTRO." }'

  sort -t"$(printf '\t')" -k4,4n "$TMP/filas" | cut -f2,5 | awk -F'\t' -v maq="$MAQ" '
    { r[NR]=$1; v[NR]=$2 }
    END{
      lider=r[1]; k=0
      for(i=1;i<=NR;i++){ if(r[i]==lider) k++; else break }
      total=0; for(i=1;i<=NR;i++) if(r[i]==lider) total++
      if (k!=total || k==NR) { print "     (sin separacion limpia: no se afirma probabilidad)"; exit }
      for(i=1;i<=k;i++) if (v[i]!="ok" && v[i]!="success") {
        printf "     (los %d primeros incluyen un job '\''%s'\'': no se afirma probabilidad, porque un\n", k, v[i]
        print  "      fallo temprano se ordena como si fuera rapido)"; exit }
      c=1; for(i=0;i<k;i++) c=c*(NR-i)/(i+1)
      pp=1/c
      printf "     SEPARACION COMPLETA: %s ocupa los %d primeros de %d, sin que nadie se cuele.\n", lider, k, NR
      printf "     Bajo la nula de asignacion al azar: p = 1/%d = %.4f%%.\n", c, pp*100
      printf "     Corregido por haber ELEGIDO esa maquina despues de mirar (x%d maquinas): %.2f%%.\n", maq, pp*maq*100
      print  "     Supone que la asignacion es al azar; si el planificador prefiere una maquina, no lo es."
    }'
fi
exit 0
