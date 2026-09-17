#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-rescue-backlog.sh — un rescate no deja de estar pendiente por estar publicado.
#
# CENSUS-SUBJECT: external
#   Su sujeto son los refs del REMOTO, no el árbol: pasar sobre un árbol vacío es correcto.
#
# ⛔ POR QUÉ EXISTE, medido el 2026-09-02 (a repository gate). Lo levantó al publicar su propio rescate:
#
#   «el control post-rescate de esta casa es CIEGO a refs/rescate/*. Un ref ahí existe pero no
#    aparece en el censo que busca trabajo sin publicar, así que esto sobrevive al borrado del
#    worktree y sigue sin salir en ninguna lista. Si nadie lo recoge, se pierde igual — sólo que
#    más despacio.»
#
#   Y no era un riesgo de diseño: era un mes de trabajo. `check-unpublished-work.sh` cuenta con
#   `--not --remotes`, es decir, commits inalcanzables desde CUALQUIER remoto — así que **publicar
#   un rescate lo BORRA del censo sin integrarlo**. Medido ese día: **16 refs bajo
#   `refs/heads/rescate/*` con 87 parches sin integrar**, algunos del **2 de agosto**.
#   Un rescate que se anula a sí mismo es peor que no rescatar: da por salvado lo que sigue perdido.
#
# ⛔ EL PREDICADO ES `git cherry`, Y LA ALTERNATIVA OBVIA FABRICA EL DEFECTO QUE ARREGLA.
#   Un rescate se integra REBASADO a `feature/*`, así que sus SHA cambian. Con
#   `merge-base --is-ancestor <punta> main` la respuesta sería **NO para siempre**, incluso después
#   de integrarlo perfectamente — y un control que grita cuando el trabajo ya está dentro se
#   desactiva solo: a la tercera falsa alarma nadie lo mira. `git cherry` compara por **patch-id**:
#
#       +<sha>  el parche NO está en main  → sigue PENDIENTE
#       -<sha>  ya está, con otro SHA      → deja de contar
#
#   Control positivo medido, mismo parche y otro SHA:  is-ancestor ⛔ pendiente · cherry ✅ integrado.
#
# ⚠ SU LÍMITE, escrito aquí y no descubierto luego: el patch-id cambia con CUALQUIER
#   integración que MODIFIQUE el parche — un squash, un cambio pedido en revisión, un conflicto
#   resuelto de otra forma, otro `gofmt`. El squash es sólo el caso más visible. Por eso existe la
#   salida explícita: una línea `rescue-integrated: <ref> <sha-que-lo-absorbió> <fecha>` en
#   `an internal design note (not shipped)`. No es la excepción del squash: es la salida general para toda
#   integración no idéntica.
#
# Salida: 0 no hay pendientes · 1 los hay (con su edad) · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

decir() { printf '%s\n' "$*"; }
ciego() { printf 'check-rescue-backlog: NO HE PODIDO MIRAR: %s\n' "$*" >&2; exit 2; }

RAIZ="$(git rev-parse --show-toplevel 2>/dev/null)" || ciego "no estoy dentro de un repositorio git"
cd "$RAIZ" || ciego "no puedo entrar en $RAIZ"

BASE="${OLIVARES_RESCUE_BASE:-origin/main}"
git rev-parse -q --verify "$BASE" >/dev/null 2>&1 || ciego "no existe la base $BASE"

LEDGER="${OLIVARES_RESCUE_LEDGER:-design/RESCUE-LEDGER.md}"
UMBRAL_DIAS="${OLIVARES_RESCUE_AGE_DAYS:-7}"

# Los refs: del REMOTO si se puede, y si no de lo que haya local — pero se DICE cuál se usó.
REFS="$(git for-each-ref --format='%(refname:short)' 'refs/remotes/*/rescate/*' 2>/dev/null)"
[ -n "$REFS" ] || REFS="$(git for-each-ref --format='%(refname:short)' 'refs/heads/rescate/*' 2>/dev/null)"
if [ -z "$REFS" ]; then
	decir "check-rescue-backlog: LIMPIO — no hay refs bajo rescate/*."
	exit 0
fi

