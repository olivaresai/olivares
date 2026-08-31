#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Causal mutation battery for check-console-func-l4-exfil.sh. Every mutant changes
# one production contract, must be classified as ROTO with its exact diagnostic,
# and is restored byte-for-byte before the next mutation runs.
set -u -o pipefail

NAME='test-console-func-l4-exfil-mutants'
cannot() {
	printf '%s: NO PUDE MIRAR — %s\n' "$NAME" "$*" >&2
	exit 2
}

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
. "$_olivares_git_env" || cannot "no puedo cargar $_olivares_git_env (aislamiento git-env)"
unset _olivares_git_env

for command_name in git bash perl mktemp cp cmp tail; do
	command -v "$command_name" >/dev/null 2>&1 || cannot "$command_name no está disponible"
done

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || cannot 'no resuelvo la raíz del repositorio'
ORACLE="$ROOT/scripts/check-console-func-l4-exfil.sh"
API="$ROOT/web/src/features/access-map/api.ts"
PANEL="$ROOT/web/src/features/access-map/attack-paths.tsx"
DETAIL="$ROOT/web/src/features/access-map/detail.tsx"
SELECTION="$ROOT/web/src/features/access-map/selection.ts"
URL_STATE="$ROOT/web/src/lib/hooks/use-url-state.ts"

for required in "$ORACLE" "$API" "$PANEL" "$DETAIL" "$SELECTION" "$URL_STATE"; do
	[ -r "$required" ] || {
		printf '%s: NO PUDE MIRAR — no leo %s\n' "$NAME" "$required" >&2
		exit 2
	}
done
TMP=$(mktemp -d "${TMPDIR:-/tmp}/console-func-l4-mutants.XXXXXX") || cannot 'mktemp falló'
[ -d "$TMP" ] || cannot 'mktemp no devolvió un directorio'

restore_all() {
	cp "$TMP/api.ts" "$API" &&
		cp "$TMP/attack-paths.tsx" "$PANEL" &&
		cp "$TMP/detail.tsx" "$DETAIL" &&
		cp "$TMP/selection.ts" "$SELECTION" &&
		cp "$TMP/use-url-state.ts" "$URL_STATE"
}

assert_restored() {
	cmp -s "$TMP/api.ts" "$API" &&
		cmp -s "$TMP/attack-paths.tsx" "$PANEL" &&
		cmp -s "$TMP/detail.tsx" "$DETAIL" &&
		cmp -s "$TMP/selection.ts" "$SELECTION" &&
		cmp -s "$TMP/use-url-state.ts" "$URL_STATE"
}

cleanup_on_exit() {
	local original_rc=$?
	trap - EXIT HUP INT TERM
	if ! restore_all || ! assert_restored; then
		printf '%s: NO PUDE MIRAR — cleanup no restauró bytes exactos; snapshots preservados en %s\n' \
			"$NAME" "$TMP" >&2
		exit 2
	fi
	if ! rm -rf -- "$TMP"; then
		printf '%s: NO PUDE MIRAR — no retiro el temporal restaurado %s\n' "$NAME" "$TMP" >&2
		exit 2
	fi
	exit "$original_rc"
}
on_signal() {
	exit 2
}
trap cleanup_on_exit EXIT
trap on_signal HUP INT TERM

cp "$API" "$TMP/api.ts" &&
	cp "$PANEL" "$TMP/attack-paths.tsx" &&
	cp "$DETAIL" "$TMP/detail.tsx" &&
	cp "$SELECTION" "$TMP/selection.ts" &&
	cp "$URL_STATE" "$TMP/use-url-state.ts" || cannot 'no creo el snapshot completo antes de mutar'

replace_once() {
	local file=$1 old=$2 new=$3
	OLD=$old NEW=$new perl -0pi -e '
		$count = s/\Q$ENV{OLD}\E/$ENV{NEW}/g;
		END { exit((defined($count) && $count == 1) ? 0 : 3) }
	' "$file"
}

run_oracle() {
	local log=$1 rc
	if TMPDIR="${TMPDIR:-/tmp}" GOTMPDIR="${GOTMPDIR:-${TMPDIR:-/tmp}}" \
		FORCE_COLOR=0 bash "$ORACLE" >"$log" 2>&1; then
		rc=0
	else
		rc=$?
	fi
	return "$rc"
}

run_mutant() {
	local label=$1 file=$2 old=$3 new=$4 message=$5 rc expected actual
	restore_all || cannot "no restauro antes del mutante $label"
	if ! replace_once "$file" "$old" "$new"; then
		printf '%s: NO PUDE MIRAR — el mutante %s no aplicó exactamente una vez\n' "$NAME" "$label" >&2
		exit 2
	fi
	if run_oracle "$TMP/$label.log"; then rc=0; else rc=$?; fi
	expected="console-func-l4-exfil: ROTO — $message"
	actual=$(tail -n 1 "$TMP/$label.log")
	if [ "$rc" -ne 1 ] || [ "$actual" != "$expected" ]; then
		printf '%s: NO PUDE MIRAR — mutante %s: rc=%s, última línea inesperada\n' "$NAME" "$label" "$rc" >&2
		cat "$TMP/$label.log" >&2
		exit 2
	fi
	restore_all || cannot "no restauro después del mutante $label"
	if ! assert_restored; then
		printf '%s: NO PUDE MIRAR — mutante %s no restauró bytes exactos\n' "$NAME" "$label" >&2
		exit 2
	fi
	printf '%s: MUERDE %s — %s\n' "$NAME" "$label" "$message"
}

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

