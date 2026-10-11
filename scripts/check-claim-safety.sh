#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# ¿Este commit, materializado, DESTRUIRIA algo que nadie ha decidido destruir?
#
# ⛔ POR QUE EXISTE, Y NO ES HIPOTETICO. El 2026-08-30 publique un claim que declare como «un
# fichero, +14/−3» y que borraba **32 lineas en CADA uno de los nueve buzones** — un asiento
# publicado a toda la flota. La causa: `read-tree origin/main` en una llamada y
# `commit-tree -p origin/main` minutos despues; `origin/main` se movio CUATRO VECES en cinco
# minutos (los worktrees comparten refs y el de publicacion fetchea en cada asiento), asi que el
# arbol era de un `main` y el padre de OTRO, y todo lo que `main` gano en medio salio como BORRADO.
#
# ⛔ Y LO QUE HACE FALTA ENTENDER: `git merge-tree` daba **rc 0**. No mentia — decia «no
# CONFLICTA», que es otra pregunta. **Un borrado limpio en fast-forward es exactamente lo que ese
# testigo no puede ver.** Para «no destruye» hay que CONTAR LOS BORRADOS, y eso es lo que hace esto.
#
# Veredictos: 0 = limpio · 1 = hallazgo (borra protegido, o toca otro numero de ficheros del
# declarado) · 2 = NO HE PODIDO MIRAR. Nunca 0 por silencio.
#
#   bash scripts/check-claim-safety.sh <commit> [base]        # base por defecto: su primer padre
#   OLIVARES_CLAIM_FILES=1 bash scripts/check-claim-safety.sh <commit>
set -u

# ⛔ EL ENTORNO GIT AMBIENTE NO DECIDE QUE REPOSITORIO NI QUE ALMACEN SE MIRAN. `GIT_DIR` llega
# exportada desde cualquier worktree enlazado, y un `GIT_OBJECT_DIRECTORY` o un
# `GIT_ALTERNATE_OBJECT_DIRECTORIES` heredados cambiarian el almacen que se lee y el que se escribe.
# Se sanea como el resto de la casa, antes de resolver nada; la libreria trae ademas el almacen
# propio de la fusion (abajo).
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
  echo "check-claim-safety: COULD NOT CHECK: cannot load $_olivares_git_env (aislamiento git)." >&2
  exit 2; }
unset _olivares_git_env

PROTEGIDO="${OLIVARES_CLAIM_PROTECTED:-sessions/status/inbox/}"
# ⛔ OBLIGATORIA desde la v2, y la razon es la que encontro el lector: siendo opcional, OMITIRLA
# daba rc 0 «limpio». Una guarda cuyo modo por defecto es no comprobar nada no protege: protege a
# quien se acuerda, que es justo quien no la necesita. Ahora sin ella es 2 — «no he podido mirar».
DECL="${OLIVARES_CLAIM_FILES:-}"
# ⛔ BORRADOS FUERA DE LO PROTEGIDO, y esta guarda nace de un fallo MIO que ella misma no cazo. El
# 2026-08-30 publique un claim que borraba **24 lineas del `Taskfile`** —por reusar ficheros de
# cableado construidos contra una base ANTERIOR— y esta guarda dijo «limpio»: solo miraba borrados
# bajo el prefijo protegido y los modos. **Un guarda que solo protege lo que su autor recordo
# proteger deja fuera justo lo que no previo.**
#
# La forma es la misma que ya funciona con `OLIVARES_CLAIM_FILES`: se DECLARA lo que se espera, y
# el defecto es CERO. Un claim que solo añade no puede tener borrados; si los tiene, o reusaste un
# fichero rancio o estas revirtiendo trabajo ajeno — las dos cosas se ven en el mismo numero.
BORRA_OK="${OLIVARES_CLAIM_DELETIONS:-0}"

