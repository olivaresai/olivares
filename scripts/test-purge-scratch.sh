#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Banco de scripts/purge-scratch.sh. Cada guarda trae su MUTANTE, y cada mutante se comprueba
# FABRICADO (que difiere del original) antes de creerse su veredicto: un `sed` que no casa produce
# un mutante identico al original, que muere por la guarda de al lado y acredita cero.
set -u -o pipefail
# Aislamiento de git: este guion empareja `mktemp -d` con `git`, y sin sanear el
# entorno un GIT_DIR envenenado lo apunta al repo real. Fallar cerrado: no poder
# sanear es «no he podido aislar», nunca «no hacia falta aislar». (Segunda pata del
# carril rapido que este banco enrojecia en main: lint:git-env, 2026-08-31.)
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
RAIZ="${OLIVARES_ROOT:-$(git rev-parse --show-toplevel)}"
SUT="$RAIZ/scripts/purge-scratch.sh"
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
PASS=0; FAIL=0
check(){ local d="$1" e="$2" g="$3"
  if [ "$e" = "$g" ]; then printf 'ok   %-62s %s\n' "$d" "$g"; PASS=$((PASS+1))
  else printf 'FAIL %s esperaba [%s], dio [%s]\n' "$d" "$e" "$g"; FAIL=$((FAIL+1)); fi; }

# ⛔ UN rc INESPERADO ES «NO HE PODIDO MIRAR», NUNCA UN PASE. Lo levanto el lector 47 y el sitio
# donde ocurria es el peor posible: ESTE guion nacio para curar la clase del rc fantasma y la tenia
# dentro. Mecanismo: casi todas las filas comprueban SUPERVIVENCIA (`[ -e ]`), asi que un mutante
# que muera al instante —`exit 2`, `exit 127`— no borra nada y **todas las filas de supervivencia
# pasan**. El banco daba 23/0 sobre un guion que no habia hecho nada.
#
# `aplica` exige que la corrida haya producido su veredicto: rc esperado Y su linea de resumen. Si
# no, devuelve un centinela que ninguna fila acepta. Y hay dos filas que lo comprueban con senuelos
# que salen 2 y 127: sin ellas, este arreglo seria otra afirmacion sin testigo.
aplica(){ # 1=dir  2=guion (por defecto el SUT)  -> OK | NO-PUDE-MIRAR(rc=N)
  local rc; bash "${2:-$SUT}" --apply "$1" > "$TMP/o" 2>&1; rc=$?
  if [ "$rc" != 0 ] || ! grep -q 'purge-scratch: borradas' "$TMP/o"; then echo "NO-PUDE-MIRAR(rc=$rc)"; else echo OK; fi; }

# Un fixture nuevo por caso: un banco que comparte estado no mide lo que cree.
nido(){ local d; d=$(mktemp -d -p "$TMP"); mkdir -p "$d/wt-a" "$d/cc-socks" "$d/claude-1000"
  : > "$d/tmp.uno"; : > "$d/basura.bin"
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$d/wt-a/bus.sock"
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$d/tmp.sock"
  printf '%s' "$d"; }

# --- el argumento y las negativas -----------------------------------------------------------
check "(1) sin directorio -> rc 2" 2 "$( bash "$SUT" >"$TMP/o" 2>&1; echo $? )"
check "(1b) y dice que SI honra (el env), no solo que falta" 0 "$( grep -q 'OLIVARES_SCRATCH_DIR' "$TMP/o"; echo $? )"
check "(2) un directorio que no existe -> rc 2" 2 "$( bash "$SUT" "$TMP/no-existe" >"$TMP/o" 2>&1; echo $? )"
check "(3) se niega sobre /workspace, que no es un scratch" 2 "$( bash "$SUT" /workspace >"$TMP/o" 2>&1; echo $? )"
check "(4) y sobre / tambien" 2 "$( bash "$SUT" / >"$TMP/o" 2>&1; echo $? )"

# --- el modo por defecto NO borra -----------------------------------------------------------
N=$(nido)
bash "$SUT" "$N" > "$TMP/o" 2>&1
check "(5) por defecto es ENSAYO y lo dice" 0 "$( grep -q 'ENSAYO' "$TMP/o"; echo $? )"
check "(5b) y el fichero candidato SIGUE ahi" 0 "$( [ -f "$N/tmp.uno" ] && echo 0 || echo 'lo borro sin --apply' )"

