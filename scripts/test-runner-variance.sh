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
  else FAIL=$((FAIL+1)); printf 'FAIL %s esperaba [%s], dio [%s]\n' "$n" "$e" "$g"; fi; }

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

check "(1) fixture completo -> rc 0" 0 "$(corre)"
check "(1b) y el paquete sale del CUARTO campo, no se pierde" 0 "$( grep -q 'modules/governance' "$TMP/out"; echo $? )"
check "(1c) y ordena por duracion dentro del paquete" 0 \
  "$( awk '/modules\/governance/{f=1;next} f&&/srv17/{print "ok";exit} f&&/ci-runner-9/{print "mal";exit}' "$TMP/out" | grep -qx ok; echo $? )"

# 2 · LA CUENTA QUE ESTE GUION EXISTE PARA PROTEGER: dos paquetes del MISMO job son UNA asignacion.
check "(2) 4 observaciones sobre 3 asignaciones independientes" 0 \
  "$( grep -q '4 observacion(es) sobre 3 asignacion(es)' "$TMP/out"; echo $? )"
check "(2b) y srv17 sale con 2, no con 3" 0 \
  "$( grep -qE '^ +srv17 +2$' "$TMP/out"; echo $? )"
# (3) La guarda que sustituye al viejo aviso por tamano de muestra: en modo `pkg` con VARIOS
# paquetes las filas NO son comparables entre si (un paquete de 800 s y otro de 8 s no compiten por
# el mismo puesto), asi que no se afirma probabilidad ninguna. El aviso viejo decia «con menos de 8
# no hay potencia» y era romo: sobre `secrets` habia separacion COMPLETA con n=3.
check "(3) modo pkg con varios paquetes: NO afirma probabilidad" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "comparo paquetes distintos entre si" || echo 0 )"
check "(3b) ni se inventa una separacion entre paquetes" 0 \
  "$( grep -q 'SEPARACION COMPLETA' "$TMP/out" && echo "afirmo separacion cruzando paquetes" || echo 0 )"

# 4 · NUNCA 0 POR SILENCIO: logs legibles pero sin ninguno de los paquetes pedidos.
check "(4) ningun paquete casa -> 2, no 0 con tabla vacia" 2 \
  "$( OLIVARES_RV_PKGS="no/existe" corre )"
check "(4b) y su mensaje dice QUE buscaba" 0 "$( grep -q 'Paquetes buscados: no/existe' "$TMP/out"; echo $? )"

echo '{"jobs":[]}' > "$TMP/vacio.json"
check "(5) cero jobs cerrados -> 2" 2 "$(corre "$TMP/vacio.json")"
check "(6) fixture ilegible -> 2, no se supone vacio" 2 "$(corre "$TMP/no-existe.json")"
printf 'no soy json' > "$TMP/roto.json"
check "(7) JSON ilegible -> 2" 2 "$(corre "$TMP/roto.json")"

# 8 · el corte se detecta por el panic, y NO se pega al paquete siguiente
{ printf 'race-modules\trace\t2026-08-30T06:00:00.0Z panic: test timed out after 2h30m0s\n'
  linea race-modules FAIL github.com/olivaresai/olivares/modules/governance 9000.5
  linea race-modules ok github.com/olivaresai/olivares/core/api 100.0; } > "$TMP/logs/R2-race-modules.log"
corre >/dev/null
check "(8) el cortado sale marcado con '>'" 0 "$( grep -qE '> +9000\.5s' "$TMP/out"; echo $? )"
check "(8b) y el paquete SIGUIENTE no hereda la marca" 0 \
  "$( grep -qE '> +100\.0s' "$TMP/out" && echo "heredo el corte" || echo 0 )"

# 9 · MUTANTE · leer el TERCER campo (la trampa de los tabs de `go test`)
sed 's|sub(/\^\[\^\\t\]\*\\t\[\^\\t\]\*\\t/,"",linea)|linea=$3|' "$SUT" > "$TMP/m3.sh"
check "(9a) el mutante del tercer campo REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m3.sh" && echo 1 || echo 0 )"
M3=$(corre "$TMP/jobs.json" "$TMP/logs" "$TMP/m3.sh")
check "(9b) MUTANTE 'leo el tercer campo' es CAZADO por su rc" 0 "$( [ "$M3" = "2" ] && echo 0 || echo "dio $M3" )"
check "(9c) y por su MENSAJE: dice que ningun log dio duraciones" 0 \
  "$( grep -q 'ningun log dio duraciones' "$TMP/out"; echo $? )"