ahora="$(date -u +%s)"
pendientes=0
ramas=0
salida=""
sin_medir=""
while IFS= read -r ref; do
	[ -n "$ref" ] || continue
	# ⛔ CON TIEMPO LÍMITE POR REF, y no por gusto: `git cherry` cuesta entre 4 y 25 s por rama
	#    (medido el 2026-09-02 sobre 14 refs: 137 s en total), y algunos rescates son historias
	#    HUÉRFANAS de miles de ficheros. Un censo que se cuelga en el ref número 9 no da un veredicto
	#    parcial: no da ninguno, y quien lo llame lo leerá como «no dijo nada». Un ref que no se puede
	#    medir se NOMBRA como no medido, que es la tercera respuesta.
	n="$(timeout "${OLIVARES_RESCUE_TIMEOUT:-60}" git cherry "$BASE" "$ref" 2>/dev/null | grep -c '^+')"
	rc_cherry=$?
	if [ "$rc_cherry" -eq 124 ]; then
		sin_medir="$sin_medir  $ref — NO MEDIDO: git cherry excedió ${OLIVARES_RESCUE_TIMEOUT:-60}s
"
		continue
	fi
	: "${n:=0}"
	[ "${n:-0}" -gt 0 ] || continue
	# la salida explícita: una integración no idéntica se declara en el libro mayor
	corto="${ref##*rescate/}"
	if [ -r "$LEDGER" ] && grep -qE "^rescue-integrated:[[:space:]]+(refs/heads/)?rescate/${corto}[[:space:]]" "$LEDGER" 2>/dev/null; then
		continue
	fi
	ramas=$((ramas + 1))
	pendientes=$((pendientes + n))
	cuando="$(git log -1 --format='%ct' "$ref" 2>/dev/null)"
	dias="?"
	case "$cuando" in ''|*[!0-9]*) ;; *) dias=$(( (ahora - cuando) / 86400 )) ;; esac
	salida="$salida  $ref — $n parche(s) sin integrar, $dias día(s)
"
done <<REFLIST
$REFS
REFLIST

if [ -n "$sin_medir" ]; then
	printf 'check-rescue-backlog: NO HE PODIDO MIRAR algunos refs:\n' >&2
	printf '%s' "$sin_medir" >&2
	printf '  Un ref no medido NO es un ref limpio. Sube OLIVARES_RESCUE_TIMEOUT o mídelo aparte.\n' >&2
	exit 2
fi

if [ "$ramas" -eq 0 ]; then
	decir "check-rescue-backlog: LIMPIO — ningún rescate tiene parches fuera de $BASE."
	exit 0
fi

printf 'check-rescue-backlog: %s rescate(s) con %s parche(s) SIN INTEGRAR:\n' "$ramas" "$pendientes" >&2
printf '%s' "$salida" >&2
# ⛔ EL DELIMITADOR VA ENTRECOMILLADO. Sin comillas, el shell EXPANDE el cuerpo: los backticks de
#    `check-unpublished-work.sh` y `--not --remotes` se EJECUTARON y dejaron la frase
#    gramatical y sin el dato — «Un rescate publicado NO está integrado: usa , así que…».
#    Un mensaje de ayuda con un agujero no parece roto: parece una frase mal escrita.
cat >&2 <<'AVISO'
  Un rescate publicado NO está integrado: `check-unpublished-work.sh` usa `--not --remotes`, así
  que publicarlo lo saca de ESE censo sin meterlo en main. Éste es el censo que sí lo ve.
  Salidas, en este orden:
    1. intégralo — rebasado a feature/*, carril rápido entero sobre esa rama;
    2. si lo integraste y el parche cambió (squash, revisión, conflicto resuelto de otra forma),
       declara la salida en el libro mayor de rescates:
           rescue-integrated: rescate/<nombre> <sha-que-lo-absorbió> <fecha ISO>
       porque el patch-id cambia con cualquier integración no idéntica y este gate volvería a
       contarlo;
    3. si no debe integrarse, dilo igual en ese libro mayor con el sha y la razón: un NO-LAND declarado
       deja de ser un pendiente silencioso.
AVISO
[ "$UMBRAL_DIAS" -gt 0 ] 2>/dev/null && printf '  (edad de referencia: %s días)\n' "$UMBRAL_DIAS" >&2
exit 1
