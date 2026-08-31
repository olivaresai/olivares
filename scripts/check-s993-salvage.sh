#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Three-state executable oracle for the four portable salvage contracts.
#   0: every focused assertion ran and passed (FUNCIONA)
#   1: one labelled contract assertion ran and failed (ROTO)
#   2: the observation was unavailable or could not be classified (NO PUDE MIRAR)
set -u -o pipefail

NAME='s993-salvage'
CONFIRM_MSG='CONFIRM_DIALOG_DISABLED_CONTRACT: caller-disabled confirm must not fire'
OWNERSHIP_CHANGED_MSG='NHI_OWNERSHIP_CHANGED_ONLY_CONTRACT'
AGENT_SPONSOR_MSG='NHI_AGENT_SPONSOR_CONTRACT'
OWNERSHIP_NOOP_MSG='NHI_OWNERSHIP_NOOP_CONTRACT'
OWNERSHIP_CLEAR_MSG='NHI_OWNERSHIP_CLEAR_CONTRACT'
OWNERSHIP_TRIM_MSG='NHI_OWNERSHIP_TRIM_CONTRACT: surrounding whitespace must neither create a change nor bypass clear refusal'
ROSTER_MSG='NHI_ROSTER_FILTER_CURSOR_CONTRACT: principal filter must not assert an empty roster while matching identities remain on unloaded pages'
ROSTER_MATCH_STOP_MSG='NHI_ROSTER_MATCH_STOP_CONTRACT: principal filter must stop auto-follow after a loaded page contains a match'
SEARCH_AUDIT_MSG='RECORDINGS_SEARCH_AUDIT_CONTRACT: typing must not issue an audited read before submit'
SEARCH_SUBJECT_MSG='RECORDINGS_SEARCH_SUBJECT_CONTRACT: submit must use subject_contains'
SEARCH_GRANT_MSG='RECORDINGS_SEARCH_GRANT_CONTRACT: grant search must use the exact grant predicate'
SEARCH_URL_MSG='RECORDINGS_SEARCH_URL_CONTRACT: URL search must hydrate and query the same predicate'
SEARCH_CLEAR_MSG='RECORDINGS_SEARCH_CLEAR_CONTRACT: clear must remove both search predicates'
SEARCH_RESPONSES_MSG='RECORDINGS_SEARCH_RESPONSES_CONTRACT: populated, empty, and error/retry responses must remain distinct'
LIKE_LITERAL_MSG='RECORDINGS_LIKE_LITERAL_CONTRACT: subject_contains must escape %, _ and \\ before adding contains wildcards'
SQL_ESCAPE_MSG='SQL_LIKE_ESCAPE_CONTRACT: OpLike must emit an explicit ESCAPE clause and preserve the bound pattern'

cannot() {
	printf '%s: NO PUDE MIRAR — %s\n' "$NAME" "$*" >&2
	exit 2
}

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd) ||
	cannot 'no resuelvo el directorio del oráculo'
. "$HERE/lib/git-env.sh" || cannot 'no cargo el aislamiento git-env'
. "$HERE/lib/exec-workdir.sh" || cannot 'no cargo el selector de scratch ejecutable'

for command_name in git pnpm go grep mktemp timeout; do
	command -v "$command_name" >/dev/null 2>&1 || cannot "$command_name no está disponible"
done

ROOT=${OLIVARES_S993_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null)}
[ -n "$ROOT" ] || cannot 'no resuelvo la raíz del repositorio'
WEB=${OLIVARES_S993_WEB_DIR:-$ROOT/web}

CONFIRM_TEST="$WEB/src/components/ui/confirm-dialog.test.tsx"
OWNERSHIP_TEST="$WEB/src/features/identity/nhi-lifecycle.test.tsx"
ROSTER_TEST="$WEB/src/features/identity/identity.test.tsx"
SEARCH_TEST="$WEB/src/features/recordings/recordings.test.tsx"
LIKE_UNIT_TEST="$ROOT/modules/recording/handlers_internal_test.go"
LIKE_HANDLER_TEST="$ROOT/modules/recording/recording_test.go"
SQL_TEST="$ROOT/core/internal/store/sqlstore/generic_filter_test.go"

