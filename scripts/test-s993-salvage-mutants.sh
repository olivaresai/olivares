#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Causal mutation battery for check-s993-salvage.sh. Each mutant changes one
# production contract, must be ROTO with its exact witness, and is restored
# byte-for-byte before the next mutant runs.
set -u -o pipefail

NAME='test-s993-salvage-mutants'
cannot() {
	printf '%s: CANNOT INSPECT — %s\n' "$NAME" "$*" >&2
	exit 2
}

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd) ||
	cannot 'cannot resolve the test directory'
. "$HERE/lib/git-env.sh" || cannot 'cannot load git-env isolation'
. "$HERE/lib/exec-workdir.sh" || cannot 'cannot load the executable scratch selector'

for command_name in git bash perl cp cmp tail mkdir rmdir; do
	command -v "$command_name" >/dev/null 2>&1 || cannot "$command_name is unavailable"
done

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || cannot 'cannot resolve the repository root'
ORACLE="$ROOT/scripts/check-s993-salvage.sh"
CONFIRM="$ROOT/web/src/components/ui/confirm-dialog.tsx"
OWNERSHIP="$ROOT/web/src/features/identity/nhi-actions.tsx"
ROSTER="$ROOT/web/src/features/identity/nhi-roster.tsx"
SEARCH="$ROOT/web/src/features/recordings/recordings-view.tsx"
LIKE="$ROOT/modules/recording/handlers.go"
SQL="$ROOT/core/internal/store/sqlstore/generic.go"

for required in "$ORACLE" "$CONFIRM" "$OWNERSHIP" "$ROSTER" "$SEARCH" "$LIKE" "$SQL"; do
	[ -r "$required" ] || cannot "cannot read $required"
done

TMP=$(olivares_pick_exec_workdir s993-salvage-mutants) ||
	cannot 'cannot find executable scratch space'
[ -d "$TMP" ] || cannot 'scratch selector did not return a directory'
touch "$TMP/.s993-salvage-mutants-scratch" || cannot 'cannot mark scratch space for safe cleanup'
SNAPSHOTS_READY=0
ACTIVE_FILE=''
ACTIVE_MUTATED=''
LOCK_DIR=''
LOCK_HELD=0

restore_one_known_state() {
	local original=$1 target=$2
	cmp -s "$original" "$target" && return 0
	if [ "$target" = "$ACTIVE_FILE" ] && [ -r "$ACTIVE_MUTATED" ] &&
		cmp -s "$ACTIVE_MUTATED" "$target"; then
		cp "$original" "$target"
		return $?
	fi
	return 1
}

restore_all() {
	local failed=0
	[ "$SNAPSHOTS_READY" -eq 1 ] || return 1
	restore_one_known_state "$TMP/confirm-dialog.tsx" "$CONFIRM" || failed=1
	restore_one_known_state "$TMP/nhi-actions.tsx" "$OWNERSHIP" || failed=1
	restore_one_known_state "$TMP/nhi-roster.tsx" "$ROSTER" || failed=1
	restore_one_known_state "$TMP/recordings-view.tsx" "$SEARCH" || failed=1
	restore_one_known_state "$TMP/handlers.go" "$LIKE" || failed=1
	restore_one_known_state "$TMP/generic.go" "$SQL" || failed=1
	return "$failed"
}

assert_restored() {
	cmp -s "$TMP/confirm-dialog.tsx" "$CONFIRM" &&
		cmp -s "$TMP/nhi-actions.tsx" "$OWNERSHIP" &&
		cmp -s "$TMP/nhi-roster.tsx" "$ROSTER" &&
		cmp -s "$TMP/recordings-view.tsx" "$SEARCH" &&
		cmp -s "$TMP/handlers.go" "$LIKE" &&
		cmp -s "$TMP/generic.go" "$SQL"
}

cleanup_on_exit() {
	local original_rc=$? cleanup_failed=0
	trap - EXIT HUP INT TERM
	if [ "$SNAPSHOTS_READY" -eq 1 ] &&
		{ ! restore_all || ! assert_restored; }; then
		printf '%s: CANNOT INSPECT — unknown concurrent state; will not overwrite; snapshots in %s\n' \
			"$NAME" "$TMP" >&2
		cleanup_failed=1
	fi
	if [ "$LOCK_HELD" -eq 1 ] && ! rmdir "$LOCK_DIR"; then
		printf '%s: CANNOT INSPECT — cannot release owned lock %s\n' "$NAME" "$LOCK_DIR" >&2
		cleanup_failed=1
	fi
	[ "$cleanup_failed" -eq 0 ] || exit 2
	if [ ! -f "$TMP/.s993-salvage-mutants-scratch" ]; then
		printf '%s: CANNOT INSPECT — scratch space without ownership marker: %s\n' \
			"$NAME" "$TMP" >&2
		exit 2
	fi
	if ! rm -rf -- "$TMP"; then
		printf '%s: CANNOT INSPECT — cannot remove validated scratch space %s\n' \
			"$NAME" "$TMP" >&2
		exit 2
	fi
	exit "$original_rc"
}
trap cleanup_on_exit EXIT HUP INT TERM

