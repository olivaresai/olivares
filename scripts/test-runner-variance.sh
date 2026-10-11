#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bateria de scripts/check-runner-variance.sh. No toca la red: todo por fixture.
set -u
SUT="${SUT:-scripts/check-runner-variance.sh}"
PASS=0; FAIL=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/trv.XXXXXX") || exit 2
trap 'rm -rf "$TMP"' EXIT
check(){ local n="$1" e="$2" g="$3"
  if [ "$e" = "$g" ]; then PASS=$((PASS+1)); printf 'ok   %-62s %s\n' "$n" "$g"
  else FAIL=$((FAIL+1)); printf 'FAIL %s expected [%s], got [%s]\n' "$n" "$e" "$g"; fi; }

# Una linea de log de GitHub: job \t paso \t <sello> ok  \t paquete \t Ns   (el paquete va en el
# CUARTO campo porque `go test` mete sus PROPIOS tabs; esa es la trampa que este fichero fija).
linea(){ printf '%s\t%s\t2026-08-30T06:00:00.0Z %s  \t%s\t%ss\n' "$1" "race" "$2" "$3" "$4"; }

mkdir -p "$TMP/logs"
{ linea race-modules ok github.com/olivaresai/olivares/modules/governance 1600.0; } > "$TMP/logs/R1-race-modules.log"
{ linea race-rest ok github.com/olivaresai/olivares/core/api 800.0
  linea race-rest ok github.com/olivaresai/olivares/core/auth 810.0; } > "$TMP/logs/R1-race-rest.log"
{ linea race-modules ok github.com/olivaresai/olivares/modules/governance 7900.0; } > "$TMP/logs/R2-race-modules.log"
cat > "$TMP/jobs.json" <<J
{"jobs":[{"run":"R1","job":"race-modules","id":1,"runner":"srv17"},
         {"run":"R1","job":"race-rest","id":2,"runner":"srv17"},
         {"run":"R2","job":"race-modules","id":3,"runner":"ci-runner-9"}]}
J
corre(){ OLIVARES_RV_JOBS="${1:-$TMP/jobs.json}" OLIVARES_RV_LOGDIR="${2:-$TMP/logs}" \
         bash "${3:-$SUT}" > "$TMP/out" 2>&1; echo $?; }

check "(1) complete fixture -> rc 0" 0 "$(corre)"
check "(1b) package comes from the FOURTH field and is not lost" 0 "$( grep -q 'modules/governance' "$TMP/out"; echo $? )"
check "(1c) sorts by duration within the package" 0 \
  "$( awk '/modules\/governance/{f=1;next} f&&/srv17/{print "ok";exit} f&&/ci-runner-9/{print "mal";exit}' "$TMP/out" | grep -qx ok; echo $? )"

# 2 · LA CUENTA QUE ESTE GUION EXISTE PARA PROTEGER: dos paquetes del MISMO job son UNA asignacion.
check "(2) 4 observations across 3 independent assignments" 0 \
  "$( grep -q '4 observation(s) across 3 independent assignment(s)' "$TMP/out"; echo $? )"
check "(2b) srv17 is reported with 2, not 3" 0 \
  "$( grep -qE '^ +srv17 +2$' "$TMP/out"; echo $? )"
# (3) La guarda que sustituye al viejo aviso por tamano de muestra: en modo `pkg` con VARIOS
# paquetes las filas NO son comparables entre si (un paquete de 800 s y otro de 8 s no compiten por
# el mismo puesto), asi que no se afirma probabilidad ninguna. El aviso viejo decia «con menos de 8
# no hay potencia» y era romo: sobre `secrets` habia separacion COMPLETA con n=3.
check "(3) pkg mode with multiple packages: does NOT claim probability" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "compared different packages" || echo 0 )"
check "(3b) does not invent separation between packages" 0 \
  "$( grep -q 'COMPLETE SEPARATION' "$TMP/out" && echo "claimed separation across packages" || echo 0 )"