for required in \
	"$WEB/package.json" \
	"$CONFIRM_TEST" \
	"$OWNERSHIP_TEST" \
	"$ROSTER_TEST" \
	"$SEARCH_TEST" \
	"$LIKE_UNIT_TEST" \
	"$LIKE_HANDLER_TEST" \
	"$SQL_TEST"; do
	[ -r "$required" ] || cannot "no leo $required"
done

require_marker_count() {
	local file=$1 message=$2 expected=$3 count
	count=$(grep -F -c -- "$message" "$file" || true)
	[ "$count" -eq "$expected" ] ||
		cannot "cardinalidad del diagnóstico $message en $file: $count, quiero $expected"
}

require_marker_count "$CONFIRM_TEST" "$CONFIRM_MSG" 1
require_marker_count "$OWNERSHIP_TEST" "$OWNERSHIP_CHANGED_MSG" 1
require_marker_count "$OWNERSHIP_TEST" "$AGENT_SPONSOR_MSG" 2
require_marker_count "$OWNERSHIP_TEST" "$OWNERSHIP_NOOP_MSG" 1
require_marker_count "$OWNERSHIP_TEST" "$OWNERSHIP_CLEAR_MSG" 1
require_marker_count "$OWNERSHIP_TEST" "$OWNERSHIP_TRIM_MSG" 1
require_marker_count "$ROSTER_TEST" "$ROSTER_MSG" 1
require_marker_count "$ROSTER_TEST" "$ROSTER_MATCH_STOP_MSG" 1
require_marker_count "$SEARCH_TEST" "$SEARCH_AUDIT_MSG" 1
require_marker_count "$SEARCH_TEST" "$SEARCH_SUBJECT_MSG" 1
require_marker_count "$SEARCH_TEST" "$SEARCH_GRANT_MSG" 1
require_marker_count "$SEARCH_TEST" "$SEARCH_URL_MSG" 1
require_marker_count "$SEARCH_TEST" "$SEARCH_CLEAR_MSG" 1
require_marker_count "$SEARCH_TEST" "$SEARCH_RESPONSES_MSG" 1
require_marker_count "$LIKE_UNIT_TEST" "$LIKE_LITERAL_MSG" 1
require_marker_count "$LIKE_HANDLER_TEST" "$LIKE_LITERAL_MSG" 1
require_marker_count "$SQL_TEST" "$SQL_ESCAPE_MSG" 1

TMP=$(olivares_pick_exec_workdir s993-salvage) || cannot 'no encuentro scratch ejecutable'
[ -d "$TMP" ] || cannot 'el selector de scratch no devolvió un directorio'
touch "$TMP/.s993-salvage-scratch" || cannot 'no marco el scratch para limpieza segura'
cleanup() {
	local original_rc=$?
	trap - EXIT HUP INT TERM
	if [ ! -f "$TMP/.s993-salvage-scratch" ]; then
		printf '%s: NO PUDE MIRAR — scratch sin marcador de propiedad: %s\n' \
			"$NAME" "$TMP" >&2
		exit 2
	fi
	if ! rm -rf -- "$TMP"; then
		printf '%s: NO PUDE MIRAR — no retiro el scratch validado %s\n' \
			"$NAME" "$TMP" >&2
		exit 2
	fi
	exit "$original_rc"
}
trap cleanup EXIT HUP INT TERM
mkdir -p "$TMP/tmp" "$TMP/go-tmp" || cannot 'no preparo temporales de ejecución'

TIMEOUT_SECONDS=${OLIVARES_S993_TIMEOUT_SECONDS:-300}
case "$TIMEOUT_SECONDS" in
	''|*[!0-9]*) cannot 'OLIVARES_S993_TIMEOUT_SECONDS no es un entero positivo' ;;
	0) cannot 'OLIVARES_S993_TIMEOUT_SECONDS debe ser mayor que cero' ;;
esac

(
	unset FORCE_COLOR
	export NO_COLOR=1
	timeout --foreground "$TIMEOUT_SECONDS" pnpm -C "$WEB" exec vitest run \
		src/components/ui/confirm-dialog.test.tsx \
		src/features/identity/nhi-lifecycle.test.tsx \
		src/features/identity/identity.test.tsx \
		src/features/recordings/recordings.test.tsx
) >"$TMP/web.log" 2>&1
web_rc=$?