# --- lista de INCLUSION: solo se borra lo nombrado -------------------------------------------
N=$(nido); check "(6-pre) la corrida produjo su veredicto" OK "$(aplica "$N")"
check "(6) --apply borra el fichero de una clase nombrada" 0 "$( [ ! -e "$N/tmp.uno" ] && echo 0 || echo 'sobrevivio' )"
check "(6b) y NO borra lo que ninguna clase nombra" 0 "$( [ -f "$N/basura.bin" ] && echo 0 || echo 'borro algo que no nombro' )"

# --- el TIPO manda, aunque el nombre case ----------------------------------------------------
check "(7) un socket que CASA la clase se rechaza por TIPO" 0 "$( [ -S "$N/tmp.sock" ] && echo 0 || echo 'borro un socket' )"
check "(7b) y la razon dice por que vale" 0 "$( grep -q 'vale por ser encontrable' "$TMP/o"; echo $? )"

# --- la guarda que me falto: rm -rf es CIEGO al tipo -----------------------------------------
check "(8) un directorio que CONTIENE un socket se rechaza" 0 "$( [ -d "$N/wt-a" ] && echo 0 || echo 'se llevo el directorio con el socket dentro' )"
check "(8b) y NOMBRA el socket que lo salva" 0 "$( grep -q 'wt-a/bus.sock' "$TMP/o"; echo $? )"

# --- los nombres intocables, comprobables solo con las clases abiertas ------------------------
N=$(nido); check "(9-pre) la corrida produjo su veredicto" OK "$(OLIVARES_PURGE_DIR_CLASSES='*' aplica "$N")"
check "(9) con las clases a '*', cc-socks sobrevive por NOMBRE" 0 "$( [ -d "$N/cc-socks" ] && echo 0 || echo 'borro el socket del bus' )"
check "(9b) y claude-1000 tambien" 0 "$( [ -d "$N/claude-1000" ] && echo 0 || echo 'borro el scratch del arnes' )"
check "(9c) y la razon los llama intocables" 0 "$( grep -q 'nombre intocable' "$TMP/o"; echo $? )"

# --- MUTANTES ---------------------------------------------------------------------------------
mut(){ python3 - "$SUT" "$2" "$3" "$4" <<'PYEOF'
import sys
o=open(sys.argv[1]).read(); v=sys.argv[3]; n=sys.argv[4]
if o.count(v)!=1: sys.exit("el patron del mutante no casa una sola vez: "+v[:50])
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
}

# M1 · ciego a lo que hay DENTRO del directorio: vuelve mi corte del bus
mut x "$TMP/m1.sh" 'dentro=$(find "$d" \( -type s -o -type p \) 2>/dev/null | head -3)' 'dentro=""'
check "(10a) M1 fabricado y difiere" 0 "$( [ -s "$TMP/m1.sh" ] && ! cmp -s "$SUT" "$TMP/m1.sh" && echo 0 || echo 'no se fabrico' )"
N=$(nido); check "(10-pre) el mutante CORRIO (no un rc fantasma)" OK "$(aplica "$N" "$TMP/m1.sh")"
check "(10b) M1 SE LLEVA el directorio con el socket dentro" 0 "$( [ ! -e "$N/wt-a" ] && echo 0 || echo 'el mutante no cambio nada: caso muerto' )"

# M2 · sin lista de nombres: con las clases abiertas, cae el bus
mut x "$TMP/m2.sh" '_intocable(){ local n; for n in "${INTOCABLES[@]}"; do [ "$1" = "$n" ] && return 0; done; return 1; }' '_intocable(){ return 1; }'
check "(11a) M2 fabricado y difiere" 0 "$( [ -s "$TMP/m2.sh" ] && ! cmp -s "$SUT" "$TMP/m2.sh" && echo 0 || echo 'no se fabrico' )"
N=$(nido); check "(11-pre) el mutante CORRIO (no un rc fantasma)" OK "$(OLIVARES_PURGE_DIR_CLASSES='*' aplica "$N" "$TMP/m2.sh")"
check "(11b) M2 borra cc-socks" 0 "$( [ ! -e "$N/cc-socks" ] && echo 0 || echo 'el mutante no cambio nada: caso muerto' )"

