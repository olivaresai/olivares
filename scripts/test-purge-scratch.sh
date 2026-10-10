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
  else printf 'FAIL %s expected [%s], got [%s]\n' "$d" "$e" "$g"; FAIL=$((FAIL+1)); fi; }

# ⛔ UN rc INESPERADO ES «NO HE PODIDO MIRAR», NUNCA UN PASE. Lo levanto el lector 47 y el sitio
# donde ocurria es el peor posible: ESTE guion nacio para curar la clase del rc fantasma y la tenia
# dentro. Mecanismo: casi todas las filas comprueban SUPERVIVENCIA (`[ -e ]`), asi que un mutante
# que muera al instante —`exit 2`, `exit 127`— no borra nada y **todas las filas de supervivencia
# pasan**. El banco daba 23/0 sobre un guion que no habia hecho nada.
#
# `aplica` exige que la corrida haya producido su veredicto: rc esperado Y su linea de resumen. Si
# no, devuelve un centinela que ninguna fila acepta. Y hay dos filas que lo comprueban con senuelos
# que salen 2 y 127: sin ellas, este arreglo seria otra afirmacion sin testigo.
aplica(){ # 1=directory  2=script (defaults to SUT)  -> OK | COULD-NOT-CHECK(rc=N)
  local rc; bash "${2:-$SUT}" --apply "$1" > "$TMP/o" 2>&1; rc=$?
  if [ "$rc" != 0 ] || ! grep -q 'purge-scratch: deleted' "$TMP/o"; then echo "COULD-NOT-CHECK(rc=$rc)"; else echo OK; fi; }

# Un fixture nuevo por caso: un banco que comparte estado no mide lo que cree.
nido(){ local d; d=$(mktemp -d -p "$TMP"); mkdir -p "$d/wt-a" "$d/cc-socks" "$d/claude-1000"
  : > "$d/tmp.uno"; : > "$d/basura.bin"
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$d/wt-a/bus.sock"
  python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$d/tmp.sock"
  printf '%s' "$d"; }

# --- el argumento y las negativas -----------------------------------------------------------
check "(1) no directory -> rc 2" 2 "$( bash "$SUT" >"$TMP/o" 2>&1; echo $? )"
check "(1b) reports what it DOES honor (the env), not only what is missing" 0 "$( grep -q 'OLIVARES_SCRATCH_DIR' "$TMP/o"; echo $? )"
check "(2) nonexistent directory -> rc 2" 2 "$( bash "$SUT" "$TMP/no-existe" >"$TMP/o" 2>&1; echo $? )"
check "(3) refuses /workspace, which is not scratch space" 2 "$( bash "$SUT" /workspace >"$TMP/o" 2>&1; echo $? )"
check "(4) also refuses /" 2 "$( bash "$SUT" / >"$TMP/o" 2>&1; echo $? )"

# --- el modo por defecto NO borra -----------------------------------------------------------
N=$(nido)
bash "$SUT" "$N" > "$TMP/o" 2>&1
check "(5) defaults to DRY RUN and reports it" 0 "$( grep -q 'DRY RUN' "$TMP/o"; echo $? )"
check "(5b) the candidate file REMAINS" 0 "$( [ -f "$N/tmp.uno" ] && echo 0 || echo 'deleted it without --apply' )"

# --- lista de INCLUSION: solo se borra lo nombrado -------------------------------------------
N=$(nido); check "(6-pre) the run produced its verdict" OK "$(aplica "$N")"
check "(6) --apply deletes the file in a named class" 0 "$( [ ! -e "$N/tmp.uno" ] && echo 0 || echo 'survived' )"
check "(6b) does NOT delete anything outside the named classes" 0 "$( [ -f "$N/basura.bin" ] && echo 0 || echo 'deleted something outside the named classes' )"

# --- el TIPO manda, aunque el nombre case ----------------------------------------------------
check "(7) a socket MATCHING the class is rejected by TYPE" 0 "$( [ -S "$N/tmp.sock" ] && echo 0 || echo 'deleted a socket' )"
check "(7b) the reason explains why it matters" 0 "$( grep -q 'must remain discoverable' "$TMP/o"; echo $? )"

