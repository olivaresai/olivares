#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Despliegue del canal refs/status en un contenedor claude-code.
# Idempotente: se puede ejecutar las veces que haga falta.
# Ejecutar como el usuario que usa git en ese contenedor (normalmente `claude`), NO como root:
# la config va al .git/config del clon, y root escribiría con otro dueño.
set -uo pipefail

# La raíz se DERIVA de dónde vive este guion, no se escribe. Dos razones, y la segunda es la que
# lo tumbaba: (a) el guion vive en <raíz>/scripts/, así que su propia ruta ES la raíz y el valor
# no puede quedarse obsoleto; (b) la ruta escrita en claro hace que `lint:export` marque fuga
# —«private org-or-domain»— y ese lint es un fast-lint, así que desde el 2026-08-16 09:32 el push
# de CUALQUIER carril moría aquí, no sólo el de quien tocara este fichero.
CLON="${1:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$CLON" || { echo "⛔ no existe $CLON"; exit 2; }

echo "== 1. Estado ANTES =="
echo "   refspecs:"; git config --get-all remote.origin.fetch | sed 's/^/     /'
echo "   refs/status en el clon: $(git for-each-ref 'refs/remotes/origin/status/*' | grep -c .)"
echo "   refs/status en el REMOTO: $(git ls-remote origin 'refs/status/*' | grep -c .)"

echo "== 2. Añadir el refspec (sólo si falta) =="
RS='+refs/status/*:refs/remotes/origin/status/*'
if git config --get-all remote.origin.fetch | grep -qxF "$RS"; then
  echo "   ya estaba puesto"
else
  git config --add remote.origin.fetch "$RS" && echo "   añadido"
fi

echo "== 3. Traer el canal =="
git fetch -q origin || { echo "⛔ el fetch falló"; exit 1; }

echo "== 4. CONTROL POSITIVO — sin esto no des el despliegue por bueno =="
N=$(git for-each-ref 'refs/remotes/origin/status/*' | grep -c .)
echo "   refs descargados: $N"
git for-each-ref --format='     %(refname:short) -> %(objectname:short)' 'refs/remotes/origin/status/*'
if [ "$N" -lt 1 ]; then
  echo "   ⛔ FALLO: el refspec está puesto y no ha bajado nada. NO es un despliegue correcto."
  exit 1
fi

echo "== 5. Que el LECTOR responda =="
if [ -x scripts/status-ref.sh ]; then
  bash scripts/status-ref.sh >/dev/null 2>&1
  echo "   status-ref.sh rc=$? (64 = imprime su uso, es correcto)"
else
  echo "   ⛔ scripts/status-ref.sh no existe o no es ejecutable en este clon"
fi

echo
echo "✅ Canal desplegado. Lectura del contenido:"
echo "   bash scripts/status-ref.sh read --all"
echo "   git show refs/remotes/origin/status/live:sessions/status/inbox/<CARRIL>.md"