# 4 · NUNCA 0 POR SILENCIO: logs legibles pero sin ninguno de los paquetes pedidos.
check "(4) no package matches -> 2, not 0 with an empty table" 2 \
  "$( OLIVARES_RV_PKGS="no/existe" corre )"
check "(4b) message reports WHAT was searched" 0 "$( grep -q 'Packages requested: no/existe' "$TMP/out"; echo $? )"

echo '{"jobs":[]}' > "$TMP/vacio.json"
check "(5) zero completed jobs -> 2" 2 "$(corre "$TMP/vacio.json")"
check "(6) unreadable fixture -> 2, not assumed empty" 2 "$(corre "$TMP/no-existe.json")"
printf 'no soy json' > "$TMP/roto.json"
check "(7) unreadable JSON -> 2" 2 "$(corre "$TMP/roto.json")"

# 8 · el corte se detecta por el panic, y NO se pega al paquete siguiente
{ printf 'race-modules\trace\t2026-08-30T06:00:00.0Z panic: test timed out after 2h30m0s\n'
  linea race-modules FAIL github.com/olivaresai/olivares/modules/governance 9000.5
  linea race-modules ok github.com/olivaresai/olivares/core/api 100.0; } > "$TMP/logs/R2-race-modules.log"
corre >/dev/null
check "(8) the truncated result is marked with '>'" 0 "$( grep -qE '> +9000\.5s' "$TMP/out"; echo $? )"
check "(8b) the NEXT package does not inherit the marker" 0 \
  "$( grep -qE '> +100\.0s' "$TMP/out" && echo "inherited truncation" || echo 0 )"

# 9 · MUTANTE · leer el TERCER campo (la trampa de los tabs de `go test`)
sed 's|sub(/\^\[\^\\t\]\*\\t\[\^\\t\]\*\\t/,"",linea)|linea=$3|' "$SUT" > "$TMP/m3.sh"
check "(9a) third-field mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/m3.sh" && echo 1 || echo 0 )"
M3=$(corre "$TMP/jobs.json" "$TMP/logs" "$TMP/m3.sh")
check "(9b) MUTANT 'read the third field' is DETECTED by its rc" 0 "$( [ "$M3" = "2" ] && echo 0 || echo "returned $M3" )"
check "(9c) and by its MESSAGE: reports that no log provided durations" 0 \
  "$( grep -q 'no log provided durations' "$TMP/out"; echo $? )"

# 10 · MUTANTE · contar observaciones como si fueran independientes (quitar el dedup)
sed 's|IND=$(cut -f3,6 "$TMP/filas" \| sort -u \| wc -l)|IND=$(wc -l < "$TMP/filas")|' "$SUT" > "$TMP/mi.sh"
check "(10a) dedup mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mi.sh" && echo 1 || echo 0 )"
MI=$(corre "$TMP/jobs.json" "$TMP/logs" "$TMP/mi.sh")
check "(10b) MUTANT 'every observation is independent' does NOT change rc" 0 "$( [ "$MI" = "0" ] && echo 0 || echo "returned $MI" )"
check "(10c) ONLY its MESSAGE detects it: inflates 3 assignments to 5" 0 \
  "$( grep -q 'across 5 independent assignment(s)' "$TMP/out"; echo $? )"

# 11 · MODO JOB · la duracion sale de los SELLOS, sin log, y sirve para jobs sin lineas `ok pkg Ns`
cat > "$TMP/sellos.json" <<J
{"jobs":[{"run":"R1","job":"secrets","id":1,"runner":"srv17","ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:26:27Z"},
         {"run":"R2","job":"secrets","id":2,"runner":"ci-runner-7","ini":"2026-08-30T07:00:00Z","fin":"2026-08-30T08:06:18Z"}]}