# 10 · MUTANTE · contar observaciones como si fueran independientes (quitar el dedup)
sed 's|IND=$(cut -f3,6 "$TMP/filas" \| sort -u \| wc -l)|IND=$(wc -l < "$TMP/filas")|' "$SUT" > "$TMP/mi.sh"
check "(10a) el mutante del dedup REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mi.sh" && echo 1 || echo 0 )"
MI=$(corre "$TMP/jobs.json" "$TMP/logs" "$TMP/mi.sh")
check "(10b) MUTANTE 'toda observacion es independiente' NO cambia el rc" 0 "$( [ "$MI" = "0" ] && echo 0 || echo "dio $MI" )"
check "(10c) y SOLO su MENSAJE lo caza: infla 3 a 5 asignaciones" 0 \
  "$( grep -q 'sobre 5 asignacion(es)' "$TMP/out"; echo $? )"

# 11 · MODO JOB · la duracion sale de los SELLOS, sin log, y sirve para jobs sin lineas `ok pkg Ns`
cat > "$TMP/sellos.json" <<J
{"jobs":[{"run":"R1","job":"secrets","id":1,"runner":"srv17","ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:26:27Z"},
         {"run":"R2","job":"secrets","id":2,"runner":"ci-runner-7","ini":"2026-08-30T07:00:00Z","fin":"2026-08-30T08:06:18Z"}]}
J
J11=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sellos.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(11) modo job -> rc 0 SIN ningun log" 0 "$J11"
check "(11b) y la duracion sale de los sellos (1587 s)" 0 "$( grep -qE '1587\.0s' "$TMP/out"; echo $? )"
check "(11c) y cuenta 2 asignaciones, una por maquina" 0 \
  "$( grep -q 'sobre 2 asignacion(es)' "$TMP/out"; echo $? )"

# 12 · MODO JOB sin sellos: es 2, NO una duracion inventada de cero
cat > "$TMP/sinsellos.json" <<J
{"jobs":[{"run":"R1","job":"secrets","id":1,"runner":"srv17"}]}
J
J12=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sinsellos.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(12) modo job sin sellos -> 2, no 0 s" 2 "$J12"
check "(12b) y su mensaje NOMBRA el job y la corrida" 0 \
  "$( grep -q 'el job secrets de R1 no trae sellos' "$TMP/out"; echo $? )"

# 13 · MUTANTE · en modo job, dar por buena la ausencia de sellos (la clase «0 por silencio»)
sed 's|echo "runner-variance: NO HE PODIDO MIRAR: modo job y el job ${job} de ${run} no trae sellos." >&2; exit 2|ini="2026-01-01T00:00:00Z"; fin="2026-01-01T00:00:00Z"|' "$SUT" > "$TMP/ms.sh"
check "(13a) el mutante de los sellos REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/ms.sh" && echo 1 || echo 0 )"
M13=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sinsellos.json" bash "$TMP/ms.sh" > "$TMP/out" 2>&1; echo $? )
check "(13b) MUTANTE 'sin sellos vale 0 s' es CAZADO por su rc" 0 "$( [ "$M13" = "0" ] && echo 0 || echo "dio $M13" )"
check "(13c) y por su MENSAJE: imprime una duracion de 0.0s que no existio" 0 \
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
check "(14) separacion completa -> rc 0" 0 "$RC14"
check "(14b) y NOMBRA la separacion con sus dos numeros" 0 \
  "$( grep -q 'srv17 ocupa los 2 primeros de 5' "$TMP/out"; echo $? )"
check "(14c) y da p = 1/C(5,2) = 1/10" 0 "$( grep -q 'p = 1/10 ' "$TMP/out"; echo $? )"
check "(14d) y corrige por elegir la maquina despues de mirar" 0 \
  "$( grep -q 'Corregido por haber ELEGIDO' "$TMP/out"; echo $? )"

# 14e · Y SIN veredicto declarado tampoco se afirma: un job de conclusion desconocida no lidera.
sed 's/"conc":"success"/"conc":"?"/g' "$TMP/sep.json" > "$TMP/sinv.json"
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sinv.json" bash "$SUT" > "$TMP/out" 2>&1
check "(14e) con veredicto desconocido NO se afirma probabilidad" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "afirmo con conclusion desconocida" || echo 0 )"

# 14f · DOS grupos (dos jobs distintos) NO se ordenan juntos, aunque el modo sea `job`.
python3 - "$TMP/sep.json" > "$TMP/dosjobs.json" <<'PYEOF'
import json,sys
d=json.load(open(sys.argv[1]))
for i,j in enumerate(d["jobs"]): j["job"]="race-rest" if i%2 else "race-modules"
print(json.dumps(d))
PYEOF
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/dosjobs.json" bash "$SUT" > "$TMP/out" 2>&1
check "(14f) dos jobs distintos: NO se ordenan en la misma lista" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "comparo race-rest con race-modules" || echo 0 )"

