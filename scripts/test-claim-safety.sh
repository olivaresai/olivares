#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bateria de scripts/check-claim-safety.sh. Fabrica sus commits con plumbing en el almacen de
# objetos y NO toca ninguna ref: nada que limpiar, nada que otro carril pueda recoger.
set -u

# ⛔ EL ENTORNO GIT AMBIENTE MANDA SOBRE `-C` Y SOBRE EL cwd. Con `GIT_DIR` exportada —y git la
# exporta desde CUALQUIER worktree enlazado, o sea desde cualquier sesion en paralelo— los
# repositorios de usar y tirar de esta bateria se conducirian al repositorio VIVO. Medido el
# 2026-08-06: dejo la rama de la PR #526 apuntando a un commit de fixture. Falla cerrado: un
# saneador que falta es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
SUT="${SUT:-scripts/check-claim-safety.sh}"

# ⛔ EL PREFIJO PROTEGIDO SE FIJA AQUI, Y NO ES COSMETICA. La bateria fabricaba sus fixtures bajo el
# valor POR DEFECTO del SUT, o sea escribiendo la ruta real de los buzones de este repo como literal.
# `lint:export-closure` lo caza —y con razon: esa ruta NO viaja en el arbol publicado, asi que un
# guion exportado la nombraria sin que exista— y puso la pata ROJA para todas las cajas el 2026-08-30.
#
# Fijarlo tiene ademas un valor que la version anterior no daba: al pasar un prefijo PROPIO, esta
# bateria pasa a probar que `OLIVARES_CLAIM_PROTECTED` se HONRA. Antes solo ejercitaba el defecto.
PROTEGIDO_FIXTURE="buzones-de-prueba/"
export OLIVARES_CLAIM_PROTECTED="$PROTEGIDO_FIXTURE"
PASS=0; FAIL=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/tcs.XXXXXX") || exit 2
trap 'rm -rf "$TMP"' EXIT
check(){ local n="$1" e="$2" g="$3"
  if [ "$e" = "$g" ]; then PASS=$((PASS+1)); printf 'ok   %-64s %s\n' "$n" "$g"
  else FAIL=$((FAIL+1)); printf 'FAIL %s esperaba [%s], dio [%s]\n' "$n" "$e" "$g"; fi; }

# --- fabrica: un commit con un mapa ruta=contenido, sin tocar refs ---
mk(){ local parent="$1"; shift
  local idx="$TMP/idx.$$"; rm -f "$idx"
  if [ -n "$parent" ]; then GIT_INDEX_FILE="$idx" git read-tree "$parent"; fi
  local pair path body blob
  for pair in "$@"; do
    path="${pair%%=*}"; body="${pair#*=}"
    blob=$(printf '%s\n' "$body" | git hash-object -w --stdin)
    GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo 100644,"$blob","$path"
  done
  local tree; tree=$(GIT_INDEX_FILE="$idx" git write-tree); rm -f "$idx"
  if [ -n "$parent" ]; then git commit-tree "$tree" -p "$parent" -m t
  else git commit-tree "$tree" -m t; fi; }

LINEAS=$(printf 'l%s\n' 1 2 3 4 5 6 7 8)
BASE=$(mk "" "wf.yml=techo 60" "${PROTEGIDO_FIXTURE}BUZON.md=$LINEAS")
LIMPIO=$(mk "$BASE" "wf.yml=techo 82")
BORRA=$(mk "$BASE" "wf.yml=techo 82" "${PROTEGIDO_FIXTURE}BUZON.md=l1")
IGUAL=$(mk "$BASE" "wf.yml=techo 60")

# `OLIVARES_CLAIM_FILES` es OBLIGATORIA desde la v2 (A-03: siendo opcional, OMITIRLA daba rc 0
# «limpio» — una guarda cuyo defecto es no comprobar nada solo protege a quien se acuerda). El
# helper la pone al valor REAL del commit para que los casos que no hablan del conteo no fallen
# por el; `CF` la sobreescribe cuando un caso SI quiere hablar de el.
# El helper declara AMBAS cifras al valor REAL del commit —ficheros y borrados fuera de lo
# protegido— para que los casos que no hablan de ellas no fallen por ellas; `CF` y `CD` las
# sobreescriben cuando un caso SI quiere hablar de una.
nd(){ git show --numstat --format='' "$1" | awk -F'\t' '$3 !~ /^sessions\/status\/inbox\// && $2 ~ /^[0-9]+$/ {s+=$2} END{print s+0}'; }
corre(){ local n d
         n=$(git show --numstat --format='' "$1" 2>/dev/null | wc -l)
         d=$(git show --numstat --format='' "$1" 2>/dev/null | awk -F'\t' '$3 !~ /^sessions\/status\/inbox\// && $2 ~ /^[0-9]+$/ {s+=$2} END{print s+0}')
         OLIVARES_CLAIM_FILES="${CF:-$n}" OLIVARES_CLAIM_DELETIONS="${CD:-$d}" \
           bash "${2:-$SUT}" "$1" > "$TMP/out" 2>&1; echo $?; }