J
J11=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sellos.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(11) job mode -> rc 0 WITHOUT any log" 0 "$J11"
check "(11b) duration comes from timestamps (1587 s)" 0 "$( grep -qE '1587\.0s' "$TMP/out"; echo $? )"
check "(11c) counts 2 assignments, one per machine" 0 \
  "$( grep -q 'across 2 independent assignment(s)' "$TMP/out"; echo $? )"

# 12 · MODO JOB sin sellos: es 2, NO una duracion inventada de cero
cat > "$TMP/sinsellos.json" <<J
{"jobs":[{"run":"R1","job":"secrets","id":1,"runner":"srv17"}]}
J
J12=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sinsellos.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(12) job mode without timestamps -> 2, not 0 s" 2 "$J12"
check "(12b) message NAMES the job and run" 0 \
  "$( grep -q 'job secrets in R1 has no timestamps' "$TMP/out"; echo $? )"

# 13 · MUTANTE · en modo job, dar por buena la ausencia de sellos (la clase «0 por silencio»)
sed 's|echo "runner-variance: COULD NOT CHECK: job mode, but job ${job} in ${run} has no timestamps." >&2; exit 2|ini="2026-01-01T00:00:00Z"; fin="2026-01-01T00:00:00Z"|' "$SUT" > "$TMP/ms.sh"
check "(13a) timestamp mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/ms.sh" && echo 1 || echo 0 )"
M13=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sinsellos.json" bash "$TMP/ms.sh" > "$TMP/out" 2>&1; echo $? )
check "(13b) MUTANT 'missing timestamps mean 0 s' is DETECTED by its rc" 0 "$( [ "$M13" = "0" ] && echo 0 || echo "returned $M13" )"
check "(13c) and by its MESSAGE: prints a nonexistent 0.0s duration" 0 \
  "$( grep -qE '0\.0s' "$TMP/out"; echo $? )"

# 14 · SEPARACION COMPLETA: la maquina rapida ocupa los K primeros SIN que nadie se cuele
sep(){ printf '{"jobs":[' > "$TMP/sep.json"; local i=0
  for e in "$@"; do i=$((i+1)); [ "$i" -gt 1 ] && printf ',' >> "$TMP/sep.json"
    m="${e%%:*}"; d="${e##*:}"
    printf '{"run":"R%s","job":"secrets","id":%s,"runner":"%s","conc":"success","ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:%02d:%02dZ"}' \
      "$i" "$i" "$m" "$((d/60))" "$((d%60))" >> "$TMP/sep.json"
  done; printf ']}' >> "$TMP/sep.json"; }
sep srv17:100 srv17:110 lento-a:300 lento-b:400 lento-c:500
RC14=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sep.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(14) complete separation -> rc 0" 0 "$RC14"
check "(14b) NAMES the separation with both numbers" 0 \
  "$( grep -q 'srv17 occupies the first 2 of 5' "$TMP/out"; echo $? )"
check "(14c) reports p = 1/C(5,2) = 1/10" 0 "$( grep -q 'p = 1/10 ' "$TMP/out"; echo $? )"
check "(14d) corrects for selecting the machine after inspecting results" 0 \
  "$( grep -q 'Adjusted for selecting that host' "$TMP/out"; echo $? )"

# 14e · Y SIN veredicto declarado tampoco se afirma: un job de conclusion desconocida no lidera.
sed 's/"conc":"success"/"conc":"?"/g' "$TMP/sep.json" > "$TMP/sinv.json"
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sinv.json" bash "$SUT" > "$TMP/out" 2>&1
check "(14e) unknown verdict => NO probability claim" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "claimed probability with an unknown conclusion" || echo 0 )"

# 14f · DOS grupos (dos jobs distintos) NO se ordenan juntos, aunque el modo sea `job`.
python3 - "$TMP/sep.json" > "$TMP/dosjobs.json" <<'PYEOF'
import json,sys
d=json.load(open(sys.argv[1]))
for i,j in enumerate(d["jobs"]): j["job"]="race-rest" if i%2 else "race-modules"
print(json.dumps(d))
PYEOF
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/dosjobs.json" bash "$SUT" > "$TMP/out" 2>&1
check "(14f) different jobs: NOT sorted in the same list" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "compared race-rest with race-modules" || echo 0 )"