GIT_ADMIN=$(git rev-parse --absolute-git-dir 2>/dev/null) ||
	cannot 'cannot resolve the worktree administrative directory'
LOCK_DIR="$GIT_ADMIN/s993-salvage-mutants.lock"
if mkdir "$LOCK_DIR" 2>/dev/null; then
	LOCK_HELD=1
else
	cannot "another test already owns $LOCK_DIR"
fi

cp "$CONFIRM" "$TMP/confirm-dialog.tsx" &&
	cp "$OWNERSHIP" "$TMP/nhi-actions.tsx" &&
	cp "$ROSTER" "$TMP/nhi-roster.tsx" &&
	cp "$SEARCH" "$TMP/recordings-view.tsx" &&
	cp "$LIKE" "$TMP/handlers.go" &&
	cp "$SQL" "$TMP/generic.go" || cannot 'cannot create the complete snapshot before mutation'
SNAPSHOTS_READY=1

replace_once() {
	local file=$1 old=$2 new=$3
	OLD=$old NEW=$new perl -0pi -e '
		$count = s/\Q$ENV{OLD}\E/$ENV{NEW}/g;
		END { exit((defined($count) && $count == 1) ? 0 : 3) }
	' "$file"
}

run_oracle() {
	local log=$1 rc
	if OLIVARES_GATE_BINDIR="$TMP" bash "$ORACLE" >"$log" 2>&1; then
		rc=0
	else
		rc=$?
	fi
	return "$rc"
}

run_mutant() {
	local label=$1 file=$2 old=$3 new=$4 message=$5 rc expected actual candidate
	restore_all || cannot "cannot restore before mutant $label"
	ACTIVE_FILE=''
	ACTIVE_MUTATED=''
	assert_restored || cannot "divergent baseline before mutant $label"
	candidate="$TMP/$label.mutated"
	cp "$file" "$candidate" || cannot "cannot prepare mutant $label"
	if ! replace_once "$candidate" "$old" "$new"; then
		cannot "mutant $label was not applied exactly once"
	fi
	ACTIVE_FILE=$file
	ACTIVE_MUTATED=$candidate
	assert_restored || cannot "the tree changed before installing mutant $label"
	cp "$candidate" "$file" || cannot "cannot install mutant $label"
	cmp -s "$candidate" "$file" || cannot "mutant $label does not match byte for byte"
	if run_oracle "$TMP/$label.log"; then rc=0; else rc=$?; fi
	expected="s993-salvage: FAIL — $message"
	actual=$(tail -n 1 "$TMP/$label.log")
	if [ "$rc" -ne 1 ] || [ "$actual" != "$expected" ]; then
		printf '%s: CANNOT INSPECT — mutant %s: rc=%s, unexpected message\n' \
			"$NAME" "$label" "$rc" >&2
		cat "$TMP/$label.log" >&2
		exit 2
	fi
	restore_all || cannot "cannot restore after mutant $label"
	ACTIVE_FILE=''
	ACTIVE_MUTATED=''
	assert_restored || cannot "mutant $label did not restore exact bytes"
	printf '%s: DETECTED %s — %s\n' "$NAME" "$label" "$message"
}

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

run_mutant search-responses "$SEARCH" \
	'error={query.error}' \
	'error={undefined}' \
	"$SEARCH_RESPONSES_MSG"
run_mutant like-backslash "$LIKE" \
	'escaped := strings.ReplaceAll(value, `\`, `\\`)' \
	'escaped := value' \
	"$LIKE_LITERAL_MSG"
run_mutant like-percent "$LIKE" \
	'escaped = strings.ReplaceAll(escaped, `%`, `\%`)' \
	'escaped = escaped' \
	"$LIKE_LITERAL_MSG"
run_mutant like-underscore "$LIKE" \
	'escaped = strings.ReplaceAll(escaped, `_`, `\_`)' \
	'escaped = escaped' \
	"$LIKE_LITERAL_MSG"
run_mutant sql-escape "$SQL" \
	'op = "LIKE ? ESCAPE '\''\\'\''"' \
	'op = "LIKE ?"' \
	"$SQL_ESCAPE_MSG"
run_mutant confirm-disabled "$CONFIRM" \
	'const confirmDisabled = callerDisabled || pending || !phraseSatisfied' \
	'const confirmDisabled = pending || !phraseSatisfied' \
	"$CONFIRM_MSG"