# M3 · sin comprobacion de tipo en ficheros: borra el socket que casa la clase
mut x "$TMP/m3.sh" 'if [ -S "$f" ] || [ -p "$f" ]; then' 'if false; then'
check "(12a) M3 fabricado y difiere" 0 "$( [ -s "$TMP/m3.sh" ] && ! cmp -s "$SUT" "$TMP/m3.sh" && echo 0 || echo 'no se fabrico' )"
N=$(nido); check "(12-pre) el mutante CORRIO (no un rc fantasma)" OK "$(aplica "$N" "$TMP/m3.sh")"
# ⛔ ESTE CASO SE JUZGA POR EL MENSAJE, y la razon la descubrio el propio mutante: al neutralizar la
# comprobacion de tipo el socket SIGUE sobreviviendo, porque el `[ -f ]` de mas abajo ya lo excluye.
# Es decir, mi caso (7) pasaba por una guarda DISTINTA de la que nombraba. Lo que la rama aporta es
# el diagnostico —sin ella el socket se cae de la lista en silencio—, asi que lo que tiene que
# desaparecer con el mutante es la RAZON, no el fichero.
check "(12b) M3 sigue sin borrarlo (lo salva el [ -f ], no la rama)" 0 \
  "$( [ -S "$N/tmp.sock" ] && echo 0 || echo 'lo borro' )"
check "(12c) pero DEJA DE DECIR por que lo respeta" 0 \
  "$( grep -q 'tmp.sock' "$TMP/o" && echo 'sigue diagnosticando: el mutante no cambio nada' || echo 0 )"

# 15-17 · LAS GUARDAS DE USO, VISTAS CORTAR CON UN fd REAL Y UN cwd REAL. La v1 no las tenia y la
#         cabecera hablaba de ellas: el lector 47 borro un fichero con descriptor abierto y un
#         directorio que era el cwd de un proceso, con rc 0. Estas filas existen para que eso no
#         pueda volver a pasar sin que el banco lo diga, y llevan su CONTROL NEGATIVO al lado: si
#         la guarda fuese demasiado ancha y rechazara todo, el banco saldria igual de verde.
U=$(nido); : > "$U/tmp.conFD"
( exec 9< "$U/tmp.conFD"; sleep 30 ) & FDPID=$!
# ⛔ El cwd va en un directorio SIN socket dentro: `wt-a` lo tiene, y entonces lo rechazaria la
#    guarda del socket y esta fila acreditaria a la guarda equivocada — el mismo defecto que el
#    mutante M3 me enseño un rato antes. Una fila tiene que morir por la pata que NOMBRA.
mkdir -p "$U/wt-cwd"
( cd "$U/wt-cwd" && sleep 30 ) & CWDPID=$!
sleep 1
V=$(OLIVARES_PURGE_DIR_CLASSES='wt-*' aplica "$U")
check "(15-pre) la corrida produjo su veredicto" OK "$V"
check "(15) un fichero con DESCRIPTOR ABIERTO se respeta" 0 "$( [ -f "$U/tmp.conFD" ] && echo 0 || echo 'lo borro con un fd abierto' )"
check "(15b) y lo dice, con la razon" 0 "$( grep -q 'EN USO: algun proceso lo tiene abierto' "$TMP/o"; echo $? )"
check "(16) un directorio que es el CWD de un proceso se respeta" 0 "$( [ -d "$U/wt-cwd" ] && echo 0 || echo 'se llevo el cwd de un proceso vivo' )"
check "(16b) y lo dice" 0 "$( grep -q 'EN USO: es el cwd' "$TMP/o"; echo $? )"
check "(17) CONTROL NEGATIVO: el fichero LIBRE si se borra" 0 "$( [ ! -e "$U/tmp.uno" ] && echo 0 || echo 'no borra nada: la guarda es demasiado ancha' )"