# 15 · SIN separacion limpia: un intruso entre los rapidos. NO se afirma probabilidad ninguna.
sep srv17:100 intruso:110 srv17:120 lento-b:400 lento-c:500
RC15=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sep.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(15) no clean separation -> still rc 0" 0 "$RC15"
check "(15b) REPORTS it" 0 "$( grep -q 'no clear separation' "$TMP/out"; echo $? )"
check "(15c) does NOT print any probability" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "claimed p without separation" || echo 0 )"

# 16 · MUTANTE · quitar la comprobacion de separacion: afirma una p donde no la hay
sed 's|if (k!=total \|\| k==NR) { print "     (no clear separation: no probability claimed)"; exit }|if (0) { }|' "$SUT" > "$TMP/msep.sh"
check "(16a) separation mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/msep.sh" && echo 1 || echo 0 )"
M16=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sep.json" bash "$TMP/msep.sh" > "$TMP/out" 2>&1; echo $? )
check "(16b) MUTANT 'without checking separation' does NOT change rc" 0 "$( [ "$M16" = "0" ] && echo 0 || echo "returned $M16" )"
check "(16c) ONLY its MESSAGE detects it: claims a p for data containing an interloper" 0 \
  "$( grep -q 'p = 1/' "$TMP/out"; echo $? )"

# 17 · UN `cancelled` RAPIDO NO ES UNA MEDIDA: sale del universo antes de ordenar.
#      Este caso existe porque el defecto fue REAL y ya estaba publicado: el mejor tiempo de srv17
#      (1 127 s) estaba `cancelled` y sostenia una «separacion completa» de tres puestos.
cancel(){ printf '{"jobs":[' > "$TMP/can.json"; local i=0
  for e in "$@"; do i=$((i+1)); [ "$i" -gt 1 ] && printf ',' >> "$TMP/can.json"
    m="${e%%:*}"; rest="${e#*:}"; d="${rest%%:*}"; c="${rest##*:}"
    printf '{"run":"R%s","job":"secrets","id":%s,"runner":"%s","conc":"%s","ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:%02d:%02dZ"}' \
      "$i" "$i" "$m" "$c" "$((d/60))" "$((d%60))" >> "$TMP/can.json"
  done; printf ']}' >> "$TMP/can.json"; }
cancel rapida:60:cancelled srv17:100:success srv17:110:success lento-a:300:success lento-b:400:failure
C17=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(17) a fast cancelled job -> rc 0" 0 "$C17"
check "(17b) DECLARES how many were excluded" 0 "$( grep -q "1 cancelled job(s) excluded" "$TMP/out"; echo $? )"
check "(17c) cancelled-job machine disappears from the table" 0 \
  "$( grep -q 'rapida' "$TMP/out" && echo "cancelled is still ranked" || echo 0 )"
check "(17d) separation is calculated across 4, not 5" 0 \
  "$( grep -q 'the first 2 of 4' "$TMP/out"; echo $? )"
check "(17e) the slow 'failure' IS retained (a censored slow tail)" 0 \
  "$( grep -q 'lento-b' "$TMP/out"; echo $? )"

# 18 · MUTANTE · no excluir los cancelled: el rapido lidera y la conclusion cambia
sed 's|^NCANC=$(awk -F.\\t. .$5=="cancelled". "$TMP/filas" \| wc -l)|NCANC=0|' "$SUT" > "$TMP/mc.sh"
check "(18a) cancelled mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mc.sh" && echo 1 || echo 0 )"
M18=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$TMP/mc.sh" > "$TMP/out" 2>&1; echo $? )
check "(18b) MUTANT 'cancelled counts' does NOT change rc" 0 "$( [ "$M18" = "0" ] && echo 0 || echo "returned $M18" )"
check "(18c) ONLY its MESSAGE detects it: cancelled reappears ranked first" 0 \
  "$( grep -q 'rapida' "$TMP/out"; echo $? )"

