#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-prep-gate-impact.sh — which PREPARATION gates would a branch turn red when it lands?
#
# WHY THIS IS A SCRIPT AND NOT A LIST. On 2026-08-21 the integrator measured that 32 of the 56
# open PRs redden a prep gate — the real bottleneck of the queue, ahead of merge conflicts — and
# closed the message with the only instruction that survives: «re-derive this list against the
# main of the day before each landing, NEVER against this message, which also expires». A list
# of PR numbers is stale the moment a lot lands. The measurement is not.
#
# THE METHOD IS EXECUTION, NOT PATTERN MATCHING, and the difference is the whole point: of the
# 33 PRs that TOUCH a watched file, 32 actually fire the assertion. Touching is not tripping.
# Publishing 33 would have inflated the problem with a method that cannot tell the two apart.
#
# AND EVERY VERDICT CARRIES ITS BASELINE. For each gate we first run it against pristine
# origin/main. Without that, "your branch reddens 4 gates" could just mean "4 gates are broken
# today". A gate already red on main is reported as its OWN problem, not the branch's.
#
# THREE ANSWERS: 0 nothing trips / 1 at least one gate trips / 2 CANNOT LOOK. Never two: a
# derivation that found nothing is not "clean".
set -uo pipefail
export LC_ALL=C

# ⛔ AISLAMIENTO DEL ENTORNO GIT, Y NO ES CEREMONIA: este guion empareja `mktemp -d` con `git`.
# Un `GIT_DIR` heredado MANDA SOBRE EL DIRECTORIO, así que un entorno envenenado haría que el
# espejo y sus `git checkout`/`git clean` cayeran en el repositorio equivocado — el del que
# invoca, con su trabajo dentro. `lint:git-env` lo señaló en el primer push de este fichero, que
# es exactamente para lo que existe ese trinquete.
#
# Fail-closed: un saneador que no se puede cargar es «no he podido aislar», nunca «no hacía falta».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "check-prep-gate-impact: FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
REF="${1:-HEAD}"
BASE_REF="${OLIVARES_PREP_BASE:-origin/main}"
# Floors. A derivation that silently returns nothing would report "clean" over every branch,
# which is the false green this script exists to prevent. Both are deliberately below today's
# counts (54 gates, ~170 paths) so growth does not trip them and shrinkage does.
MIN_GATES="${OLIVARES_PREP_MIN_GATES:-20}"
MIN_PATHS="${OLIVARES_PREP_MIN_PATHS:-50}"

no_puedo() { printf 'check-prep-gate-impact: CANNOT LOOK — %s\n' "$*" >&2; exit 2; }

command -v git >/dev/null 2>&1 || no_puedo "git is not on PATH."
cd "$ROOT" || no_puedo "cannot enter the repository root $ROOT."

git rev-parse --verify -q "$REF" >/dev/null || no_puedo "the ref $REF does not resolve."
git rev-parse --verify -q "$BASE_REF" >/dev/null || no_puedo "the base $BASE_REF does not resolve (fetch first)."
BASE="$(git merge-base "$BASE_REF" "$REF" 2>/dev/null)" || no_puedo "no merge base between $BASE_REF and $REF."
[ -n "$BASE" ] || no_puedo "the merge base came back empty."

CAMBIADOS="$(git diff --name-only "$BASE".."$REF" 2>/dev/null)" || no_puedo "cannot diff $BASE..$REF."

# ── LOS GUARDIANES Y LO QUE VIGILAN ───────────────────────────────────────────────────────────
# Todos declaran sus rutas con la MISMA forma, `VAR="${ENV:-ruta}"`, y ahí es donde la primera
# derivación de la flota se quedó corta: usaba `[A-Z]*` para el nombre de la variable de entorno
# y varias llevan DÍGITOS (`OLIVARES_ALC01S3_WIRE`), así que 14 de 15 no era un recuento, era un
# suelo. Aquí el nombre admite `[A-Za-z0-9_]`.
#
# Se descarta el valor que empieza por `$(`: ése es `ROOT`, la raíz del repo, no un fichero
# vigilado.
GATES="$(ls scripts/check-*-prep.sh 2>/dev/null)"
NGATES="$(printf '%s\n' "$GATES" | grep -c . || true)"
[ "${NGATES:-0}" -ge "$MIN_GATES" ] ||
	no_puedo "only ${NGATES:-0} prep gates found under scripts/ (floor is $MIN_GATES)."

TMP="$(mktemp -d "${TMPDIR:-/tmp}/prepimpact.XXXXXX")" || no_puedo "cannot create a scratch directory."
ESPEJO="$TMP/espejo"
limpiar() {
	[ -n "${ESPEJO:-}" ] && [ -d "$ESPEJO" ] && git worktree remove --force "$ESPEJO" >/dev/null 2>&1
	[ -n "${TMP:-}" ] && rm -rf -- "$TMP"
}
trap limpiar EXIT INT TERM

