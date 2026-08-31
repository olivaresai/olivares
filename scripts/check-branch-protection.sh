#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-branch-protection.sh — la protección de rama se APLICA y nadie comprobaba que siguiera puesta.
#
# CENSUS-SUBJECT: external
#   Su sujeto es la CONFIGURACIÓN VIVA del repositorio en GitHub, no el árbol. Pasar sobre un repo
#   vacío sería correcto; lo que NO puede es pasar sin haberla leído.
#
# WHY THIS EXISTS, 2026-08-19. `scripts/apply-branch-protection.sh` APLICA la protección desde hace
# tiempo; no había nada que comprobara que sigue aplicada. Una configuración que sólo se escribe es
# una configuración que nadie sabe si alguien deshizo — y la deshace un clic en la UI, sin dejar
# rastro en el árbol ni en el historial.
#
# El empujón vino de fuera: ese día el integrador midió que el `Deploy-protection audit` del REPO
# WEB llevaba 17 días sin pasar —49 fallos, 0 éxitos— porque le falta la credencial, y su workflow
# **se niega a dar verde en vez de fingirlo**. Aquí ni siquiera se intentaba.
#
# ⭐ EL ORÁCULO SALE DEL SCRIPT QUE APLICA, NO DE UNA COPIA. Los contextos requeridos se leen de
# `apply-branch-protection.sh` (perfil `hub`), así que cuando alguien cambie ahí la lista, esta
# comprobación cambia con ella. Copiarlos aquí habría creado la segunda cifra que se pudre.
#
# ⚠ Y NO COMPRUEBA `enforce_admins` NI EL NÚMERO DE REVISIONES, a propósito y con la razón del
# propio script delante: en el hub `approvals` es **0** porque todos los carriles empujan con la
# MISMA identidad y GitHub prohíbe aprobar tu propio PR (medido en #458 el 2026-08-01), así que
# exigir ≥1 bloquearía TODOS los merges. Un gate que pidiera lo contrario pediría que el repo se
# atasque, que es la forma más cara de tener razón.
#
# LAS TRES RESPUESTAS
#   LIMPIO (0)             la protección viva coincide con lo declarado
#   ROTO (1)               falta un contexto, `strict` está apagado, o se permiten force-push/borrados
#   NO_HE_PODIDO_MIRAR (2) sin `gh`/`jq`, sin permiso para leer la protección, o sin leer lo declarado

set -uo pipefail
cd "$(dirname "$0")/.."

APLICA="${OLIVARES_PROTECTION_SOURCE:-scripts/apply-branch-protection.sh}"
RAMA="${OLIVARES_PROTECTED_BRANCH:-main}"

no_he_podido() {
  echo "check-branch-protection: NO HE PODIDO MIRAR — $1" >&2
  echo "  Un 'no he podido leer la configuración' NO es 'la configuración está bien'." >&2
  exit 2
}

command -v gh >/dev/null 2>&1 || no_he_podido "no hay 'gh' en el PATH"
command -v jq >/dev/null 2>&1 || no_he_podido "no hay 'jq' en el PATH"
[ -r "$APLICA" ] || no_he_podido "no puedo leer $APLICA, de donde salen los contextos declarados"

# ⛔ SE ANCLA AL BLOQUE `hub)`, no al primer `CONTEXTS=` del fichero. El primero es el del perfil
# PÚBLICO y su valor es una VARIABLE (`$DEFAULT_PUBLIC_CONTEXTS`), así que un `head -1` lee el
# perfil equivocado. Lo cazó el propio fallo cerrado de este gate en su primera corrida: dijo «no he
# sabido leer los contextos» en vez de comparar contra la lista de otro perfil, que es exactamente
# la diferencia entre no mirar y mirar mal.
declarados="$(awk '/^  hub\)/{d=1} d && /CONTEXTS="\$\{CONTEXTS:-/{print; exit}' "$APLICA" \
  | sed -E 's/.*:-//; s/\}".*$//')"
case "$declarados" in
  ''|*[!a-zA-Z0-9,_-]*) no_he_podido "no he sabido leer los contextos declarados en $APLICA" ;;
esac

if [ -n "${OLIVARES_PROTECTION_JSON:-}" ]; then
  # Camino de PRUEBA: la batería inyecta la respuesta de la API para poder ser hermética.
  [ -r "$OLIVARES_PROTECTION_JSON" ] || no_he_podido "no puedo leer $OLIVARES_PROTECTION_JSON"
  viva="$(cat "$OLIVARES_PROTECTION_JSON")"
  REPO="(fichero inyectado)"
else
  REPO="${OLIVARES_PROTECTED_REPO:-$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null || true)}"
  [ -n "$REPO" ] || no_he_podido "no he podido resolver el repositorio (¿fuera de un clon con remoto?)"
  viva="$(gh api "repos/$REPO/branches/$RAMA/protection" 2>/dev/null)" \
    || no_he_podido "la API no me deja leer la protección de $REPO@$RAMA (¿token sin permiso de admin?)"
fi
[ -n "$viva" ] || no_he_podido "la respuesta de protección vino VACÍA para $REPO@$RAMA"
printf '%s' "$viva" | jq -e . >/dev/null 2>&1 || no_he_podido "la respuesta de protección no es JSON"

# ⛔ NADA DE `// "ausente"` AQUÍ, y me costó un ROTO sobre la configuración CORRECTA: en jq el
# operador `//` devuelve la alternativa cuando el lado izquierdo es null **O FALSE**. Como los tres
# campos que importan son booleanos que valen `false` cuando están bien, `false // "ausente"` daba
# "ausente" y el gate acusaba de estar rota una protección impecable. Un guardián que grita por lo
# correcto se desactiva en una semana. Se distingue null de false con un `if . == null`.
fallos=""
add() { fallos="${fallos}
  ✖ $1"; }

strict="$(printf '%s' "$viva" | jq -r '.required_status_checks.strict | if . == null then "ausente" else tostring end')"
[ "$strict" = "true" ] || add "required_status_checks.strict = $strict (sin él se mergea sobre una base sin verificar)"

force="$(printf '%s' "$viva" | jq -r '.allow_force_pushes.enabled | if . == null then "ausente" else tostring end')"
[ "$force" = "false" ] || add "allow_force_pushes = $force (debe ser false)"

borra="$(printf '%s' "$viva" | jq -r '.allow_deletions.enabled | if . == null then "ausente" else tostring end')"
[ "$borra" = "false" ] || add "allow_deletions = $borra (debe ser false)"

faltan=""
while IFS= read -r c; do
  [ -n "$c" ] || continue
  printf '%s' "$viva" | jq -e --arg c "$c" '.required_status_checks.contexts // [] | index($c)' >/dev/null 2>&1 \
    || faltan="${faltan} $c"
done <<EOF
$(printf '%s' "$declarados" | tr ',' '\n')
EOF
[ -z "$faltan" ] || add "faltan contextos requeridos:${faltan} (declarados en $APLICA)"

if [ -n "$fallos" ]; then
  echo "check-branch-protection: ROTO — la protección viva de $REPO@$RAMA no es la declarada:$fallos" >&2
  echo "  Comparado contra $APLICA (contextos declarados: $declarados)." >&2
  exit 1
fi

echo "check-branch-protection: OK — $REPO@$RAMA con strict, sin force-push ni borrados, y los"
echo "                          contextos declarados presentes ($declarados)."
