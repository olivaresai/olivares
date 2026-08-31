#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Three-state executable oracle for the L4 access-map exfiltration contract.
#   0: the focused assertions ran and passed (FUNCIONA)
#   1: a labelled contract assertion ran and failed (ROTO)
#   2: the assertion could not be run or its outcome could not be identified (NO PUDE MIRAR)
set -u -o pipefail

NAME='console-func-l4-exfil'
QUERY_MSG='EXFIL_QUERY_CONTRACT: /attack-paths/exfil requires resource_id=resource-7 and must not send agent_id'
SUBJECT_MSG='EXFIL_SUBJECT_CONTRACT: agent selection must not invoke resource-scoped exfil'
RESOURCE_IDLE_MSG='EXFIL_RESOURCE_IDLE_CONTRACT: resource selection must not auto-run audited exfil'
AUDIT_ONCE_MSG='EXFIL_AUDIT_CONTRACT: one deliberate analysis click must produce exactly one audited GET'
AUDIT_REOPEN_MSG='EXFIL_AUDIT_CONTRACT: every deliberate reopen and click must produce one new audited GET'
SHAPE_MSG='EXFIL_SHAPE_CONTRACT: a successful response without a paths array must render unreadable, never empty'
NULL_SHAPE_MSG='EXFIL_NULL_SHAPE_CONTRACT: a successful null response must render unreadable, never empty or crash'
PATH_SHAPE_MSG='EXFIL_PATH_SHAPE_CONTRACT: malformed path entries must render unreadable, never empty or crash'
EMPTY_STEPS_MSG='EXFIL_EMPTY_STEPS_CONTRACT: a path without a chain must render unreadable'
KIND_MSG='EXFIL_KIND_CONTRACT: response paths must match the requested analysis kind'
CLUSTER_MAP_MSG='EXFIL_CLUSTER_CONTRACT: synthetic cluster IDs must remain marked and must not reach resource-scoped exfil'
CLUSTER_EXPAND_MSG='EXFIL_CLUSTER_EXPAND_CONTRACT: synthetic cluster selection must not expose expansion'
CLUSTER_PANEL_MSG='EXFIL_CLUSTER_PANEL_CONTRACT: synthetic cluster selection must expose neither expand nor attack-path actions'
AGENT_CLUSTER_MSG='EXFIL_AGENT_CLUSTER_CONTRACT: synthetic agent clusters must expose no attack-path action'
URL_STATE_MSG='URL_STATE_ROUTER_CONTRACT: subscribed searchStr must win before window.location catches up'

cannot() {
	printf '%s: NO PUDE MIRAR — %s\n' "$NAME" "$*" >&2
	exit 2
}

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
. "$_olivares_git_env" || cannot "no puedo cargar $_olivares_git_env (aislamiento git-env)"
unset _olivares_git_env

command -v git >/dev/null 2>&1 || cannot 'git no está disponible'
command -v pnpm >/dev/null 2>&1 || cannot 'pnpm no está disponible'
command -v awk >/dev/null 2>&1 || cannot 'awk no está disponible'
command -v grep >/dev/null 2>&1 || cannot 'grep no está disponible'
command -v mktemp >/dev/null 2>&1 || cannot 'mktemp no está disponible'

ROOT=${OLIVARES_L4_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null)}
[ -n "$ROOT" ] || cannot 'no resuelvo la raíz del repositorio'
WEB=${OLIVARES_L4_WEB_DIR:-$ROOT/web}

API_TEST="$WEB/src/features/access-map/api.test.ts"
PANEL_TEST="$WEB/src/features/access-map/attack-paths.test.tsx"
SELECTION_TEST="$WEB/src/features/access-map/selection.test.ts"
URL_STATE_TEST="$WEB/src/lib/hooks/use-url-state.test.tsx"
for required in "$WEB/package.json" "$API_TEST" "$PANEL_TEST" "$SELECTION_TEST" "$URL_STATE_TEST"; do
	[ -r "$required" ] || cannot "no leo $required"
done

