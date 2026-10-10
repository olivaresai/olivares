#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Check freshly built output, or keep generated assets out of source pushes.
set -uo pipefail
LC_ALL=C
export LC_ALL

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "check-web-bundle-freshness: ⛔ COULD NOT CHECK: cannot load $_olivares_git_env" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-web-bundle-freshness: ⛔ COULD NOT CHECK: missing $RAIZ" >&2
	exit 2
}
DIST="${OLIVARES_WEBUI_DIST:-core/internal/webui/dist}"

# --built checks generated output, never the committed bundle. The builder seals
# only after a successful clean build whose inputs stayed unchanged.
if [ "${1:-}" = "--built" ]; then
    stamp=core/internal/webui/bundle-source.stamp
    actual=$(bash "$RAIZ/scripts/web-bundle-source-digest.sh") || exit 2
    if [ ! -s "$stamp" ] || [ "$(cat "$stamp")" != "$actual" ]; then
        echo "check-web-bundle-freshness: generated bundle source stamp is missing or stale" >&2
        exit 1
    fi
    if [ ! -s "$DIST/index.html" ] || [ ! -d "$DIST/assets" ]; then
        echo "check-web-bundle-freshness: generated index or assets are missing" >&2
        exit 1
    fi
    assets=$(find "$DIST/assets" -type f -size +0c -print) || exit 2
    if [ -z "$assets" ]; then
        echo "check-web-bundle-freshness: generated assets are empty" >&2
        exit 1
    fi
    echo "check-web-bundle-freshness: fresh generated console ($actual)"
    exit 0
fi
if [ "$#" -ne 0 ]; then
    echo "usage: check-web-bundle-freshness.sh [--built]" >&2
    exit 2
fi

# --- el rango ---------------------------------------------------------------------------------
# En un push, del protocolo. Fuera de un push, contra `origin/main`. Nunca se adivina: sin rango no
# hay veredicto.
rangos=()
if [ -n "${OLIVARES_PUSH_REFS_FILE:-}" ]; then
	[ -r "${OLIVARES_PUSH_REFS_FILE}" ] || {
		echo "check-web-bundle-freshness: ⛔ COULD NOT CHECK: OLIVARES_PUSH_REFS_FILE points to" >&2
		echo "  '${OLIVARES_PUSH_REFS_FILE}', which cannot be read." >&2
		exit 2
	}
	while read -r _lref lsha _rref rsha; do
		[ -n "${lsha:-}" ] || continue
		case "$lsha" in *[!0-9a-f]*) continue ;; esac
		# Borrado: nada que empaquetar.
		case "$lsha" in 0000000000000000000000000000000000000000 | 0000000000000000000000000000000000000000000000000000000000000000) continue ;; esac
		case "${rsha:-}" in
		0000000000000000000000000000000000000000 | 0000000000000000000000000000000000000000000000000000000000000000 | "")
			base="$(git merge-base "$lsha" origin/main 2>/dev/null)" || base=""
			[ -n "$base" ] && rangos+=("${base}..${lsha}")
			;;
		*)
			if git cat-file -e "${rsha}^{commit}" 2>/dev/null; then
				rangos+=("${rsha}..${lsha}")
			else
				base="$(git merge-base "$lsha" origin/main 2>/dev/null)" || base=""
				[ -n "$base" ] && rangos+=("${base}..${lsha}")
			fi
			;;
		esac
	done <"${OLIVARES_PUSH_REFS_FILE}"
else
	base="$(git merge-base HEAD origin/main 2>/dev/null)" || base=""
	[ -n "$base" ] || {
		echo "check-web-bundle-freshness: ⛔ COULD NOT CHECK: no merge-base with origin/main." >&2
		echo "  A verdict requires a comparison range; an unchecked bundle cannot be reported as current." >&2
		exit 2
	}
	rangos+=("${base}..HEAD")
fi

if [ "${#rangos[@]}" -eq 0 ]; then
	echo "check-web-bundle-freshness: OK — nothing to bundle in this push (deletions or no commits)."
	exit 0
fi

for r in "${rangos[@]}"; do
    # Deletions migrate old generated files out of Git. Source-only changes are
    # validated by the clean CI build; generated additions/edits belong nowhere
    # in a source PR. Check every pushed range, not just its last tip.
    changed=$(git diff --name-only --no-renames --diff-filter=ACMT "$r" -- \
        "$DIST" core/internal/webui/bundle-source.stamp) || exit 2
    generated=$(printf '%s\n' "$changed" | grep -v -x "$DIST/PLACEHOLDER" | grep . || true)
    if [ -n "$generated" ]; then
        echo "check-web-bundle-freshness: do not commit generated console output:" >&2
        printf '%s\n' "$generated" >&2
        exit 1
    fi
done
echo "check-web-bundle-freshness: source push OK; CI builds and verifies the console"