# 15 · SIN separacion limpia: un intruso entre los rapidos. NO se afirma probabilidad ninguna.
sep srv17:100 intruso:110 srv17:120 lento-b:400 lento-c:500
RC15=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sep.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(15) sin separacion limpia -> sigue rc 0" 0 "$RC15"
check "(15b) y lo DICE" 0 "$( grep -q 'sin separacion limpia' "$TMP/out"; echo $? )"
check "(15c) y NO imprime ninguna probabilidad" 0 \
  "$( grep -q 'p = 1/' "$TMP/out" && echo "afirmo una p sin separacion" || echo 0 )"

# 16 · MUTANTE · quitar la comprobacion de separacion: afirma una p donde no la hay
sed 's|if (k!=total \|\| k==NR) { print "     (sin separacion limpia: no se afirma probabilidad)"; exit }|if (0) { }|' "$SUT" > "$TMP/msep.sh"
check "(16a) el mutante de la separacion REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/msep.sh" && echo 1 || echo 0 )"
M16=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/sep.json" bash "$TMP/msep.sh" > "$TMP/out" 2>&1; echo $? )
check "(16b) MUTANTE 'sin comprobar separacion' NO cambia el rc" 0 "$( [ "$M16" = "0" ] && echo 0 || echo "dio $M16" )"
check "(16c) y SOLO su MENSAJE lo caza: afirma una p sobre datos con intruso" 0 \
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
check "(17) un cancelled rapido -> rc 0" 0 "$C17"
check "(17b) y se DECLARA cuantos se excluyeron" 0 "$( grep -q "1 job(s) 'cancelled' EXCLUIDOS" "$TMP/out"; echo $? )"
check "(17c) y la maquina del cancelled desaparece de la tabla" 0 \
  "$( grep -q 'rapida' "$TMP/out" && echo "el cancelled sigue ordenando" || echo 0 )"
check "(17d) y la separacion se calcula sobre 4, no sobre 5" 0 \
  "$( grep -q 'los 2 primeros de 4' "$TMP/out"; echo $? )"
check "(17e) y el 'failure' lento SI se conserva (es cola lenta censurada)" 0 \
  "$( grep -q 'lento-b' "$TMP/out"; echo $? )"

# 18 · MUTANTE · no excluir los cancelled: el rapido lidera y la conclusion cambia
sed 's|^NCANC=$(awk -F.\\t. .$5=="cancelled". "$TMP/filas" \| wc -l)|NCANC=0|' "$SUT" > "$TMP/mc.sh"
check "(18a) el mutante del cancelled REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mc.sh" && echo 1 || echo 0 )"
M18=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$TMP/mc.sh" > "$TMP/out" 2>&1; echo $? )
check "(18b) MUTANTE 'el cancelled cuenta' NO cambia el rc" 0 "$( [ "$M18" = "0" ] && echo 0 || echo "dio $M18" )"
check "(18c) y SOLO su MENSAJE lo caza: el cancelled reaparece ordenando el primero" 0 \
  "$( grep -q 'rapida' "$TMP/out"; echo $? )"

# 19 · ENTRE vs DENTRO: el estadistico que contesta aunque NO haya separacion limpia.
#      Caso A: la maquina manda (medianas muy separadas, repeticiones muy juntas).
cancel A:100:success A:105:success A:110:success B:400:success B:410:success B:420:success
A19=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(19) entre/dentro -> rc 0" 0 "$A19"
check "(19b) con medianas separadas y repeticiones juntas: manda la MAQUINA" 0 \
  "$( grep -q 'Manda la MAQUINA' "$TMP/out"; echo $? )"
#      Caso B: la corrida manda (la misma maquina abarca todo el rango).
cancel A:100:success A:400:success A:250:success B:200:success B:260:success B:230:success
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1
check "(19c) con una maquina abarcando el rango: manda la CORRIDA" 0 \
  "$( grep -q 'Manda la CORRIDA' "$TMP/out"; echo $? )"
check "(19d) y lo dice con su consecuencia, no solo con el veredicto" 0 \
  "$( grep -q 'etiquetar o retirar maquinas NO arreglaria esto' "$TMP/out"; echo $? )"