[ -n "$DECL" ] || { echo "check-claim-safety: COULD NOT CHECK: OLIVARES_CLAIM_FILES is not set." >&2
                    echo "  Declare how many files the change must touch. A change declared as one file" >&2
                    echo "  that touches ten must be rejected; without a declared count, there is nothing" >&2
                    echo "  to compare against. Example: OLIVARES_CLAIM_FILES=1 bash $0 <commit> [base]" >&2; exit 2; }

C="${1:-}"
[ -n "$C" ] || { echo "check-claim-safety: COULD NOT CHECK: no commit specified for review." >&2
                 echo "  usage: bash scripts/check-claim-safety.sh <commit> [base]" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "check-claim-safety: COULD NOT CHECK: outside a repository." >&2; exit 2; }
CS=$(git rev-parse --verify "${C}^{commit}" 2>/dev/null) \
  || { echo "check-claim-safety: COULD NOT CHECK: '$C' is not a commit in this clone." >&2; exit 2; }

if [ $# -ge 2 ] && [ -n "${2:-}" ]; then
  BS=$(git rev-parse --verify "${2}^{commit}" 2>/dev/null) \
    || { echo "check-claim-safety: COULD NOT CHECK: base '$2' cannot be resolved." >&2; exit 2; }
else
  BS=$(git rev-parse --verify "${CS}^" 2>/dev/null) \
    || { echo "check-claim-safety: COULD NOT CHECK: '$C' has no first parent and no base was specified." >&2; exit 2; }
fi

# ⛔ LA BASE SE IMPRIME RESUELTA A SHA, SIEMPRE. Un veredicto contra `origin/main` no es citable:
# ese nombre vale una cosa ahora y otra en cinco minutos, que es justo el defecto que creo esto.
echo "check-claim-safety: commit ${CS} · base ${BS}"

# ⛔ LA PREGUNTA ES SOBRE EL ARBOL FUSIONADO, NO SOBRE EL DIFF. Un `git diff base..claim` mide
# «sustituir», y para un claim que DIVERGE de la base eso marca como borrado todo lo que la base
# gano por su cuenta — que es la misma alarma falsa que ya me comi hoy con los diez claims. Lo que
# de verdad aterriza es la FUSION: para un claim en fast-forward el arbol fusionado ES el del claim
# (y el borrado se ve), y para uno divergente la fusion conserva lo de la base (y no hay borrado).
# Se calcula el arbol de la fusion y se compara ESE con la base.
# ⛔ EL VEREDICTO DEL MERGE SE LEE DEL rc, NO DE SI stdout VINO VACIO. Un stdout vacio puede ser
# un conflicto, un fallo de git o un binario que no esta: los tres se escriben igual. `merge-tree`
# sale != 0 cuando conflicta, y ese es el dato.
#
# ⛔ Y LA FUSION ESCRIBE, Y NO PUEDE HACERLO EN EL ALMACEN DEL REPOSITORIO QUE SE JUZGA. `merge-tree
# --write-tree` escribe el arbol fusionado y, al conflictar, los blobs con marcadores: medido con git
# 2.39.5 en un repositorio desechable, 2 objetos sueltos en una fusion que conflicta y 1 en una limpia
# divergente. Al conflictar este guion sale 2 aqui abajo y nadie vuelve a alcanzarlos. La fusion y
# TODA lectura que dependa de su arbol van contra un almacen PROPIO y temporal que lee del almacen
# resuelto del repositorio y de sus alternates (`scripts/lib/git-env.sh`). No se exporta nada, y se
# borra al salir o al ser interrumpido; un SIGKILL no deja correr ninguna limpieza. Si no se puede
# abrir, o no lee la base y el claim, es 2: un almacen que no ve lo que mide no da un veredicto.
trap olivares_git_owned_store_close EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
olivares_git_owned_store_open "$BS" "$CS" "${BS}^{tree}" "${CS}^{tree}" || {
  echo "check-claim-safety: COULD NOT CHECK: could not open a separate object store for the merge." >&2
  echo "  Without it, the merge would write unreachable objects to the repository's store." >&2
  echo "  A store that cannot read both the base and the change cannot produce a valid result." >&2
  exit 2; }
FUS=$(olivares_git_owned merge-tree --write-tree "$BS" "$CS" 2>/dev/null); RCM=$?
if [ "$RCM" -ne 0 ] || [ -z "$FUS" ]; then
  echo "check-claim-safety: COULD NOT CHECK: merging ${CS} onto ${BS} causes conflicts." >&2
  echo "  The deletion check requires a conflict-free merge: rebase the change first." >&2; exit 2
fi
# El OID va MARCADO: ese arbol vive en el almacen propio y desaparece con el. Citarlo como un objeto
# recuperable del repositorio seria citar algo que alli no existe.
echo "  merged tree (temporary, in a separate object store removed on exit): ${FUS}"
NUM=$(olivares_git_owned diff --numstat "$BS" "$FUS" 2>/dev/null) || { echo "check-claim-safety: COULD NOT CHECK: git diff failed." >&2; exit 2; }
if [ -z "$NUM" ]; then
  echo "check-claim-safety: COULD NOT CHECK: the merge changes nothing from the base." >&2
  echo "  An empty change cannot be reported as clean: it may already be merged, or compared against itself." >&2; exit 2
fi

# La guarda de arriba es la que hace fiable esta cuenta: `printf '%s\n' ""` emite UNA linea vacia,
# asi que sobre un diff vacio esto daria 1 y no 0. No se llega aqui con $NUM vacio, a proposito.
NF=$(printf '%s\n' "$NUM" | wc -l)
printf '%s\n' "$NUM" | awk '{printf "  %6s +%-6s -%s\n", "", $1, $2" "$3}'
echo "  -- ${NF} file(s)"

RC=0
# 0-bis · borrados FUERA del prefijo protegido, contra lo declarado
case "$BORRA_OK" in ''|*[!0-9]*) echo "check-claim-safety: COULD NOT CHECK: OLIVARES_CLAIM_DELETIONS='$BORRA_OK' is not a number." >&2; exit 2;; esac
BORRADAS=$(printf '%s\n' "$NUM" | awk -F'\t' -v p="$PROTEGIDO" '$3 !~ "^"p && $2 ~ /^[0-9]+$/ {s+=$2} END{print s+0}')
if [ "$BORRADAS" -gt "$BORRA_OK" ]; then
  echo "check-claim-safety: ⛔ FINDING — deletes ${BORRADAS} line(s) outside '${PROTEGIDO}', but ${BORRA_OK} were declared:" >&2
  printf '%s\n' "$NUM" | awk -F'\t' -v p="$PROTEGIDO" '$3 !~ "^"p && $2+0 > 0 {printf "      %s: -%s\n", $3, $2}' >&2
  echo "      An additions-only change cannot delete lines. Deletions may come from reusing a file" >&2
  echo "      built against a different base or reverting someone else's work." >&2
  echo "      If intentional: OLIVARES_CLAIM_DELETIONS=${BORRADAS}" >&2
  RC=1