# 19 · ENTRE vs DENTRO: el estadistico que contesta aunque NO haya separacion limpia.
#      Caso A: la maquina manda (medianas muy separadas, repeticiones muy juntas).
cancel A:100:success A:105:success A:110:success B:400:success B:410:success B:420:success
A19=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(19) between/within -> rc 0" 0 "$A19"
check "(19b) separated medians and close repeats: MACHINE dominates" 0 \
  "$( grep -q 'Host variation dominates' "$TMP/out"; echo $? )"
#      Caso B: la corrida manda (la misma maquina abarca todo el rango).
cancel A:100:success A:400:success A:250:success B:200:success B:260:success B:230:success
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1
check "(19c) one machine spans the range: RUN dominates" 0 \
  "$( grep -q 'Run variation dominates' "$TMP/out"; echo $? )"
check "(19d) reports the consequence as well as the verdict" 0 \
  "$( grep -q 'labeling or removing hosts would not fix this' "$TMP/out"; echo $? )"
#      Caso C: sin repeticiones no se afirma nada.
cancel A:100:success B:200:success C:300:success
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1
check "(19e) without repeats per machine, NO between/within claim" 0 \
  "$( grep -q 'insufficient repetitions' "$TMP/out"; echo $? )"

# 20 · MUTANTE · invertir la comparacion entre/dentro: diria «la flota» donde manda la corrida
sed 's|if (e > dentro)|if (e < dentro)|' "$SUT" > "$TMP/mev.sh"
check "(20a) comparison mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mev.sh" && echo 1 || echo 0 )"
cancel A:100:success A:105:success A:110:success B:400:success B:410:success B:420:success
M20=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$TMP/mev.sh" > "$TMP/out" 2>&1; echo $? )
check "(20b) MUTANT 'inverted comparison' does NOT change rc" 0 "$( [ "$M20" = "0" ] && echo 0 || echo "returned $M20" )"
check "(20c) ONLY its MESSAGE detects it: reports RUN where the machine dominates" 0 \
  "$( grep -q 'Run variation dominates' "$TMP/out"; echo $? )"

# 21 · F-01 · un `failure` NO es una sola clase: la fila ensena la duracion del PASO rojo, que es lo
#      que distingue «acabo y salio rojo» de «lo mato el techo». Nueve de once `failure` de `secrets`
#      habian acabado el barrido; yo los habia declarado a los once «reloj agotado».
cat > "$TMP/rojo.json" <<J
{"jobs":[{"run":"R1","job":"secrets","id":1,"runner":"A","conc":"success","rojo":0,"ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:10:00Z"},
         {"run":"R2","job":"secrets","id":2,"runner":"B","conc":"failure","rojo":2496,"ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:48:00Z"},
         {"run":"R3","job":"secrets","id":3,"runner":"C","conc":"failure","rojo":3613,"ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T07:06:00Z"}]}
J
R21=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/rojo.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(21) fixture with failed steps -> rc 0" 0 "$R21"
check "(21b) completed job shows its short failed step" 0 "$( grep -q 'failure failed step 2496s' "$TMP/out"; echo $? )"
check "(21c) cap-terminated job shows its different failed step" 0 "$( grep -q 'failure failed step 3613s' "$TMP/out"; echo $? )"
check "(21d) passing job has NO failed-step annotation" 0 \
  "$( grep -qE 'A .*failed step' "$TMP/out" && echo "annotated a failed step on a passing job" || echo 0 )"