# 18 · MUTANTE de las guardas de uso: sin ellas vuelve el caso del lector 47
python3 - "$SUT" "$TMP/m18.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
v='case "$u" in "$r"|"$r"/*) return 0;; esac'
n='case "$u" in __nunca_casa__) return 0;; esac'
if o.count(v)!=1: sys.exit("el patron del mutante 18 no casa")
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(18a) M18 fabricado y difiere" 0 "$( [ -s "$TMP/m18.sh" ] && ! cmp -s "$SUT" "$TMP/m18.sh" && echo 0 || echo 'no se fabrico' )"
U2=$(nido); : > "$U2/tmp.conFD"
( exec 9< "$U2/tmp.conFD"; sleep 30 ) & FDPID2=$!
sleep 1
check "(18-pre) el mutante CORRIO" OK "$(aplica "$U2" "$TMP/m18.sh")"
check "(18b) M18 SE LLEVA el fichero con el fd abierto" 0 "$( [ ! -e "$U2/tmp.conFD" ] && echo 0 || echo 'el mutante no cambio nada: caso muerto' )"
kill $FDPID $CWDPID $FDPID2 2>/dev/null; wait $FDPID $CWDPID $FDPID2 2>/dev/null

# 19-21 · LA FORMA DE LA ENTRADA. Las guardas de uso existian, estaban probadas... y yo solo las
#         habia ejercitado con rutas de `mktemp -d`, que son ABSOLUTAS. El lector 47 paso una
#         RELATIVA y las guardas no cortaron: `/proc/<pid>/cwd` y `/proc/<pid>/fd/*` resuelven
#         siempre a absolutas, y yo comparaba contra la ruta tal como llegaba. **Probar la funcion
#         con una sola forma de su argumento no es probarla.**
R1=$(nido); : > "$R1/tmp.conFD"
( exec 9< "$R1/tmp.conFD"; sleep 30 ) & RFD=$!
sleep 1
V=$( cd "$(dirname "$R1")" && OLIVARES_PURGE_DIR_CLASSES='wt-*' bash "$SUT" --apply "$(basename "$R1")" > "$TMP/o" 2>&1; echo $? )
check "(19-pre) con ruta RELATIVA la corrida produjo su veredicto" 0 "$V"
check "(19) y el fichero con FD abierto SIGUE ahi" 0 \
  "$( [ -f "$R1/tmp.conFD" ] && echo 0 || echo 'una ruta relativa elude la guarda de uso' )"
check "(19b) y lo dice con su razon" 0 "$( grep -q 'EN USO' "$TMP/o"; echo $? )"
check "(20) CONTROL NEGATIVO: con ruta relativa el fichero LIBRE si se borra" 0 \
  "$( [ ! -e "$R1/tmp.uno" ] && echo 0 || echo 'no borra nada: la guarda es demasiado ancha' )"
kill $RFD 2>/dev/null; wait $RFD 2>/dev/null

# 21 · el MISMO defecto en la negativa de raices: comparaba TEXTO, asi que un `.` estando en una raiz
#      peligrosa no casaba. Con la canonicalizacion por delante, si.
check "(21) una ruta RELATIVA que apunta a una raiz peligrosa se rechaza" 2 \
  "$( cd / && bash "$SUT" . > "$TMP/o" 2>&1; echo $? )"

# 22-24 · LAS DOS MITADES DE LA COMPARACION. La v3 canonicalizaba el ARGUMENTO y comparaba contra
#         `$HOME` **crudo**: con HOME siendo un enlace al scratch, la negativa no disparaba y el
#         lector 47 borro un fichero con rc 0 y cero rechazos. **Canonicalizar una mitad deja la
#         guarda igual de ciega.** Y se prueba la clase, no la instancia: `/tmp` puede ser un enlace
#         en otra maquina y el defecto seria el mismo.
H=$(mktemp -d -p "$TMP"); mkdir -p "$H/real"; : > "$H/real/tmp.owned"; ln -sfn "$H/real" "$H/casa"
RC22=$( HOME="$H/casa" bash "$SUT" --apply "$H/real" > "$TMP/o" 2>&1; echo $? )
check "(22) un DIR que resuelve a \$HOME por enlace se rechaza" 2 "$RC22"
check "(22b) y la razon nombra la raiz a la que resuelve" 0 "$( grep -q 'resuelve a' "$TMP/o"; echo $? )"
check "(22c) y el fichero SIGUE ahi" 0 "$( [ -f "$H/real/tmp.owned" ] && echo 0 || echo 'lo borro' )"