RUTAS_TOTAL=0
: > "$TMP/pares"   # <gate>\t<ruta>
while IFS= read -r g; do
	[ -n "$g" ] || continue
	while IFS= read -r ruta; do
		[ -n "$ruta" ] || continue
		case "$ruta" in '$('*) continue ;; esac
		RUTAS_TOTAL=$((RUTAS_TOTAL + 1))
		printf '%s\t%s\n' "$g" "$ruta" >> "$TMP/pares"
	done <<-EOF
		$(sed -nE 's/^[A-Z][A-Z0-9_]*="\$\{[A-Za-z0-9_]+:-(.*)\}"[[:space:]]*$/\1/p' "$g")
	EOF
done <<EOF
$GATES
EOF

[ "$RUTAS_TOTAL" -ge "$MIN_PATHS" ] ||
	no_puedo "derived only $RUTAS_TOTAL watched paths from $NGATES gates (floor is $MIN_PATHS): the intersection would match nothing."

# ── LA INTERSECCIÓN ───────────────────────────────────────────────────────────────────────────
printf '%s\n' "$CAMBIADOS" | sort -u > "$TMP/cambiados"
: > "$TMP/afectados"
while IFS="$(printf '\t')" read -r g ruta; do
	grep -qxF "$ruta" "$TMP/cambiados" && printf '%s\t%s\n' "$g" "$ruta" >> "$TMP/afectados"
done < "$TMP/pares"

if [ ! -s "$TMP/afectados" ]; then
	printf 'check-prep-gate-impact: CLEAN — %s touches none of the %d paths watched by %d prep gates.\n' \
		"$REF" "$RUTAS_TOTAL" "$NGATES"
	exit 0
fi

# ── EJECUTAR, EN UN ESPEJO, NUNCA EN EL ÁRBOL DE QUIEN LLAMA ──────────────────────────────────
# El espejo se crea en `origin/main` y DESPRENDIDO a propósito: crear una rama en un clon que
# comparten cinco carriles le tumbó a otro un push de 45 minutos por la fila `host repo
# untouched`, y esta herramienta no puede pagar ese peaje cada vez que alguien la corre.
git worktree add --detach "$ESPEJO" "$BASE_REF" >/dev/null 2>&1 ||
	no_puedo "cannot create the scratch worktree at $ESPEJO."

TRIPS=0; TOCA=0; ROTOS=0
GATES_VISTOS="$(cut -f1 "$TMP/afectados" | sort -u)"
while IFS= read -r g; do
	[ -n "$g" ] || continue
	nombre="$(basename "$g" .sh)"
	rutas="$(awk -F'\t' -v G="$g" '$1==G{print $2}' "$TMP/afectados")"

	( cd "$ESPEJO" && bash "$g" ) >/dev/null 2>&1; base_rc=$?
	if [ "$base_rc" -ne 0 ]; then
		printf 'stale %-46s already rc=%s on %s — its own problem, not %s\n' "$nombre" "$base_rc" "$BASE_REF" "$REF"
		ROTOS=$((ROTOS + 1))
		continue
	fi

	fallo=0
	while IFS= read -r ruta; do
		[ -n "$ruta" ] || continue
		mkdir -p "$ESPEJO/$(dirname "$ruta")" 2>/dev/null
		if git show "$REF:$ruta" > "$ESPEJO/$ruta" 2>/dev/null; then :; else fallo=1; fi
	done <<-EOF
		$rutas
	EOF
	[ "$fallo" -eq 0 ] || no_puedo "cannot read a blob of $REF for $nombre."

	( cd "$ESPEJO" && bash "$g" ) >"$TMP/out" 2>&1; mio_rc=$?

	# ⛔ RESTAURAR NO ES `git checkout -- .`, Y ESTE DEFECTO CASI PUBLICA UN HALLAZGO FALSO.
	# Un blob de la rama puede CREAR un fichero que `main` no tiene; `checkout` devuelve los
	# rastreados y deja el nuevo ahí. Con el espejo contaminado, la LINEA BASE del guardian
	# siguiente sale roja y este guion lo declara «ya rojo en main» — es decir, culpa al arbol
	# de la suciedad que dejo el mismo. Medido: dos guardianes salieron «stale» y corridos en
	# un espejo limpio daban CLEAN.
	( cd "$ESPEJO" && git checkout -q -- . && git clean -fdq ) 2>/dev/null
	sucio="$( cd "$ESPEJO" && git status --porcelain 2>/dev/null | grep -c . || true )"
	[ "${sucio:-1}" -eq 0 ] ||
		no_puedo "the mirror is still dirty after restoring $nombre ($sucio entries): every later baseline would be measured against contamination."

	if [ "$mio_rc" -ne 0 ]; then
		printf 'TRIP  %-46s rc=%s  %s\n' "$nombre" "$mio_rc" "$(printf '%s' "$rutas" | tr '\n' ' ')"
		sed -n '1,3p' "$TMP/out" | sed 's/^/        /'
		TRIPS=$((TRIPS + 1))
	else
		printf 'touch %-46s green with your blobs in place\n' "$nombre"
		TOCA=$((TOCA + 1))
	fi
done <<EOF
$GATES_VISTOS
EOF

printf '\ncheck-prep-gate-impact: %s vs %s — %d would TRIP, %d touch-only, %d already red on %s\n' \
	"$REF" "$BASE_REF" "$TRIPS" "$TOCA" "$ROTOS" "$BASE_REF"
[ "$TRIPS" -eq 0 ] || exit 1
exit 0