# 22 · EL SELECTOR DE NOMBRES, que hasta hoy no lo ejercitaba NADIE. El camino de fixture entra
#      por `OLIVARES_RV_JOBS` y salta el filtro entero, asi que cuando `race-modules` se partio en
#      una matriz —y la API paso a devolver `race-modules (partition N)`— esta bateria habria
#      seguido verde mientras el censo perdia la pata mas cara del arbol SIN DECIRLO. `--filter`
#      existe para que el filtro real se pueda mirar sin red.
cat > "$TMP/api.json" <<J
{"jobs":[{"name":"race-modules (partition 3)"},{"name":"race-modules"},{"name":"race-rest"},
         {"name":"race-core"},{"name":"web"},{"name":"race-hot"},
         {"name":"race-modules-extra"},{"name":"race-restricted"}]}
J
bash "$SUT" --filter "$TMP/api.json" > "$TMP/filtro" 2>&1
check "(22) filter accepts the MATRIX name" 0 \
  "$( grep -qxF 'race-modules (partition 3)' "$TMP/filtro"; echo $? )"
check "(22b) still accepts the name without a matrix" 0 \
  "$( grep -qxF 'race-modules' "$TMP/filtro"; echo $? )"
check "(22c) discards jobs outside -race legs" 0 \
  "$( grep -qxE 'web|race-hot' "$TMP/filtro" && echo "included an unrelated job" || echo 0 )"
check "(22d) all three declared legs are present" 4 "$( grep -c . "$TMP/filtro" )"
check "(22f) a prefix does not match a longer name" 0 \
  "$( grep -qxE 'race-modules-extra|race-restricted' "$TMP/filtro" && echo "matched a prefix" || echo 0 )"

# 22e · CONTROL POSITIVO. Con el predicado de antes —igualdad exacta— el nombre de la matriz
#       desaparece: eso es lo que estaba a punto de pasar, y lo que (22) mide de verdad.
sed 's/matrix_leg(\$j)/false/' "$SUT" > "$TMP/m22.sh"
if cmp -s "$SUT" "$TMP/m22.sh"; then
  check "(22e) MUTATION NOT APPLIED: selector anchor moved" 0 1
else
  bash "$TMP/m22.sh" --filter "$TMP/api.json" > "$TMP/filtro22" 2>&1
  check "(22e) exact equality LOSES the matrix name" 0 \
    "$( grep -qxF 'race-modules (partition 3)' "$TMP/filtro22" && echo "the mutant changed nothing" || echo 0 )"
fi

# 22g · a declared name is jq data, not jq source.
OLIVARES_RV_JOBNAMES='x") or true or (.name|startswith("' bash "$SUT" --filter "$TMP/api.json" \
  > "$TMP/filtroinj" 2>&1
check "(22g) a name is not interpolated into jq" 0 \
  "$( grep -qxF 'web' "$TMP/filtroinj" && echo "injected jq" || echo 0 )"

# 22h · a wildcard in OLIVARES_RV_JOBNAMES is a name, not a pathname.
#      cwd contains files that would match '*' and 'race-*'.
mkdir -p "$TMP/globcwd"
touch "$TMP/globcwd/web" "$TMP/globcwd/race-modules" "$TMP/globcwd/race-rest"
cat > "$TMP/globjobs.json" <<J
{"jobs":[{"name":"*"},{"name":"web"},{"name":"race-modules"},{"name":"race-rest"},{"name":"race-*"}]}
J
SUT_ABS="$(CDPATH='' cd -- "$(dirname -- "$SUT")" && pwd)/$(basename -- "$SUT")"
( CDPATH='' cd -- "$TMP/globcwd" && OLIVARES_RV_JOBNAMES='*' bash "$SUT_ABS" --filter "$TMP/globjobs.json" ) \
  > "$TMP/filtroglob" 2>&1
check "(22h) a literal glob is not expanded against cwd" 0 \
  "$( grep -qxF '*' "$TMP/filtroglob"; echo $? )"
check "(22i) does not select files that would match" 0 \
  "$( grep -qxE 'web|race-modules|race-rest' "$TMP/filtroglob" && echo glob || echo 0 )"
