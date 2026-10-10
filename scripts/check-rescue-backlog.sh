#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-rescue-backlog.sh — publishing a rescue does not integrate its pending work.
#
# CENSUS-SUBJECT: external
# The subject is remote refs, not local files; an empty tree can pass correctly.
#
# found this gap on 2026-09-02 (a repository gate): check-unpublished-work.sh uses
# --not --remotes, counting commits unreachable from any remote. Publishing a rescue
# therefore removed it from that census without integrating it. Remote refs survive
# worktree deletion and can still lose work if no one claims them. Measured that day:
# 16 refs under refs/heads/rescate/*, 87 unintegrated patches, some from August 2.
#
# Use git cherry's patch-id comparison. Rescues rebase onto feature/* and change SHA;
# merge-base --is-ancestor <tip> main would report pending forever after integration.
# Repeated false alarms defeat the control. git cherry reports +<sha> for pending
# patches and -<sha> for integrated equivalents. The positive control used the same
# patch with another SHA: ancestry said pending, cherry said integrated.
#
# Limit: any edit changes patch-id, including squash, review fixes, conflict
# resolution or gofmt. The explicit general escape for nonidentical integration is
# `rescue-integrated: <ref> <absorbing-sha> <date>` in an internal design note (not shipped)
# Exit: 0 no pending work · 1 pending work with age · 2 could not check.
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
ciego() { printf 'check-rescue-backlog: COULD NOT CHECK: %s\n' "$*" >&2; exit 2; }

RAIZ="$(git rev-parse --show-toplevel 2>/dev/null)" || ciego "not inside a Git repository"
cd "$RAIZ" || ciego "cannot enter $RAIZ"

BASE="${OLIVARES_RESCUE_BASE:-origin/main}"
git rev-parse -q --verify "$BASE" >/dev/null 2>&1 || ciego "base $BASE does not exist"

LEDGER="${OLIVARES_RESCUE_LEDGER:-design/RESCUE-LEDGER.md}"
UMBRAL_DIAS="${OLIVARES_RESCUE_AGE_DAYS:-7}"

# Los refs: del REMOTO si se puede, y si no de lo que haya local — pero se DICE cuál se usó.
REFS="$(git for-each-ref --format='%(refname:short)' 'refs/remotes/*/rescate/*' 2>/dev/null)"
[ -n "$REFS" ] || REFS="$(git for-each-ref --format='%(refname:short)' 'refs/heads/rescate/*' 2>/dev/null)"
if [ -z "$REFS" ]; then
	decir "check-rescue-backlog: CLEAN — no refs under rescate/*."
	exit 0
fi

ahora="$(date -u +%s)"
pendientes=0
ramas=0
output=""
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
		sin_medir="$sin_medir  $ref — UNMEASURED: git cherry exceeded ${OLIVARES_RESCUE_TIMEOUT:-60}s
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
	output="$output  $ref — $n unintegrated patch(es), $dias day(s)
"
done <<REFLIST
$REFS
REFLIST

if [ -n "$sin_medir" ]; then
	printf 'check-rescue-backlog: COULD NOT CHECK some refs:\n' >&2
	printf '%s' "$sin_medir" >&2
	printf '  An unmeasured ref cannot be reported as clean. Raise OLIVARES_RESCUE_TIMEOUT or measure it separately.\n' >&2
	exit 2
fi

if [ "$ramas" -eq 0 ]; then
	decir "check-rescue-backlog: CLEAN — no recovery branch has patches missing from $BASE."
	exit 0
fi

printf 'check-rescue-backlog: %s recovery branch(es) with %s unintegrated patch(es):\n' "$ramas" "$pendientes" >&2
printf '%s' "$output" >&2
# ⛔ EL DELIMITADOR VA ENTRECOMILLADO. Sin comillas, el shell EXPANDE el cuerpo: los backticks de
#    `check-unpublished-work.sh` y `--not --remotes` se EJECUTARON y dejaron la frase
#    gramatical y sin el dato — «Un rescate publicado NO está integrado: usa , así que…».
#    Un mensaje de ayuda con un agujero no parece roto: parece una frase mal escrita.
cat >&2 <<'AVISO'
  Publishing a recovery branch does not integrate it: `check-unpublished-work.sh` uses `--not --remotes`,
  so publication removes it from that inventory without merging it into main. This inventory still sees it.
  Resolve it in this order:
    1. Integrate it: rebase onto feature/* and run the full fast lane on that branch.
    2. If integration changed the patch (squash, review changes, or a different conflict resolution),
       record its integration in the recovery ledger:
           rescue-integrated: rescate/<name> <absorbing-sha> <ISO-date>
       Patch IDs change with any nonidentical integration, so this gate would otherwise count
       it again.
    3. If it should not land, record its SHA and rationale in the same ledger: an explicit NO-LAND
       decision removes a silently pending patch.
AVISO
[ "$UMBRAL_DIAS" -gt 0 ] 2>/dev/null && printf '  (reference age: %s days)\n' "$UMBRAL_DIAS" >&2
exit 1