run_mutant query "$API" \
	'query: { resource_id: resourceId }' \
	'query: { agent_id: resourceId }' \
	"$QUERY_MSG"
run_mutant subject "$PANEL" \
	'              <Analysis subjectId={subject.id} kind="escalation" />' \
	$'              <Analysis subjectId={subject.id} kind="escalation" />\n              <Analysis subjectId={subject.id} kind="exfil" />' \
	"$SUBJECT_MSG"
run_mutant resource-idle "$PANEL" \
	'      {asked ? (' \
	"      {asked || subject.kind === 'resource' ? (" \
	"$RESOURCE_IDLE_MSG"
run_mutant audit-once "$PANEL" \
	'    retry: false,' \
	'    retry: 1,' \
	"$AUDIT_ONCE_MSG"
run_mutant audit-reopen "$PANEL" \
	$'    gcTime: 0,\n    refetchOnMount: '\''always'\'',' \
	$'    gcTime: Infinity,\n    refetchOnMount: false,' \
	"$AUDIT_REOPEN_MSG"
run_mutant shape "$PANEL" \
	'  if (!Array.isArray(value.paths)) return null' \
	'  if (!Array.isArray(value.paths)) return []' \
	"$SHAPE_MSG"
run_mutant null-shape "$PANEL" \
	'  if (value === null) return null' \
	'  if (value === null) return []' \
	"$NULL_SHAPE_MSG"
run_mutant path-shape "$PANEL" \
	'  if (!value.paths.every(isAttackPath)) return null' \
	'  if (!value.paths.every(isAttackPath)) return []' \
	"$PATH_SHAPE_MSG"
run_mutant empty-steps "$PANEL" \
	'    value.steps.length > 0 &&' \
	'    value.steps.length >= 0 &&' \
	"$EMPTY_STEPS_MSG"
run_mutant kind-shape "$PANEL" \
	'  if (!value.paths.every((path) => path.kind === expectedKind)) return null' \
	'  if (!value.paths.every((path) => path.kind === expectedKind)) return value.paths' \
	"$KIND_MSG"
run_mutant cluster-map "$SELECTION" \
	'    cluster: node.cluster === true,' \
	'    cluster: false,' \
	"$CLUSTER_MAP_MSG"
run_mutant cluster-expand "$DETAIL" \
	'            {onExpand && !selection.cluster && (' \
	'            {onExpand && (' \
	"$CLUSTER_EXPAND_MSG"
run_mutant cluster-panel "$DETAIL" \
	$'            {!selection.cluster &&\n              ((selection.role === '\''origin'\'' && selection.kind === '\''agent'\'') ||' \
	$'            {true &&\n              ((selection.role === '\''origin'\'' && selection.kind === '\''agent'\'') ||' \
	"$CLUSTER_PANEL_MSG"
run_mutant agent-cluster "$DETAIL" \
	$'            {!selection.cluster &&\n              ((selection.role === \'origin\' && selection.kind === \'agent\') ||' \
	$'            {(!selection.cluster || selection.kind === \'agent\') &&\n              ((selection.role === \'origin\' && selection.kind === \'agent\') ||' \
	"$AGENT_CLUSTER_MSG"
run_mutant url-state "$URL_STATE" \
	'    const next = readSearch(keysRef.current, searchStr)' \
	'    const next = readSearch(keysRef.current)' \
	"$URL_STATE_MSG"

restore_all || cannot 'no restauro antes del control final'
if ! run_oracle "$TMP/final-green.log"; then
	printf '%s: NO PUDE MIRAR — el control final limpio no quedó verde\n' "$NAME" >&2
	cat "$TMP/final-green.log" >&2
	exit 2
fi
expected_green='console-func-l4-exfil: FUNCIONA — exfil y estado URL verificados en cuatro suites'
[ "$(tail -n 1 "$TMP/final-green.log")" = "$expected_green" ] || {
	printf '%s: NO PUDE MIRAR — mensaje final verde inesperado\n' "$NAME" >&2
	exit 2
}

MISSING="$TMP/no-web"
if OLIVARES_L4_WEB_DIR="$MISSING" bash "$ORACLE" >"$TMP/cannot.log" 2>&1; then
	rc=0
else
	rc=$?
fi
expected_cannot="console-func-l4-exfil: NO PUDE MIRAR — no leo $MISSING/package.json"
if [ "$rc" -ne 2 ] || [ "$(tail -n 1 "$TMP/cannot.log")" != "$expected_cannot" ]; then
	printf '%s: NO PUDE MIRAR — el control rc2 no conservó código y mensaje exactos\n' "$NAME" >&2
	cat "$TMP/cannot.log" >&2
	exit 2
fi

assert_restored || {
	printf '%s: NO PUDE MIRAR — la batería terminó con bytes distintos\n' "$NAME" >&2
	exit 2
}
printf '%s: FUNCIONA — 15/15 mutantes mordieron, rc0/rc1/rc2 y restauración byte-exacta\n' "$NAME"