(
	cd "$ROOT" || exit 125
	TMPDIR="$TMP/tmp" GOTMPDIR="$TMP/go-tmp" timeout --foreground \
		"$TIMEOUT_SECONDS" go test -v ./modules/recording -count=1 \
		-run '^(TestListSessions_FilterBySubjectContains|TestLiteralContainsPattern)$'
) >"$TMP/recording.log" 2>&1
recording_rc=$?

(
	cd "$ROOT" || exit 125
	TMPDIR="$TMP/tmp" GOTMPDIR="$TMP/go-tmp" timeout --foreground \
		"$TIMEOUT_SECONDS" go test -v ./core/internal/store/sqlstore -count=1 \
		-run '^TestFilterFragmentLikeUsesExplicitEscape$'
) >"$TMP/sql.log" 2>&1
sql_rc=$?

cat "$TMP/web.log" "$TMP/recording.log" "$TMP/sql.log"

if [ "$web_rc" -eq 0 ] && [ "$recording_rc" -eq 0 ] && [ "$sql_rc" -eq 0 ]; then
	grep -Eq '^[[:space:]]*Test Files[[:space:]]+4 passed \(4\)[[:space:]]*$' \
		"$TMP/web.log" || cannot 'vitest rc0 sin las cuatro suites focales'
	grep -Eq '^[[:space:]]*Tests[[:space:]]+79 passed \(79\)[[:space:]]*$' \
		"$TMP/web.log" || cannot 'vitest rc0 sin las 79 celdas focales'
	grep -Fq -- '--- PASS: TestLiteralContainsPattern' "$TMP/recording.log" ||
		cannot 'go recording rc0 sin TestLiteralContainsPattern'
	grep -Fq -- '--- PASS: TestListSessions_FilterBySubjectContains' \
		"$TMP/recording.log" ||
		cannot 'go recording rc0 sin TestListSessions_FilterBySubjectContains'
	grep -Fq -- '--- PASS: TestFilterFragmentLikeUsesExplicitEscape' "$TMP/sql.log" ||
		cannot 'go sqlstore rc0 sin TestFilterFragmentLikeUsesExplicitEscape'
	printf '%s: FUNCIONA — 4 web suites/79 tests and 3 Go contracts\n' "$NAME"
	exit 0
fi

if [ "$web_rc" -eq 1 ] && [ "$recording_rc" -eq 0 ] && [ "$sql_rc" -eq 0 ]; then
	for message in \
		"$CONFIRM_MSG" \
		"$OWNERSHIP_CHANGED_MSG" \
		"$AGENT_SPONSOR_MSG" \
		"$OWNERSHIP_NOOP_MSG" \
		"$OWNERSHIP_CLEAR_MSG" \
		"$OWNERSHIP_TRIM_MSG" \
		"$ROSTER_MSG" \
		"$ROSTER_MATCH_STOP_MSG" \
		"$SEARCH_AUDIT_MSG" \
		"$SEARCH_SUBJECT_MSG" \
		"$SEARCH_GRANT_MSG" \
		"$SEARCH_URL_MSG" \
		"$SEARCH_CLEAR_MSG" \
		"$SEARCH_RESPONSES_MSG"; do
		if grep -Fq -- "Error: $message" "$TMP/web.log" ||
			grep -Fq -- "AssertionError: $message" "$TMP/web.log"; then
			printf '%s: ROTO — %s\n' "$NAME" "$message" >&2
			exit 1
		fi
	done
fi

if [ "$web_rc" -eq 0 ] && [ "$recording_rc" -eq 1 ] && [ "$sql_rc" -eq 0 ] &&
	grep -Fq -- "$LIKE_LITERAL_MSG" "$TMP/recording.log"; then
	printf '%s: ROTO — %s\n' "$NAME" "$LIKE_LITERAL_MSG" >&2
	exit 1
fi

if [ "$web_rc" -eq 0 ] && [ "$recording_rc" -eq 0 ] && [ "$sql_rc" -eq 1 ] &&
	grep -Fq -- "$SQL_ESCAPE_MSG" "$TMP/sql.log"; then
	printf '%s: ROTO — %s\n' "$NAME" "$SQL_ESCAPE_MSG" >&2
	exit 1
fi

cannot "resultados no clasificables: web=$web_rc recording=$recording_rc sql=$sql_rc"