check "(22j) only that line" 1 "$( grep -c . "$TMP/filtroglob" )"
( CDPATH='' cd -- "$TMP/globcwd" && OLIVARES_RV_JOBNAMES='race-*' bash "$SUT_ABS" --filter "$TMP/globjobs.json" ) \
  > "$TMP/filtroglob2" 2>&1
check "(22k) race-* is a name, not a glob" 0 \
  "$( grep -qxF 'race-*' "$TMP/filtroglob2"; echo $? )"
check "(22l) does not include race-modules/race-rest" 0 \
  "$( grep -qxE 'race-modules|race-rest' "$TMP/filtroglob2" && echo glob || echo 0 )"

# 23–26 · LIVE PATH with a fake `gh`. JSON fixtures never call repo_slug or the
# API name filter. The stub never execs a real client; unexpected argv fails.
mkdir -p "$TMP/fakebin"
cat > "$TMP/fakebin/gh" <<'GHEOF'
#!/bin/sh
log="${FAKE_GH_LOG:-/dev/null}"
printf '%s\n' "$*" >> "$log"
case "$1" in
  repo)
    if [ "$2" = "view" ] && [ "${FAKE_GH_VIEW_RC:-0}" -ne 0 ]; then
      exit "${FAKE_GH_VIEW_RC}"
    fi
    if [ "$2" = "view" ] && [ "${3:-}" = "--json" ] && [ "${4:-}" = "nameWithOwner" ]; then
      [ -n "${FAKE_GH_SLUG:-}" ] || exit 1
      printf '%s\n' "$FAKE_GH_SLUG"
      exit 0
    fi
    echo "fake-gh: unexpected repo invocation" >&2
    exit 1
    ;;
  api)
    # ALWAYS_OK succeeds even on an empty slug. A swallowed repo_slug failure
    # would then print a table; the unknown-source case must not reach here.
    if [ "${FAKE_GH_API_ALWAYS_OK:-}" = "1" ]; then
      case "$2" in
        *"/jobs"*) [ -r "${FAKE_GH_JOBS:-}" ] || exit 1; cat "${FAKE_GH_JOBS}"; exit 0 ;;
        *) [ -r "${FAKE_GH_RUNS:-}" ] || exit 1; cat "${FAKE_GH_RUNS}"; exit 0 ;;
      esac
    fi
    slug="${FAKE_GH_SLUG:-}"
    [ -n "$slug" ] || exit 1
    path="$2"
    case "$path" in
      "repos/${slug}/actions/workflows/mainline-ci.yml/runs"*)
        [ -r "${FAKE_GH_RUNS:-}" ] || exit 1
        cat "${FAKE_GH_RUNS}"
        exit 0
        ;;
      "repos/${slug}/actions/runs/"*"/jobs"*)
        [ -r "${FAKE_GH_JOBS:-}" ] || exit 1
        cat "${FAKE_GH_JOBS}"
        exit 0
        ;;
    esac
    echo "fake-gh: unexpected api path" >&2
    exit 1
    ;;
  *)
    echo "fake-gh: unexpected command" >&2
    exit 1
    ;;
esac
GHEOF
chmod +x "$TMP/fakebin/gh"

live_env() {
  env -u OLIVARES_RV_JOBS -u OLIVARES_RV_LOGDIR \
    -u GH_TOKEN -u GITHUB_TOKEN -u GH_HOST -u GH_ENTERPRISE_TOKEN \
    "$@"
}