#      Caso C: sin repeticiones no se afirma nada.
cancel A:100:success B:200:success C:300:success
OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$SUT" > "$TMP/out" 2>&1
check "(19e) sin repeticiones por maquina NO se afirma entre/dentro" 0 \
  "$( grep -q 'sin suficientes repeticiones' "$TMP/out"; echo $? )"

# 20 · MUTANTE · invertir la comparacion entre/dentro: diria «la flota» donde manda la corrida
sed 's|if (e > dentro)|if (e < dentro)|' "$SUT" > "$TMP/mev.sh"
check "(20a) el mutante de la comparacion REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mev.sh" && echo 1 || echo 0 )"
cancel A:100:success A:105:success A:110:success B:400:success B:410:success B:420:success
M20=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/can.json" bash "$TMP/mev.sh" > "$TMP/out" 2>&1; echo $? )
check "(20b) MUTANTE 'comparacion invertida' NO cambia el rc" 0 "$( [ "$M20" = "0" ] && echo 0 || echo "dio $M20" )"
check "(20c) y SOLO su MENSAJE lo caza: dice CORRIDA donde manda la maquina" 0 \
  "$( grep -q 'Manda la CORRIDA' "$TMP/out"; echo $? )"

# 21 · F-01 · un `failure` NO es una sola clase: la fila ensena la duracion del PASO rojo, que es lo
#      que distingue «acabo y salio rojo» de «lo mato el techo». Nueve de once `failure` de `secrets`
#      habian acabado el barrido; yo los habia declarado a los once «reloj agotado».
cat > "$TMP/rojo.json" <<J
{"jobs":[{"run":"R1","job":"secrets","id":1,"runner":"A","conc":"success","rojo":0,"ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:10:00Z"},
         {"run":"R2","job":"secrets","id":2,"runner":"B","conc":"failure","rojo":2496,"ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T06:48:00Z"},
         {"run":"R3","job":"secrets","id":3,"runner":"C","conc":"failure","rojo":3613,"ini":"2026-08-30T06:00:00Z","fin":"2026-08-30T07:06:00Z"}]}