run_mutant ownership-changed-only "$OWNERSHIP" \
	": identity.kind === 'agent' && ownerChanged" \
	': ownerChanged' \
	"$OWNERSHIP_CHANGED_MSG"
run_mutant ownership-noop "$OWNERSHIP" \
	'const ownershipChanged = ownerChanged || sponsorChanged' \
	'const ownershipChanged = true || ownerChanged || sponsorChanged' \
	"$OWNERSHIP_NOOP_MSG"
run_mutant ownership-clear "$OWNERSHIP" \
	'const ownershipCannotSubmit = ownershipWouldClear || agentSponsorMissing' \
	'const ownershipCannotSubmit = agentSponsorMissing' \
	"$OWNERSHIP_CLEAR_MSG"
run_mutant ownership-owner-trim "$OWNERSHIP" \
	'const ownerDraft = ownerRef.trim()' \
	'const ownerDraft = ownerRef' \
	"$OWNERSHIP_TRIM_MSG"
run_mutant ownership-sponsor-trim "$OWNERSHIP" \
	'const sponsorDraft = sponsorRef.trim()' \
	'const sponsorDraft = sponsorRef' \
	"$OWNERSHIP_TRIM_MSG"
run_mutant agent-sponsor-carry "$OWNERSHIP" \
	'? currentSponsorRef' \
	"? ''" \
	"$AGENT_SPONSOR_MSG"
run_mutant agent-sponsor-missing "$OWNERSHIP" \
	"const agentSponsorMissing = identity.kind === 'agent' && sponsorDraft === ''" \
	'const agentSponsorMissing = false' \
	"$AGENT_SPONSOR_MSG"
run_mutant roster-follow "$ROSTER" \
	'void fetchNextPage()' \
	'void Promise.resolve()' \
	"$ROSTER_MSG"
run_mutant roster-error-stop "$ROSTER" \
	'!isFetchNextPageError' \
	'true' \
	"$ROSTER_MSG"
run_mutant roster-match-stop "$ROSTER" \
	'rows.length === 0' \
	'rows.length >= 0' \
	"$ROSTER_MATCH_STOP_MSG"
run_mutant search-audit "$SEARCH" \
	'onChange={(event) => setSearchDraft(event.currentTarget.value)}' \
	$'onChange={(event) => {\n              setSearchDraft(event.currentTarget.value)\n              patchFilters({ subject_contains: event.currentTarget.value })\n            }}' \
	"$SEARCH_AUDIT_MSG"
run_mutant search-subject "$SEARCH" \
	"searchField === 'subject_contains' && value ? value : undefined" \
	"searchField === 'grant' && value ? value : undefined" \
	"$SEARCH_SUBJECT_MSG"
run_mutant search-grant "$SEARCH" \
	"grant: searchField === 'grant' && value ? value : undefined," \
	"grant: false && searchField === 'grant' && value ? value : undefined," \
	"$SEARCH_GRANT_MSG"
run_mutant search-clear "$SEARCH" \
	'patchFilters({ subject_contains: undefined, grant: undefined })' \
	'patchFilters({ subject_contains: undefined })' \
	"$SEARCH_CLEAR_MSG"
run_mutant search-url "$SEARCH" \
	'if (value.subject_contains && value.grant) {' \
	'if (false && value.subject_contains && value.grant) {' \
	"$SEARCH_URL_MSG"
restore_all || cannot 'cannot restore before the final control'
if ! run_oracle "$TMP/final-green.log"; then
	printf '%s: CANNOT INSPECT — the clean final control did not pass\n' "$NAME" >&2
	cat "$TMP/final-green.log" >&2
	exit 2
fi
expected_green='s993-salvage: PASS — 4 web suites/79 tests and 3 Go contracts'
[ "$(tail -n 1 "$TMP/final-green.log")" = "$expected_green" ] ||
	cannot 'unexpected final passing message'

MISSING="$TMP/no-web"
if OLIVARES_S993_WEB_DIR="$MISSING" bash "$ORACLE" >"$TMP/cannot.log" 2>&1; then
	rc=0
else
	rc=$?
fi
expected_cannot="s993-salvage: COULD NOT CHECK — cannot read $MISSING/package.json"
if [ "$rc" -ne 2 ] || [ "$(tail -n 1 "$TMP/cannot.log")" != "$expected_cannot" ]; then
	printf '%s: CANNOT INSPECT — the rc2 control did not preserve the exact code and message\n' \
		"$NAME" >&2
	cat "$TMP/cannot.log" >&2
	exit 2
fi

assert_restored || cannot 'the test ended with different bytes'
printf '%s: PASS — 21/21 mutants detected, rc0/rc1/rc2 and byte-for-byte restoration\n' \
	"$NAME"