check "(1) claim limpio -> rc 0" 0 "$(corre "$LIMPIO")"
check "(1b) y lo dice con el conteo de ficheros" 0 "$( grep -q '1 fichero(s), cero borrados' "$TMP/out"; echo $? )"
check "(1c) y la base va RESUELTA a SHA, no como nombre de ref" 0 \
  "$( grep -qE "base ${BASE}\$" "$TMP/out"; echo $? )"

check "(2) claim que BORRA en un buzon -> rc 1" 1 "$(corre "$BORRA")"
check "(2b) y NOMBRA el fichero y cuantas lineas" 0 \
  "$( grep -q "${PROTEGIDO_FIXTURE}BUZON.md: -7" "$TMP/out"; echo $? )"

check "(3) mismo arbol que la base -> 2, NO 0" 2 "$(corre "$IGUAL")"
check "(3b) y dice que la fusion no aporta nada" 0 "$( grep -q 'no aporta nada' "$TMP/out"; echo $? )"

# 3c · LA RAZON DE SER DEL REDISEÑO: un claim DIVERGENTE no se juzga por su `diff` contra la base.
#      Si la base gano lineas por su cuenta, `diff` las marca como borradas y el gate grita en falso.
#      Lo que aterriza es la FUSION, y la fusion las conserva.
DIVERGE=$(mk "$BASE" "otro.txt=algo")
BASE2=$(mk "$BASE" "${PROTEGIDO_FIXTURE}BUZON.md=$(printf 'l%s\n' 1 2 3 4 5 6 7 8 9)")
check "(3c) claim divergente cuya BASE crecio: limpio, no falso positivo" 0 \
  "$( n=$(git show --numstat --format='' "$DIVERGE" | wc -l); OLIVARES_CLAIM_FILES="$n" bash "$SUT" "$DIVERGE" "$BASE2" > "$TMP/out" 2>&1; echo $? )"
check "(3d) y el diff CRUDO contra esa base si habria gritado" 1 \
  "$( git diff --numstat "$BASE2" "$DIVERGE" | awk '$1=="0" && $2>0 {n++} END{print (n>0)?1:0}' )"

check "(4) sin argumento -> 2" 2 "$( bash "$SUT" > "$TMP/out" 2>&1; echo $? )"
check "(5) commit inexistente -> 2" 2 "$( bash "$SUT" 0000000000000000000000000000000000000000 > "$TMP/out" 2>&1; echo $? )"
check "(6) base que no resuelve -> 2" 2 "$( bash "$SUT" "$LIMPIO" no-existe-esta-base > "$TMP/out" 2>&1; echo $? )"

check "(7) declarar 1 fichero cuando toca 2 -> 1" 1 \
  "$( OLIVARES_CLAIM_FILES=1 bash "$SUT" "$BORRA" > "$TMP/out" 2>&1; echo $? )"
check "(7b) y su mensaje da los DOS numeros" 0 "$( grep -q 'declaraste 1 fichero(s) y toca 2' "$TMP/out"; echo $? )"
check "(8) declarar el numero correcto no molesta" 0 \
  "$( OLIVARES_CLAIM_FILES=1 OLIVARES_CLAIM_DELETIONS="$(nd "$LIMPIO")" bash "$SUT" "$LIMPIO" > "$TMP/out" 2>&1; echo $? )"
check "(9) OLIVARES_CLAIM_FILES no numerico -> 2" 2 \
  "$( OLIVARES_CLAIM_FILES=uno bash "$SUT" "$LIMPIO" > "$TMP/out" 2>&1; echo $? )"

# 10 · MUTANTE · quitar la comprobacion de borrados protegidos
sed 's|if \[ -n "$MAL" \]; then|if false; then|' "$SUT" > "$TMP/m1.sh"
check "(10a) el mutante de borrados REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m1.sh" && echo 1 || echo 0 )"
M1=$(corre "$BORRA" "$TMP/m1.sh")
check "(10b) MUTANTE 'no miro borrados' es CAZADO por su rc" 0 "$( [ "$M1" = "0" ] && echo 0 || echo "dio $M1" )"
check "(10c) y por su MENSAJE: llama LIMPIO a un commit que borra 7 lineas de correo" 0 \
  "$( grep -q 'limpio —' "$TMP/out"; echo $? )"