require_assertion_marker() {
	local file=$1
	local message=$2
	local diagnostic=$3
	local count
	count=$(awk -v want="'$message'," '
		{
			line = $0
			sub(/^[[:space:]]+/, "", line)
			sub(/[[:space:]]+$/, "", line)
		}
		line == want { n++ }
		END { print n + 0 }
	' "$file")
	[ "$count" -eq 1 ] || cannot "$diagnostic (marcadores=$count, quiero 1)"
}

require_assertion_marker "$API_TEST" "$QUERY_MSG" 'falta el diagnóstico EXFIL_QUERY_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$SUBJECT_MSG" 'falta el diagnóstico EXFIL_SUBJECT_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$RESOURCE_IDLE_MSG" 'falta el diagnóstico EXFIL_RESOURCE_IDLE_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$AUDIT_ONCE_MSG" 'falta el diagnóstico EXFIL_AUDIT_CONTRACT one-click'
require_assertion_marker "$PANEL_TEST" "$AUDIT_REOPEN_MSG" 'falta el diagnóstico EXFIL_AUDIT_CONTRACT reopen'
require_assertion_marker "$PANEL_TEST" "$SHAPE_MSG" 'falta el diagnóstico EXFIL_SHAPE_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$NULL_SHAPE_MSG" 'falta el diagnóstico EXFIL_NULL_SHAPE_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$PATH_SHAPE_MSG" 'falta el diagnóstico EXFIL_PATH_SHAPE_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$EMPTY_STEPS_MSG" 'falta el diagnóstico EXFIL_EMPTY_STEPS_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$KIND_MSG" 'falta el diagnóstico EXFIL_KIND_CONTRACT'
require_assertion_marker "$SELECTION_TEST" "$CLUSTER_MAP_MSG" 'falta el diagnóstico EXFIL_CLUSTER_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$CLUSTER_EXPAND_MSG" 'falta el diagnóstico EXFIL_CLUSTER_EXPAND_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$CLUSTER_PANEL_MSG" 'falta el diagnóstico EXFIL_CLUSTER_PANEL_CONTRACT'
require_assertion_marker "$PANEL_TEST" "$AGENT_CLUSTER_MSG" 'falta el diagnóstico EXFIL_AGENT_CLUSTER_CONTRACT'
require_assertion_marker "$URL_STATE_TEST" "$URL_STATE_MSG" 'falta el diagnóstico URL_STATE_ROUTER_CONTRACT'

TMP=$(mktemp -d "${TMPDIR:-/tmp}/console-func-l4.XXXXXX") || cannot 'no creo el temporal'
[ -d "$TMP" ] || cannot 'mktemp no devolvió un directorio'
trap 'rm -rf -- "$TMP"' EXIT

(
	unset FORCE_COLOR
	export NO_COLOR=1
	pnpm -C "$WEB" exec vitest run \
		src/features/access-map/api.test.ts \
		src/features/access-map/attack-paths.test.tsx \
		src/features/access-map/selection.test.ts \
		src/lib/hooks/use-url-state.test.tsx
) >"$TMP/vitest.log" 2>&1
rc=$?
cat "$TMP/vitest.log"

if [ "$rc" -eq 0 ]; then
	grep -Eq '^[[:space:]]*Test Files[[:space:]]+4 passed \(4\)[[:space:]]*$' "$TMP/vitest.log" ||
		cannot 'vitest rc0 sin las cuatro suites focales'
	grep -Eq '^[[:space:]]*Tests[[:space:]]+38 passed \(38\)[[:space:]]*$' "$TMP/vitest.log" ||
		cannot 'vitest rc0 sin las 38 celdas focales'
	printf '%s: FUNCIONA — exfil y estado URL verificados en cuatro suites\n' "$NAME"
	exit 0
fi

for message in \
	"$QUERY_MSG" \
	"$SUBJECT_MSG" \
	"$RESOURCE_IDLE_MSG" \
	"$AUDIT_ONCE_MSG" \
	"$AUDIT_REOPEN_MSG" \
	"$SHAPE_MSG" \
	"$NULL_SHAPE_MSG" \
	"$PATH_SHAPE_MSG" \
	"$EMPTY_STEPS_MSG" \
	"$KIND_MSG" \
	"$CLUSTER_MAP_MSG" \
	"$CLUSTER_EXPAND_MSG" \
	"$CLUSTER_PANEL_MSG" \
	"$AGENT_CLUSTER_MSG" \
	"$URL_STATE_MSG"; do
	if grep -Fq "AssertionError: $message" "$TMP/vitest.log"; then
		printf '%s: ROTO — %s\n' "$NAME" "$message" >&2
		exit 1
	fi
done

cannot "vitest terminó rc=$rc sin ejecutar una aserción contractual etiquetada"