# --- la guarda que me falto: rm -rf es CIEGO al tipo -----------------------------------------
check "(8) a directory CONTAINING a socket is rejected" 0 "$( [ -d "$N/wt-a" ] && echo 0 || echo 'deleted the directory containing the socket' )"
check "(8b) NAMES the socket protecting it" 0 "$( grep -q 'wt-a/bus.sock' "$TMP/o"; echo $? )"

# --- los nombres intocables, comprobables solo con las clases abiertas ------------------------
N=$(nido); check "(9-pre) the run produced its verdict" OK "$(OLIVARES_PURGE_DIR_CLASSES='*' aplica "$N")"
check "(9) with classes set to '*', cc-socks survives by NAME" 0 "$( [ -d "$N/cc-socks" ] && echo 0 || echo 'deleted the bus socket' )"
check "(9b) claude-1000 also survives" 0 "$( [ -d "$N/claude-1000" ] && echo 0 || echo 'deleted the harness scratch space' )"
check "(9c) the reason calls them protected" 0 "$( grep -q 'protected name' "$TMP/o"; echo $? )"

# --- MUTANTES ---------------------------------------------------------------------------------
mut(){ python3 - "$SUT" "$2" "$3" "$4" <<'PYEOF'
import sys
o=open(sys.argv[1]).read(); v=sys.argv[3]; n=sys.argv[4]
if o.count(v)!=1: sys.exit("the mutant pattern does not match exactly once: "+v[:50])
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
}

# M1 · ciego a lo que hay DENTRO del directorio: vuelve mi corte del bus
mut x "$TMP/m1.sh" 'dentro=$(find "$d" \( -type s -o -type p \) 2>/dev/null | head -3)' 'dentro=""'
check "(10a) M1 constructed and differs" 0 "$( [ -s "$TMP/m1.sh" ] && ! cmp -s "$SUT" "$TMP/m1.sh" && echo 0 || echo 'was not constructed' )"
N=$(nido); check "(10-pre) the mutant RAN (not a phantom rc)" OK "$(aplica "$N" "$TMP/m1.sh")"
check "(10b) M1 DELETES the directory containing the socket" 0 "$( [ ! -e "$N/wt-a" ] && echo 0 || echo 'the mutant changed nothing: ineffective case' )"

# M2 · sin lista de nombres: con las clases abiertas, cae el bus
mut x "$TMP/m2.sh" '_intocable(){ local n; for n in "${INTOCABLES[@]}"; do [ "$1" = "$n" ] && return 0; done; return 1; }' '_intocable(){ return 1; }'
check "(11a) M2 constructed and differs" 0 "$( [ -s "$TMP/m2.sh" ] && ! cmp -s "$SUT" "$TMP/m2.sh" && echo 0 || echo 'was not constructed' )"
N=$(nido); check "(11-pre) the mutant RAN (not a phantom rc)" OK "$(OLIVARES_PURGE_DIR_CLASSES='*' aplica "$N" "$TMP/m2.sh")"
check "(11b) M2 deletes cc-socks" 0 "$( [ ! -e "$N/cc-socks" ] && echo 0 || echo 'the mutant changed nothing: ineffective case' )"

# M3 · sin comprobacion de tipo en ficheros: borra el socket que casa la clase
mut x "$TMP/m3.sh" 'if [ -S "$f" ] || [ -p "$f" ]; then' 'if false; then'
check "(12a) M3 constructed and differs" 0 "$( [ -s "$TMP/m3.sh" ] && ! cmp -s "$SUT" "$TMP/m3.sh" && echo 0 || echo 'was not constructed' )"
N=$(nido); check "(12-pre) the mutant RAN (not a phantom rc)" OK "$(aplica "$N" "$TMP/m3.sh")"
# ⛔ ESTE CASO SE JUZGA POR EL MENSAJE, y la razon la descubrio el propio mutante: al neutralizar la
# comprobacion de tipo el socket SIGUE sobreviviendo, porque el `[ -f ]` de mas abajo ya lo excluye.
# Es decir, mi caso (7) pasaba por una guarda DISTINTA de la que nombraba. Lo que la rama aporta es
# el diagnostico —sin ella el socket se cae de la lista en silencio—, asi que lo que tiene que
# desaparecer con el mutante es la RAZON, no el fichero.
check "(12b) M3 still does not delete it (protected by [ -f ], not the branch)" 0 \
  "$( [ -S "$N/tmp.sock" ] && echo 0 || echo 'deleted it' )"
