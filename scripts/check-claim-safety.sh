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

[ -n "$DECL" ] || { echo "check-claim-safety: NO HE PODIDO MIRAR: falta OLIVARES_CLAIM_FILES." >&2
                    echo "  Declara cuantos ficheros DEBE tocar el claim: un claim de un fichero que toca" >&2
                    echo "  diez es un NO antes de leer nada, y sin la cifra declarada no hay contra que" >&2
                    echo "  comparar. Ej.: OLIVARES_CLAIM_FILES=1 bash $0 <commit> [base]" >&2; exit 2; }

C="${1:-}"
[ -n "$C" ] || { echo "check-claim-safety: NO HE PODIDO MIRAR: falta el commit a revisar." >&2
                 echo "  uso: bash scripts/check-claim-safety.sh <commit> [base]" >&2; exit 2; }
git rev-parse --git-dir >/dev/null 2>&1 || { echo "check-claim-safety: NO HE PODIDO MIRAR: no estoy en un repositorio." >&2; exit 2; }
CS=$(git rev-parse --verify "${C}^{commit}" 2>/dev/null) \
  || { echo "check-claim-safety: NO HE PODIDO MIRAR: '$C' no es un commit de este clon." >&2; exit 2; }

if [ $# -ge 2 ] && [ -n "${2:-}" ]; then
  BS=$(git rev-parse --verify "${2}^{commit}" 2>/dev/null) \
    || { echo "check-claim-safety: NO HE PODIDO MIRAR: la base '$2' no resuelve." >&2; exit 2; }
else
  BS=$(git rev-parse --verify "${CS}^" 2>/dev/null) \
    || { echo "check-claim-safety: NO HE PODIDO MIRAR: '$C' no tiene primer padre y no diste base." >&2; exit 2; }
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
FUS=$(git merge-tree --write-tree "$BS" "$CS" 2>/dev/null); RCM=$?
if [ "$RCM" -ne 0 ] || [ -z "$FUS" ]; then
  echo "check-claim-safety: NO HE PODIDO MIRAR: la fusion de ${CS} sobre ${BS} CONFLICTA." >&2
  echo "  Un claim que no fusiona no se puede juzgar por lo que destruiria: primero se rebasa." >&2; exit 2
fi
echo "  arbol fusionado: ${FUS}"
NUM=$(git diff --numstat "$BS" "$FUS" 2>/dev/null) || { echo "check-claim-safety: NO HE PODIDO MIRAR: git diff fallo." >&2; exit 2; }
if [ -z "$NUM" ]; then
  echo "check-claim-safety: NO HE PODIDO MIRAR: la fusion no cambia NADA sobre la base." >&2
  echo "  Un claim que no aporta nada no es 'limpio': o ya esta aterrizado, o comparas contra ti mismo." >&2; exit 2
fi

# La guarda de arriba es la que hace fiable esta cuenta: `printf '%s\n' ""` emite UNA linea vacia,
# asi que sobre un diff vacio esto daria 1 y no 0. No se llega aqui con $NUM vacio, a proposito.
NF=$(printf '%s\n' "$NUM" | wc -l)
printf '%s\n' "$NUM" | awk '{printf "  %6s +%-6s -%s\n", "", $1, $2" "$3}'
echo "  -- ${NF} fichero(s)"

RC=0
# 0-bis · borrados FUERA del prefijo protegido, contra lo declarado
case "$BORRA_OK" in ''|*[!0-9]*) echo "check-claim-safety: NO HE PODIDO MIRAR: OLIVARES_CLAIM_DELETIONS='$BORRA_OK' no es un numero." >&2; exit 2;; esac
BORRADAS=$(printf '%s\n' "$NUM" | awk -F'\t' -v p="$PROTEGIDO" '$3 !~ "^"p && $2 ~ /^[0-9]+$/ {s+=$2} END{print s+0}')
if [ "$BORRADAS" -gt "$BORRA_OK" ]; then
  echo "check-claim-safety: ⛔ HALLAZGO — borra ${BORRADAS} linea(s) fuera de '${PROTEGIDO}' y declaraste ${BORRA_OK}:" >&2
  printf '%s\n' "$NUM" | awk -F'\t' -v p="$PROTEGIDO" '$3 !~ "^"p && $2+0 > 0 {printf "      %s: -%s\n", $3, $2}' >&2
  echo "      Un claim que solo AÑADE no puede tener borrados. Si los tiene, o reusaste un fichero" >&2
  echo "      construido contra otra base, o estas revirtiendo trabajo ajeno." >&2
  echo "      Si son intencionados: OLIVARES_CLAIM_DELETIONS=${BORRADAS}" >&2
  RC=1
