#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Every `token:` field in the GoReleaser config must use the LITERAL `{{ .Env.VAR }}` form.
#
# WHY, and it cost a whole release. On 2026-09-01 the public release for v26.8.0 died at the very
# end, in `homebrew cask`, with every piece of work already done: the four docker images built, the
# binaries, the .deb/.rpm/.apk, the SBOMs, and the checksums SIGNED and uploaded. The cause was the
# SHAPE of one template:
#
#     token: '{{ index .Env "HOMEBREW_TAP_GITHUB_TOKEN" }}'    rejected
#     token: '{{ .Env.HOMEBREW_TAP_GITHUB_TOKEN }}'            accepted
#
# The two are identical to Go's template engine. GoReleaser's validator for token fields is not:
# it demands the literal `.Env.NAME` spelling.
#
# AND WHY NEITHER THE TOOL NOR ANY REHEARSAL CATCHES IT -- the part that makes a gate of our own
# necessary rather than convenient:
#
#     goreleaser check, config CURED ... rc 0
#     goreleaser check, config BROKEN .. rc 0      <- measured in both directions, 2026-09-01
#
# That validation runs ONLY in the publish phase. `--snapshot` skips it, so neither the source-tree witness
# nor the mirror witness exercises it, and no earlier release attempt had reached that far. A defect
# only a real publication can see is a defect that reaches production by definition.
#
# THE NEGATIVE CONTROL IS PART OF THE POINT. `index .Env` is legitimate everywhere else -- the
# `skip_upload` field of the same brews entry uses it in a conditional and MUST stay green. A gate
# that flagged every `index .Env` would be a nuisance someone disables, and it would send people to
# "fix" correct code. The subject is the FIELD, not the function.
#
# Three answers: 0 CLEAN . 1 a token field uses a form the validator rejects . 2 could not look.
set -uo pipefail

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "check-token-template-form: 2 - cannot load $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "check-token-template-form: 2 - not a git work tree" >&2; exit 2; }
cd "$ROOT" || exit 2

CFG="${OLIVARES_GORELEASER_CONFIG:-.goreleaser.yaml}"
[ -f "$CFG" ] || { echo "check-token-template-form: 2 - no $CFG" >&2; exit 2; }

# Corpus DERIVED from the file, never a typed list of line numbers: a new `token:` field is in
# scope the day it is added, which is exactly how the reported one got in.
campos="$(grep -nE '^[[:space:]]*token:[[:space:]]' "$CFG" || true)"
if [ -z "$campos" ]; then
	echo "check-token-template-form: 2 - $CFG declares no token: field at all." >&2
	echo "  That is not a clean tree: this gate would then be asserting nothing. If the config" >&2
	echo "  genuinely has no token, say so on purpose rather than letting silence pass." >&2
	exit 2
fi

rc=0
n=0
while IFS= read -r linea; do
	[ -n "$linea" ] || continue
	n=$((n + 1))
	num="${linea%%:*}"
	valor="${linea#*:}"
	# Only templated values are in scope: a literal token (or an empty one) is not this defect.
	case "$valor" in
	*'{{'*) ;;
	*) continue ;;
	esac
	# SIN TUBERIA, y no es estilo: `printf | grep -q` devuelve 141 EN EXITO bajo pipefail
	# -- grep cierra al primer acierto y printf muere de SIGPIPE, asi que el booleano se
	# invierte justo cuando la forma ES la correcta. Lo cazo `lint:sigpipe-booleans` en el
	# primer porton que este fichero atraveso, y es la misma trampa que publica-buzon.sh
	# documenta en su cabecera. Una here-string no es una tuberia.
	if grep -qE '\{\{[[:space:]]*\.Env\.[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\}\}' <<<"$valor"; then
		continue
	fi
	rc=1
	echo "check-token-template-form: REJECTED FORM - $CFG:$num"
	echo "    $(printf '%s' "$valor" | sed 's/^[[:space:]]*//')"
	echo "  GoReleaser's validator for token fields demands the literal {{ .Env.NAME }}. Any other"
	echo "  spelling -- {{ index .Env \"NAME\" }} included, which Go's engine treats as identical --"
	echo "  is refused, and ONLY in the publish phase: no snapshot and no rehearsal will tell you."
	echo "  repair: token: '{{ .Env.NAME }}'"
done <<CAMPOS
$campos
CAMPOS

if [ "$rc" = 0 ]; then
	echo "check-token-template-form: CLEAN - $n token: field(s), all in the literal .Env form."
fi
exit "$rc"