fi
# ⛔ 0 · MODOS. `git diff --numstat` CUENTA LINEAS y el modo viaja en el ARBOL, no en el diff de
# texto: mi propia comprobacion «un fichero, +12/-0» era CIERTA Y CIEGA mientras el commit volvia
# `test-claim-safety.sh` de 100755 a 100644 — una bateria no ejecutable es una bateria que el
# gancho no puede correr (`./scripts/...` sale 126). La sonda que lo ve es `git diff --summary`.
SUM=$(olivares_git_owned diff --summary "$BS" "$FUS" 2>/dev/null)
MODO=$(printf '%s\n' "$SUM" | grep -E '^ *mode change ' || true)
NOEXE=$(printf '%s\n' "$SUM" | grep -E '^ *create mode 100644 scripts/' || true)
if [ -n "$MODO" ]; then
  echo "check-claim-safety: ⛔ FINDING — the commit changes file modes:" >&2
  printf '%s\n' "$MODO" | sed 's/^ */      /' >&2
  echo "      Mode changes do not appear in \`--numstat\`; inspect \`--summary\`. If unintentional," >&2
  echo "      rebuild the tree with the correct mode." >&2
  RC=1
fi
if [ -n "$NOEXE" ]; then
  echo "check-claim-safety: ⛔ FINDING — new nonexecutable script(s) under scripts/:" >&2
  printf '%s\n' "$NOEXE" | sed 's/^ */      /' >&2
  echo "      A script created with mode 100644 cannot run as \`./scripts/<nombre>.sh\` (exit 126)." >&2
  RC=1