cat > "$TMP/runs.json" <<J
{"workflow_runs":[{"id":101,"created_at":"2026-08-30T06:00:00Z"}]}
J
cat > "$TMP/livejobs.json" <<J
{"jobs":[
  {"name":"race-modules (partition 2)","status":"completed","id":11,"runner_name":"srv17",
   "started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:10:00Z","conclusion":"success","steps":[]},
  {"name":"race-rest","status":"completed","id":12,"runner_name":"srv17",
   "started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:05:00Z","conclusion":"success","steps":[]},
  {"name":"race-core","status":"completed","id":13,"runner_name":"ci-runner-9",
   "started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:08:00Z","conclusion":"success","steps":[]},
  {"name":"race-modules-extra","status":"completed","id":14,"runner_name":"trap",
   "started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:01:00Z","conclusion":"success","steps":[]},
  {"name":"web","status":"completed","id":15,"runner_name":"web",
   "started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:01:00Z","conclusion":"success","steps":[]}
]}
J

# Unknown source: view fails, but API would succeed. If slug=$(repo_slug) swallows
# exit 2, MODE=job prints a table (rc 0). API must not run.
: > "$TMP/gh23.log"
RC23=$( live_env -u OLIVARES_RV_REPO -u GITHUB_REPOSITORY \
  OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh23.log" FAKE_GH_VIEW_RC=1 FAKE_GH_API_ALWAYS_OK=1 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(23) no known repository -> 2" 2 "$RC23"
check "(23b) names the cause" 0 "$( grep -q 'COULD NOT CHECK' "$TMP/out"; echo $? )"
check "(23c) did not reach GitHub" 0 \
  "$( grep -qiE 'api.github.com|https://' "$TMP/gh23.log" && echo hit || echo 0 )"
check "(23d) did not call the runs/jobs API" 0 \
  "$( grep -q '^api ' "$TMP/gh23.log" && echo api || echo 0 )"
check "(23e) did not print a table" 0 \
  "$( grep -q 'observation(s)' "$TMP/out" && echo tabla || echo 0 )"

: > "$TMP/gh24.log"
RC24=$( live_env -u GITHUB_REPOSITORY \
  OLIVARES_RV_REPO=owner/fixture OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh24.log" FAKE_GH_SLUG=owner/fixture FAKE_GH_VIEW_RC=1 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(24) correct source + filter in the real path -> rc 0" 0 "$RC24"
check "(24b) includes the matrix label" 0 \
  "$( grep -q 'race-modules (partition 2)' "$TMP/out"; echo $? )"
check "(24c) does not capture an unrelated prefix" 0 \
  "$( grep -q 'race-modules-extra' "$TMP/out" && echo colo || echo 0 )"
check "(24d) did not call repo view (OLIVARES_RV_REPO takes precedence)" 0 \
  "$( grep -q 'repo view' "$TMP/gh24.log" && echo view || echo 0 )"
check "(24e) no query escaped the stub" 0 \
  "$( grep -qiE 'api.github.com|https://' "$TMP/gh24.log" && echo hit || echo 0 )"

: > "$TMP/gh25.log"
RC25=$( live_env -u OLIVARES_RV_REPO \
  GITHUB_REPOSITORY=owner/fixture OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh25.log" FAKE_GH_SLUG=owner/fixture FAKE_GH_VIEW_RC=1 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(25) GITHUB_REPOSITORY resolves without repo view -> rc 0" 0 "$RC25"
check "(25b) did not call repo view" 0 \
  "$( grep -q 'repo view' "$TMP/gh25.log" && echo view || echo 0 )"

: > "$TMP/gh26.log"
RC26=$( live_env -u OLIVARES_RV_REPO -u GITHUB_REPOSITORY \
  OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh26.log" FAKE_GH_SLUG=owner/fixture FAKE_GH_VIEW_RC=0 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(26) read-only gh repo view resolves -> rc 0" 0 "$RC26"
check "(26b) requested nameWithOwner" 0 \
  "$( grep -q 'repo view --json nameWithOwner' "$TMP/gh26.log"; echo $? )"
check "(26c) did not reach GitHub" 0 \
  "$( grep -qiE 'api.github.com|https://' "$TMP/gh26.log" && echo hit || echo 0 )"

echo
echo "check-runner-variance selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