# 11 · MUTANTE · quitar la guarda del diff vacio (el 0 por silencio)
sed 's|if \[ -z "$NUM" \]; then|if false; then|' "$SUT" > "$TMP/m2.sh"
check "(11a) el mutante del diff vacio REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m2.sh" && echo 1 || echo 0 )"
M2=$(CF=1 corre "$IGUAL" "$TMP/m2.sh")
check "(11b) MUTANTE 'diff vacio vale' es CAZADO por su rc" 0 "$( [ "$M2" = "0" ] && echo 0 || echo "dio $M2" )"
# ⛔ Y el mutante cuenta «1 fichero(s)», NO cero: `printf '%s\n' ""` emite UNA linea vacia, asi que
# `wc -l` da 1 sobre la lista vacia. Es la clase «una lista vacia trae sus delimitadores», y por eso
# el caso exige el texto exacto: si aqui se hubiera puesto «0 fichero(s)» a ojo, el caso fallaria y
# se habria «arreglado» aflojandolo, que es como un mutante sobrevive con la bateria en verde.
check "(11c) y por su MENSAJE: lo llama limpio contando 1 fichero que no existe" 0 \
  "$( grep -q '1 fichero(s), cero borrados' "$TMP/out"; echo $? )"

# --- v2 (curas del NO de the reviewer) -------------------------------------------------------------
# A-03 · SIN la declaracion es 2, NO 0.
check "(24) omitir OLIVARES_CLAIM_FILES -> 2, no 0" 2 \
  "$( bash "$SUT" "$LIMPIO" > "$TMP/out" 2>&1; echo $? )"
check "(24b) y dice QUE falta y como se pasa" 0 \
  "$( grep -q 'falta OLIVARES_CLAIM_FILES' "$TMP/out" && grep -q 'OLIVARES_CLAIM_FILES=1' "$TMP/out"; echo $? )"

# A-04 · MODOS. `--numstat` cuenta LINEAS; el modo viaja en el ARBOL y solo lo ve `--summary`.
# Nace de un defecto real: un claim mio volvio esta misma bateria de 100755 a 100644 y la guarda
# dijo «limpio» — una bateria no ejecutable es una bateria que el gancho no puede correr (rc 126).
BLOBX=$(printf 'echo hola\n' | git hash-object -w --stdin)
idx="$TMP/idx.modo"; rm -f "$idx"; GIT_INDEX_FILE="$idx" git read-tree "$BASE"
GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo 100755,"$BLOBX",scripts/x.sh
T755=$(GIT_INDEX_FILE="$idx" git write-tree); CON755=$(git commit-tree "$T755" -p "$BASE" -m t)
rm -f "$idx"; GIT_INDEX_FILE="$idx" git read-tree "$CON755"
GIT_INDEX_FILE="$idx" git update-index --cacheinfo 100644,"$BLOBX",scripts/x.sh
T644=$(GIT_INDEX_FILE="$idx" git write-tree); MODO=$(git commit-tree "$T644" -p "$CON755" -m t); rm -f "$idx"
check "(25) un cambio de MODO 755->644 -> rc 1" 1 "$(corre "$MODO")"
check "(25b) y lo NOMBRA con la palabra que usa git" 0 "$( grep -q 'mode change' "$TMP/out"; echo $? )"
check "(25c) y dice que --numstat no lo ve" 0 "$( grep -q 'numstat' "$TMP/out"; echo $? )"

idx="$TMP/idx.n"; rm -f "$idx"; GIT_INDEX_FILE="$idx" git read-tree "$BASE"
GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo 100644,"$BLOBX",scripts/nace-mal.sh
TN=$(GIT_INDEX_FILE="$idx" git write-tree); NACE=$(git commit-tree "$TN" -p "$BASE" -m t); rm -f "$idx"
check "(26) guion NUEVO bajo scripts/ con modo 100644 -> rc 1" 1 "$(corre "$NACE")"
check "(26b) y explica el rc 126 que produciria" 0 "$( grep -q '126' "$TMP/out"; echo $? )"
check "(26c) el MISMO guion creado 100755 no es hallazgo de modo" 0 \
  "$( corre "$CON755" >/dev/null; grep -q 'mode change\|NO EJECUTABLES' "$TMP/out" && echo "hallazgo de mas" || echo 0 )"