J
R21=$( OLIVARES_RV_MODE=job OLIVARES_RV_JOBS="$TMP/rojo.json" bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(21) fixture con pasos rojos -> rc 0" 0 "$R21"
check "(21b) el que acabo ensena su paso rojo corto" 0 "$( grep -q 'failure paso rojo 2496s' "$TMP/out"; echo $? )"
check "(21c) el matado por el techo ensena el suyo, distinto" 0 "$( grep -q 'failure paso rojo 3613s' "$TMP/out"; echo $? )"
check "(21d) el verde NO lleva anotacion de paso rojo" 0 \
  "$( grep -qE 'A .*paso rojo' "$TMP/out" && echo "anoto un paso rojo en un job verde" || echo 0 )"

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
check "(22) el filtro acepta el nombre de la MATRIZ" 0 \
  "$( grep -qxF 'race-modules (partition 3)' "$TMP/filtro"; echo $? )"
check "(22b) y sigue aceptando el nombre sin matriz" 0 \
  "$( grep -qxF 'race-modules' "$TMP/filtro"; echo $? )"
check "(22c) y descarta lo que no es una pata -race" 0 \
  "$( grep -qxE 'web|race-hot' "$TMP/filtro" && echo "colo un job ajeno" || echo 0 )"
check "(22d) las tres patas declaradas estan" 4 "$( grep -c . "$TMP/filtro" )"
check "(22f) un prefijo no casa un nombre mas largo" 0 \
  "$( grep -qxE 'race-modules-extra|race-restricted' "$TMP/filtro" && echo "colo un prefijo" || echo 0 )"

# 22e · CONTROL POSITIVO. Con el predicado de antes —igualdad exacta— el nombre de la matriz
#       desaparece: eso es lo que estaba a punto de pasar, y lo que (22) mide de verdad.
sed 's/matrix_leg(\$j)/false/' "$SUT" > "$TMP/m22.sh"
if cmp -s "$SUT" "$TMP/m22.sh"; then
  check "(22e) MUTACION NO APLICADA: el ancla del selector se movio" 0 1
else
  bash "$TMP/m22.sh" --filter "$TMP/api.json" > "$TMP/filtro22" 2>&1
  check "(22e) con igualdad exacta el nombre de la matriz SE PIERDE" 0 \
    "$( grep -qxF 'race-modules (partition 3)' "$TMP/filtro22" && echo "el mutante no cambio nada" || echo 0 )"
fi

# 22g · a declared name is jq data, not jq source.
OLIVARES_RV_JOBNAMES='x") or true or (.name|startswith("' bash "$SUT" --filter "$TMP/api.json" \
  > "$TMP/filtroinj" 2>&1
check "(22g) un nombre no se interpola en jq" 0 \
  "$( grep -qxF 'web' "$TMP/filtroinj" && echo "inyecto jq" || echo 0 )"

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
check "(22h) un glob literal no se expande por el cwd" 0 \
  "$( grep -qxF '*' "$TMP/filtroglob"; echo $? )"
check "(22i) y no selecciona los ficheros que casarian" 0 \
  "$( grep -qxE 'web|race-modules|race-rest' "$TMP/filtroglob" && echo glob || echo 0 )"
check "(22j) y solo esa linea" 1 "$( grep -c . "$TMP/filtroglob" )"
( CDPATH='' cd -- "$TMP/globcwd" && OLIVARES_RV_JOBNAMES='race-*' bash "$SUT_ABS" --filter "$TMP/globjobs.json" ) \
  > "$TMP/filtroglob2" 2>&1
check "(22k) race-* es un nombre, no un glob" 0 \
  "$( grep -qxF 'race-*' "$TMP/filtroglob2"; echo $? )"
check "(22l) y no arrastra race-modules/race-rest" 0 \
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
check "(23) sin repositorio conocido -> 2" 2 "$RC23"
check "(23b) y nombra la causa" 0 "$( grep -q 'NO PUDE MIRAR' "$TMP/out"; echo $? )"
check "(23c) y no alcanzo GitHub" 0 \
  "$( grep -qiE 'api.github.com|https://' "$TMP/gh23.log" && echo hit || echo 0 )"
check "(23d) y no llamo a la API de corridas/jobs" 0 \
  "$( grep -q '^api ' "$TMP/gh23.log" && echo api || echo 0 )"
check "(23e) y no imprimio tabla" 0 \
  "$( grep -q 'observacion(es)' "$TMP/out" && echo tabla || echo 0 )"

: > "$TMP/gh24.log"
RC24=$( live_env -u GITHUB_REPOSITORY \
  OLIVARES_RV_REPO=owner/fixture OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh24.log" FAKE_GH_SLUG=owner/fixture FAKE_GH_VIEW_RC=1 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(24) fuente correcta + filtro en el camino real -> rc 0" 0 "$RC24"
check "(24b) incluye la etiqueta de matriz" 0 \
  "$( grep -q 'race-modules (partition 2)' "$TMP/out"; echo $? )"
check "(24c) no captura un prefijo ajeno" 0 \
  "$( grep -q 'race-modules-extra' "$TMP/out" && echo colo || echo 0 )"
check "(24d) no llamo repo view (manda OLIVARES_RV_REPO)" 0 \
  "$( grep -q 'repo view' "$TMP/gh24.log" && echo view || echo 0 )"
check "(24e) ninguna consulta salio del stub" 0 \
  "$( grep -qiE 'api.github.com|https://' "$TMP/gh24.log" && echo hit || echo 0 )"

: > "$TMP/gh25.log"
RC25=$( live_env -u OLIVARES_RV_REPO \
  GITHUB_REPOSITORY=owner/fixture OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh25.log" FAKE_GH_SLUG=owner/fixture FAKE_GH_VIEW_RC=1 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(25) GITHUB_REPOSITORY resuelve sin repo view -> rc 0" 0 "$RC25"
check "(25b) y no llamo repo view" 0 \
  "$( grep -q 'repo view' "$TMP/gh25.log" && echo view || echo 0 )"

: > "$TMP/gh26.log"
RC26=$( live_env -u OLIVARES_RV_REPO -u GITHUB_REPOSITORY \
  OLIVARES_RV_MODE=job \
  FAKE_GH_LOG="$TMP/gh26.log" FAKE_GH_SLUG=owner/fixture FAKE_GH_VIEW_RC=0 \
  FAKE_GH_RUNS="$TMP/runs.json" FAKE_GH_JOBS="$TMP/livejobs.json" \
  PATH="$TMP/fakebin:$PATH" \
  timeout 8 bash "$SUT" > "$TMP/out" 2>&1; echo $? )
check "(26) gh repo view de solo lectura resuelve -> rc 0" 0 "$RC26"
check "(26b) y pidio nameWithOwner" 0 \
  "$( grep -q 'repo view --json nameWithOwner' "$TMP/gh26.log"; echo $? )"
check "(26c) y no alcanzo GitHub" 0 \
  "$( grep -qiE 'api.github.com|https://' "$TMP/gh26.log" && echo hit || echo 0 )"

echo
echo "check-runner-variance selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
