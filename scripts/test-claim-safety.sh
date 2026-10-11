#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bateria de scripts/check-claim-safety.sh. Fabrica sus commits con plumbing en un repositorio PROPIO
# de usar y tirar: el repositorio desde el que se invoca no recibe objetos, indices ni refs (v4).
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
# ⛔ EL SUJETO Y LA PROPIA BATERIA SE FIJAN POR RUTA ABSOLUTA ANTES DE SALIR DEL REPOSITORIO QUE
# INVOCA: la bateria trabaja dentro de su repositorio desechable (abajo) y una ruta relativa dejaria de
# nombrar el guion bajo prueba al cambiar el cwd. `SUT` conserva su semantica —relativa al cwd de quien
# invoca, por defecto `scripts/check-claim-safety.sh`—; solo deja de depender del cwd despues.
SUT="${SUT:-scripts/check-claim-safety.sh}"
case "$SUT" in /*) ;; *) SUT="$PWD/$SUT" ;; esac
[ -r "$SUT" ] || { echo "FATAL: cannot read the subject $SUT" >&2; exit 2; }
BATERIA="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/$(basename -- "${BASH_SOURCE[0]:-$0}")"

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
# Los mutantes son copias del sujeto dentro de `$TMP`, y el sujeto carga `<su directorio>/lib/git-env.sh`:
# sin este enlace un mutante saldria 2 por no encontrar la libreria ANTES de llegar a la linea mutada.
ln -s "$(dirname -- "$SUT")/lib" "$TMP/lib" || exit 2

# ⛔ LOS FIXTURES VIVEN EN UN REPOSITORIO DE USAR Y TIRAR, NO EN EL QUE INVOCA. Hasta el 2026-09-11
# esta bateria —pata del gancho— fabricaba blobs, arboles, indices y commits con plumbing EN EL
# REPOSITORIO DESDE EL QUE SE CORRIA: medido invocandola desde un repositorio de laboratorio, sus
# ficheros bajo `objects/` pasaron de 2 a 43 en una sola pasada, y nada los alcanzaba despues. El
# saneador de arriba va primero; el repositorio se crea bajo `$TMP` y la bateria ENTRA en el. Si git
# no resolviera ESE repositorio desde dentro, es 2 antes del primer fixture, no un intento.
REPO="$TMP/repo"
entra_repo_desechable(){
  git init -q "$REPO" >/dev/null 2>&1 || return 1
  CDPATH= cd -- "$REPO" || return 1
  [ "$(CDPATH= cd -- "$(git rev-parse --absolute-git-dir 2>/dev/null)" 2>/dev/null && pwd -P)" = \
    "$(CDPATH= cd -- "$REPO/.git" && pwd -P)" ] || return 1
  git config user.email bateria@example.invalid && git config user.name bateria && git config commit.gpgsign false; }
entra_repo_desechable || { echo "FATAL: could not enter the disposable repository $REPO" >&2; exit 2; }
check(){ local n="$1" e="$2" g="$3"
  if [ "$e" = "$g" ]; then PASS=$((PASS+1)); printf 'ok   %-64s %s\n' "$n" "$g"
  else FAIL=$((FAIL+1)); printf 'FAIL %s expected [%s], got [%s]\n' "$n" "$e" "$g"; fi; }

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

check "(1) clean claim returns rc 0" 0 "$(corre "$LIMPIO")"
check "(1b) reports the file count" 0 "$( grep -q '1 file(s), no deletions' "$TMP/out"; echo $? )"
check "(1c) the base resolves to a SHA instead of a ref name" 0 \
  "$( grep -qE "base ${BASE}\$" "$TMP/out"; echo $? )"

check "(2) claim deleting mailbox contents returns rc 1" 1 "$(corre "$BORRA")"
check "(2b) names the file and deleted-line count" 0 \
  "$( grep -q "${PROTEGIDO_FIXTURE}BUZON.md: -7" "$TMP/out"; echo $? )"

check "(3) same tree as base returns 2" 2 "$(corre "$IGUAL")"
check "(3b) reports that the merge contributes nothing" 0 "$( grep -q 'empty change' "$TMP/out"; echo $? )"

# 3c · LA RAZON DE SER DEL REDISEÑO: un claim DIVERGENTE no se juzga por su `diff` contra la base.
#      Si la base gano lineas por su cuenta, `diff` las marca como borradas y el gate grita en falso.
#      Lo que aterriza es la FUSION, y la fusion las conserva.
DIVERGE=$(mk "$BASE" "otro.txt=algo")
BASE2=$(mk "$BASE" "${PROTEGIDO_FIXTURE}BUZON.md=$(printf 'l%s\n' 1 2 3 4 5 6 7 8 9)")
check "(3c) divergent claim with a growing BASE is clean without a false positive" 0 \
  "$( n=$(git show --numstat --format='' "$DIVERGE" | wc -l); OLIVARES_CLAIM_FILES="$n" bash "$SUT" "$DIVERGE" "$BASE2" > "$TMP/out" 2>&1; echo $? )"
check "(3d) the RAW diff against that base would report deletions" 1 \
  "$( git diff --numstat "$BASE2" "$DIVERGE" | awk '$1=="0" && $2>0 {n++} END{print (n>0)?1:0}' )"

check "(4) no argument returns 2" 2 "$( bash "$SUT" > "$TMP/out" 2>&1; echo $? )"
check "(5) nonexistent commit returns 2" 2 "$( bash "$SUT" 0000000000000000000000000000000000000000 > "$TMP/out" 2>&1; echo $? )"
check "(6) unresolved base returns 2" 2 "$( bash "$SUT" "$LIMPIO" no-existe-esta-base > "$TMP/out" 2>&1; echo $? )"

check "(7) declaring 1 file when 2 are required returns 1" 1 \
  "$( OLIVARES_CLAIM_FILES=1 bash "$SUT" "$BORRA" > "$TMP/out" 2>&1; echo $? )"
check "(7b) its message reports BOTH counts" 0 "$( grep -q '1 file(s) declared, but the change touches 2' "$TMP/out"; echo $? )"
check "(8) declaring the correct count passes" 0 \
  "$( OLIVARES_CLAIM_FILES=1 OLIVARES_CLAIM_DELETIONS="$(nd "$LIMPIO")" bash "$SUT" "$LIMPIO" > "$TMP/out" 2>&1; echo $? )"
check "(9) nonnumeric OLIVARES_CLAIM_FILES returns 2" 2 \
  "$( OLIVARES_CLAIM_FILES=uno bash "$SUT" "$LIMPIO" > "$TMP/out" 2>&1; echo $? )"

# 10 · MUTANTE · quitar la comprobacion de borrados protegidos
sed 's|if \[ -n "$MAL" \]; then|if false; then|' "$SUT" > "$TMP/m1.sh"
check "(10a) deletion mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/m1.sh" && echo 1 || echo 0 )"
M1=$(corre "$BORRA" "$TMP/m1.sh")
check "(10b) MUTANT ignoring deletions is detected by rc" 0 "$( [ "$M1" = "0" ] && echo 0 || echo "got $M1" )"
check "(10c) and its MESSAGE: reports CLEAN for a commit deleting 7 mailbox lines" 0 \
  "$( grep -q 'CLEAN —' "$TMP/out"; echo $? )"

# 11 · MUTANTE · quitar la guarda del diff vacio (el 0 por silencio)
sed 's|if \[ -z "$NUM" \]; then|if false; then|' "$SUT" > "$TMP/m2.sh"
check "(11a) empty-diff mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/m2.sh" && echo 1 || echo 0 )"
M2=$(CF=1 corre "$IGUAL" "$TMP/m2.sh")
check "(11b) MUTANT accepting an empty diff is detected by rc" 0 "$( [ "$M2" = "0" ] && echo 0 || echo "got $M2" )"
# ⛔ Y el mutante cuenta «1 fichero(s)», NO cero: `printf '%s\n' ""` emite UNA linea vacia, asi que
# `wc -l` da 1 sobre la lista vacia. Es la clase «una lista vacia trae sus delimitadores», y por eso
# el caso exige el texto exacto: si aqui se hubiera puesto «0 fichero(s)» a ojo, el caso fallaria y
# se habria «arreglado» aflojandolo, que es como un mutante sobrevive con la bateria en verde.
check "(11c) and its MESSAGE: reports clean while counting 1 nonexistent file" 0 \
  "$( grep -q '1 file(s), no deletions' "$TMP/out"; echo $? )"

# --- v2 (curas del NO de the reviewer) -------------------------------------------------------------
# A-03 · SIN la declaracion es 2, NO 0.
check "(24) omitting OLIVARES_CLAIM_FILES returns 2" 2 \
  "$( bash "$SUT" "$LIMPIO" > "$TMP/out" 2>&1; echo $? )"
check "(24b) reports WHAT is missing and how to pass it" 0 \
  "$( grep -q 'missing OLIVARES_CLAIM_FILES' "$TMP/out" && grep -q 'OLIVARES_CLAIM_FILES=1' "$TMP/out"; echo $? )"

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
check "(25) MODE change 755->644 returns rc 1" 1 "$(corre "$MODO")"
check "(25b) names it using Git terminology" 0 "$( grep -q 'mode change' "$TMP/out"; echo $? )"
check "(25c) reports that --numstat misses it" 0 "$( grep -q 'numstat' "$TMP/out"; echo $? )"

idx="$TMP/idx.n"; rm -f "$idx"; GIT_INDEX_FILE="$idx" git read-tree "$BASE"
GIT_INDEX_FILE="$idx" git update-index --add --cacheinfo 100644,"$BLOBX",scripts/nace-mal.sh
TN=$(GIT_INDEX_FILE="$idx" git write-tree); NACE=$(git commit-tree "$TN" -p "$BASE" -m t); rm -f "$idx"
check "(26) NEW script under scripts/ with mode 100644 returns rc 1" 1 "$(corre "$NACE")"
check "(26b) explains the resulting rc 126" 0 "$( grep -q '126' "$TMP/out"; echo $? )"
check "(26c) the SAME script created as 100755 is not a mode finding" 0 \
  "$( corre "$CON755" >/dev/null; grep -q 'mode change\|nonexecutable' "$TMP/out" && echo "unexpected finding" || echo 0 )"
check "(26d) reports the new-script warning under scripts/" 0 \
  "$( grep -q 'new script(s) under scripts/' "$TMP/out"; echo $? )"

# MUTANTE · quitar la sonda de modos: el defecto real volveria a pasar por limpio
sed 's|if \[ -n "$MODO" \]; then|if false; then|' "$SUT" > "$TMP/mm.sh"
check "(27a) mode mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mm.sh" && echo 1 || echo 0 )"
M27=$(corre "$MODO" "$TMP/mm.sh")
check "(27b) MUTANT ignoring modes allows the mode change" 0 "$( [ "$M27" = "0" ] && echo 0 || echo "got $M27" )"
check "(27c) and its MESSAGE reports clean" 0 "$( grep -q 'CLEAN —' "$TMP/out"; echo $? )"

# --- v3 · LA RAMA DE CONFLICTO, QUE FUNCIONABA Y NO MATABA A NADIE ------------------------------
# ⛔ the reviewer (2026-08-30T20:14Z): la guarda corta el conflicto (`merge-tree` rc != 0 -> rc 2) y esta
# bateria NO tenia ni una fila sobre esa comparacion: retirar `RCM -ne 0` conservaba 34/0. Una rama
# que funciona y que nadie mata es una rama que se puede borrar sin que el verde se entere — la
# clase «el arreglo pone verde por otra via». Aqui va su fixture y su mutante.
ANC=$(mk "" "wf.yml=techo 60" "${PROTEGIDO_FIXTURE}BUZON.md=$LINEAS")
LADO_A=$(mk "$ANC" "wf.yml=techo 82")
LADO_B=$(mk "$ANC" "wf.yml=techo 90")
check "(28) both sides actually differ (fixture control)" 0 \
  "$( [ "$(git rev-parse "${LADO_A}^{tree}")" != "$(git rev-parse "${LADO_B}^{tree}")" ] && echo 0 || echo "both sides are identical: the fixture has no conflict" )"
C28=$( OLIVARES_CLAIM_FILES=1 bash "$SUT" "$LADO_B" "$LADO_A" > "$TMP/out" 2>&1; echo $? )
check "(28b) CONFLICTING merge returns 2 without a verdict" 2 "$C28"
check "(28c) its MESSAGE explicitly reports a CONFLICT" 0 "$( grep -q 'causes conflicts' "$TMP/out"; echo $? )"
check "(28d) names both merge SHAs" 0 \
  "$( grep -q "${LADO_B}" "$TMP/out" && grep -q "${LADO_A}" "$TMP/out"; echo $? )"
check "(28e) does NOT emit a clean verdict" 0 \
  "$( grep -q 'CLEAN —' "$TMP/out" && echo "gave a verdict for a nonexistent merge" || echo 0 )"

# 29 · MUTANTE · retirar la comprobacion del rc de merge-tree: `$FUS` viene NO vacio aunque haya
#      conflicto —merge-tree escribe igualmente un arbol— asi que la guarda seguiria y daria un
#      veredicto sobre una fusion que nadie puede materializar.
sed 's|if \[ "$RCM" -ne 0 \] \|\| \[ -z "$FUS" \]; then|if [ -z "$FUS" ]; then|' "$SUT" > "$TMP/mrcm.sh"
check "(29a) RCM mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mrcm.sh" && echo 1 || echo 0 )"
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
check "(29b) MUTANT ignoring merge-tree rc no longer names the conflict" 0 \
  "$( grep -q 'causes conflicts' "$TMP/out" && echo "still names it: the mutant made no change" || echo 0 )"
check "(29c) fails in ANOTHER guard, so rc does not discriminate" 0 \
  "$( [ "$M29" = "2" ] && grep -q 'COULD NOT CHECK' "$TMP/out" && echo 0 || echo "failed in another way: rc=$M29" )"

# --- v3 · BORRADOS FUERA DE LO PROTEGIDO --------------------------------------------------------
# ⛔ Nace de un fallo mio que esta misma guarda dio por LIMPIO: publique un claim que borraba 24
# lineas del `Taskfile` por reusar ficheros construidos contra una base anterior, y como no eran
# lineas de buzon, paso. Un guarda que solo protege lo que su autor recordo proteger deja fuera lo
# que no previo — y de rebote lo heredaban las sondas de verificacion de otro carril.
BORRABLE=$(mk "$BASE" "wf.yml=una sola linea")
check "(30) claim deleting outside protected paths returns rc 1" 1 "$(CF=1 CD=0 corre "$BORRABLE")"
check "(30b) names the file with its count" 0 "$( grep -qE 'wf.yml: -[0-9]+' "$TMP/out"; echo $? )"
check "(30c) reports BOTH possible causes" 0 \
  "$( grep -q 'built against a different base' "$TMP/out" && grep -q 'reverting someone' "$TMP/out"; echo $? )"
check "(30d) provides the recipe for declaring deletions" 0 "$( grep -q 'OLIVARES_CLAIM_DELETIONS=' "$TMP/out"; echo $? )"
n30=$(nd "$BORRABLE")
check "(30e) declaring the exact count passes" 0 \
  "$( OLIVARES_CLAIM_FILES=1 OLIVARES_CLAIM_DELETIONS="$n30" bash "$SUT" "$BORRABLE" > "$TMP/out" 2>&1; echo $? )"
check "(30f) nonnumeric OLIVARES_CLAIM_DELETIONS returns 2" 2 \
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
check "(31a) deletion mutant was BUILT and differs" 0 \
  "$( [ -s "$TMP/mb.sh" ] && ! cmp -s "$SUT" "$TMP/mb.sh" && echo 0 || echo "was not built" )"
M31=$(CF=1 CD=0 corre "$BORRABLE" "$TMP/mb.sh")
check "(31b) MUTANT ignoring deletions returns rc 0 for a destructive claim" 0 \
  "$( [ "$M31" = "0" ] && echo 0 || echo "got $M31" )"
check "(31c) and its MESSAGE reports clean" 0 "$( grep -q 'CLEAN —' "$TMP/out"; echo $? )"

# La corrida MUTANTE de la (44d) solo tiene que demostrar que unos fixtures fuera de `$REPO` se ven, y
# eso ya ha ocurrido al llegar aqui: con `OLIVARES_TCS_INTERIOR=fixtures` corta y no repite v4 entera.
if [ "${OLIVARES_TCS_INTERIOR:-}" = fixtures ]; then
  echo; echo "check-claim-safety selftest (fixtures v1-v3 only): $PASS passed, $FAIL failed"; exit 0
fi

# --- v4 · NI LA BATERIA NI LA FUSION ESCRIBEN EN EL REPOSITORIO QUE MIDEN (RD4, 2026-09-11) -----
# ⛔ DOS VIAS DE ESCRITURA, CADA UNA CON SU MUTANTE. (a) Los fixtures de esta bateria, ya dentro de
# `$REPO`: su testigo es la (44). (b) `git merge-tree --write-tree` escribe el arbol fusionado —medido
# con git 2.39.5: 2 sueltos al conflictar, 1 en una fusion limpia divergente— y al conflictar el sujeto
# sale 2 sin que nadie los alcance; su cura es el almacen propio de `scripts/lib/git-env.sh`. Todo se
# mide en repositorios DEDICADOS con la huella COMPLETA de su directorio git comun —objetos, refs,
# indices, config y alternates—, nunca en el repositorio compartido: ahi escriben otros carriles y la
# cuenta no seria atribuible.
#
# ⚠ Las fechas de los commits van FIJAS: cada mutante corre sobre un sandbox RECIEN hecho —en uno ya
# usado sus objetos existirian y no escribiria nada— y asi los SHAs, y con ellos la salida entera, son
# los mismos en todos y se comparan byte a byte.
g(){ GIT_AUTHOR_DATE='2026-09-11T00:00:00Z' GIT_COMMITTER_DATE='2026-09-11T00:00:00Z' \
     git -c user.name=sandbox -c user.email=sandbox@example.invalid -c commit.gpgsign=false -c init.defaultBranch=main "$@"; }
huella(){ ( CDPATH= cd -- "$1" 2>/dev/null && find . -type f -exec cksum {} + | LC_ALL=C sort -k3 ) || echo "UNREADABLE FINGERPRINT: $1"; }
sb_commit(){ local d="$1" m="$2" pair p; shift 2
  for pair in "$@"; do p="${pair%%=*}"
    mkdir -p "$d/$(dirname -- "$p")" && printf '%s\n' "${pair#*=}" > "$d/$p" && g -C "$d" add -- "$p" || return 1; done
  g -C "$d" commit -qm "$m"; }
nuevo_sb(){ local d="$1"
  { g init -q "$d" && sb_commit "$d" base "wf.yml=techo 60" "${PROTEGIDO_FIXTURE}BUZON.md=$LINEAS" &&
    g -C "$d" branch lado_b && g -C "$d" branch limpio &&
    g -C "$d" checkout -q -b lado_a && sb_commit "$d" a "wf.yml=techo 82" &&
    g -C "$d" checkout -q lado_b && sb_commit "$d" b "wf.yml=techo 90" &&
    g -C "$d" checkout -q limpio && sb_commit "$d" limpio "otro.txt=algo" &&
    g -C "$d" checkout -q main; } >/dev/null 2>&1; }
muta(){ # muta <origen> <destino> <viejo> <nuevo> ; sale 1 si el patron no casa UNA sola vez
  python3 - "$@" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
if o.count(sys.argv[3]) != 1:
    sys.exit(1)
open(sys.argv[2], "w").write(o.replace(sys.argv[3], sys.argv[4], 1))
PYEOF
}
SBR="$TMP/sand box"; SB="$SBR/origen con espacio"; WT="$SBR/enlazado con espacio"; CLON="$SBR/clon compartido"
# El TMPDIR del sujeto: su almacen propio nace aqui y aqui tiene que dejar de existir.
SCR="$TMP/scratch con espacio"
mkdir -p "$SBR" "$SCR" || exit 2
{ nuevo_sb "$SB" && g -C "$SB" worktree add -q --detach "$WT" lado_a && g clone -q --shared "$SB" "$CLON"; } >/dev/null 2>&1 \
  || { echo "FATAL: could not build the v4 sandbox" >&2; exit 2; }
SB_A=$(git -C "$SB" rev-parse lado_a); SB_B=$(git -C "$SB" rev-parse lado_b); SB_L=$(git -C "$SB" rev-parse limpio)
WT_GIT_DIR=$(git -C "$WT" rev-parse --absolute-git-dir)
corre_sb(){ # corre_sb <cwd> <sujeto> <claim> <base> [VAR=valor...] -> RSB, salida en $TMP/out-sb
  local cwd="$1" sujeto="$2" claim="$3" base="$4"; shift 4
  ( CDPATH= cd -- "$cwd" && exec env TMPDIR="$SCR" OLIVARES_CLAIM_FILES=1 "$@" bash "$sujeto" "$claim" "$base" ) > "$TMP/out-sb" 2>&1
  RSB=$?; }
# 0 si el TMPDIR del sujeto quedo vacio; lo vacia para que el caso siguiente no herede residuo ajeno.
vacio(){ local q; q=$(ls -A "$SCR" 2>/dev/null); rm -rf "$SCR"; mkdir -p "$SCR"
  [ -z "$q" ] && echo 0 || echo "remained in TMPDIR: $(printf '%s' "$q" | tr '\n' ' ')"; }
igual(){ [ "$1" = "$2" ] && echo 0 || echo "the fingerprint changed"; }
mismo(){ # mismo <rc> <salida de referencia> -> 0 si el rc y la salida coinciden byte a byte
  [ "$RSB" = "$1" ] && cmp -s "$TMP/out-sb" "$2" && echo 0 || echo "rc=$RSB: $(head -c 160 "$TMP/out-sb" | tr '\n' ' ')"; }

H_SB=$(huella "$SB/.git"); H_CLON=$(huella "$CLON/.git")
check "(32) control: both sides conflict and the clean claim DIVERGES from its base" 0 \
  "$( [ "$(git -C "$SB" rev-parse "${SB_A}^{tree}")" != "$(git -C "$SB" rev-parse "${SB_B}^{tree}")" ] &&
      ! git -C "$SB" merge-base --is-ancestor "$SB_A" "$SB_L" && ! git -C "$SB" merge-base --is-ancestor "$SB_L" "$SB_A" &&
      echo 0 || echo "invalid fixture: it would not measure writes" )"
corre_sb "$SB" "$SUT" "$SB_B" "$SB_A"; cp -- "$TMP/out-sb" "$TMP/out-conflicto"
check "(32b) CONFLICTING merge in a dedicated repository returns 2" 2 "$RSB"
check "(32c) explicitly reports a CONFLICT" 0 "$( grep -q 'causes conflicts' "$TMP/out-sb"; echo $? )"
check "(32d) repository objects, refs, indexes and configuration remain unchanged" 0 "$(igual "$(huella "$SB/.git")" "$H_SB")"
check "(32e) the owned store does not survive exit" 0 "$(vacio)"

corre_sb "$SB" "$SUT" "$SB_L" "$SB_A"; cp -- "$TMP/out-sb" "$TMP/out-limpio"
check "(33) CLEAN divergent merge returns 0" 0 "$RSB"
check "(33b) reports its count" 0 "$( grep -q 'CLEAN — 1 file(s)' "$TMP/out-sb"; echo $? )"
FUS33=$(sed -n 's/^  merged tree (transient[^)]*): //p' "$TMP/out-sb")
check "(33c) the printed merge tree is MARKED transient" 0 "$( [ -n "$FUS33" ] && echo 0 || echo "unmarked" )"
check "(33d) that tree does NOT exist in the measured repository" 0 \
  "$( [ -n "$FUS33" ] && ! git -C "$SB" cat-file -e "$FUS33" 2>/dev/null && echo 0 || echo "exists: it was written there" )"
check "(33e) the measured repository remains unchanged" 0 "$(igual "$(huella "$SB/.git")" "$H_SB")"
check "(33f) the owned store does not survive" 0 "$(vacio)"

corre_sb "$WT" "$SUT" "$SB_B" "$SB_A" "GIT_DIR=$WT_GIT_DIR"
check "(34) from a LINKED worktree exporting GIT_DIR, as in a hook: IDENTICAL (2)" 0 "$(mismo 2 "$TMP/out-conflicto")"
corre_sb "$WT" "$SUT" "$SB_L" "$SB_A" "GIT_DIR=$WT_GIT_DIR"
check "(34b) clean case is IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(34c) common directory and linked-worktree index remain unchanged" 0 "$(igual "$(huella "$SB/.git")" "$H_SB")"
check "(34d) the owned store does not survive" 0 "$(vacio)"

check "(35) control: --shared clone reads the base ONLY through alternates" 0 \
  "$( [ -s "$CLON/.git/objects/info/alternates" ] && git -C "$CLON" cat-file -e "$SB_B" &&
      [ -z "$(find "$CLON/.git/objects" -mindepth 2 -type f ! -path '*/info/*')" ] &&
      echo 0 || echo "the clone owns a copy: it would not measure alternates" )"
corre_sb "$CLON" "$SUT" "$SB_B" "$SB_A"
check "(35b) conflicting case in that clone is IDENTICAL (2)" 0 "$(mismo 2 "$TMP/out-conflicto")"
corre_sb "$CLON" "$SUT" "$SB_L" "$SB_A"
check "(35c) clean case is IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(35d) clone and borrowed store remain unchanged" 0 \
  "$( [ "$(huella "$CLON/.git")" = "$H_CLON" ] && [ "$(huella "$SB/.git")" = "$H_SB" ] && echo 0 || echo "a fingerprint changed" )"
check "(35e) the owned store does not survive" 0 "$(vacio)"

# 36 · EL PRESUPUESTO DE ANIDAMIENTO. git sigue alternates encadenados solo unos niveles por debajo
#      del almacen que los lista; un almacen propio que listara SOLO el del repositorio gastaria uno.
#      Seis clones --shared en cadena es lo que git a secas todavia lee: ahi se ve la diferencia.
prev="$SB"; n=0
while [ "$n" -lt 6 ]; do n=$((n+1)); g clone -q --shared "$prev" "$SBR/cadena $n" >/dev/null 2>&1 || break; prev="$SBR/cadena $n"; done
FONDO="$SBR/cadena 6"
H_FONDO=$(huella "$FONDO/.git")
check "(36) control: six chained alternates; plain Git reads the base at the bottom" 0 \
  "$( [ "$prev" = "$FONDO" ] && git -C "$FONDO" cat-file -e "$SB_B" && echo 0 || echo "invalid chain" )"
corre_sb "$FONDO" "$SUT" "$SB_L" "$SB_A"
check "(36b) the owned store preserves that depth: IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(36c) the bottom of the chain remains unchanged" 0 "$(igual "$(huella "$FONDO/.git")" "$H_FONDO")"
check "(36d) the owned store does not survive" 0 "$(vacio)"
LIB_SUT="$(dirname -- "$SUT")/lib/git-env.sh"
MLIB="$TMP/mlib"; mkdir -p "$MLIB/lib" && cp -- "$SUT" "$MLIB/check-claim-safety.sh" || exit 2
check "(36e) mutant using only repository objects without its alternates was BUILT" 0 \
  "$( muta "$LIB_SUT" "$MLIB/lib/git-env.sh" 'if [ -e "$objects/info/alternates" ]; then' 'if false; then' && echo 0 || echo "was not built" )"
corre_sb "$FONDO" "$MLIB/check-claim-safety.sh" "$SB_L" "$SB_A"
check "(36f) MUTANT cannot read the bottom; subject REFUSES (2)" 2 "$RSB"
check "(36g) reports that no owned store capable of reading the base could be opened" 0 "$( grep -q 'could not open a separate object store' "$TMP/out-sb"; echo $? )"
check "(36h) performs no writes and leaves no residue" 0 \
  "$( [ "$(huella "$FONDO/.git")" = "$H_FONDO" ] && [ "$(vacio)" = 0 ] && echo 0 || echo "wrote or left residue" )"

# 37 · ENTORNO INYECTADO. El sujeto sanea antes de resolver nada; un repositorio SENUELO recibe todo
#      lo que un entorno heredado podria desviar, y ni el senuelo ni el medido pueden moverse.
D="$SBR/senuelo con espacio"
{ g init -q "$D" && sb_commit "$D" decoy "nada.txt=nada"; } >/dev/null 2>&1 || { echo "FATAL: could not build the decoy" >&2; exit 2; }
H_D=$(huella "$D/.git")
INY=("GIT_DIR=$D/.git" "GIT_COMMON_DIR=$D/.git" "GIT_WORK_TREE=$D" "GIT_INDEX_FILE=$D/.git/index"
     "GIT_OBJECT_DIRECTORY=$D/.git/objects" "GIT_ALTERNATE_OBJECT_DIRECTORIES=$D/.git/objects")
check "(37) control: injected environment redirects unsanitized Git (claim not visible)" 0 \
  "$( ( CDPATH= cd -- "$SB" && env "${INY[@]}" git cat-file -e "$SB_B" ) 2>/dev/null && echo "nothing is redirected: the case would measure nothing" || echo 0 )"
corre_sb "$SB" "$SUT" "$SB_B" "$SB_A" "${INY[@]}"
check "(37b) injected GIT_DIR/COMMON_DIR/INDEX/OBJECT_DIRECTORY/ALTERNATES: IDENTICAL (2)" 0 "$(mismo 2 "$TMP/out-conflicto")"
corre_sb "$SB" "$SUT" "$SB_L" "$SB_A" "${INY[@]}"
check "(37c) clean case is IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(37d) measured repository and decoy remain unchanged" 0 \
  "$( [ "$(huella "$SB/.git")" = "$H_SB" ] && [ "$(huella "$D/.git")" = "$H_D" ] && echo 0 || echo "a fingerprint changed" )"
check "(37e) the owned store does not survive" 0 "$(vacio)"

# 38 · NO HE PODIDO MIRAR: sin donde crear el almacen propio no se fusiona sobre el del repositorio.
( CDPATH= cd -- "$SB" && exec env TMPDIR="$TMP/no existe" OLIVARES_CLAIM_FILES=1 bash "$SUT" "$SB_B" "$SB_A" ) > "$TMP/out-sb" 2>&1; RSB=$?
check "(38) no location for the owned store returns 2, COULD NOT CHECK" 2 "$RSB"
check "(38b) reports the cause and does not merge in repository objects" 0 \
  "$( grep -q 'could not open a separate object store' "$TMP/out-sb" && ! grep -q 'causes conflicts' "$TMP/out-sb" && echo 0 || echo "another diagnostic" )"
check "(38c) the measured repository remains unchanged" 0 "$(igual "$(huella "$SB/.git")" "$H_SB")"

# 39-42 · MUTANTES DE LA VIA (b) Y DE SU CIERRE. Cada uno devuelve una escritura o quita un cierre, y
#         la fila que lo caza es la que acredita a su gemela de arriba.
check "(39) plain-Git merge mutant was BUILT" 0 \
  "$( muta "$SUT" "$TMP/mfus.sh" 'FUS=$(olivares_git_owned merge-tree' 'FUS=$(git merge-tree' && echo 0 || echo "was not built" )"
nuevo_sb "$SBR/mutante fusion" || exit 2; H_M=$(huella "$SBR/mutante fusion/.git")
corre_sb "$SBR/mutante fusion" "$TMP/mfus.sh" "$SB_B" "$SB_A"
check "(39b) MUTANT on the conflicting case: IDENTICAL output (2)..." 0 "$(mismo 2 "$TMP/out-conflicto")"
check "(39c) ...but WRITES in the measured repository: (32d) discriminates" 0 \
  "$( [ "$(huella "$SBR/mutante fusion/.git")" != "$H_M" ] && echo 0 || echo "did not write: (32d) measures nothing" )"
H_M=$(huella "$SBR/mutante fusion/.git")
corre_sb "$SBR/mutante fusion" "$TMP/mfus.sh" "$SB_L" "$SB_A"
check "(39d) MUTANT on the CLEAN case: IDENTICAL output (0)..." 0 "$(mismo 0 "$TMP/out-limpio")"
check "(39e) ...and its printed tree EXISTS in the repository: (33d) discriminates" 0 \
  "$( git -C "$SBR/mutante fusion" cat-file -e "$FUS33" 2>/dev/null && [ "$(huella "$SBR/mutante fusion/.git")" != "$H_M" ] && echo 0 || echo "was not written" )"
vacio >/dev/null

check "(40) library wrapper mutant without GIT_OBJECT_DIRECTORY was BUILT" 0 \
  "$( muta "$LIB_SUT" "$MLIB/lib/git-env.sh" 'GIT_OBJECT_DIRECTORY="$OLIVARES_GIT_OWNED_STORE" git "$@"' 'git "$@"' && echo 0 || echo "was not built" )"
nuevo_sb "$SBR/mutante envoltorio" || exit 2; H_M=$(huella "$SBR/mutante envoltorio/.git")
corre_sb "$SBR/mutante envoltorio" "$MLIB/check-claim-safety.sh" "$SB_B" "$SB_A"
check "(40b) library MUTANT: IDENTICAL output (2)..." 0 "$(mismo 2 "$TMP/out-conflicto")"
check "(40c) ...but WRITES in the measured repository" 0 \
  "$( [ "$(huella "$SBR/mutante envoltorio/.git")" != "$H_M" ] && echo 0 || echo "did not write: the wrapper measures nothing" )"
vacio >/dev/null

check "(41) mutant without cleanup was BUILT" 0 \
  "$( muta "$SUT" "$TMP/mcierre.sh" 'trap olivares_git_owned_store_close EXIT' ':' && echo 0 || echo "was not built" )"
corre_sb "$SB" "$TMP/mcierre.sh" "$SB_B" "$SB_A"
check "(41b) MUTANT without cleanup: same rc, but store SURVIVES and (32e) detects it" 0 \
  "$( [ "$RSB" = 2 ] && [ "$(vacio)" != 0 ] && echo 0 || echo "rc=$RSB or nothing remained: (32e) measures nothing" )"

check "(42) mutant without store opening was BUILT" 0 \
  "$( muta "$SUT" "$TMP/mabrir.sh" 'olivares_git_owned_store_open "$BS" "$CS"' ': "$BS" "$CS"' && echo 0 || echo "was not built" )"
nuevo_sb "$SBR/mutante sin abrir" || exit 2; H_M=$(huella "$SBR/mutante sin abrir/.git")
corre_sb "$SBR/mutante sin abrir" "$TMP/mabrir.sh" "$SB_L" "$SB_A"
check "(42b) MUTANT: unopened store makes the wrapper REFUSE without a verdict (2)" 2 "$RSB"
check "(42c) no plain-Git fallback: measured repository remains unchanged" 0 "$(igual "$(huella "$SBR/mutante sin abrir/.git")" "$H_M")"
vacio >/dev/null

# 43 · INTERRUPCION. Una funcion `git` exportada pausa SOLO `merge-tree` hasta que la bateria la
#      suelta —sin ejecutable de apoyo, que en un /tmp noexec no correria—. La senal llega con el
#      almacen abierto; bash atiende el TERM atrapado al volver la sustitucion, y el cierre corre.
rm -f "$TMP/pausa-lista" "$TMP/pausa-sigue"
(
  git(){ local n=0
    case " $* " in *" merge-tree "*)
      : > "$TCS_PAUSA_LISTA"
      while [ ! -e "$TCS_PAUSA_SIGUE" ] && [ "$n" -lt 400 ]; do sleep 0.05; n=$((n+1)); done ;;
    esac
    command git "$@"; }
  export -f git
  CDPATH= cd -- "$SB" && exec env TCS_PAUSA_LISTA="$TMP/pausa-lista" TCS_PAUSA_SIGUE="$TMP/pausa-sigue" \
    TMPDIR="$SCR" OLIVARES_CLAIM_FILES=1 bash "$SUT" "$SB_B" "$SB_A"
) > "$TMP/out-sb" 2>&1 &
PID_INT=$!
n=0; while [ ! -e "$TMP/pausa-lista" ] && [ "$n" -lt 400 ]; do sleep 0.05; n=$((n+1)); done
DURANTE=$(ls -A "$SCR" 2>/dev/null)
kill -TERM "$PID_INT" 2>/dev/null
: > "$TMP/pausa-sigue"
wait "$PID_INT"; RINT=$?
check "(43) control: merge paused WITH the owned store already open" 0 \
  "$( [ -e "$TMP/pausa-lista" ] && [ -n "$DURANTE" ] && echo 0 || echo "did not pause or no store was present" )"
check "(43b) TERM interruption exits without a verdict (143)" 143 "$RINT"
check "(43c) the owned store does NOT survive interruption" 0 "$(vacio)"
check "(43d) the measured repository remains unchanged" 0 "$(igual "$(huella "$SB/.git")" "$H_SB")"

# 45 · ALTERNATES QUE GIT CITA. Una entrada de `objects/info/alternates` puede ir entre comillas al
#      estilo C —`\"`, `\\`, `\t`, octales— y puede ser relativa al directorio de objetos que la
#      lista, y git la lee. El almacen propio no descifra nada: copia la lista que git ya resolvio.
#      Cada caso lleva su control con git a secas, y los rotos tienen que REHUSAR sin que nadie gane
#      un objeto. El prestamista lleva comillas, barra invertida, dos puntos, tabulador y un octeto
#      no ASCII a proposito: son los que obligan a citar.
LQ_NOMBRE=$'prestamista "citado" \\ barra:dos\tpuntos \xc3\xb1'
LQ="$SBR/$LQ_NOMBRE"
nuevo_sb "$LQ" || { echo "FATAL: could not build the quoted lender" >&2; exit 2; }
cita(){ # cita <ruta> -> la ruta entre comillas al estilo C, como la escribiria una herramienta que cita
  local s="$1"
  s=${s//\\/'\\'}; s=${s//\"/'\"'}; s=${s//$'\t'/'\t'}; s=${s//$'\xc3\xb1'/'\303\261'}
  printf '"%s"' "$s"; }
presta(){ # presta <prestatario> <linea>... ; repositorio vacio cuyo info/alternates son esas lineas
  local d="$1"; shift
  { g init -q "$d" && printf '%s\n' "$@" > "$d/.git/objects/info/alternates"; } >/dev/null 2>&1; }
sin_objetos(){ [ -z "$(find "$1/.git/objects" -mindepth 2 -type f ! -path '*/info/*')" ]; }
BQA="$SBR/prestatario citado absoluto"; BQR="$SBR/prestatario citado relativo"
BQN="$SBR/prestatario anidado"; BQC="$SBR/prestatario crudo relativo"; BQM="$SBR/prestatario roto"
{ presta "$BQA" "$(cita "$LQ/.git/objects")" &&
  presta "$BQR" "$(cita "../../../$LQ_NOMBRE/.git/objects")" &&
  presta "$BQN" "$(cita "$BQR/.git/objects")" &&
  presta "$BQC" "../../../$LQ_NOMBRE/.git/objects" &&
  presta "$BQM" "$(cita "$LQ/.git/objects")" "\"$SBR/sin cerrar/objects"; } || { echo "FATAL: could not build the borrowers" >&2; exit 2; }
H_LQ=$(huella "$LQ/.git"); H_BQA=$(huella "$BQA/.git"); H_BQR=$(huella "$BQR/.git")
H_BQN=$(huella "$BQN/.git"); H_BQC=$(huella "$BQC/.git"); H_BQM=$(huella "$BQM/.git")

check "(45) control: absolute entry is QUOTED with \\\" \\\\ \\t and octal escapes; plain Git reads through it" 0 \
  "$( grep -qF '\"citado\" \\ barra:dos\tpuntos \303\261' "$BQA/.git/objects/info/alternates" &&
      [ "$(head -c 1 "$BQA/.git/objects/info/alternates")" = '"' ] && sin_objetos "$BQA" &&
      git -C "$BQA" cat-file -e "$SB_B" && echo 0 || echo "invalid fixture" )"
corre_sb "$BQA" "$SUT" "$SB_B" "$SB_A"
check "(45b) absolute QUOTED alternate: conflicting case is IDENTICAL (2)" 0 "$(mismo 2 "$TMP/out-conflicto")"
corre_sb "$BQA" "$SUT" "$SB_L" "$SB_A"
check "(45c) clean case is IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(45d) control: quoted RELATIVE entry resolves against its OWN directory in plain Git" 0 \
  "$( [ "$(head -c 4 "$BQR/.git/objects/info/alternates")" = '"../' ] && sin_objetos "$BQR" &&
      git -C "$BQR" cat-file -e "$SB_B" && echo 0 || echo "invalid fixture" )"
corre_sb "$BQR" "$SUT" "$SB_L" "$SB_A"
check "(45e) relative QUOTED alternate: IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(45f) control: quoted entry borrowing through another quoted relative entry is readable" 0 \
  "$( sin_objetos "$BQN" && git -C "$BQN" cat-file -e "$SB_B" && echo 0 || echo "invalid fixture" )"
corre_sb "$BQN" "$SUT" "$SB_L" "$SB_A"
check "(45g) nested through two quoted entries: IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(45h) control: plain Git also reads an UNQUOTED relative entry" 0 \
  "$( sin_objetos "$BQC" && git -C "$BQC" cat-file -e "$SB_B" && echo 0 || echo "invalid fixture" )"
corre_sb "$BQC" "$SUT" "$SB_L" "$SB_A"
check "(45i) unquoted relative alternate: IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(45j) borrowers and lender remain unchanged" 0 \
  "$( [ "$(huella "$LQ/.git")" = "$H_LQ" ] && [ "$(huella "$BQA/.git")" = "$H_BQA" ] &&
      [ "$(huella "$BQR/.git")" = "$H_BQR" ] && [ "$(huella "$BQN/.git")" = "$H_BQN" ] &&
      [ "$(huella "$BQC/.git")" = "$H_BQC" ] && echo 0 || echo "a fingerprint changed" )"
check "(45k) the owned store does not survive" 0 "$(vacio)"

check "(45l) control: plain Git READS through a valid entry and WARNS about the broken one" 0 \
  "$( LC_ALL=C git -C "$BQM" cat-file -e "$SB_B" 2>"$TMP/bqm.err" && grep -q '^error: ' "$TMP/bqm.err" && echo 0 || echo "invalid fixture" )"
corre_sb "$BQM" "$SUT" "$SB_L" "$SB_A"
check "(45m) broken and valid entries together: COULD NOT CHECK (2), no verdict" 0 \
  "$( [ "$RSB" = 2 ] && grep -q 'could not open a separate object store' "$TMP/out-sb" && echo 0 || echo "rc=$RSB" )"
check "(45n) broken borrower and lender remain unchanged; no store survives" 0 \
  "$( [ "$(huella "$BQM/.git")" = "$H_BQM" ] && [ "$(huella "$LQ/.git")" = "$H_LQ" ] && [ "$(vacio)" = 0 ] && echo 0 || echo "wrote or left residue" )"

# El alternates ILEGIBLE: git solo AVISA y sigue sin el. Para que la negativa sea del almacen propio y no
# de una resolucion anterior, este prestatario tiene objetos propios: git a secas SI daria un veredicto.
BQX="$SBR/prestatario ilegible"
{ nuevo_sb "$BQX" && printf '%s\n' "$(cita "$LQ/.git/objects")" > "$BQX/.git/objects/info/alternates"; } \
  || { echo "FATAL: could not build the unreadable borrower" >&2; exit 2; }
H_BQX=$(huella "$BQX/.git")
chmod 000 "$BQX/.git/objects/info/alternates"
if [ -r "$BQX/.git/objects/info/alternates" ]; then
  chmod 644 "$BQX/.git/objects/info/alternates"
  echo "note (45o-45s) do not apply: this user can read a mode-000 file, so the unreadable case is not built"
else
  check "(45o) control: plain Git reads the base from owned objects and WARNS about unreadable alternates" 0 \
    "$( LC_ALL=C git -C "$BQX" cat-file -e "$SB_B" 2>"$TMP/bqx.err" && grep -q '^warning: ' "$TMP/bqx.err" &&
        ! grep -q '^error: ' "$TMP/bqx.err" && echo 0 || echo "invalid fixture" )"
  corre_sb "$BQX" "$SUT" "$SB_L" "$SB_A"
  check "(45p) UNREADABLE alternates: COULD NOT CHECK (2), even though plain Git would give a verdict" 0 \
    "$( [ "$RSB" = 2 ] && grep -q 'could not open a separate object store' "$TMP/out-sb" && echo 0 || echo "rc=$RSB" )"
  check "(45q) library mutant omitting the readability check was BUILT" 0 \
    "$( muta "$LIB_SUT" "$MLIB/lib/git-env.sh" '[ ! -r "$objects/info/alternates" ] ||' 'false ||' && echo 0 || echo "was not built" )"
  corre_sb "$BQX" "$MLIB/check-claim-safety.sh" "$SB_L" "$SB_A"
  check "(45r) MUTANT omitting readability check: gives verdict (0) on unreadable input; (45p) discriminates" 0 \
    "$( [ "$RSB" = 0 ] && echo 0 || echo "rc=$RSB: (45p) does not measure the unreadable file" )"
  chmod 644 "$BQX/.git/objects/info/alternates"
  check "(45s) borrower remains unchanged and no store survives" 0 \
    "$( [ "$(huella "$BQX/.git")" = "$H_BQX" ] && [ "$(vacio)" = 0 ] && echo 0 || echo "wrote or left residue" )"
fi

# 46 · EL PRESUPUESTO DE ANIDAMIENTO CON UNA ENTRADA CITADA Y RELATIVA ARRIBA DEL TODO. Un clon al mismo
#      nivel que el fondo de la cadena cuyo alternates cita, en relativo, al quinto eslabon: git a secas
#      llega a la base con el ultimo nivel que permite. Una copia literal del fichero pierde ese nivel
#      —la entrada relativa se resuelve contra el almacen propio, falla, y git solo la alcanza de nuevo
#      a traves del repositorio, un nivel mas abajo—, y por eso discrimina este caso y no uno somero.
BQD="$SBR/cadena citada"
{ g clone -q --shared "$SBR/cadena 5" "$BQD" &&
  printf '%s\n' "$(cita "../../../cadena 5/.git/objects")" > "$BQD/.git/objects/info/alternates"; } >/dev/null 2>&1 \
  || { echo "FATAL: could not build the quoted chain" >&2; exit 2; }
H_BQD=$(huella "$BQD/.git")
check "(46) control: QUOTED RELATIVE alternates; plain Git reads the base six levels down" 0 \
  "$( [ "$(head -c 4 "$BQD/.git/objects/info/alternates")" = '"../' ] && sin_objetos "$BQD" &&
      git -C "$BQD" cat-file -e "$SB_B" && echo 0 || echo "invalid fixture" )"
corre_sb "$BQD" "$SUT" "$SB_L" "$SB_A"
check "(46b) owned store preserves the last level: IDENTICAL (0)" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(46c) clone remains unchanged and no store survives" 0 \
  "$( [ "$(huella "$BQD/.git")" = "$H_BQD" ] && [ "$(vacio)" = 0 ] && echo 0 || echo "wrote or left residue" )"
check "(46d) library mutant copying literal alternates was BUILT" 0 \
  "$( muta "$LIB_SUT" "$MLIB/lib/git-env.sh" "sed -n 's/^alternate: //p' <\"\$store/info/resolve.out\"" 'cat -- "$objects/info/alternates"' && echo 0 || echo "was not built" )"
corre_sb "$BQD" "$MLIB/check-claim-safety.sh" "$SB_L" "$SB_A"
check "(46e) literal-copy MUTANT loses the last level and REFUSES (2); (46b) discriminates" 0 \
  "$( [ "$RSB" = 2 ] && grep -q 'could not open a separate object store' "$TMP/out-sb" && echo 0 || echo "rc=$RSB: (46b) does not measure relative resolution" )"
corre_sb "$BQR" "$MLIB/check-claim-safety.sh" "$SB_L" "$SB_A"
check "(46f) that mutant reads the shallow relative entry (0): (45e) alone cannot discriminate" 0 "$(mismo 0 "$TMP/out-limpio")"
check "(46g) library mutant ignoring Git error: lines was BUILT" 0 \
  "$( muta "$LIB_SUT" "$MLIB/lib/git-env.sh" "grep -q '^error: ' \"\$store/info/resolve.err\"" 'false' && echo 0 || echo "was not built" )"
corre_sb "$BQM" "$MLIB/check-claim-safety.sh" "$SB_L" "$SB_A"
check "(46h) MUTANT ignoring error: lines gives verdict (0) for broken alternates; (45m) discriminates" 0 \
  "$( [ "$RSB" = 0 ] && echo 0 || echo "rc=$RSB: (45m) does not measure the broken entry" )"
vacio >/dev/null

# --- (44) la via (a): la bateria entera, invocada DESDE un repositorio dedicado --------------------
# ⛔ La huella del repositorio que INVOCA es el unico testigo de que los fixtures no le caen encima, y
# no se toma sobre el repositorio compartido: se fabrica uno, se corre la bateria desde el y se exige
# que no cambie. El mutante devuelve los fixtures al cwd. `OLIVARES_TCS_INTERIOR=1` evita que la
# corrida interior repita esta seccion.
if [ "${OLIVARES_TCS_INTERIOR:-}" != 1 ]; then
  INV="$SBR/invocante con espacio"
  { g init -q "$INV" && sb_commit "$INV" semilla "leeme.txt=semilla"; } >/dev/null 2>&1 || { echo "FATAL: could not build the caller" >&2; exit 2; }
  mkdir -p "$TMP/interior" || exit 2
  H_INV=$(huella "$INV/.git")
  ( CDPATH= cd -- "$INV" && exec env OLIVARES_TCS_INTERIOR=1 SUT="$SUT" TMPDIR="$TMP/interior" bash "$BATERIA" ) > "$TMP/out-interior" 2>&1; RI=$?
  check "(44) complete test suite passes when run FROM another repository" 0 \
    "$( [ "$RI" = 0 ] && grep -q ', 0 failed$' "$TMP/out-interior" && echo 0 || echo "rc=$RI: $(grep '^FAIL' "$TMP/out-interior" | tr '\n' ' ' | head -c 300)" )"
  check "(44b) invoking repository receives no objects, indexes or refs" 0 "$(igual "$(huella "$INV/.git")" "$H_INV")"
  check "(44c) mutant writing fixtures into the invoking repository was BUILT" 0 \
    "$( muta "$BATERIA" "$TMP/bateria-mutante.sh" "entra_repo_desechable "'|| {' ': || {' && echo 0 || echo "was not built" )"
  ( CDPATH= cd -- "$INV" && exec env OLIVARES_TCS_INTERIOR=fixtures SUT="$SUT" TMPDIR="$TMP/interior" bash "$TMP/bateria-mutante.sh" ) > "$TMP/out-interior-mutante" 2>&1
  check "(44d) MUTANT writes fixtures into invoking repository again; (44b) detects it" 0 \
    "$( [ "$(huella "$INV/.git")" != "$H_INV" ] && echo 0 || echo "did not write: (44b) measures nothing" )"
fi

echo
echo "check-claim-safety selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