check "(26d) y ese caso SI lleva el aviso de guion nuevo bajo scripts/" 0 \
  "$( grep -q 'guion(es) NUEVO(s) bajo scripts/' "$TMP/out"; echo $? )"

# MUTANTE · quitar la sonda de modos: el defecto real volveria a pasar por limpio
sed 's|if \[ -n "$MODO" \]; then|if false; then|' "$SUT" > "$TMP/mm.sh"
check "(27a) el mutante de modos REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mm.sh" && echo 1 || echo 0 )"
M27=$(corre "$MODO" "$TMP/mm.sh")
check "(27b) MUTANTE 'no miro modos' deja pasar el cambio de modo" 0 "$( [ "$M27" = "0" ] && echo 0 || echo "dio $M27" )"
check "(27c) y por su MENSAJE lo llama limpio" 0 "$( grep -q 'limpio —' "$TMP/out"; echo $? )"

# --- v3 · LA RAMA DE CONFLICTO, QUE FUNCIONABA Y NO MATABA A NADIE ------------------------------
# ⛔ the reviewer (2026-08-30T20:14Z): la guarda corta el conflicto (`merge-tree` rc != 0 -> rc 2) y esta
# bateria NO tenia ni una fila sobre esa comparacion: retirar `RCM -ne 0` conservaba 34/0. Una rama
# que funciona y que nadie mata es una rama que se puede borrar sin que el verde se entere — la
# clase «el arreglo pone verde por otra via». Aqui va su fixture y su mutante.
ANC=$(mk "" "wf.yml=techo 60" "${PROTEGIDO_FIXTURE}BUZON.md=$LINEAS")
LADO_A=$(mk "$ANC" "wf.yml=techo 82")
LADO_B=$(mk "$ANC" "wf.yml=techo 90")
check "(28) los dos lados difieren de verdad (control del fixture)" 0 \
  "$( [ "$(git rev-parse "${LADO_A}^{tree}")" != "$(git rev-parse "${LADO_B}^{tree}")" ] && echo 0 || echo "los lados son iguales: el fixture no conflicta" )"
C28=$( OLIVARES_CLAIM_FILES=1 bash "$SUT" "$LADO_B" "$LADO_A" > "$TMP/out" 2>&1; echo $? )
check "(28b) fusion que CONFLICTA -> 2, no un veredicto" 2 "$C28"
check "(28c) y su MENSAJE lo dice con la palabra CONFLICTA" 0 "$( grep -q 'CONFLICTA' "$TMP/out"; echo $? )"
check "(28d) y NOMBRA los dos SHAs de la fusion" 0 \
  "$( grep -q "${LADO_B}" "$TMP/out" && grep -q "${LADO_A}" "$TMP/out"; echo $? )"
check "(28e) y NO cuela un veredicto de limpieza" 0 \
  "$( grep -q 'limpio —' "$TMP/out" && echo "dio un veredicto sobre una fusion que no existe" || echo 0 )"

# 29 · MUTANTE · retirar la comprobacion del rc de merge-tree: `$FUS` viene NO vacio aunque haya
#      conflicto —merge-tree escribe igualmente un arbol— asi que la guarda seguiria y daria un
#      veredicto sobre una fusion que nadie puede materializar.
sed 's|if \[ "$RCM" -ne 0 \] \|\| \[ -z "$FUS" \]; then|if [ -z "$FUS" ]; then|' "$SUT" > "$TMP/mrcm.sh"
check "(29a) el mutante de RCM REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mrcm.sh" && echo 1 || echo 0 )"
M29=$( OLIVARES_CLAIM_FILES=1 bash "$TMP/mrcm.sh" "$LADO_B" "$LADO_A" > "$TMP/out" 2>&1; echo $? )
# ⛔ EL MUTANTE SIGUE DANDO 2, Y POR ESO ESTA FILA SE JUZGA POR EL MENSAJE. Medido: con un
# conflicto real `merge-tree --write-tree` devuelve **rc 1 y stdout NO VACIO** (el arbol y la
# informacion del conflicto), asi que quitando `RCM` la guarda sigue y muere mas abajo, cuando
# `git diff` recibe ese texto en vez de un arbol. Es decir: **lo mata la guarda de al lado**, y un
# caso que exigiera «rc distinto de 2» acreditaria CERO. Lo que discrimina es que el diagnostico
# propio de esta rama —la palabra CONFLICTA— DESAPARECE.
#
# ⛔ AQUI DECIA «272 bytes», Y ESA CIFRA NO DEBIA ESTAR: **depende del FIXTURE, no de git**. Medido
# el 2026-08-30 con git 2.39.5 variando SOLO la ruta en conflicto: `f.txt` 273 · `wf.yml` **278** ·
# `scripts/un-nombre-mas-largo.sh` 398 — el conflicto imprime la ruta, asi que el tamaño escala con
# ella. Mis 272 salieron ademas de una sonda de usar y tirar sobre `f.txt` y CAPTURADOS (`$(...)`
# come la nueva de cola: 273 crudo -> 272). El lector midio 278 porque midio sobre `wf.yml`, que es
# el fixture de ESTA bateria: **su cifra era la correcta para el sujeto y la mia para otro**.
# Lo que el argumento necesita es «NO VACIO»; el numero exacto no sostiene nada y envejece con
# cualquier cambio de fixture.
check "(29b) MUTANTE 'no miro el rc de merge-tree' DEJA DE NOMBRAR el conflicto" 0 \
  "$( grep -q 'CONFLICTA' "$TMP/out" && echo "sigue nombrandolo: el mutante no cambio nada" || echo 0 )"