check "(12c) but STOPS REPORTING why it preserves it" 0 \
  "$( grep -q 'tmp.sock' "$TMP/o" && echo 'still reports the reason: the mutant changed nothing' || echo 0 )"

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
check "(15-pre) the run produced its verdict" OK "$V"
check "(15) a file with an OPEN DESCRIPTOR is preserved" 0 "$( [ -f "$U/tmp.conFD" ] && echo 0 || echo 'deleted it with an open fd' )"
check "(15b) reports it with the reason" 0 "$( grep -q 'IN USE: a process has it open' "$TMP/o"; echo $? )"
check "(16) a directory used as a process CWD is preserved" 0 "$( [ -d "$U/wt-cwd" ] && echo 0 || echo 'deleted a live process cwd' )"
check "(16b) reports it" 0 "$( grep -q 'IN USE: a process has its working directory' "$TMP/o"; echo $? )"
check "(17) NEGATIVE CONTROL: the UNUSED file is deleted" 0 "$( [ ! -e "$U/tmp.uno" ] && echo 0 || echo 'deletes nothing: the guard is too broad' )"

# 18 · MUTANTE de las guardas de uso: sin ellas vuelve el caso del lector 47
python3 - "$SUT" "$TMP/m18.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
v='case "$u" in "$r"|"$r"/*) return 0;; esac'
n='case "$u" in __nunca_casa__) return 0;; esac'
if o.count(v)!=1: sys.exit("mutant 18 pattern does not match")
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(18a) M18 constructed and differs" 0 "$( [ -s "$TMP/m18.sh" ] && ! cmp -s "$SUT" "$TMP/m18.sh" && echo 0 || echo 'was not constructed' )"
U2=$(nido); : > "$U2/tmp.conFD"
( exec 9< "$U2/tmp.conFD"; sleep 30 ) & FDPID2=$!
sleep 1
check "(18-pre) the mutant RAN" OK "$(aplica "$U2" "$TMP/m18.sh")"
check "(18b) M18 DELETES the file with an open fd" 0 "$( [ ! -e "$U2/tmp.conFD" ] && echo 0 || echo 'the mutant changed nothing: ineffective case' )"
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
check "(19-pre) with a RELATIVE path, the run produced its verdict" 0 "$V"
check "(19) the file with an open FD REMAINS" 0 \
  "$( [ -f "$R1/tmp.conFD" ] && echo 0 || echo 'a relative path bypasses the in-use guard' )"
check "(19b) reports it with its reason" 0 "$( grep -q 'IN USE' "$TMP/o"; echo $? )"
check "(20) NEGATIVE CONTROL: with a relative path, the UNUSED file is deleted" 0 \
  "$( [ ! -e "$R1/tmp.uno" ] && echo 0 || echo 'deletes nothing: the guard is too broad' )"
kill $RFD 2>/dev/null; wait $RFD 2>/dev/null

# 21 · el MISMO defecto en la negativa de raices: comparaba TEXTO, asi que un `.` estando en una raiz
#      peligrosa no casaba. Con la canonicalizacion por delante, si.
check "(21) a RELATIVE path pointing to a dangerous root is rejected" 2 \
  "$( cd / && bash "$SUT" . > "$TMP/o" 2>&1; echo $? )"

# 22-24 · LAS DOS MITADES DE LA COMPARACION. La v3 canonicalizaba el ARGUMENTO y comparaba contra
#         `$HOME` **crudo**: con HOME siendo un enlace al scratch, la negativa no disparaba y el
#         lector 47 borro un fichero con rc 0 y cero rechazos. **Canonicalizar una mitad deja la
#         guarda igual de ciega.** Y se prueba la clase, no la instancia: `/tmp` puede ser un enlace
#         en otra maquina y el defecto seria el mismo.
H=$(mktemp -d -p "$TMP"); mkdir -p "$H/real"; : > "$H/real/tmp.owned"; ln -sfn "$H/real" "$H/casa"
RC22=$( HOME="$H/casa" bash "$SUT" --apply "$H/real" > "$TMP/o" 2>&1; echo $? )
check "(22) a DIR resolving to \$HOME through a symlink is rejected" 2 "$RC22"
check "(22b) the reason names the resolved root" 0 "$( grep -q 'resolves to' "$TMP/o"; echo $? )"
check "(22c) the file REMAINS" 0 "$( [ -f "$H/real/tmp.owned" ] && echo 0 || echo 'deleted it' )"