fi

# 1 · borrados en rutas protegidas
MAL=$(printf '%s\n' "$NUM" | awk -v p="$PROTEGIDO" '$3 ~ "^"p && $2 ~ /^[0-9]+$/ && $2+0 > 0 {print "      " $3 ": -" $2}')
if [ -n "$MAL" ]; then
  echo "check-claim-safety: ⛔ FINDING — the commit deletes lines under '${PROTEGIDO}':" >&2
  printf '%s\n' "$MAL" >&2
  echo "      Published correspondence must not be deleted as part of a code change. If an entry" >&2
  echo "      must be removed, use a separate commit that describes the removal." >&2
  RC=1
fi

# 2 · el numero de ficheros DECLARADO
if [ -n "$DECL" ]; then
  case "$DECL" in ''|*[!0-9]*) echo "check-claim-safety: COULD NOT CHECK: OLIVARES_CLAIM_FILES='$DECL' is not a number." >&2; exit 2;; esac
  if [ "$NF" -ne "$DECL" ]; then
    echo "check-claim-safety: ⛔ FINDING — ${DECL} file(s) declared, but the change touches ${NF}." >&2
    echo "      A one-file change with ${NF} rows must be rejected before further review." >&2
    RC=1
  fi
fi

# ⛔ AVISO ESTRECHO, Y NACE DE UN INCIDENTE DE FLOTA. `scripts/` VIAJA ENTERO EN EL EXPORT, asi
# que un guion NUEVO puede referenciar rutas que el espejo cura — y `lint:export-closure` es PATA
# DEL GANCHO (tres invocaciones en `.githooks/pre-push`). El 2026-08-30 publique
# `scripts/test-claim-safety.sh` sin correr ese gate sobre el: `main` se puso rojo y desde las
# 17:20Z **ningun push local de ninguna caja pasaba el gancho**. No fue un fallo de una sesion: lo
# pago la flota entera.
#
# Salta SOLO con ficheros NUEVOS bajo `scripts/` —no con modificaciones— porque un aviso que se
# enciende en todo no informa: se aprende a saltarlo. Y es AVISO, no hallazgo: no cambia el rc,
# porque esto no sabe si ya lo corriste.
NUEVOS=$(printf '%s\n' "$NUM" | awk -F'\t' '$3 ~ /^scripts\// {print $3}' | while IFS= read -r f; do
           olivares_git_owned cat-file -e "${BS}:${f}" 2>/dev/null || printf '%s\n' "$f"; done)
if [ -n "$NUEVOS" ]; then
  echo "  ⚠ new script(s) under scripts/ — \`scripts/\` is included in the export:"
  printf '%s\n' "$NUEVOS" | sed 's/^/      /'
  echo "     Run \`task lint:export-closure\` in a real checkout before publishing: this check"
  echo "     runs in the hook, and a finding on \`main\` blocks pushes from every host."
fi

[ "$RC" -eq 0 ] && echo "check-claim-safety: CLEAN — ${NF} file(s), no deletions under '${PROTEGIDO}'."
exit "$RC"