check "(29c) y muere por OTRA guarda, no por la suya (por eso el rc no discrimina)" 0 \
  "$( [ "$M29" = "2" ] && grep -q 'NO HE PODIDO MIRAR' "$TMP/out" && echo 0 || echo "murio de otro modo: rc=$M29" )"

# --- v3 · BORRADOS FUERA DE LO PROTEGIDO --------------------------------------------------------
# ⛔ Nace de un fallo mio que esta misma guarda dio por LIMPIO: publique un claim que borraba 24
# lineas del `Taskfile` por reusar ficheros construidos contra una base anterior, y como no eran
# lineas de buzon, paso. Un guarda que solo protege lo que su autor recordo proteger deja fuera lo
# que no previo — y de rebote lo heredaban las sondas de verificacion de otro carril.
BORRABLE=$(mk "$BASE" "wf.yml=una sola linea")
check "(30) un claim que BORRA fuera de lo protegido -> rc 1" 1 "$(CF=1 CD=0 corre "$BORRABLE")"
check "(30b) y NOMBRA el fichero con su cuenta" 0 "$( grep -qE 'wf.yml: -[0-9]+' "$TMP/out"; echo $? )"
check "(30c) y dice las DOS causas posibles, no una" 0 \
  "$( grep -q 'construido contra otra base' "$TMP/out" && grep -q 'revirtiendo trabajo ajeno' "$TMP/out"; echo $? )"
check "(30d) y da la receta para declararlos" 0 "$( grep -q 'OLIVARES_CLAIM_DELETIONS=' "$TMP/out"; echo $? )"
n30=$(nd "$BORRABLE")
check "(30e) declarando esa cifra exacta, pasa" 0 \
  "$( OLIVARES_CLAIM_FILES=1 OLIVARES_CLAIM_DELETIONS="$n30" bash "$SUT" "$BORRABLE" > "$TMP/out" 2>&1; echo $? )"
check "(30f) OLIVARES_CLAIM_DELETIONS no numerico -> 2" 2 \
  "$( OLIVARES_CLAIM_FILES=1 OLIVARES_CLAIM_DELETIONS=dos bash "$SUT" "$BORRABLE" > "$TMP/out" 2>&1; echo $? )"

# 31 · MUTANTE · quitar la comprobacion: vuelve el falso «limpio» sobre un claim que destruye
python3 - "$SUT" "$TMP/mb.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
v='if [ "$BORRADAS" -gt "$BORRA_OK" ]; then'
n='if false; then'
assert o.count(v)==1, "el patron del mutante de borrados no casa"
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(31a) el mutante de borrados se FABRICO y difiere" 0 \
  "$( [ -s "$TMP/mb.sh" ] && ! cmp -s "$SUT" "$TMP/mb.sh" && echo 0 || echo "no se fabrico" )"
M31=$(CF=1 CD=0 corre "$BORRABLE" "$TMP/mb.sh")
check "(31b) MUTANTE 'no miro borrados' da rc 0 sobre un claim que destruye" 0 \
  "$( [ "$M31" = "0" ] && echo 0 || echo "dio $M31" )"
check "(31c) y por su MENSAJE lo llama limpio" 0 "$( grep -q 'limpio —' "$TMP/out"; echo $? )"

echo
echo "check-claim-safety selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