# 23 · CONTROL NEGATIVO: con el mismo HOME enlazado, un scratch de verdad se sigue purgando —
#      si la negativa fuese demasiado ancha, el banco saldria igual de verde.
L=$(nido)
check "(23) CONTROL NEGATIVO: un scratch normal sigue purgandose" OK "$( HOME="$H/casa" aplica "$L" )"
check "(23b) y de hecho borro el fichero de la clase nombrada" 0 \
  "$( [ ! -e "$L/tmp.uno" ] && echo 0 || echo 'no borro nada' )"

# 24 · MUTANTE: comparar contra la referencia CRUDA vuelve al agujero del 47
python3 - "$SUT" "$TMP/m24.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
v='if [ "$DIR" = "$(_canon "$_raiz")" ]; then'
n='if [ "$DIR" = "$_raiz" ]; then'
if o.count(v)!=1: sys.exit("el patron del mutante 24 no casa")
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(24a) M24 fabricado y difiere" 0 "$( [ -s "$TMP/m24.sh" ] && ! cmp -s "$SUT" "$TMP/m24.sh" && echo 0 || echo 'no se fabrico' )"
H2=$(mktemp -d -p "$TMP"); mkdir -p "$H2/real"; : > "$H2/real/tmp.owned"; ln -sfn "$H2/real" "$H2/casa"
( HOME="$H2/casa" bash "$TMP/m24.sh" --apply "$H2/real" > "$TMP/o" 2>&1 )
check "(24b) M24 vuelve a BORRAR lo que cuelga de \$HOME enlazado" 0 \
  "$( [ ! -e "$H2/real/tmp.owned" ] && echo 0 || echo 'el mutante no cambio nada: caso muerto' )"

# 25-27 · REGRESION DE «HOME AUSENTE». Con `${HOME:-}` el cuarto elemento del bucle quedaba VACIO y
#         la linea siguiente lo saltaba: funcionaba, pero el salto era MUDO — ni canonicalizado ni
#         diagnosticado. Con el centinela, el brazo se compara y no casa: mismo efecto, explicito.
#         Y lo que hay que fijar no es solo que no muera, sino que **las otras tres raices sigan
#         protegidas** y que la purga normal siga funcionando. Se ve CORTAR, no se supone.
S1=$(nido)
check "(25) sin HOME, un scratch normal se purga igual" 0 \
  "$( env -u HOME bash "$SUT" --apply "$S1" > "$TMP/o" 2>&1; echo $? )"
check "(25b) y de hecho borro el fichero de la clase nombrada" 0 \
  "$( [ ! -e "$S1/tmp.uno" ] && echo 0 || echo 'no borro nada' )"
check "(26) sin HOME, la raiz /workspace SIGUE protegida" 2 \
  "$( env -u HOME bash "$SUT" /workspace > "$TMP/o" 2>&1; echo $? )"
check "(26b) y la raiz / tambien" 2 "$( env -u HOME bash "$SUT" / > "$TMP/o" 2>&1; echo $? )"
check "(26c) y /tmp tambien" 2 "$( env -u HOME bash "$SUT" /tmp > "$TMP/o" 2>&1; echo $? )"
check "(27) y el centinela NO se cuela como raiz real" 0 \
  "$( env -u HOME bash "$SUT" --apply "$S1" > "$TMP/o" 2>&1; grep -q 'sin-home' "$TMP/o" && echo 'lo nombra en una purga normal' || echo 0 )"

# 13-14 · LOS SENUELOS QUE ACREDITAN EL CENTINELA. Sin ellos, «rechazo los rc inesperados» seria una
#         afirmacion sin testigo — justo lo que este guion existe para no ser.
printf '#!/usr/bin/env bash\nexit 2\n'   > "$TMP/senuelo2.sh"
printf '#!/usr/bin/env bash\nexit 127\n' > "$TMP/senuelo127.sh"
N=$(nido)
check "(13) un guion que sale 2 NO cuenta como corrida" "NO-PUDE-MIRAR(rc=2)" "$(aplica "$N" "$TMP/senuelo2.sh")"
check "(13b) y el fixture sigue INTACTO, que es como colaba antes" 0 "$( [ -f "$N/tmp.uno" ] && echo 0 || echo 'lo borro' )"
check "(14) un guion que sale 127 tampoco" "NO-PUDE-MIRAR(rc=127)" "$(aplica "$N" "$TMP/senuelo127.sh")"

echo
echo "purge-scratch selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