# 23 · CONTROL NEGATIVO: con el mismo HOME enlazado, un scratch de verdad se sigue purgando —
#      si la negativa fuese demasiado ancha, el banco saldria igual de verde.
L=$(nido)
check "(23) NEGATIVE CONTROL: normal scratch space is still purged" OK "$( HOME="$H/casa" aplica "$L" )"
check "(23b) actually deleted the file in the named class" 0 \
  "$( [ ! -e "$L/tmp.uno" ] && echo 0 || echo 'deleted nothing' )"

# 24 · MUTANTE: comparar contra la referencia CRUDA vuelve al agujero del 47
python3 - "$SUT" "$TMP/m24.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
v='if [ "$DIR" = "$(_canon "$_raiz")" ]; then'
n='if [ "$DIR" = "$_raiz" ]; then'
if o.count(v)!=1: sys.exit("mutant 24 pattern does not match")
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(24a) M24 constructed and differs" 0 "$( [ -s "$TMP/m24.sh" ] && ! cmp -s "$SUT" "$TMP/m24.sh" && echo 0 || echo 'was not constructed' )"
H2=$(mktemp -d -p "$TMP"); mkdir -p "$H2/real"; : > "$H2/real/tmp.owned"; ln -sfn "$H2/real" "$H2/casa"
( HOME="$H2/casa" bash "$TMP/m24.sh" --apply "$H2/real" > "$TMP/o" 2>&1 )
check "(24b) M24 again DELETES contents under symlinked \$HOME" 0 \
  "$( [ ! -e "$H2/real/tmp.owned" ] && echo 0 || echo 'the mutant changed nothing: ineffective case' )"

# 25-27 · REGRESION DE «HOME AUSENTE». Con `${HOME:-}` el cuarto elemento del bucle quedaba VACIO y
#         la linea siguiente lo saltaba: funcionaba, pero el salto era MUDO — ni canonicalizado ni
#         diagnosticado. Con el centinela, el brazo se compara y no casa: mismo efecto, explicito.
#         Y lo que hay que fijar no es solo que no muera, sino que **las otras tres raices sigan
#         protegidas** y que la purga normal siga funcionando. Se ve CORTAR, no se supone.
S1=$(nido)
check "(25) without HOME, normal scratch space is still purged" 0 \
  "$( env -u HOME bash "$SUT" --apply "$S1" > "$TMP/o" 2>&1; echo $? )"
check "(25b) actually deleted the file in the named class" 0 \
  "$( [ ! -e "$S1/tmp.uno" ] && echo 0 || echo 'deleted nothing' )"
check "(26) without HOME, /workspace REMAINS protected" 2 \
  "$( env -u HOME bash "$SUT" /workspace > "$TMP/o" 2>&1; echo $? )"
check "(26b) / also remains protected" 2 "$( env -u HOME bash "$SUT" / > "$TMP/o" 2>&1; echo $? )"
check "(26c) /tmp also remains protected" 2 "$( env -u HOME bash "$SUT" /tmp > "$TMP/o" 2>&1; echo $? )"
check "(27) the sentinel is NOT accepted as a real root" 0 \
  "$( env -u HOME bash "$SUT" --apply "$S1" > "$TMP/o" 2>&1; grep -q 'sin-home' "$TMP/o" && echo 'names it during a normal purge' || echo 0 )"

# 13-14 · LOS SENUELOS QUE ACREDITAN EL CENTINELA. Sin ellos, «rechazo los rc inesperados» seria una
#         afirmacion sin testigo — justo lo que este guion existe para no ser.
printf '#!/usr/bin/env bash\nexit 2\n'   > "$TMP/decoy2.sh"
printf '#!/usr/bin/env bash\nexit 127\n' > "$TMP/decoy127.sh"
N=$(nido)
check "(13) a script exiting 2 does NOT count as a run" "COULD-NOT-CHECK(rc=2)" "$(aplica "$N" "$TMP/decoy2.sh")"
check "(13b) the fixture remains INTACT, which previously concealed the defect" 0 "$( [ -f "$N/tmp.uno" ] && echo 0 || echo 'deleted it' )"
check "(14) a script exiting 127 does not count either" "COULD-NOT-CHECK(rc=127)" "$(aplica "$N" "$TMP/decoy127.sh")"

echo
echo "purge-scratch selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