fi
# ⛔ 0 · MODOS. `git diff --numstat` CUENTA LINEAS y el modo viaja en el ARBOL, no en el diff de
# texto: mi propia comprobacion «un fichero, +12/-0» era CIERTA Y CIEGA mientras el commit volvia
# `test-claim-safety.sh` de 100755 a 100644 — una bateria no ejecutable es una bateria que el
# gancho no puede correr (`./scripts/...` sale 126). La sonda que lo ve es `git diff --summary`.
SUM=$(git diff --summary "$BS" "$FUS" 2>/dev/null)
MODO=$(printf '%s\n' "$SUM" | grep -E '^ *mode change ' || true)
NOEXE=$(printf '%s\n' "$SUM" | grep -E '^ *create mode 100644 scripts/' || true)
if [ -n "$MODO" ]; then
  echo "check-claim-safety: ⛔ HALLAZGO — el commit cambia MODOS de fichero:" >&2
  printf '%s\n' "$MODO" | sed 's/^ */      /' >&2
  echo "      Un cambio de modo no aparece en \`--numstat\`: se ve con \`--summary\`. Si no era" >&2
  echo "      intencionado, reconstruye el arbol con el modo correcto." >&2
  RC=1
fi
if [ -n "$NOEXE" ]; then
  echo "check-claim-safety: ⛔ HALLAZGO — guion(es) creados NO EJECUTABLES bajo scripts/:" >&2
  printf '%s\n' "$NOEXE" | sed 's/^ */      /' >&2
  echo "      Un guion que nace 100644 no se puede correr como \`./scripts/<nombre>.sh\` (rc 126)." >&2
  RC=1
fi

# 1 · borrados en rutas protegidas
MAL=$(printf '%s\n' "$NUM" | awk -v p="$PROTEGIDO" '$3 ~ "^"p && $2 ~ /^[0-9]+$/ && $2+0 > 0 {print "      " $3 ": -" $2}')
if [ -n "$MAL" ]; then
  echo "check-claim-safety: ⛔ HALLAZGO — el commit BORRA lineas bajo '${PROTEGIDO}':" >&2
  printf '%s\n' "$MAL" >&2
  echo "      Nadie decide borrar correo publicado dentro de un claim de codigo. Si de verdad" >&2
  echo "      quieres retirar un asiento, es un commit propio que lo diga." >&2
  RC=1
fi

# 2 · el numero de ficheros DECLARADO
if [ -n "$DECL" ]; then
  case "$DECL" in ''|*[!0-9]*) echo "check-claim-safety: NO HE PODIDO MIRAR: OLIVARES_CLAIM_FILES='$DECL' no es un numero." >&2; exit 2;; esac
  if [ "$NF" -ne "$DECL" ]; then
    echo "check-claim-safety: ⛔ HALLAZGO — declaraste ${DECL} fichero(s) y toca ${NF}." >&2
    echo "      'un fichero' con ${NF} filas es un NO antes de leer nada mas." >&2
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
           git cat-file -e "${BS}:${f}" 2>/dev/null || printf '%s\n' "$f"; done)
if [ -n "$NUEVOS" ]; then
  echo "  ⚠ guion(es) NUEVO(s) bajo scripts/ — \`scripts/\` viaja en el export:"
  printf '%s\n' "$NUEVOS" | sed 's/^/      /'
  echo "     Corre \`task lint:export-closure\` en un checkout REAL antes de publicar: ese gate es"
  echo "     pata del gancho, y un hallazgo suyo en \`main\` bloquea el push de TODAS las cajas."
fi

[ "$RC" -eq 0 ] && echo "check-claim-safety: limpio — ${NF} fichero(s), cero borrados bajo '${PROTEGIDO}'."
exit "$RC"
