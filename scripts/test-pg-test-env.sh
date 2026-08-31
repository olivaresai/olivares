#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Proves the three regimes of scripts/pg-test-env.sh — in particular the branch that cannot
# be observed on a machine that happens to have Postgres running — and the CI consistency of
# the -race legs that consume it.
#
# WHY THE ABSENT-SERVER BRANCH IS THE IMPORTANT ONE. The defect this whole change closes was
# not "Postgres was missing" — the server was there all along. It was that a skipped test and
# a passing test looked identical in the gate's output. A wiring that only works where a
# server happens to exist would reintroduce exactly that on every machine without one.
#
# THE GUARANTEE, STATED PER REGIME, because a single sentence about it was false. This header
# used to read "WHEN THERE IS NO SERVER, THE GATE SAYS SO, LOUDLY, AND STILL SUCCEEDS" as if
# it held everywhere. It holds in ONE regime of three:
#
#   FAIL-CLOSED (default, no opt-in)  no DSN configured => exit 1. It does NOT succeed.
#   LOCAL (OLIVARES_PG_LOCAL_DEFAULTS=1)  no server => exit 0 with a loud PARTIAL. This is
#                                     the regime the old sentence described.
#   PROMOTION (OLIVARES_GATE_STRICT_PG=1)  no server => exit 1, deliberately.
#
# The irony is the point: a battery written because a skip and a pass looked alike carried a
# header claiming a success the default regime does not give. And until 2026-08-01 it also
# PRINTED four skipped rows as `ok` and added them to `passed`. Both are closed; see skip().
# NO `set -e` HERE, DELIBERATELY. This file REPORTS failures through check(); `set -e`
# turns a failing assertion into a silent STOP instead, so the run ends after the last
# success and looks like a clean tail. That is exactly the failure mode these batteries
# exist to catch, and it bit this repository twice on 2026-07-25 — once truncating a
# 23-case battery at case 11, once truncating this one at case 2. Critical SETUP commands
# below carry an explicit `|| exit`; assertions must not abort the run.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/pg-test-env.sh"

# HERMETICITY (2026-07-30). Most forced branches below unset the three DSNs so they run
# identically on any machine — but the helper reads the further environment inputs listed below,
# which
# nothing here controlled, and each of them changes the answer this battery asserts. It said
# THREE until 2026-08-01 while listing four below: the fourth arrived by merge and the count
# in the sentence was never updated, which is the same class of defect as the counts this
# battery's own contrast rounds keep finding in its prose.
#
#   OLIVARES_GATE_STRICT_PG  turns an unreachable server into exit 1. A caller that had
#                            legitimately enabled the promotion regime — precisely the
#                            caller it exists for — made `absent server does NOT fail the
#                            gate` fail. Measured: 35 passed, 1 failed. Only the exit-code
#                            assertion could see it, because the strict check runs AFTER
#                            the warnings are printed, so every grep still passed.
#   OLIVARES_PG_PROBE        short-circuits reachability before any tool is consulted, so
#                            an ambient value silently rewrites the branch under test.
#                            Measured: with `false` exported, 40 passed and 2 failed.
#   PG_PROBE_TIMEOUT         reaches `timeout` as its first argument. Measured: `--help`
#                            made it exit 0 without running psql, so an absent server was
#                            reported reachable.
#   OLIVARES_PG_LOCAL_DEFAULTS  the FOURTH, and it arrived by merge (2026-07-31): the
#                            fail-closed change made the helper refuse to synthesise a DSN
#                            without this opt-in, and .githooks/pre-push exports it — so the
#                            battery inherits it in the real gate. Measured on the merged
#                            file BEFORE this line existed: 56 passed / 4 failed clean, and
#                            60 passed / 0 failed with the variable exported. A battery whose
#                            score depends on its caller is the defect, whichever way it
#                            lands; and worse, several strict-regime cases passed for the
#                            WRONG reason, because fail-closed also exits 1.
#
# The fix is a property, not a patch of one call site: clear all FOUR ONCE here, so every
# existing case and every case added later runs in the regime it asserts. Each regime is
# then exercised deliberately, by the batteries that set the variable explicitly.
#
# Note the asymmetry that remains ON PURPOSE: the DSN variables are cleared per invocation
# rather than here, because the fixture groups named next must each set one — custom-DSN, strict-live,
# missing-database, credential-leak, fixed-5432, ephemeral-port and localhost. The four above
# have no such case. (This used to say "two cases", counted when there were two; the number
# grew with the file and the sentence did not.)
# Captured BEFORE the unset, and used for exactly ONE decision: whether a case that cannot run
# its predicate is tolerable here. See skip(). It is never re-exported — the regime under test
# is still chosen per invocation, which is the property the unset establishes. A caller that
# says `OLIVARES_GATE_STRICT_PG=1` is saying "this gate must not be bought without PostgreSQL",
# and a battery that answers with unrun rows counted as passes has sold it one.
GATE_DEMANDS_PG="${OLIVARES_GATE_STRICT_PG:-0}"
unset OLIVARES_GATE_STRICT_PG OLIVARES_PG_PROBE PG_PROBE_TIMEOUT OLIVARES_PG_LOCAL_DEFAULTS

# Captured BEFORE anything clears it: a real superuser DSN, when the caller supplied one.
# The strict-regime case uses it to test an actually reachable server instead of a forced
# probe answer, and says so when it cannot.
#
# PREMISE PINNED, and it had to be. On this container ~/.zshenv exports both DSNs pointing
# at localhost:5432 — a cluster that is DOWN, with `olivares` as the maintenance database
# rather than `postgres`. So "the caller supplied a DSN" is NOT the same question as "there
# is a server to test against", and using the first as if it were the second made the live
# case fail for a reason that has nothing to do with the strict regime. Probe it once; an
# unreachable value is treated exactly like an absent one, and the case says which it was.
REAL_SUPER="${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}"
REAL_SUPER_WHY="no superuser DSN supplied"
if [ -n "$REAL_SUPER" ]; then
	if ! command -v psql >/dev/null 2>&1; then
		REAL_SUPER=""
		REAL_SUPER_WHY="no psql to verify the supplied DSN"
	elif ! PGCONNECT_TIMEOUT=10 psql -X -q -w -At -d "$REAL_SUPER" -v ON_ERROR_STOP=1 \
		-c 'SELECT 1' >/dev/null 2>&1; then
		# Never echo the value: it carries a password.
		REAL_SUPER=""
		REAL_SUPER_WHY="the supplied DSN is not reachable (value withheld)"
	fi
fi
WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-pgenv.XXXXXX")" || exit 1
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

# EVERY CASE MUST BE ACCOUNTED FOR (2026-08-01). The counters below only count cases that
# were REACHED. Several cases live inside `if <build the fixture>; then … check …; fi`, so a
# block whose fixture cannot be built runs no check at all: the case does not fail, it simply
# CEASES TO EXIST, and the battery still prints a green total. Measured, not theorised — with
# `awk` made to fail, three mutation cases disappeared and the run ended `72 passed, 0 failed`,
# exit 0. That is a fail-OPEN inside a fail-CLOSED guard, which is the worst kind.
#
# Declaring the number closes it: a run that measures less than this battery claims to measure
# is a FAILED run, whatever its individual cases said. Raise it deliberately when you add a
# case — a diff that changes this line is exactly the review signal you want.
EXPECTED_CASES=146

pass=0
fail=0
# `cases` counts DECLARED CASES, which is not the same as failures. A fixture that fails to
# build produces a `require_mutation` failure AND an unexercised case — two true failures for
# one case — so counting cases separately is what lets the total below stay exact.
cases=0

# AND THE TWO KINDS OF FAILURE ARE COUNTED APART (2026-08-02). `fail` is what decides the exit
# status and it must keep folding both kinds together, but it cannot also serve as an
# accounting term: a `require_mutation` failure raises it WITHOUT raising `cases`, so
# `cases == pass + skips + fail` is false by construction whenever a fixture does not build.
#
# The round-4 contrast asked for `cases == pass + skip + case_fail` and this file answered with
# EXPECTED_CASES instead, arguing the sum could not hold. The argument was against a sentence
# the contrast did not write: it said case_fail, not fail, and the distinction is exactly the
# one being made here. So both now exist, because they catch DIFFERENT things —
#
#   case_fail  makes the sum a DERIVED invariant that cannot drift, and catches a counter that
#              stops being incremented, a case that reports through TWO of them, or a new third
#              answer added without a term. NOT a case that reports through none: that one raises
#              no term on either side and the identity still holds — it is EXPECTED_CASES that
#              catches a case which quietly ceases to exist, which is why both exist.
#   EXPECTED_CASES  is a DECLARED constant, and catches the case that silently ceases to exist
#              — which the sum cannot see, because a case that never runs is absent from both
#              sides of it.
#
# One is hand-maintained on purpose; the other is free. Keeping only the constant left the
# accounting itself unchecked, which is how four skips were sold as passes in the first place.
case_fail=0
setup_fail=0

check() {
	cases=$((cases + 1))
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		case_fail=$((case_fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

# A case whose FIXTURE could not be built is not a pass, and it is not an absence either: it
# is coverage this run did not provide, and it must say so where it happens. `require_mutation`
# already fails loudly when a mutation applied to nothing, but a `mutate_workflow` that ERRORS
# — awk missing, a workflow whose shape moved under it — short-circuits the `&&` before
# require_mutation is ever called, and the check inside the block never runs.
unexercised() {
	cases=$((cases + 1))
	fail=$((fail + 1))
	case_fail=$((case_fail + 1))
	printf '  FAIL  %-58s %s\n' "$1" "NOT EXERCISED — its fixture could not be built"
}

# A case whose PREMISE is absent has not passed. It has not failed either: it did not run.
# Printing it as `ok` and adding it to `passed` is precisely the defect this battery exists to
# catch, committed by the battery itself. Measured by the round-4 contrast on 2026-08-01: the
# summary said `75 passed, 0 failed` while four rows had never executed a predicate — 71
# measurements sold as 75. `skipped` is a THIRD answer, like the helper's own exit 2, and for
# the same reason: "I could not look" is not "I looked and it is clean".
#
# AND IN THE PROMOTION REGIME IT IS A FAILURE. A caller that sets OLIVARES_GATE_STRICT_PG=1 is
# refusing a green bought without a server; handing it a skip would be that same purchase with
# a politer label.
skips=0
skip() {
	cases=$((cases + 1))
	if [ "$GATE_DEMANDS_PG" = "1" ]; then
		fail=$((fail + 1))
		case_fail=$((case_fail + 1))
		printf '  FAIL  %-58s %s\n' "$1" "NOT RUN and this gate demands PostgreSQL: $2"
		return
	fi
	skips=$((skips + 1))
	printf '  skip  %-58s %s\n' "$1" "$2"
}

echo "pg-test-env — three regimes: fail-closed default, LOCAL opt-in, PROMOTION strict"

# --- ABSENT SERVER (forced, so this runs identically on any machine) --------------------
# OLIVARES_PG_LOCAL_DEFAULTS=1 everywhere the legacy branches are under test (2026-07-30):
# without it the helper is FAIL-CLOSED and refuses to synthesise any DSN — that regime has
# its own section below. These cases prove the LOCAL loop the opt-in preserves.
set +e
out_absent="$(env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE='false' bash "$SCRIPT" 2>"$WORK/absent.err")"
rc_absent=$?

[ "$rc_absent" -eq 0 ]
check "absent server does NOT fail the gate" "exit 0" $?

[ -z "$out_absent" ]
check "absent server exports nothing" "empty stdout" $?

grep -q "WARNING — no Postgres" "$WORK/absent.err"
check "absent server WARNS on stderr" "warning present" $?

grep -q "will SKIP" "$WORK/absent.err"
check "the warning says the tests will SKIP" "'will SKIP' present" $?

grep -q "ROW LEVEL SECURITY" "$WORK/absent.err"
check "it names the uncovered classes" "RLS named" $?

grep -q "PARTIAL" "$WORK/absent.err"
check "it labels the green as PARTIAL" "'PARTIAL' present" $?

grep -q "2026-07-08" "$WORK/absent.err"
check "it cites the outage that came through this gap" "date cited" $?

# --- PRESENT SERVER (forced, so this runs identically on any machine) -------------------
set +e
out_present="$(env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE='true' bash "$SCRIPT" 2>"$WORK/present.err")"
rc_present=$?

[ "$rc_present" -eq 0 ]
check "present server succeeds" "exit 0" $?

grep -q "export OLIVARES_TEST_POSTGRES_DSN=" <<<"$out_present"
check "present server exports the app DSN" "app DSN exported" $?

grep -q "export OLIVARES_TEST_POSTGRES_ADMIN_DSN=" <<<"$out_present"
check "present server exports the admin DSN" "admin DSN exported" $?

grep -q "export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=" <<<"$out_present"
check "present server exports the superuser DSN" "superuser DSN exported" $?

grep -q "will RUN" "$WORK/present.err"
check "present server says the tests will RUN" "'will RUN' present" $?

# The emitted lines must be valid shell that actually sets the variables.
(
	eval "$out_present"
	[ -n "${OLIVARES_TEST_POSTGRES_DSN:-}" ] && [ -n "${OLIVARES_TEST_POSTGRES_ADMIN_DSN:-}" ]
)
check "the emitted lines eval to real exports" "variables set" $?

# An existing value must win, so a developer can point the gate elsewhere.
(
	export OLIVARES_TEST_POSTGRES_DSN="postgres://custom/db"
	eval "$(OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE='true' bash "$SCRIPT" 2>/dev/null)"
	[ "$OLIVARES_TEST_POSTGRES_DSN" = "postgres://custom/db" ]
)
check "an existing DSN is not overwritten" "custom value preserved" $?

# --- THE PROMOTION REGIME: OLIVARES_GATE_STRICT_PG ---------------------------------------
# The two regimes shipped on 2026-07-30 with no case of their own, which is how the
# hermeticity hole above stayed invisible: the flag changed this helper's exit code and
# nothing here ever set it or cleared it. Neutralising a variable is not testing it, so the
# regime is pinned in both directions.
#
# Mutation that must turn these red: delete the strict block at the end of pg-test-env.sh
# (the hard-failure cases go red), make it fire regardless of reachability (the reachable
# case does), or drop the PG_PROBE_TIMEOUT validation (the bypass case does).
[ "${OLIVARES_GATE_STRICT_PG:-unset}" = "unset" ]
check "the battery runs in the DEVELOPMENT regime by default" "ambient flag cleared" $?

set +e
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_GATE_STRICT_PG=1 OLIVARES_PG_PROBE='false' bash "$SCRIPT" >"$WORK/strict.out" 2>"$WORK/strict.err"
rc_strict_absent=$?

[ "$rc_strict_absent" -eq 1 ]
check "strict regime: an absent server is a HARD failure" "exit 1" $?

[ -z "$(cat "$WORK/strict.out")" ]
check "strict regime: and exports NOTHING on the way out" "empty stdout" $?

# It must still say everything the development regime says. An operator who hits this in a
# release pipeline needs the same diagnosis, plus the reason the exit code differs. Assert
# the WHOLE warning, not one token of it: an earlier version of this case checked only
# "PARTIAL" while its own name claimed the full diagnosis was preserved.
strict_msg_ok=0
for needle in "PARTIAL" "will SKIP" "ROW LEVEL SECURITY" "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" "2026-07-08"; do
	grep -q "$needle" "$WORK/strict.err" || strict_msg_ok=1
done
[ "$strict_msg_ok" -eq 0 ]
check "strict regime: the ENTIRE development diagnosis is still printed" "all five markers" $?

grep -q "OLIVARES_GATE_STRICT_PG" "$WORK/strict.err"
check "strict regime: the message names the flag that made it fatal" "cause named" $?

# A REACHABLE server must still pass — and this has to be a REAL server. Forcing
# OLIVARES_PG_PROBE=true returns before psql is ever run, so it proves only that the
# strict block does not fire after a forced answer; it does not prove the regime works
# against a live database. Use the caller's real superuser DSN when there is one, and
# report the case as SKIPPED rather than let it pass for the weaker reason.
if [ -n "$REAL_SUPER" ]; then
	env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN \
		OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="$REAL_SUPER" OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_GATE_STRICT_PG=1 \
		bash "$SCRIPT" >"$WORK/strict_ok.out" 2>"$WORK/strict_ok.err"
	rc_strict_present=$?

	[ "$rc_strict_present" -eq 0 ]
	check "strict regime: a LIVE server still passes" "exit 0, real session" $?

	# UNCHANGED means EQUAL, not "the three names appear". The old form asserted presence and
	# called it `exports unchanged` in its own detail column — a label stronger than the
	# predicate under it, which is how a green row ends up meaning less than its reader thinks.
	# The claim worth making is that the promotion regime changes the DECISION and not the
	# VALUES, so the same DSN is run through the normal regime and the three assignments are
	# compared literally.
	env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN \
		OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="$REAL_SUPER" OLIVARES_PG_LOCAL_DEFAULTS=1 \
		bash "$SCRIPT" >"$WORK/normal_ok.out" 2>/dev/null
	strict_lines="$(grep -E '^export OLIVARES_TEST_(POSTGRES|VECTOR)' "$WORK/strict_ok.out" | sort)"
	normal_lines="$(grep -E '^export OLIVARES_TEST_(POSTGRES|VECTOR)' "$WORK/normal_ok.out" | sort)"
	[ -n "$strict_lines" ] && [ "$strict_lines" = "$normal_lines" ]
	check "strict regime: exports are IDENTICAL to the normal regime's" "same values, not just same names" $?

	grep -q "will RUN" "$WORK/strict_ok.err"
	check "strict regime: and reports the suites will RUN" "no downgrade" $?
else
	skip "strict regime: a LIVE server still passes" "${REAL_SUPER_WHY}"
	skip "strict regime: exports are IDENTICAL to the normal regime's" "${REAL_SUPER_WHY}"
	skip "strict regime: and reports the suites will RUN" "${REAL_SUPER_WHY}"
fi

# --- PG_PROBE_TIMEOUT: a duration, never an option ---------------------------------------
# Live defect found by an adversarial review on 2026-07-30 and reproduced here before it
# was closed: the value reached `timeout` as its FIRST argument, where a leading dash is an
# OPTION. With no server reachable anywhere, PG_PROBE_TIMEOUT=--help made timeout print its
# usage and exit 0 WITHOUT running psql, so the helper took the reachable branch and exited
# 0 emitting all three DSNs — under the promotion regime, whose entire purpose is to make
# a server-less run fatal. Control: exit 1, zero exports.
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_GATE_STRICT_PG=1 PG_PROBE_TIMEOUT='--help' bash "$SCRIPT" >"$WORK/tmo.out" 2>"$WORK/tmo.err"
rc_tmo=$?

[ "$rc_tmo" -ne 0 ]
check "an option-shaped PG_PROBE_TIMEOUT cannot buy a strict green" "refused" $?

[ -z "$(cat "$WORK/tmo.out")" ]
check "and it exports no DSN on the way out" "empty stdout" $?

env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	PG_PROBE_TIMEOUT='not-a-duration' bash "$SCRIPT" >/dev/null 2>"$WORK/tmo2.err"
[ "$?" -eq 2 ]
check "a non-duration PG_PROBE_TIMEOUT is refused outright" "exit 2" $?

grep -q "PG_PROBE_TIMEOUT" "$WORK/tmo2.err"
check "and the refusal names the variable" "diagnostic present" $?

# The legitimate forms must still work, or the validation would have closed the interface
# instead of correcting it.
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	OLIVARES_PG_LOCAL_DEFAULTS=1 PG_PROBE_TIMEOUT='5' OLIVARES_PG_PROBE='false' bash "$SCRIPT" >/dev/null 2>/dev/null
[ "$?" -eq 0 ]
check "a plain-seconds PG_PROBE_TIMEOUT is still accepted" "exit 0" $?

env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	OLIVARES_PG_LOCAL_DEFAULTS=1 PG_PROBE_TIMEOUT='30s' OLIVARES_PG_PROBE='false' bash "$SCRIPT" >/dev/null 2>/dev/null
[ "$?" -eq 0 ]
check "a suffixed PG_PROBE_TIMEOUT is still accepted" "exit 0" $?

# --- the maintenance DSN must name the MAINTENANCE database ------------------------------
# Live defect, 2026-07-29: the default named `/olivares`, that database did not exist on the
# development host, and `pg_isready` said yes anyway because it probes the SERVER and never
# the database. The helper announced "will RUN" and every Postgres suite then died with
# `FATAL: database "olivares" does not exist`. It is also wrong on the merits: pgtest uses
# this DSN only to CREATE/DROP DATABASE, and PostgreSQL refuses to drop the database the
# session is connected to.
#
# Mutation that must turn this red: point the superuser default back at an application
# database.
super_default="$(echo "$out_present" | sed -n 's/^export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="\${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-\(.*\)}"$/\1/p')"
[ -n "$super_default" ]
check "the superuser default is extractable" "parsed" $?

case "$super_default" in
*/postgres | */postgres\?*) rc_maint=0 ;;
*) rc_maint=1 ;;
esac
[ "$rc_maint" -eq 0 ]
check "the superuser DSN defaults to the MAINTENANCE database" "not an application database" $?

# --- reachability must open a real session, not just ping the listener -------------------
# pg_isready reports success for a database that does not exist, a role that cannot log in
# and a password that is wrong. Any of those makes the suites FAIL while the helper says
# they will RUN.
#
# Mutation that must turn this red: go back to deciding reachability with pg_isready.
# NOTE ON errexit: this battery deliberately runs WITHOUT `set -e` (see the header). An
# earlier version of this block turned it on and left it on, so any later failure would
# have aborted before check() and truncated the very summary that makes failures visible
# — the exact defect the header warns about, reintroduced two cases below its own warning.
# DERIVED FROM THE VERIFIED SERVER, not from a default (corrected 2026-08-01). This block used
# to pin its premise by probing the helper's DEFAULT DSN and then, if that answered, fire a
# negative case at a hardcoded `127.0.0.1:5432`. Both halves contradict the topology this repo
# moved to: CI publishes Postgres on an EPHEMERAL host port, and on this container 5432 is a
# dead cluster while the live one is elsewhere. The result was measured by the round-4 contrast:
# with a real server supplied on 5433 and the three strict rows passing against it, THIS row
# still printed `skipped: default DSN not reachable` and counted a pass. It tested the default,
# not the server.
#
# So the premise is the session already verified at the top (REAL_SUPER), and the negative DSN
# is that same DSN with ONLY the database name replaced — same user, credential, host, port and
# parameters. That is what makes the negative isolate the missing database instead of isolating
# an unreachable host.
missing_db_dsn=""
case "$REAL_SUPER" in
"") ;;
postgres://*/* | postgresql://*/*)
	# URL form: replace the path segment between the authority and any `?query`.
	missing_db_dsn="$(printf '%s' "$REAL_SUPER" |
		sed 's#\(://[^/]*/\)[^?]*#\1olv_database_that_does_not_exist#')"
	;;
*dbname=*)
	# key=value form: replace the dbname token, whatever it is.
	missing_db_dsn="$(printf '%s' "$REAL_SUPER" |
		sed 's#dbname=[^ ]*#dbname=olv_database_that_does_not_exist#')"
	;;
esac

if [ -z "$REAL_SUPER" ]; then
	skip "a live server with a MISSING database is not reported as RUN" "${REAL_SUPER_WHY}"
elif [ -z "$missing_db_dsn" ] || [ "$missing_db_dsn" = "$REAL_SUPER" ]; then
	# The DSN is in a shape this block cannot rewrite. Deriving nothing and firing the
	# ORIGINAL DSN would assert the opposite of the property, so say so instead.
	skip "a live server with a MISSING database is not reported as RUN" \
		"the supplied DSN shape yields no derivable database name"
else
	env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN \
		OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="$missing_db_dsn" \
		bash "$SCRIPT" >"$WORK/missingdb.out" 2>"$WORK/missingdb.err"
	! grep -q "will RUN" "$WORK/missingdb.err"
	check "a live server with a MISSING database is not reported as RUN" "no false RUN" $?
fi

# --- neither probe tool: report that nothing was MEASURED, not that nothing is there ----
# "No Postgres reachable" is a claim about the world. With no client tool installed the
# helper has made no observation at all, and dressing that up as an absence is the same
# dishonesty as a false RUN, pointed the other way.
TOOLLESS="$WORK/bin-empty"
mkdir -p "$TOOLLESS"
# bash by ABSOLUTE path: emptying PATH also hides the interpreter, and `env ... bash`
# then fails with "No such file or directory" before the script ever runs — which looked
# exactly like the helper misbehaving.
BASH_BIN="$(command -v bash)"
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	OLIVARES_PG_LOCAL_DEFAULTS=1 \
	PATH="$TOOLLESS" "$BASH_BIN" "$SCRIPT" >"$WORK/notool.out" 2>"$WORK/notool.err"
grep -q "could not look" "$WORK/notool.err"
check "with no probe tool it says it could not LOOK" "not an absence claim" $?

[ -z "$(cat "$WORK/notool.out")" ]
check "and it still exports nothing" "empty stdout" $?

! grep -q "no Postgres reachable" "$WORK/notool.err"
check "and it does not claim the server is absent" "no unmeasured claim" $?

# --- the message must be TRUE per leg, not broadly reassuring ---------------------------
# The first version said "every Postgres-backed test", which was false: the core suites
# gate on the SUPERUSER DSN, and pgvector gates on a different variable entirely.
grep -q "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" "$WORK/absent.err"
check "it names the variable that ACTUALLY gates the core suites" "superuser DSN named" $?

grep -q "OLIVARES_TEST_VECTOR_DSN" "$WORK/absent.err"
check "it declares the pgvector leg as separately gated" "vector DSN named" $?

grep -q "OLIVARES_TEST_VECTOR_DSN" "$WORK/present.err"
check "and declares it even when Postgres IS reachable" "no unearned green" $?

# --- a DSN carries a password: never echo it, and never mis-describe what happens --------
# The warning used to interpolate the configured superuser DSN into the message, and to say
# the suites "will SKIP" — but an inherited-but-unreachable DSN stays exported, so they do
# not skip, they fail to connect. A warning that is wrong in a reassuring direction is worse
# than none.
SENTINEL='postgres://leaky:HUNTER2SECRET@unreachable.invalid:5432/db'
OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="$SENTINEL" OLIVARES_PG_PROBE=false \
	bash "$SCRIPT" >/dev/null 2>"$WORK/leak.err"
! grep -q "HUNTER2SECRET" "$WORK/leak.err"
check "the configured DSN password is never echoed" "no credential leak" $?

grep -q "value withheld" "$WORK/leak.err"
check "and the message says the value was withheld" "explained" $?

grep -q "will FAIL to connect rather than" "$WORK/leak.err"
check "an unreachable CONFIGURED DSN is described as a failure, not a skip" "accurate" $?

env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE=false bash "$SCRIPT" >/dev/null 2>"$WORK/plain.err"
grep -q "will SKIP" "$WORK/plain.err"
check "with no DSN configured it is still a skip" "both cases distinguished" $?

# --- the probe override is a constrained interface, not eval ----------------------------
# It used to run its value through `eval`: arbitrary code execution through an environment
# variable, for no benefit over accepting two literals.
#
# The canary lives in this run's PRIVATE directory, not at a fixed /tmp path (corrected
# 2026-08-01). A shared path made this the one case in the battery with global state: two
# concurrent runs on the same host would race each other's canary — and worse, the unconditional
# `rm -f` deleted a file this run may never have created. The private directory is already
# removed by the EXIT trap, so the cleanup line goes with it.
set +e
RCE_CANARY="$WORK/pg-probe-rce-canary"
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_PG_PROBE="touch $RCE_CANARY" bash "$SCRIPT" >/dev/null 2>"$WORK/rce.err"
rc_rce=$?
[ "$rc_rce" -eq 2 ]
check "a non-boolean probe value is refused" "exit 2" $?
[ ! -e "$RCE_CANARY" ]
check "and is never executed" "no canary file" $?

# --- FAIL-CLOSED (2026-07-30): no synthesis without the explicit opt-in ------------------
# CI publishes Postgres on an EPHEMERAL host port, so 127.0.0.1:5432 on a shared runner
# host is either nothing or ANOTHER job's cluster — and pgtest CREATE/DROP DATABASEs
# through the maintenance DSN. If a job's $GITHUB_ENV exports are lost (a `container:`
# job, `act`, `env -i`), the helper must FAIL, never synthesise the old fixed-port
# default and aim the suite at the neighbour. Five rows per
# an internal design note (not shipped) §1.6: without them the fail-closed property is
# an assertion, not a guarantee.

# (a) no DSNs and no opt-in → hard failure that NAMES the missing variable.
set +e
out_fc="$(env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN -u OLIVARES_PG_LOCAL_DEFAULTS bash "$SCRIPT" 2>"$WORK/fc-a.err")"
rc_fc=$?
[ "$rc_fc" -eq 1 ]
check "(a) empty DSNs without opt-in FAIL the helper" "exit 1" $?
grep -q "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN is empty" "$WORK/fc-a.err"
check "(a) the failure names the missing variable" "variable named" $?
grep -q "OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/fc-a.err"
check "(a) and names the local opt-in escape hatch" "opt-in named" $?
[ -z "$out_fc" ]
check "(a) and exports nothing on the failure path" "empty stdout" $?

# (b) a superuser DSN on the FIXED port 5432 without opt-in → refused. That address in
# CI is a neighbouring job's cluster; only the resolver-step port is legitimate there.
set +e
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_PG_LOCAL_DEFAULTS \
	OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable' \
	bash "$SCRIPT" >"$WORK/fc-b.out" 2>"$WORK/fc-b.err"
rc_fc=$?
[ "$rc_fc" -eq 1 ]
check "(b) a 127.0.0.1:5432 DSN without opt-in is refused" "exit 1" $?
grep -q "FIXED port 5432" "$WORK/fc-b.err"
check "(b) and the refusal says why" "fixed port named" $?

# (c) three DSNs on an ephemeral port → pass, and the emitted exports carry EXACTLY the
# configured values (existing values win; nothing is rewritten to a default).
EPH_SUP='postgres://postgres:postgres@127.0.0.1:45999/postgres?sslmode=disable'
EPH_APP='postgres://olivares_app:apppw@127.0.0.1:45999/olivares?sslmode=disable'
EPH_ADM='postgres://olivares_admin:adminpw@127.0.0.1:45999/olivares?sslmode=disable'
set +e
out_fc="$(env -u OLIVARES_PG_LOCAL_DEFAULTS OLIVARES_PG_PROBE=true \
	OLIVARES_TEST_POSTGRES_DSN="$EPH_APP" OLIVARES_TEST_POSTGRES_ADMIN_DSN="$EPH_ADM" \
	OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="$EPH_SUP" bash "$SCRIPT" 2>"$WORK/fc-c.err")"
rc_fc=$?
[ "$rc_fc" -eq 0 ]
check "(c) ephemeral-port DSNs pass WITHOUT the opt-in" "exit 0" $?
(
	export OLIVARES_TEST_POSTGRES_DSN="$EPH_APP" OLIVARES_TEST_POSTGRES_ADMIN_DSN="$EPH_ADM" \
		OLIVARES_TEST_POSTGRES_SUPERUSER_DSN="$EPH_SUP"
	eval "$out_fc"
	[ "$OLIVARES_TEST_POSTGRES_DSN" = "$EPH_APP" ] &&
		[ "$OLIVARES_TEST_POSTGRES_ADMIN_DSN" = "$EPH_ADM" ] &&
		[ "$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" = "$EPH_SUP" ]
)
check "(c) and the exports echo the configured values unchanged" "identical DSNs" $?

# (d) the opt-in without a server keeps the OLD local loop: exit 0, loud PARTIAL warning.
# This is the regression guard for the development regime the opt-in exists to preserve.
set +e
out_fc="$(env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN \
	OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE=false bash "$SCRIPT" 2>"$WORK/fc-d.err")"
rc_fc=$?
[ "$rc_fc" -eq 0 ] && [ -z "$out_fc" ] && grep -q "PARTIAL" "$WORK/fc-d.err"
check "(d) opt-in without a server keeps the loud partial local loop" "exit 0 + PARTIAL" $?

# (e) the localhost spelling of the same fixed-port class must not slip past the guard.
set +e
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_PG_LOCAL_DEFAULTS \
	OLIVARES_TEST_POSTGRES_SUPERUSER_DSN='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
	bash "$SCRIPT" >"$WORK/fc-e.out" 2>"$WORK/fc-e.err"
rc_fc=$?
[ "$rc_fc" -eq 1 ] && grep -q "FIXED port 5432" "$WORK/fc-e.err"
check "(e) the localhost:5432 host variant is refused too" "exit 1" $?

# --- with-pg-env.sh: the producer's STATUS is checked, not discarded ---------------------
WRAP="$ROOT/scripts/with-pg-env.sh"

out_ok="$(env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE=true bash "$WRAP" sh -c 'echo "SUPER=${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-unset}"' 2>/dev/null)"
grep -q "SUPER=postgres://" <<<"$out_ok"
check "the wrapper exports reach the child command" "DSN visible downstream" $?

set +e
OLIVARES_PG_PROBE='not-a-boolean' bash "$WRAP" sh -c 'echo SHOULD_NOT_RUN' >"$WORK/wrap.out" 2>"$WORK/wrap.err"
rc_wrap=$?
[ "$rc_wrap" -ne 0 ]
check "a failing helper FAILS the wrapper" "nonzero" $?
! grep -q "SHOULD_NOT_RUN" "$WORK/wrap.out"
check "and the tests never run with an unknown posture" "child not executed" $?
grep -q "unknown Postgres posture" "$WORK/wrap.err"
check "the wrapper says why it refused" "diagnostic present" $?

# The warning must survive a caller that consumes stdout — that is the whole point of it
# being on stderr.
env -u OLIVARES_TEST_POSTGRES_DSN -u OLIVARES_TEST_POSTGRES_ADMIN_DSN -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_PG_LOCAL_DEFAULTS=1 OLIVARES_PG_PROBE=false bash "$WRAP" true >/dev/null 2>"$WORK/wrapwarn.err"
grep -q "PARTIAL" "$WORK/wrapwarn.err"
check "the PARTIAL warning survives stdout capture" "reaches stderr" $?

# --- EVERY MODULE TREE THAT TOUCHES POSTGRES IS RACED *ON* POSTGRES ----------------------
# THE INVERSION, AND WHY THE TRIPWIRE WAS NOT DELETED (2026-08-01).
#
# Until today this block asserted the opposite: that ./modules was Postgres-FREE. That was
# a measured fact on 2026-07-30 (zero .go files under modules/ referenced pgtest or
# POSTGRES_DSN, jackc/pgx was `// indirect`), and it was the premise that let CI race
# ./modules in its own job with no service and let test:race-hot:modules drop the
# with-pg-env.sh wrapper. The block existed because that premise was one commit away from
# being false, and its own comment named the two sanctioned exits: move the test out of
# modules/, or give race-modules a service and its task the wrapper back — never delete
# the tripwire.
#
# unit H put 12 writer-fence ENFORCEMENT tests on real PostgreSQL in modules/eventing.
# The tripwire fired, before those tests could ever run green while silently skipping. The
# hub took the second exit: race-modules now provisions Postgres identically to race-rest,
# the wrapper is back, and this block asserts the NEW premise.
#
# The property protected is unchanged and is not "modules has no Postgres". It is: NO TEST
# SKIPS IN SILENCE UNDER A GREEN JOB. That property has two consistent worlds — a tree with
# no Postgres raced by a job with no server, and a tree with Postgres raced by a job with
# one — and exactly one broken one: a tree that acquires Postgres while its job does not.
# So the sweep below is no longer a PROHIBITION, it is a DETECTOR.
#
# WHAT THE DETECTOR ACTUALLY DECIDES, stated exactly, because this block used to claim it
# asserted "the agreement between what a tree needs and what its CI job provides" — and it
# does not. Its result only picks MODULES_VERDICT, the label printed with the wiring rows.
# The service, the digest pin and the with-pg-env.sh wrapper are demanded UNCONDITIONALLY of
# every discovered -race leg, whatever the detector says. So a false positive here costs a
# word in the output, not a container; and the agreement claim, if anyone wants it, means
# branching the assertion on the detector's answer, which is a change nobody has made.
#
# The detector stays deliberately conservative — a bare mention of pgtest in a comment counts
# as "this tree wants Postgres" — and that conservatism is now nearly free, precisely because
# a false positive only mislabels. A false negative would still cost the modules/eventing fence tests
# skipping forever under a green check, if the unconditional demand above were ever relaxed.

# THE PATTERN LIST, AND A HOLE THAT WAS IN IT FROM THE START (found 2026-08-01, by trying
# to watch the detector fire and watching it NOT fire). The original list was
# pgtest / POSTGRES_DSN / jackc/pgx. `POSTGRES_DSN` is not a substring of
# OLIVARES_TEST_POSTGRES_SUPERUSER_DSN — the SUPERUSER infix breaks it — and the same holds
# for OLIVARES_TEST_POSTGRES_ADMIN_DSN and OLIVARES_TEST_VECTOR_DSN. So a test gated on the
# superuser DSN, which is the one a test provisioning its own database uses and exactly
# what unit H does, was invisible to the tripwire. Unit H is caught anyway, by its
# `jackc/pgx/v5/stdlib` import; a test that opened the same server through a driver
# registered somewhere else would not have been.
#
# Measured before the fix: a modules/ file whose only Postgres reference was
# os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN") produced ZERO hits and the gate stayed
# green. The list below is the fix, and the fixtures under it are the red half.
PG_SWEEP_PATTERNS=(
	-e 'pgtest'
	-e 'OLIVARES_TEST_POSTGRES' # DSN, ADMIN_DSN, SUPERUSER_DSN — the infix broke the old pattern
	-e 'OLIVARES_TEST_VECTOR_DSN'
	-e 'POSTGRES_DSN' # kept: catches spellings that are not the OLIVARES_TEST_ family
	-e 'jackc/pgx'
	-e 'lib/pq'
)

modules_pg_sweep() {
	# $1 = module root. Nonzero when the tree references Postgres — .go files matching any
	# pattern above, or a go.mod declaring a Postgres driver DIRECT. Hits on stdout.
	sweep_rc=0
	sweep_hits="$(grep -rln --include='*.go' "${PG_SWEEP_PATTERNS[@]}" "$1" 2>/dev/null)"
	if [ -n "$sweep_hits" ]; then
		printf '        Postgres reference under %s:\n' "$1"
		printf '%s\n' "$sweep_hits" | sed 's/^/          /'
		sweep_rc=1
	fi
	if [ -f "$1/go.mod" ]; then
		# NOT `grep A "$f" | grep -qv B`. Under `set -o pipefail` the -q consumer exits at
		# its first match and the producer dies on SIGPIPE, so the pipeline reports 141
		# ON SUCCESS — and 141 inside this `if` is read as "no direct driver", i.e. the
		# gate fails OPEN on exactly the input it is looking for. It survives today only
		# because go.mod happens to hold one matching line. Read into a variable instead.
		driver_lines="$(grep -e 'jackc/pgx' -e 'lib/pq' "$1/go.mod" 2>/dev/null)"
		direct_drivers="$(printf '%s\n' "$driver_lines" | grep -v '// indirect' | grep -v '^$')"
		if [ -n "$direct_drivers" ]; then
			printf '        a Postgres driver is a DIRECT dependency in %s/go.mod:\n' "$1"
			printf '%s\n' "$direct_drivers" | sed 's/^/          /'
			sweep_rc=1
		fi
	fi
	return "$sweep_rc"
}

# RED first — each fixture is a signal the detector must SEE. A detector proven only on the
# tree it currently answers for is a gate whose red half is imaginary.
RED_ENV="$WORK/modules-red-env"
mkdir -p "$RED_ENV"
cat >"$RED_ENV/db_test.go" <<'EOF'
package red

import "os"

var dsn = os.Getenv("OLIVARES_TEST_POSTGRES_DSN")
EOF
modules_pg_sweep "$RED_ENV" >/dev/null
rc_sweep_env=$?
[ "$rc_sweep_env" -ne 0 ]
check "a module file reading POSTGRES_DSN is DETECTED" "nonzero" $?

RED_PGTEST="$WORK/modules-red-pgtest"
mkdir -p "$RED_PGTEST"
cat >"$RED_PGTEST/store_test.go" <<'EOF'
package red

// wires this package to core/internal/pgtest at the next refactor
EOF
modules_pg_sweep "$RED_PGTEST" >/dev/null
rc_sweep_pgtest=$?
[ "$rc_sweep_pgtest" -ne 0 ]
check "a module file referencing pgtest is DETECTED" "nonzero" $?

# The two spellings the original pattern list could not see. These are not hypothetical
# shapes: the first is how unit H gates itself, and the second is how the pgvector
# integration test gates itself.
RED_SUPER="$WORK/modules-red-superuser"
mkdir -p "$RED_SUPER"
cat >"$RED_SUPER/fence_test.go" <<'EOF'
package red

import "os"

// Provisions its own database, so the SUPERUSER DSN is the only one it reads.
var super = os.Getenv("OLIVARES_TEST_POSTGRES_SUPERUSER_DSN")
EOF
modules_pg_sweep "$RED_SUPER" >/dev/null
rc_sweep_super=$?
[ "$rc_sweep_super" -ne 0 ]
check "a module file reading the SUPERUSER DSN is DETECTED" "the 2026-07-30 blind spot" $?

RED_VECTOR="$WORK/modules-red-vector"
mkdir -p "$RED_VECTOR"
cat >"$RED_VECTOR/vector_test.go" <<'EOF'
package red

import "os"

var vec = os.Getenv("OLIVARES_TEST_VECTOR_DSN")
EOF
modules_pg_sweep "$RED_VECTOR" >/dev/null
rc_sweep_vector=$?
[ "$rc_sweep_vector" -ne 0 ]
check "a module file reading the VECTOR DSN is DETECTED" "same blind spot, pgvector leg" $?

RED_PGX="$WORK/modules-red-pgx"
mkdir -p "$RED_PGX"
printf 'module red\n\ngo 1.26\n\nrequire github.com/jackc/pgx/v5 v5.10.0\n' >"$RED_PGX/go.mod"
modules_pg_sweep "$RED_PGX" >/dev/null
rc_sweep_pgx=$?
[ "$rc_sweep_pgx" -ne 0 ]
check "go.mod promoting pgx to a DIRECT dependency is DETECTED" "nonzero" $?

# The detector's GREEN half, which the old prohibition never had a fixture for: it used the
# real tree as its own negative control, so the day the tree changed the row simply
# inverted and there was nothing left proving the detector can say "no".
GREEN_CLEAN="$WORK/modules-green-clean"
mkdir -p "$GREEN_CLEAN"
printf 'package green\n\n// sqlite only; no server of any kind\nvar Engine = "sqlite"\n' >"$GREEN_CLEAN/engine.go"
printf 'module green\n\ngo 1.26\n\nrequire github.com/jackc/pgx/v5 v5.10.0 // indirect\n' >"$GREEN_CLEAN/go.mod"
modules_pg_sweep "$GREEN_CLEAN" >/dev/null
check "a PG-free tree with pgx INDIRECT stays clean" "the detector can say no" $?

# --- the structural read of the CI wiring ------------------------------------------------
# A YAML fact is read with a YAML decoder, not with grep. The repository already argues this
# in scripts/check-ci-ports.sh, and here the failure mode is worse than a false red: a
# line-oriented scan of mainline-ci.yml cannot tell which JOB a `services:` block belongs
# to, so it would answer "race-modules has Postgres" while looking at race-rest's — a false
# GREEN in a fail-closed gate.
#
# THREE ANSWERS, NOT TWO: 0 wired, 1 NOT wired, 2 COULD NOT LOOK. Exit 2 is treated as a
# failure by every assertion below, and has its own row: a gate that reports "clean" when
# its decoder is missing is the fail-open pattern this repository spent 2026-08-01 removing
# from twenty files.
# THE READER IS A GO PROGRAM, NOT EMBEDDED PYTHON (cmd/olivares/tools/checkpgwiring).
# The first version of this section WAS embedded Python and it worked — and it made this
# file the ONLY script in the repository that requires PyYAML. lint:pg-env is a candidate
# for mainline CI (task #38: it runs in .githooks/pre-push and in no workflow at all), and
# this repository's self-hosted runners have never been measured for PyYAML. A gate that
# runs where its interpreter's optional dependency happens to be installed is the shape
# .githooks/pre-push warns about: "an unverified 'cannot' in a gate is indistinguishable
# from a hole". Go is present wherever any of these gates run, cmd/olivares already depends
# on gopkg.in/yaml.v3, and sibling tools already read these same files that way.
#
# THREE ANSWERS, NOT TWO: 0 wired, 1 NOT wired, 2 COULD NOT LOOK. Exit 2 is a failure for
# every assertion below, and has its own rows.
#
# AND IT IS BUILT, NOT `go run` — measured 2026-08-01, and this is the whole reason the
# rows below exist. `go run` does NOT propagate a program's exit code: with the program
# exiting 2 it prints `exit status 2` and exits 1 ITSELF. Three answers arrive at the
# caller as two, and "I could not look" becomes indistinguishable from "I looked and it is
# broken" — inside the gate whose entire subject is that distinction. Measured before the
# fix: the exit-2 rows below FAILED, receiving 1.
#
# (The sibling scripts/check-ci-ports.sh has the same `exec go run`, and checkciports also
# reserves 2 for "could not look". Nothing there is wrong today — `task lint:ci-ports` only
# asks zero/non-zero — but the distinction its header claims is not observable at its call
# site either. Recorded here rather than changed from this file.)
#
# The binary needs a directory that permits execve, which /tmp in the development container
# does NOT (measured 2026-07-31: every execve under it dies EACCES). Same probe as
# scripts/test-gates-failclosed.sh: try the candidates and PROVE one works by running
# something from it, rather than assuming.
pick_exec_dir() {
	local base d
	for base in "${RUNNER_TEMP:-}" "${TMPDIR:-}" /workspace/.olivares-tmptest /tmp; do
		[ -n "$base" ] || continue
		mkdir -p "$base" 2>/dev/null || continue
		d="$(mktemp -d "$base/pgwiring.XXXXXX" 2>/dev/null)" || continue
		if printf '#!/bin/sh\nexit 0\n' >"$d/probe" 2>/dev/null &&
			chmod +x "$d/probe" 2>/dev/null && "$d/probe" 2>/dev/null; then
			rm -f "$d/probe"
			printf '%s' "$d"
			return 0
		fi
		rm -rf "$d"
	done
	return 1
}

BINDIR="$(pick_exec_dir)" || {
	echo "pg-test-env: found no directory that permits execve (tried RUNNER_TEMP, TMPDIR," >&2
	echo "  /workspace/.olivares-tmptest, /tmp). checkpgwiring cannot be built or run, so the" >&2
	echo "  CI wiring is UNVERIFIED — refusing to report a pass." >&2
	exit 2
}
PGWIRING="$BINDIR/checkpgwiring"
bindir_cleanup() { rm -rf "$BINDIR"; }
trap 'cleanup; bindir_cleanup' EXIT HUP INT TERM

if ! (cd "$ROOT/cmd/olivares" && go build -o "$PGWIRING" ./tools/checkpgwiring) >"$WORK/build.err" 2>&1; then
	echo "pg-test-env: cannot BUILD cmd/olivares/tools/checkpgwiring — the CI wiring is" >&2
	echo "  UNVERIFIED, not clean. Build output:" >&2
	sed 's/^/    /' "$WORK/build.err" >&2
	exit 2
fi

pg_wiring() { # pg_wiring <workflow> <taskfile> [job task]
	# -root is always the REAL repository: every fixture below is a copy of the workflow
	# living in $WORK, and the workflow's steps reference local composite actions by a
	# `./.github/actions/...` path that is relative to the repository, not to the copy.
	# Without this the verifier would answer UNVERIFIED about the fixture's own location
	# instead of about the property under test.
	if [ -n "${3:-}" ]; then
		"$PGWIRING" -workflow "$1" -taskfile "$2" -root "$ROOT" -job "$3" -task "$4"
	else
		"$PGWIRING" -workflow "$1" -taskfile "$2" -root "$ROOT"
	fi
}

pg_image_agreement() { # pg_image_agreement <workflow>
	"$PGWIRING" -mode image-agreement -workflow "$1"
}

WORKFLOW="$ROOT/.github/workflows/mainline-ci.yml"
TASKFILE="$ROOT/Taskfile.yml"

# COUPLING GUARD. Twenty fixtures below hardcode this recipe VERBATIM, timeout included,
# because a mutation has to reproduce the line byte-for-byte to strip a piece off it. That
# makes ANY edit to it a repo-wide push outage.
#
# It happened on 2026-08-18 and the cause is worth keeping: the #856 merge silently reverted
# the modules race cap 45m -> 30m. Nobody retuned anything on purpose. All twenty mutations
# stopped applying, this battery refused (correctly -- a mutation that does not apply proves
# nothing) with 34 fixture failures and NOTHING naming the cause, and five lanes were blocked
# by a merge artefact. The repair was to restore the Taskfile, not to chase the anchors.
#
# So the coupling is asserted ONCE, up front, and says what to do. One actionable line
# instead of 34 -- and it points at the recipe, which is where the accident lands.
PINNED_RECIPE="      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
if ! grep -qxF "$PINNED_RECIPE" "$TASKFILE"; then
	printf 'pg-test-env: ⛔ NO HE PODIDO MIRAR: the modules race recipe this battery mutates is not in %s\n' "$TASKFILE" >&2
	printf '             expected verbatim: %s\n' "$PINNED_RECIPE" >&2
	printf '             found:             %s\n' "$(grep -n "with-pg-env.sh bash -c 'cd modules" "$TASKFILE" | head -1)" >&2
	printf '             If the recipe changed ON PURPOSE, update PINNED_RECIPE and the 20 hardcoded\n' >&2
	printf '             copies below IN THE SAME COMMIT. If it changed by accident -- a merge revert\n' >&2
	printf '             is how this last happened -- restore the RECIPE, not the anchors.\n' >&2
	exit 2
fi

# NON-VACUITY, and the shape of it matters. The first version of this block asserted
# "./modules DOES need Postgres today" as its own row, and that row was RED when written
# (2026-08-01, measured): the wiring landed on main BEFORE unit H, which is still in
# PR #457 — deliberately, because the other order deadlocks. Unit H cannot merge while the
# old tripwire calls its 12 tests a violation, and a gate that runs in .githooks/pre-push
# blocks every push in the repository while it is red, including the one that would fix it.
#
# The row was wrong on the merits too, not just on timing. "modules uses Postgres" is a
# fact about the tree, not a property worth gating: a future change that legitimately
# removed every Postgres test from modules/ would earn a red for doing nothing wrong. The
# falsifiable property is the IMPLICATION — a tree that needs Postgres is raced on
# Postgres — and the rows below assert it unconditionally, in the strong direction: the
# service and the wrapper must BE there. Removing them is allowed only the way it was
# allowed on 2026-07-30, by editing this gate on purpose and saying why.
#
# So the detector's verdict is REPORTED in the label rather than asserted. Neither branch
# is vacuous: both run the same assertion, and the label says which world it ran in.
#
# Status into a variable on its own line, never read as a bare `$?` further down: `$?`
# after an `if` is the IF's status, and that mistake broke a sibling gate earlier today.
modules_pg_sweep "$ROOT/modules" >/dev/null
rc_modules_detect=$?
if [ "$rc_modules_detect" -ne 0 ]; then
	MODULES_VERDICT="./modules needs PG"
else
	MODULES_VERDICT="./modules needs no PG yet; server provisioned anyway"
fi

# No -job/-task: the program DISCOVERS the legs from the workflow itself, so a third -race
# leg is checked whether or not anybody registered it. This comment used to say the program
# "walks its OWN table of legs" — it stopped being true on 2026-08-01, when discovery replaced
# the table, and the sentence outlived the mechanism it described for a day. That is the same
# class of defect the round-4 contrast catalogued, sitting in the battery written to catch it.
pg_wiring "$WORKFLOW" "$TASKFILE"
check "every -race leg gives its tree a server and uses the wrapper" "${MODULES_VERDICT}" $?

pg_image_agreement "$WORKFLOW"
check "every Postgres service in mainline-ci is the SAME pinned image" "no digest drift" $?

# --- RED half of the wiring rows ---------------------------------------------------------
# Mutants are made by LINE SURGERY on the real files, not by a decoder round-trip: a
# re-encoded copy loses every comment and normalises the shape, so it stops being the file
# a human edits. awk rather than sed because each mutation must be confined to ONE job —
# race-rest carries a byte-identical `image:` line and its own `services:` block, and a
# global substitution would hit both and prove nothing about job scoping, which is the
# exact property this reader exists to have.
ZERO_DIGEST="$(printf '0%.0s' {1..64})"
mutate_workflow() { # mutate_workflow <mutation> <job> <in> <out>
	awk -v mut="$1" -v target="  $2:" -v zeros="$ZERO_DIGEST" '
		$0 == target { injob = 1; print; next }
		/^  [a-z][a-z0-9_-]*:[[:space:]]*$/ { injob = 0 }
		{
			if (injob && mut == "drop-service") {
				if ($0 == "    services:") { skip = 1; next }
				if (skip == 1) {
					if ($0 ~ /^      / || $0 ~ /^[[:space:]]*$/) next
					skip = 0
				}
			}
			if (injob && $0 ~ /image:[[:space:]]/) {
				if (mut == "unpin-image") sub(/@sha256:[0-9a-f]+/, "")
				if (mut == "drift-digest") sub(/@sha256:[0-9a-f]+/, "@sha256:" zeros)
			}
			print
		}
	' "$3" >"$4"
}

# A mutation that did not apply is a fixture that proves nothing while looking green — the
# failure this campaign hit for real earlier today, twice. So it is SETUP, and setup that
# does not take is reported as a failure rather than skipped.
require_mutation() { # require_mutation <label> <original> <mutant>
	if [ ! -s "$3" ] || cmp -s "$2" "$3"; then
		fail=$((fail + 1))
		setup_fail=$((setup_fail + 1)) # a fixture, not a case: it must not enter the case sum
		printf '  FAIL  %-58s %s\n' "fixture '$1'" "MUTATION DID NOT APPLY — it would prove nothing"
		return 1
	fi
	return 0
}

if mutate_workflow drop-service race-modules "$WORKFLOW" "$WORK/wf-noservice.yml" &&
	require_mutation "race-modules without its service" "$WORKFLOW" "$WORK/wf-noservice.yml"; then
	pg_wiring "$WORK/wf-noservice.yml" "$TASKFILE" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/nosvc.err"
	rc_nosvc=$?
	[ "$rc_nosvc" -eq 1 ] && grep -q "declares no \`postgres\` service" "$WORK/nosvc.err"
	check "race-modules losing its Postgres service turns this red" "exit 1, names the service" $?
else
	unexercised "race-modules losing its Postgres service turns this red"
fi

# The wrapper mutation is a single unique line, so sed is exact here; the guard above still
# proves it landed.
sed "s|bash scripts/with-pg-env.sh bash -c 'cd modules|bash -c 'cd modules|" \
	"$TASKFILE" >"$WORK/tf-nowrapper.yml"
if require_mutation "the modules recipe unwrapped" "$TASKFILE" "$WORK/tf-nowrapper.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-nowrapper.yml" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/nowrap.err"
	rc_nowrap=$?
	[ "$rc_nowrap" -eq 1 ] && grep -q "without with-pg-env.sh" "$WORK/nowrap.err"
	check "the task losing with-pg-env.sh turns this red" "exit 1, names the recipe" $?
else
	unexercised "the task losing with-pg-env.sh turns this red"
fi

if mutate_workflow unpin-image race-modules "$WORKFLOW" "$WORK/wf-unpinned.yml" &&
	require_mutation "race-modules pinned by tag only" "$WORKFLOW" "$WORK/wf-unpinned.yml"; then
	pg_wiring "$WORK/wf-unpinned.yml" "$TASKFILE" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/unpin.err"
	rc_unpin=$?
	[ "$rc_unpin" -eq 1 ] && grep -q "not pinned by digest" "$WORK/unpin.err"
	check "a Postgres service pinned by TAG only turns this red" "exit 1, names the pin" $?
else
	unexercised "a Postgres service pinned by TAG only turns this red"
fi

if mutate_workflow drift-digest race-modules "$WORKFLOW" "$WORK/wf-drift.yml" &&
	require_mutation "one leg's digest drifted" "$WORKFLOW" "$WORK/wf-drift.yml"; then
	pg_image_agreement "$WORK/wf-drift.yml" >/dev/null 2>"$WORK/drift.err"
	rc_drift=$?
	[ "$rc_drift" -eq 1 ] && grep -q "disagree on the image" "$WORK/drift.err"
	check "one leg's digest drifting from the other's turns this red" "exit 1, both shown" $?
else
	unexercised "one leg's digest drifting from the other's turns this red"
fi

# A THIRD -RACE LEG THAT NOBODY REGISTERED (2026-08-01). Until today the verifier iterated a
# table of two jobs compiled into it, and its own header claimed that adding a third leg without
# an entry "is a decision somebody has to make in a diff, not an omission nobody sees". Nothing
# made that true. The round-4 contrast added a valid job that raced ./modules with no Postgres
# service and the verifier exited 0, printing its two inventoried rows. The legs are discovered
# from the workflow now, and this case is what keeps that so: the fixture is a whole new job,
# not a field edited inside an existing one, because that is the shape of the next partition of
# the race suite.
cat "$WORKFLOW" >"$WORK/wf-thirdleg.yml"
printf '\n  race-undetected:\n    runs-on: ubuntu-latest\n    steps:\n      - run: task test:race-hot:modules\n' \
	>>"$WORK/wf-thirdleg.yml"
if require_mutation "an unregistered third race leg" "$WORKFLOW" "$WORK/wf-thirdleg.yml"; then
	pg_wiring "$WORK/wf-thirdleg.yml" "$TASKFILE" >/dev/null 2>"$WORK/thirdleg.err"
	rc_thirdleg=$?
	[ "$rc_thirdleg" -eq 1 ] && grep -q "race-undetected" "$WORK/thirdleg.err"
	check "a THIRD race leg with no service turns this red" "exit 1, names the new job" $?
else
	unexercised "a THIRD race leg with no service turns this red"
fi

# THE LOAD-BEARING LAYER (2026-08-02, after the sixth contrast). Everything below about
# workflow spellings is the NET. This is the GUARANTEE, and the cases below are what make it one:
# every `test:race*` target routes its `go test` through with-pg-env.sh EXCEPT where it reaches a
# reviewed exemption; that wrapper refuses without a Postgres posture IN THE FAIL-CLOSED DEFAULT
# AND IN PROMOTION, which are the regimes CI runs (LOCAL opt-in exits 0 by design); and the
# exemption list the checker honours is the same one this battery verifies the premise of.
# Both qualifications are in this sentence rather than three paragraphs below it, because a
# correction that arrives after the universal claim does not retract it.
#
# The reason the layers were swapped is measured, not preferred. Three rules tried to establish
# the guarantee by reading the WORKFLOW's shell and adversarial contrast broke all three — six
# spellings, then four more plus a false positive, then seventeen. A Taskfile, by contrast, is
# YAML: the set of targets whose name starts with test:race is enumerable exactly.
cat "$TASKFILE" >"$WORK/tf-unwrapped.yml"
sed -i "s|bash scripts/with-pg-env.sh bash -c 'cd modules|bash -c 'cd modules|" "$WORK/tf-unwrapped.yml"
if require_mutation "a race target stripped of its wrapper" "$TASKFILE" "$WORK/tf-unwrapped.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-unwrapped.yml" >/dev/null 2>"$WORK/unwrapped.err"
	rc_unwrapped=$?
	[ "$rc_unwrapped" -eq 1 ] && grep -q "WITHOUT with-pg-env.sh" "$WORK/unwrapped.err"
	check "a race target losing its wrapper turns this red" "exit $rc_unwrapped, names the target" $?
else
	unexercised "a race target losing its wrapper turns this red"
fi

# And the other half of the chain: the wrapper the targets route through must REFUSE to run
# tests when it cannot tell what Postgres it has. Without this, wrapping every target would
# guarantee nothing at all.
(
	unset OLIVARES_TEST_POSTGRES_DSN OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_TEST_POSTGRES_ADMIN_DSN
	bash "$ROOT/scripts/with-pg-env.sh" true
) >"$WORK/wrapper-closed.out" 2>&1
rc_wrapper=$?
[ "$rc_wrapper" -ne 0 ] && grep -q "refusing to run tests" "$WORK/wrapper-closed.out"
check "the wrapper REFUSES to run tests with an unknown Postgres posture" "exit $rc_wrapper" $?

# THE PREMISE THE GUARANTEE RESTS ON. with-pg-env.sh only refuses to run tests without a Postgres
# posture in the fail-closed default and in PROMOTION. Under the LOCAL opt-in an absent server is
# exit 0 and the child RUNS — measured: a real Postgres test then reports SKIP, the suite reports
# PASS, exit 0. That is deliberate, so a developer without a server can still run the suite, and
# the pre-push hook is its one sanctioned caller. It stops being deliberate the moment CI enables
# it, and "no workflow job does" is a premise, so it is asserted rather than assumed.
cat "$WORKFLOW" >"$WORK/wf-localoptin.yml"
printf '\n  race-local-optin:\n    runs-on: ubuntu-latest\n    env:\n      OLIVARES_PG_LOCAL_DEFAULTS: "1"\n    steps:\n      - run: task test:race-hot:modules\n' \
	>>"$WORK/wf-localoptin.yml"
if require_mutation "a job enabling the LOCAL regime" "$WORKFLOW" "$WORK/wf-localoptin.yml"; then
	pg_wiring "$WORK/wf-localoptin.yml" "$TASKFILE" >/dev/null 2>"$WORK/localoptin.err"
	rc_local=$?
	[ "$rc_local" -eq 1 ] && grep -q "OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/localoptin.err"
	check "a CI job enabling the LOCAL regime turns this red" "exit $rc_local, names the opt-in" $?
else
	unexercised "a CI job enabling the LOCAL regime turns this red"
fi

# THE FIVE ESCAPES THE TENTH CONTRAST BUILT, pinned. Four of them walked past a guard that read a
# recipe as TEXT instead of as commands, and the fifth ran in a Task surface this program was not
# reading at all. `status:` is the serious one: its commands execute BEFORE the recipe and their
# exit status decides whether the recipe runs, so an unwrapped go test there both runs and can
# declare the target up to date — measured, a status exiting 0 left a recipe returning 99 unrun.
tenth_case() { # tenth_case <label> <expected-exit> <needle> <workflow> <taskfile>
	"$PGWIRING" -workflow "$4" -taskfile "$5" -root "$ROOT" >/dev/null 2>"$WORK/tenth.err"
	rc_tenth=$?
	[ "$rc_tenth" -eq "$2" ] && grep -q "$3" "$WORK/tenth.err"
	check "$1" "exit $rc_tenth" $?
}

python3 - "$TASKFILE" "$WORK/tf-decoywrap.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
old = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = "      - echo scripts/with-pg-env.sh >/dev/null; bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(old, new, 1) if old in s else s)
PY
if require_mutation "a decoy mention of the wrapper" "$TASKFILE" "$WORK/tf-decoywrap.yml"; then
	tenth_case "a go test hidden behind a decoy mention of the wrapper turns this red" 1 \
		"WITHOUT with-pg-env.sh" "$WORKFLOW" "$WORK/tf-decoywrap.yml"
else
	unexercised "a go test hidden behind a decoy mention of the wrapper turns this red"
fi

python3 - "$TASKFILE" "$WORK/tf-status.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
old = "  test:race-hot:modules:\n"
new = "  test:race-hot:modules:\n    status:\n      - cd modules && go test -count=1 ./eventing/...\n"
open(sys.argv[2], 'w').write(s.replace(old, new, 1) if old in s else s)
PY
if require_mutation "an executable status: running go test" "$TASKFILE" "$WORK/tf-status.yml"; then
	tenth_case "an unwrapped go test in a status: turns this red" 1 \
		"WITHOUT with-pg-env.sh" "$WORKFLOW" "$WORK/tf-status.yml"
else
	unexercised "an unwrapped go test in a status: turns this red"
fi

python3 - "$TASKFILE" "$WORK/tf-echoargs.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
old = "      - bash scripts/with-pg-env.sh bash scripts/private-leg.sh test:cloud cloud/control-plane go test -race -count=1 -timeout 10m ./..."
new = "      - echo go test cloud/control-plane >/dev/null; cd modules && go test -race -count=1 -timeout 10m ./..."
open(sys.argv[2], 'w').write(s.replace(old, new, 1) if old in s else s)
PY
if require_mutation "go test as arguments to echo" "$TASKFILE" "$WORK/tf-echoargs.yml"; then
	tenth_case "'go test' as arguments to a command that does not run them turns this red" 1 \
		"no longer mentions" "$WORKFLOW" "$WORK/tf-echoargs.yml"
else
	unexercised "'go test' as arguments to a command that does not run them turns this red"
fi

python3 - "$TASKFILE" "$WORK/tf-comment.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
old = "      - bash scripts/with-pg-env.sh bash scripts/private-leg.sh test:cloud cloud/control-plane go test -race -count=1 -timeout 10m ./..."
new = "      - cd modules # cloud/control-plane\n      - go test -race -count=1 -timeout 10m ./..."
open(sys.argv[2], 'w').write(s.replace(old, new, 1) if old in s else s)
PY
if require_mutation "the exempt tree hidden in a shell comment" "$TASKFILE" "$WORK/tf-comment.yml"; then
	tenth_case "the exempt tree named only inside a shell comment turns this red" 1 \
		"no longer mentions" "$WORKFLOW" "$WORK/tf-comment.yml"
else
	unexercised "the exempt tree named only inside a shell comment turns this red"
fi

cat "$WORKFLOW" >"$WORK/wf-exportopt.yml"
printf '\n  race-export-opt:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          export -- OLIVARES_PG_LOCAL_DEFAULTS=1\n          task test:race-hot:modules\n' \
	>>"$WORK/wf-exportopt.yml"
if require_mutation "export with an option enabling LOCAL" "$WORKFLOW" "$WORK/wf-exportopt.yml"; then
	tenth_case "\`export -- NAME=value\` enabling LOCAL turns this red" 1 \
		"OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/wf-exportopt.yml" "$TASKFILE"
else
	unexercised "\`export -- NAME=value\` enabling LOCAL turns this red"
fi

# THE SIXTH ESCAPE, and the one that says most about the two layers. A `go test` reached through a
# runtime variable — `T='go test'; cd modules && $T ./...` — DID turn the whole program red before
# this case existed. But the finding came from layer 2, the workflow net, whose text search still
# saw the literal `go test` in the recipe; layer 1 was silent. Reading a green out of that is the
# exact mistake the split was made to stop: the net is DECLARED incomplete, so it is not allowed
# to be what catches something on the guarantee's behalf.
#
# Layer 1 claims completeness, so anything it cannot read is UNVERIFIED. The case therefore forces
# the layer-1 path with -job/-task and demands exit 2, not merely "not zero".
python3 - "$TASKFILE" "$WORK/tf-varcmd.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
old = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = ("      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./eventing/...'\n"
       "      - T='go test'; cd modules && $T -count=1 ./eventing/...")
open(sys.argv[2], 'w').write(s.replace(old, new, 1) if old in s else s)
PY
if require_mutation "a go test reached through a runtime variable" "$TASKFILE" "$WORK/tf-varcmd.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-varcmd.yml" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/varcmd.err"
	rc_varcmd=$?
	[ "$rc_varcmd" -eq 2 ] && grep -q "cannot be resolved statically" "$WORK/varcmd.err"
	check "LAYER 1 ALONE answers UNVERIFIED to an unreadable executable" "exit $rc_varcmd" $?
else
	unexercised "LAYER 1 ALONE answers UNVERIFIED to an unreadable executable"
fi

# DENY-CLOSED OVER GO-TASK'S OWN GRAMMAR (2026-08-04). Eleven rounds of contrast each found
# another Taskfile key that executes — cmds, deps, status, defer, vars.sh, env.sh — because the
# checker enumerated the keys it knew how to READ. That is a blacklist wearing a whitelist's
# clothes: it passes every form nobody has written down yet.
#
# The default is inverted now. Every key a race target carries is looked up in one table, and a
# key absent from it makes the answer UNVERIFIED *by name*. These cases are the proof, one per
# executing key plus the unknown-key gate itself, and they are what stops the table from being
# quietly widened without a mutation to match.
key_case() { # key_case <label> <expected-exit> <needle> <inject-after-target-header>
	printf '%b' "$4" >"$WORK/inject.txt"
	python3 - "$TASKFILE" "$WORK/tf-key.yml" "$WORK/inject.txt" <<'PY'
import sys
s = open(sys.argv[1]).read()
inject = open(sys.argv[3]).read()
old = "  test:race-hot:modules:\n"
open(sys.argv[2], 'w').write(s.replace(old, old + inject, 1) if old in s else s)
PY
	if require_mutation "taskfile key: $1" "$TASKFILE" "$WORK/tf-key.yml"; then
		pg_wiring "$WORKFLOW" "$WORK/tf-key.yml" race-modules test:race-hot:modules \
			>/dev/null 2>"$WORK/key.err"
		rc_key=$?
		[ "$rc_key" -eq "$2" ] && grep -q "$3" "$WORK/key.err"
		check "an unwrapped go test in \`$1\` turns this red" "exit $rc_key" $?
	else
		unexercised "an unwrapped go test in \`$1\` turns this red"
	fi
}

# `&&`, NOT `\&\&` — and the difference was four cases passing for the wrong reason (2026-08-04).
# `printf '%b'` does not recognise `\&` as an escape, so it emitted the backslashes verbatim and
# every one of these fixtures injected `cd modules \&\& go test …`. In a shell that is `cd` with
# the arguments `modules && go test …`: it runs NO test, and bash answers `cd: too many arguments`.
# The four rows were green over a recipe that could not commit the offence they name. They still
# went red, because the checker's own reader treated `\&` as an escaped literal and then found the
# words `go` and `test` in the same command — the right answer by the wrong route, and one that
# stopped being given the moment `cd` was classified as a command that starts nothing. A fixture
# whose mutation is not the mutation in its label proves nothing about the property in its label.
key_case "status:" 1 "WITHOUT with-pg-env.sh" \
	'    status:\n      - cd modules && go test -count=1 ./eventing/...\n'
key_case "preconditions:" 1 "WITHOUT with-pg-env.sh" \
	'    preconditions:\n      - cd modules && go test -count=1 ./eventing/...\n'
key_case "vars.<name>.sh" 1 "WITHOUT with-pg-env.sh" \
	'    vars:\n      X:\n        sh: cd modules && go test -count=1 ./eventing/...\n'
key_case "env.<name>.sh" 1 "WITHOUT with-pg-env.sh" \
	'    env:\n      Y:\n        sh: cd modules && go test -count=1 ./eventing/...\n'

# The gate itself: a key this program does not model must be UNVERIFIED, and must SAY WHICH KEY.
# A generic "could not look" teaches nobody anything on the day a new one appears.
key_case "an unmodelled target key" 2 '"watch"' '    watch: true\n'

# And the same at recipe-entry level, injected into the EXISTING cmds list — a second `cmds:` key
# is invalid YAML and would turn the row red for the wrong reason.
python3 - "$TASKFILE" "$WORK/tf-entry.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - unknown_entry_key: whatever", 1) if a in s else s)
PY
if require_mutation "an unmodelled cmds entry key" "$TASKFILE" "$WORK/tf-entry.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-entry.yml" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/entry.err"
	rc_entry=$?
	[ "$rc_entry" -eq 2 ] && grep -q '"unknown_entry_key"' "$WORK/entry.err"
	check "an unmodelled cmds entry key is UNVERIFIED, by name" "exit $rc_entry" $?
else
	unexercised "an unmodelled cmds entry key is UNVERIFIED, by name"
fi

# `defer:` runs after the recipe and is an ENTRY key, so it goes in the same list.
python3 - "$TASKFILE" "$WORK/tf-defer.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - defer: cd modules && go test -count=1 ./eventing/...", 1) if a in s else s)
PY
if require_mutation "a defer: running an unwrapped go test" "$TASKFILE" "$WORK/tf-defer.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-defer.yml" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/defer.err"
	rc_defer=$?
	[ "$rc_defer" -eq 1 ] && grep -q "WITHOUT with-pg-env.sh" "$WORK/defer.err"
	check "an unwrapped go test in a cmds defer: turns this red" "exit $rc_defer" $?
else
	unexercised "an unwrapped go test in a cmds defer: turns this red"
fi

# An exemption cannot follow a target that relocates itself: `dir:` decides where the recipe runs,
# and a traversal that CONTAINS the declared tree as a substring resolves somewhere else.
python3 - "$TASKFILE" "$WORK/tf-dir.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
old = "  test:cloud:\n"
open(sys.argv[2], 'w').write(s.replace(old, old + "    dir: cloud/control-plane/../../modules\n", 1) if old in s else s)
PY
if require_mutation "an exempt target relocated by dir:" "$TASKFILE" "$WORK/tf-dir.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-dir.yml" >/dev/null 2>"$WORK/dir.err"
	rc_dir=$?
	[ "$rc_dir" -eq 1 ] && grep -q "no longer mentions" "$WORK/dir.err"
	check "a dir: traversal must not carry the exemption with it" "exit $rc_dir" $?
else
	unexercised "a dir: traversal must not carry the exemption with it"
fi

# THE TABLE MUST DRIVE THE WALK, not merely filter names (2026-08-04). The first inversion
# declared shell/targets/dir flags that NO CODE READ: the tables were membership filters, so
# "adding a key is one line" was false — the line silenced the unknown-key gate and connected
# nothing. These cases pin the flags to behaviour.
python3 - "$TASKFILE" "$WORK/tf-dirsib.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
o = "  test:cloud:\n"
open(sys.argv[2], 'w').write(s.replace(o, o + "    dir: cloud/control-plane-evil\n", 1) if o in s else s)
PY
if require_mutation "an exempt target relocated to a sibling of its tree" "$TASKFILE" "$WORK/tf-dirsib.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-dirsib.yml" >/dev/null 2>"$WORK/dirsib.err"
	rc_ds=$?
	[ "$rc_ds" -eq 1 ] && grep -q "no longer mentions" "$WORK/dirsib.err"
	check "a dir: that merely CONTAINS the exempt tree does not inherit its excuse" "exit $rc_ds" $?
else
	unexercised "a dir: that merely CONTAINS the exempt tree does not inherit its excuse"
fi

python3 - "$TASKFILE" "$WORK/tf-tenv.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
o = "  test:race-hot:modules:\n"
open(sys.argv[2], 'w').write(s.replace(o, o + '    env:\n      OLIVARES_PG_LOCAL_DEFAULTS: "1"\n', 1) if o in s else s)
PY
if require_mutation "a Task target env selecting LOCAL" "$TASKFILE" "$WORK/tf-tenv.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-tenv.yml" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/tenv.err"
	rc_te=$?
	[ "$rc_te" -eq 2 ] && grep -q "OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/tenv.err"
	check "a Task target's OWN env selecting LOCAL is UNVERIFIED" "exit $rc_te" $?
else
	unexercised "a Task target's OWN env selecting LOCAL is UNVERIFIED"
fi

# GO-TASK TEMPLATING (2026-08-04). `vars: {GO_CMD: go}` with `{{.GO_CMD}} test ./...` expands at
# run time to a real, unwrapped `go test` — the thirteenth contrast ran it and watched the test
# process start while this checker exited 0. Templating belongs to go-task, not to the shell, so
# the shell reader cannot resolve it; by this program's own rule, what it cannot resolve is
# UNVERIFIED rather than assumed harmless.
python3 - "$TASKFILE" "$WORK/tf-tmpl.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
o = "  test:race-hot:modules:\n"
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
if o in s and a in s:
    s = s.replace(o, o + "    vars:\n      GO_CMD: go\n", 1)
    s = s.replace(a, a + "\n      - cd modules && {{.GO_CMD}} test -count=1 ./eventing/...", 1)
open(sys.argv[2], 'w').write(s)
PY
if require_mutation "a go test behind go-task templating" "$TASKFILE" "$WORK/tf-tmpl.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-tmpl.yml" race-modules test:race-hot:modules \
		>/dev/null 2>"$WORK/tmpl.err"
	rc_tmpl=$?
	[ "$rc_tmpl" -eq 2 ] && grep -q "cannot be resolved statically" "$WORK/tmpl.err"
	check "a templated command head is UNVERIFIED, never assumed harmless" "exit $rc_tmpl" $?
else
	unexercised "a templated command head is UNVERIFIED, never assumed harmless"
fi

# ---------------------------------------------------------------------------------------------
# THE EXECUTED GRAPH, NOT THE READABLE ONE (2026-08-04, fourteenth contrast).
#
# The table above closed the KEYS a race target may carry. The contrast then walked round it five
# times with valid go-task input whose keys were all in the table, because what escaped was not a
# key: it was a COMMAND the checker declined to classify. Each fixture below was built, executed
# against a `go` stub, and watched to print an unwrapped canary while the checker exited 0 —
# `defer: {task: …}`, a shell whose executable is `task` reaching a target held in `includes:`, a
# helper script, and that same helper named from `vars.<name>.sh` and `env.<name>.sh`.
#
# The fix is the same inversion, one level down: a command a race recipe runs is classified or it
# is UNVERIFIED. These rows are what stops that from being quietly widened.
cat "$TASKFILE" >"$WORK/tf-defertask.yml"
cat >>"$WORK/tf-defertask.yml" <<'YAML'

  pgprobe:unwrapped:
    cmds:
      - bash -c 'cd modules && go test -count=1 ./eventing/...'
YAML
python3 - "$WORK/tf-defertask.yml" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(p, 'w').write(s.replace(a, a + "\n      - defer: {task: pgprobe:unwrapped}", 1) if a in s else s)
PY
if require_mutation "a defer: calling another target" "$TASKFILE" "$WORK/tf-defertask.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-defertask.yml" >/dev/null 2>"$WORK/defertask.err"
	rc_dt=$?
	[ "$rc_dt" -eq 1 ] && grep -q "WITHOUT with-pg-env.sh" "$WORK/defertask.err"
	check "a defer: that CALLS a target is followed, not just read as shell" "exit $rc_dt" $?
else
	unexercised "a defer: that CALLS a target is followed, not just read as shell"
fi

# And the nested call mapping is judged by the same table as the entry above it.
python3 - "$TASKFILE" "$WORK/tf-defkey.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - defer: {task: test:race-hot:root, watch: true}", 1) if a in s else s)
PY
if require_mutation "an unmodelled key inside a nested call" "$TASKFILE" "$WORK/tf-defkey.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-defkey.yml" >/dev/null 2>"$WORK/defkey.err"
	rc_dk=$?
	[ "$rc_dk" -eq 2 ] && grep -q '"watch"' "$WORK/defkey.err"
	check "an unmodelled key INSIDE a nested call is UNVERIFIED, by name" "exit $rc_dk" $?
else
	unexercised "an unmodelled key INSIDE a nested call is UNVERIFIED, by name"
fi

# A recipe that shells out to go-task is delegating. The target it names lives in a top-level
# `includes:` file this program never decodes, so the honest answer is that it could not look —
# not exit 0 over a `go test` it never saw.
python3 - "$TASKFILE" "$WORK/tf-shelltask.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - task inc:unwrapped", 1) if a in s else s)
PY
if require_mutation "a shell command whose executable is task" "$TASKFILE" "$WORK/tf-shelltask.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-shelltask.yml" >/dev/null 2>"$WORK/shelltask.err"
	rc_st=$?
	[ "$rc_st" -eq 2 ] && grep -q 'inc:unwrapped' "$WORK/shelltask.err"
	check "a recipe delegating through \`task <target>\` is followed by name" "exit $rc_st" $?
else
	unexercised "a recipe delegating through \`task <target>\` is followed by name"
fi

# EXECUTABLE INDIRECTION. The recipe hands work to a program this gate has not read. It does not
# matter what that program does today; what matters is that the gate cannot say, and said clean.
python3 - "$TASKFILE" "$WORK/tf-helper.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
# export-closure: fixture scripts/pgprobe-hidden.sh — this path is INJECTED into a copy of
# the Taskfile as a deliberate mutant, to prove the wiring gate refuses a recipe that hands
# work to an unreviewed helper. It must never exist: if it ever did, this case would stop
# testing anything.
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - bash scripts/pgprobe-hidden.sh", 1) if a in s else s)
PY
if require_mutation "a recipe running an unreviewed helper script" "$TASKFILE" "$WORK/tf-helper.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-helper.yml" >/dev/null 2>"$WORK/helper.err"
	rc_hp=$?
	[ "$rc_hp" -eq 2 ] && grep -q "does not model" "$WORK/helper.err"
	check "a recipe handing work to an unreviewed program is UNVERIFIED" "exit $rc_hp" $?
else
	unexercised "a recipe handing work to an unreviewed program is UNVERIFIED"
fi

# The same indirection with a different spelling: a command SUBSTITUTION runs before the command
# that holds it, and the real Taskfile has one — so this reads them, and says where it was found.
python3 - "$TASKFILE" "$WORK/tf-subst.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
# export-closure: fixture scripts/pgprobe-hidden.sh — same mutant, spelled as a command
# SUBSTITUTION so the case covers the other indirection. Deliberately absent, for the same
# reason as the declaration above: a mutant that resolves proves nothing.
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + '\n      - PGPROBE="$(bash scripts/pgprobe-hidden.sh)"', 1) if a in s else s)
PY
if require_mutation "a program run inside a command substitution" "$TASKFILE" "$WORK/tf-subst.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-subst.yml" >/dev/null 2>"$WORK/subst.err"
	rc_sb=$?
	[ "$rc_sb" -eq 2 ] && grep -q "command substitution" "$WORK/subst.err"
	check "a command substitution is READ, and named where it was found" "exit $rc_sb" $?
else
	unexercised "a command substitution is READ, and named where it was found"
fi

# WRAPPER IDENTITY IS A FILE, NOT A NAME — and this is the one that failed OPEN. A runtime-expanded
# prefix leaves the expected basename visible after the unresolvable part is erased, so a
# counterfeit wrapper elsewhere in the tree ran while the checker printed that every go test went
# through with-pg-env.sh. Runtime expansion is by definition not resolvable here, so it must deny.
python3 - "$TASKFILE" "$WORK/tf-prefix.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
o = "  test:race-hot:modules:\n"
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
if o in s and a in s:
    s = s.replace(o, o + "    env:\n      WRAPPER_ROOT: relocated\n", 1)
    s = s.replace(a, a + '\n      - bash "$WRAPPER_ROOT/scripts/with-pg-env.sh" go test -count=1 ./eventing/...', 1)
open(sys.argv[2], 'w').write(s)
PY
if require_mutation "a wrapper path completed at run time" "$TASKFILE" "$WORK/tf-prefix.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-prefix.yml" >/dev/null 2>"$WORK/prefix.err"
	rc_px=$?
	[ "$rc_px" -eq 2 ] && grep -q "cannot resolve statically" "$WORK/prefix.err"
	check "a runtime-expanded wrapper path is UNVERIFIED, never assumed canonical" "exit $rc_px" $?
else
	unexercised "a runtime-expanded wrapper path is UNVERIFIED, never assumed canonical"
fi

# And the same protection failure with no variable at all: `dir:` decides where the recipe runs, so
# the identical relative path names a DIFFERENT FILE. The existing dir: rows above test an EXEMPT
# target's excuse; this one tests the wrapper of an ordinary race target, which nothing covered.
python3 - "$TASKFILE" "$WORK/tf-dirwrap.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
o = "  test:race-hot:modules:\n"
open(sys.argv[2], 'w').write(s.replace(o, o + "    dir: relocated\n", 1) if o in s else s)
PY
if require_mutation "an ordinary race target relocated by dir:" "$TASKFILE" "$WORK/tf-dirwrap.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-dirwrap.yml" >/dev/null 2>"$WORK/dirwrap.err"
	rc_dw=$?
	[ "$rc_dw" -eq 1 ] && grep -q "is not the wrapper" "$WORK/dirwrap.err"
	check "a dir: that relocates the wrapper's path turns this red" "exit $rc_dw" $?
else
	unexercised "a dir: that relocates the wrapper's path turns this red"
fi

# `set -a; source` SELECTS THE REGIME WITH NOTHING IN THE DIFF THAT NAMES IT. The fourteenth
# contrast ran the real, green wrapper on a test canary with no PostgreSQL posture at all this way:
# the load-bearing premise stayed green while the regime it asserts had been switched off.
printf 'OLIVARES_PG_LOCAL_DEFAULTS=1\n' >"$WORK/pgprobe-local.env"
{
	cat "$WORKFLOW"
	printf '\n  race-source-local:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          set -a\n          source %s\n          task test:race-hot:modules\n' "$WORK/pgprobe-local.env"
} >"$WORK/wf-source.yml"
if require_mutation "a job sourcing the LOCAL opt-in" "$WORKFLOW" "$WORK/wf-source.yml"; then
	pg_wiring "$WORK/wf-source.yml" "$TASKFILE" >/dev/null 2>"$WORK/source.err"
	rc_src=$?
	[ "$rc_src" -eq 1 ] && grep -q "which sets OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/source.err"
	check "a SOURCED file selecting LOCAL turns this red" "exit $rc_src" $?
else
	unexercised "a SOURCED file selecting LOCAL turns this red"
fi

# And a sourced file this program cannot read is UNVERIFIED, not clean: unreadable and harmless
# look identical from here, and only one of them is safe to report as clean.
{
	cat "$WORKFLOW"
	printf '\n  race-source-missing:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          source %s/pgprobe-absent.env\n          task test:race-hot:modules\n' "$WORK"
} >"$WORK/wf-srcmiss.yml"
if require_mutation "a job sourcing an unreadable file" "$WORKFLOW" "$WORK/wf-srcmiss.yml"; then
	pg_wiring "$WORK/wf-srcmiss.yml" "$TASKFILE" >/dev/null 2>"$WORK/srcmiss.err"
	rc_sm=$?
	[ "$rc_sm" -eq 2 ] && grep -q "cannot read" "$WORK/srcmiss.err"
	check "a SOURCED file this program cannot read is UNVERIFIED" "exit $rc_sm" $?
else
	unexercised "a SOURCED file this program cannot read is UNVERIFIED"
fi

# AN OPTION THAT TAKES A VALUE EATS THE NEXT WORD (2026-08-04, fifteenth contrast). `env -u
# scripts/with-pg-env.sh go test …` is valid and runs the tests unwrapped: `-u` consumes the
# wrapper path as its argument, and a reader that merely SKIPS options read that consumed word as
# the executable and called the command wrapped. This program models exactly one option, `-c`, and
# refuses every other by name.
python3 - "$TASKFILE" "$WORK/tf-optarg.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - env -u scripts/with-pg-env.sh go test -count=1 ./eventing/...", 1) if a in s else s)
PY
if require_mutation "an option consuming the wrapper path" "$TASKFILE" "$WORK/tf-optarg.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-optarg.yml" >/dev/null 2>"$WORK/optarg.err"
	rc_oa=$?
	[ "$rc_oa" -eq 2 ] && grep -q "models only" "$WORK/optarg.err"
	check "an option this program does not model is UNVERIFIED, by name" "exit $rc_oa" $?
else
	unexercised "an option this program does not model is UNVERIFIED, by name"
fi

# `readonly NAME=value` is an assignment with a keyword in front, like `export` — and it was the
# one keyword the shared predicate did not know. Under `set -a` it is exported like any other.
printf 'readonly OLIVARES_PG_LOCAL_DEFAULTS=1\n' >"$WORK/pgprobe-ro.env"
{
	cat "$WORKFLOW"
	printf '\n  race-source-readonly:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          set -a\n          source %s\n          task test:race-hot:modules\n' "$WORK/pgprobe-ro.env"
} >"$WORK/wf-ro.yml"
if require_mutation "a job sourcing a readonly LOCAL opt-in" "$WORKFLOW" "$WORK/wf-ro.yml"; then
	pg_wiring "$WORK/wf-ro.yml" "$TASKFILE" >/dev/null 2>"$WORK/ro.err"
	rc_ro=$?
	[ "$rc_ro" -eq 1 ] && grep -q "which sets OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/ro.err"
	check "a \`readonly\` assignment of the LOCAL opt-in is seen too" "exit $rc_ro" $?
else
	unexercised "a \`readonly\` assignment of the LOCAL opt-in is seen too"
fi

# ENTERING THE WRAPPER IS NOT THE END OF THE QUESTION (2026-08-04, fifteenth contrast). The
# argument that carried the whole previous change — with-pg-env.sh decides the posture and execs
# its argv, so everything downstream is safe — is false when the argv itself undoes the decision.
# Measured: `with-pg-env.sh env -u <the three DSNs> go test …` ran the tests with no posture at
# all, checker exit 0. Two spellings, two rows: an option to a runner inside the argv, and an
# `unset` in a shell the wrapper is handed.
python3 - "$TASKFILE" "$WORK/tf-cleared.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n      - bash scripts/with-pg-env.sh env -u OLIVARES_TEST_POSTGRES_DSN go test -count=1 ./eventing/...", 1) if a in s else s)
PY
if require_mutation "the posture cleared by an option inside the argv" "$TASKFILE" "$WORK/tf-cleared.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-cleared.yml" >/dev/null 2>"$WORK/cleared.err"
	rc_cl=$?
	[ "$rc_cl" -eq 2 ] && grep -q "INSIDE scripts/with-pg-env.sh's argv" "$WORK/cleared.err"
	check "an unmodelled option INSIDE the wrapper's argv is UNVERIFIED" "exit $rc_cl" $?
else
	unexercised "an unmodelled option INSIDE the wrapper's argv is UNVERIFIED"
fi

# AND THE ARGV WALK DOES NOT STOP AT THE FIRST UNRECOGNISED PROGRAM (2026-08-05, sixteenth
# contrast). The row above closed `wrapper env -u … go test`; inserting ONE ordinary executable in
# front of that same `env -u` restored the false green, because the walk returned success at the
# first executable outside shellRunners — "a real program: the rest are its own arguments". It is
# not: an ordinary program runs the words after it. Measured on the reviewed SHA: checker 0,
# go-task 3.51.1 exit 0, and the executed `go` reporting all three DSNs absent from INSIDE the
# wrapper.
#
# THE PROGRAM HERE IS DELIBERATELY NOT THE ONE THE CONTRAST USED. The contrast wrote `nice`; a fix
# that recognised `nice` would pass its fixture and fail the next contrast's `nohup`. So this row
# uses `nohup` and the finding must come from the DEFAULT — an unrecognised head is UNVERIFIED —
# rather than from any entry naming either of them.
python3 - "$TASKFILE" "$WORK/tf-ordinary.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = "      - bash scripts/with-pg-env.sh env WRAPPER_ENTERED=inside nohup env -u OLIVARES_TEST_POSTGRES_DSN go test -count=1 ./eventing/..."
open(sys.argv[2], 'w').write(s.replace(a, a + "\n" + new, 1) if a in s else s)
PY
if require_mutation "an ordinary executable hiding the clear inside the argv" "$TASKFILE" "$WORK/tf-ordinary.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-ordinary.yml" >/dev/null 2>"$WORK/ordinary.err"
	rc_ord=$?
	[ "$rc_ord" -eq 2 ] && grep -q 'to "nohup", which this program does not model' "$WORK/ordinary.err"
	check "an ordinary executable INSIDE the wrapper's argv is UNVERIFIED" "exit $rc_ord" $?
else
	unexercised "an ordinary executable INSIDE the wrapper's argv is UNVERIFIED"
fi

# AND THE SAME ROW WITH THE POSTURE REMOVED WITHOUT NAMING IT. The row above spells the variable
# out, so TWO current rules cover it — the unknown-head walk and the arm that reports a decided
# name handed to a non-data command — and withdrawing either one leaves the other answering. That
# makes it a fine regression row and a POOR isolation of the walk, which the eighteenth contrast
# measured and said: withdrawing the walk moved that input only from 2 to 1, never to accepted.
# `env -i` removes the whole environment while naming nothing, so this row answers about the walk
# and about nothing else — withdrawing it takes this input to exit 0.
python3 - "$TASKFILE" "$WORK/tf-unnamed.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = "      - bash scripts/with-pg-env.sh nice env -i PATH=\"$PATH\" go test -count=1 ./eventing/..."
open(sys.argv[2], 'w').write(s.replace(a, a + "\n" + new, 1) if a in s else s)
PY
if require_mutation "the whole environment dropped without naming a variable" "$TASKFILE" "$WORK/tf-unnamed.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-unnamed.yml" >/dev/null 2>"$WORK/unnamed.err"
	rc_unnamed=$?
	[ "$rc_unnamed" -eq 2 ] && grep -q 'to "nice", which this program does not model' "$WORK/unnamed.err"
	check "the walk alone answers when nothing is named" "exit $rc_unnamed" $?
else
	unexercised "the walk alone answers when nothing is named"
fi

# AND ONE QUOTE DEEPER, which is the same input inside the `-c` body the wrapper is handed. The row
# above was closed first and this one was measured OPEN straight afterwards: the argv walk and the
# reader for a `-c` body were two functions, only one of them was hardened, and
# `wrapper bash -c '<ordinary> env -u <DSN> go test …'` returned exit 0 while the row above
# returned 2. They are one function now (argvChain), and this row is what keeps them one.
python3 - "$TASKFILE" "$WORK/tf-ordinary-c.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = "      - bash scripts/with-pg-env.sh bash -c 'nohup env -u OLIVARES_TEST_POSTGRES_DSN go test -count=1 ./eventing/...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n" + new, 1) if a in s else s)
PY
if require_mutation "an ordinary executable hiding the clear inside a -c body" "$TASKFILE" "$WORK/tf-ordinary-c.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-ordinary-c.yml" >/dev/null 2>"$WORK/ordinary-c.err"
	rc_ordc=$?
	[ "$rc_ordc" -eq 2 ] && grep -q 'to "nohup", which this program does not model' "$WORK/ordinary-c.err" &&
		grep -q 'via -c' "$WORK/ordinary-c.err"
	check "the same input one quote deeper, in a -c body, is UNVERIFIED too" "exit $rc_ordc" $?
else
	unexercised "the same input one quote deeper, in a -c body, is UNVERIFIED too"
fi

# THE SCRIPT THE ARGV WALK IS ALLOWED THROUGH IS READ, NOT TRUSTED. test:race-hot:workspace puts
# scripts/go-work-each.sh inside the wrapper's argv, so the walk has to continue past it to reach
# `go test` — and a walk that continues past a file without reading it is the same fail-open one
# level along. argvRunnerScripts states a premise (it touches no OLIVARES_* variable of its own)
# and the checker enforces it with the same posture reader it uses on the argv. Both halves run:
# the real script green, a script that clears the posture red.
argvrunner_fixture() { # argvrunner_fixture <dir> <go-work-each body>
	rm -rf "$1"
	mkdir -p "$1/scripts"
	cp "$TASKFILE" "$1/Taskfile.yml"
	ln -s "$ROOT/.github" "$1/.github"
	# ⛔ EL ARBOL DE GUIONES ENTERO, no una pareja elegida a mano. Medido el 2026-08-18: un
	# paso NUEVO del workflow que sourcea `scripts/lib/exec-workdir.sh` dejo CINCO casos de
	# esta bateria en rojo — y no porque el paso estuviera mal, sino porque el root falso no
	# llevaba ese fichero y el comprobador contesta UNVERIFIED sobre el ROOT en vez de sobre
	# lo que la fila pregunta. Una lista escrita a mano de dependencias envejece cada vez que
	# alguien toca el workflow, y el rojo aparece lejos de la causa.
	cp -R "$ROOT/scripts/." "$1/scripts/"
	printf '%s\n' "$2" >"$1/scripts/go-work-each.sh"
	"$PGWIRING" -workflow "$1/.github/workflows/mainline-ci.yml" -taskfile "$1/Taskfile.yml" -root "$1"
}
argvrunner_fixture "$WORK/argvrunner-clean" "$(cat "$ROOT/scripts/go-work-each.sh")" \
	>/dev/null 2>"$WORK/arclean.err"
rc_ar_clean=$?
argvrunner_fixture "$WORK/argvrunner-clears" '#!/usr/bin/env bash
unset OLIVARES_TEST_POSTGRES_DSN
( cd . && "$@" )' >/dev/null 2>"$WORK/arclear.err"
rc_ar_clear=$?
[ "$rc_ar_clean" -eq 0 ] && [ "$rc_ar_clear" -eq 1 ] &&
	grep -q "unsets OLIVARES_TEST_POSTGRES_DSN" "$WORK/arclear.err"
check "a script the wrapper's argv runs is READ, not trusted" "clean=$rc_ar_clean cleared=$rc_ar_clear" $?

# AND READ WITH THE SAME WALK, not with a narrower reader. The row above was closed with a reader
# that saw only an assignment and an `unset` head, and the seventeenth contrast executed straight
# through it: an ordinary head inside the reviewed script carrying `env -u` of all three DSNs gave
# checker 0, real `task test:race-hot:workspace` 0, and the observed `go` reported
# `wrapper=inside super=absent app=absent admin=absent`. The script is walked by argvChain now, the
# same walk the argv itself gets, which is why this file's own go-work-each.sh had to become
# readable to it (function definitions, `mapfile`, `local`, `$@`).
argvrunner_fixture "$WORK/argvrunner-ordinary" '#!/usr/bin/env bash
set -euo pipefail
nohup env -u OLIVARES_TEST_POSTGRES_SUPERUSER_DSN -u OLIVARES_TEST_POSTGRES_DSN "$@"' \
	>/dev/null 2>"$WORK/arord.err"
rc_ar_ord=$?
[ "$rc_ar_ord" -eq 2 ] && grep -q 'to "nohup", which this program does not model' "$WORK/arord.err"
check "an ordinary head INSIDE the reviewed argv script is UNVERIFIED" "exit $rc_ar_ord" $?

# AND THE ARM THAT DOES NOT DEPEND ON RECOGNISING THE VERB. Every round found another spelling of
# "remove this variable" — an assignment, `unset`, `env -u`, the same behind an ordinary head, the
# same inside a reviewed script — and they all have to NAME the variable. So a bare word naming
# the decided namespace, handed to anything not known to take its words as data, is a finding on
# its own. Here the head is a function DEFINED IN THE SAME FILE, which the walk deliberately does
# not treat as an unread program; this arm is what still covers it.
argvrunner_fixture "$WORK/argvrunner-passthru" '#!/usr/bin/env bash
set -euo pipefail
passthru() { "$@"; }
passthru env -u OLIVARES_TEST_POSTGRES_DSN "$@"' >/dev/null 2>"$WORK/arpass.err"
rc_ar_pass=$?
[ "$rc_ar_pass" -eq 1 ] && grep -q "names OLIVARES_TEST_POSTGRES_DSN" "$WORK/arpass.err"
check "naming the decided namespace to a non-data command is a finding" "exit $rc_ar_pass" $?

# AND A CLEAR WHOSE TARGET THE READER CANNOT NAME. The two rows above key on the VERB and on the
# OBJECT; the third pass of the seventeenth contrast supplied an input where the verb is visible
# and the object is not — the three decided names put in an array and cleared by
# `for n in "${names[@]}"; do unset "$n"; done`. Checker 0, real task 0, observer `wrapper=inside`
# with all three absent. "I could not look" is not "it is clean", so a clear or a bind acting on a
# name this reader cannot resolve is UNVERIFIED. A name that IS visible with only its value unknown
# — `local mod="${1#./}"`, which this repository's own script writes — is not that case.
python3 - "$TASKFILE" "$WORK/tf-indirect.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = ("      - bash scripts/with-pg-env.sh bash -c 'names=(OLIVARES_TEST_POSTGRES_DSN); "
       "for n in \"${names[@]}\"; do unset \"$n\"; done; go test -count=1 ./eventing/...'")
open(sys.argv[2], 'w').write(s.replace(a, a + "\n" + new, 1) if a in s else s)
PY
if require_mutation "the posture cleared through a name the reader cannot resolve" "$TASKFILE" "$WORK/tf-indirect.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-indirect.yml" >/dev/null 2>"$WORK/indirect.err"
	rc_ind=$?
	[ "$rc_ind" -eq 2 ] && grep -q "act on a NAME this program cannot resolve" "$WORK/indirect.err"
	check "a clear whose target the reader cannot name is UNVERIFIED" "exit $rc_ind" $?
else
	unexercised "a clear whose target the reader cannot name is UNVERIFIED"
fi

python3 - "$TASKFILE" "$WORK/tf-unset.yml" <<'PY'
import sys
s = open(sys.argv[1]).read()
a = "      - bash scripts/with-pg-env.sh bash -c 'cd modules && go test -race -count=1 -timeout 150m ./...'"
new = "      - bash scripts/with-pg-env.sh bash -c 'unset OLIVARES_TEST_POSTGRES_DSN; cd modules && go test -count=1 ./eventing/...'"
open(sys.argv[2], 'w').write(s.replace(a, a + "\n" + new, 1) if a in s else s)
PY
if require_mutation "the posture unset inside the wrapper's own shell" "$TASKFILE" "$WORK/tf-unset.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-unset.yml" >/dev/null 2>"$WORK/unset.err"
	rc_un=$?
	[ "$rc_un" -eq 1 ] && grep -q "unsets OLIVARES_TEST_POSTGRES_DSN" "$WORK/unset.err"
	check "an \`unset\` of the posture inside the wrapper turns this red" "exit $rc_un" $?
else
	unexercised "an \`unset\` of the posture inside the wrapper turns this red"
fi

# THE HELPER PREMISE IS CHECKED BY THE PROGRAM, SEMANTICALLY — not by the grep two rows below.
# That grep was walked around on the day it shipped: a helper reaching the tests through a
# variable satisfies a search for `go test` and runs them. The fixture root below carries a
# mutated helper and symlinks the rest of the repository, so the row is about the HELPER's
# content and nothing else. Both halves run: clean helper green, mutated helper red.
helper_fixture() { # helper_fixture <dir> <helper-body>
	rm -rf "$1"
	mkdir -p "$1/scripts"
	cp "$TASKFILE" "$1/Taskfile.yml"
	ln -s "$ROOT/.github" "$1/.github"
	# ⛔ EL ORDEN ES EL ARREGLO: primero TODO el arbol de guiones, y la mutacion DESPUES.
	# `cp -R` sobrescribe `race-hot-tests.sh`, que es exactamente el fichero que esta fixture
	# escribe mutado — copiar despues de mutar borraba la mutacion y el caso salia 0 fingiendo
	# que el comprobador lo habia aceptado. Y hace falta el arbol entero, no una pareja elegida
	# a mano: desde el 2026-08-18 el workflow nombra `scripts/lib/exec-workdir.sh`, y un root sin
	# el hace que el comprobador conteste UNVERIFIED sobre el ROOT en vez de sobre el helper que
	# esta fila pregunta. Una lista de dependencias escrita a mano envejece con cada paso nuevo.
	cp -R "$ROOT/scripts/." "$1/scripts/"
	printf '%s\n' "$2" >"$1/scripts/race-hot-tests.sh"
	"$PGWIRING" -workflow "$1/.github/workflows/mainline-ci.yml" -taskfile "$1/Taskfile.yml" -root "$1"
}
helper_fixture "$WORK/helper-clean" "$(cat "$ROOT/scripts/race-hot-tests.sh")" >/dev/null 2>"$WORK/hclean.err"
rc_hc=$?
helper_fixture "$WORK/helper-var" 'GO=go
$GO test -count=1 ./...' >/dev/null 2>"$WORK/hvar.err"
rc_hv=$?
[ "$rc_hc" -eq 0 ] && [ "$rc_hv" -eq 2 ] && grep -q "cannot resolve statically" "$WORK/hvar.err"
check "a helper reaching go test through a VARIABLE breaks its premise" "clean=$rc_hc mutated=$rc_hv" $?

# AND THROUGH AN ORDINARY PROGRAM IN FRONT OF THAT VARIABLE. The row above rejects an unresolvable
# HEAD; the sixteenth contrast moved the variable one word along — `nice $GO test …` — and the
# premise passed while the helper ran the tests unwrapped, because `nice` resolves, is no shell
# runner and is no `.sh`. The four predicates enumerated what is dangerous; a head that is not on
# the reviewed list of heads that run none of their own argv is UNVERIFIED now. `nohup` here for
# the same reason as the argv row above: the finding has to come from the default, not from a
# fixture's own spelling.
helper_fixture "$WORK/helper-ordinary" 'GO=go
nohup $GO test -count=1 ./...' >/dev/null 2>"$WORK/hord.err"
rc_ho=$?
[ "$rc_ho" -eq 2 ] && grep -q 'runs "nohup"' "$WORK/hord.err" &&
	grep -q "not on the reviewed list of heads" "$WORK/hord.err"
check "a helper reaching go test behind an ordinary program is UNVERIFIED" "exit $rc_ho" $?

# AND THROUGH A HEAD THAT THE TABLE ITSELF USED TO ALLOW. `sed` and `compgen` were briefly on the
# reviewed head list, each with its residual named in the entry, because this repository's own
# helper used them. The seventeenth contrast reached a `go test` straight through the sed one —
# GNU sed's `e` command runs a shell command taken from the SED SCRIPT, which is an argv word — at
# checker exit 0. Both entries are gone and the helper uses grep/cut and bash's own glob instead,
# with byte-identical output. This row is what stops either one coming back as a convenience.
helper_fixture "$WORK/helper-sed-e" "$(printf '#!/usr/bin/env bash\nGO=go sed -n %s <<<%s\n' \
	"'e \$GO test helper-sed-e-unwrapped >&2'" "'x'")" >/dev/null 2>"$WORK/hsede.err"
rc_hs=$?
[ "$rc_hs" -eq 2 ] && grep -q 'runs "sed"' "$WORK/hsede.err" &&
	grep -q "not on the reviewed list of heads" "$WORK/hsede.err"
check "a helper head whose residual can run an argv word is UNVERIFIED" "exit $rc_hs" $?

# THE REVIEWED HELPER LIST, and its premise. recipeHelpers is the same kind of object as
# wrapperExempt — a reviewed claim with somewhere it can go red — and its claim is "this script
# runs no go test". A claim nobody re-checks is a comment. The list comes from what the program
# EMITS, never from a transcription of its source (the ninth contrast walked past a grep with a
# valid entry written `"test:" + "integration"`).
helper_names_go_test() { # 0 when the file invokes `go test` outside a comment
	# Here-string y no tuberia: el productor lee un FICHERO, asi que `grep -q` puede cerrarle
	# la tuberia y devolver 141 justo cuando ha encontrado lo que buscaba (medido 2026-08-19).
	grep -qE '(^|[^[:alnum:]_./-])go[[:space:]]+test' <<<"$(grep -vE '^[[:space:]]*#' "$1")"
}
helpers="$("$PGWIRING" -mode print-helpers 2>/dev/null)"
helpers_clean=0
if [ -z "$helpers" ]; then
	helpers_clean=1 # an empty list makes the row vacuous, which is not a pass
else
	while IFS="$(printf '\t')" read -r hpath _; do
		[ -n "$hpath" ] || continue
		if [ ! -r "$ROOT/$hpath" ] || helper_names_go_test "$ROOT/$hpath"; then
			helpers_clean=1
		fi
	done <<HELPERS
$helpers
HELPERS
fi
[ "$helpers_clean" -eq 0 ]
# The label NAMES them rather than counting them. A count here printed `0` for a one-line list —
# `$( )` strips the trailing newline, so `wc -l` saw no line at all — which is this session's own
# P1 committed inside the row that closes it.
check "every reviewed recipe helper keeps its premise (runs no go test)" \
	"$(printf '%s' "$helpers" | cut -f1 | tr '\n' ' ')" $?

# And the premise check itself has to be able to fail, or it is decoration.
first_helper="$(printf '%s\n' "$helpers" | head -1 | cut -f1)"
if [ -n "$first_helper" ] && [ -r "$ROOT/$first_helper" ]; then
	cp "$ROOT/$first_helper" "$WORK/pgprobe-helper.sh"
	printf '\ngo test -count=1 ./...\n' >>"$WORK/pgprobe-helper.sh"
	helper_names_go_test "$WORK/pgprobe-helper.sh"
	check "the helper premise check DETECTS a go test added to one" "$first_helper mutated" $?
else
	unexercised "the helper premise check DETECTS a go test added to one"
fi

# An exemption honoured by the checker whose premise this battery does not verify would be a hole
# with a comment on it. The comparison is against what the program EMITS, not against a spelling
# of its source — the ninth contrast added a valid entry written `"test:" + "integration"`, which
# gofmt accepted and the checker honoured while the battery's grep could not see it. A guard that
# reads a lexical shape can always be walked around by a semantic one, so the checker prints the
# map it honours and this compares that.
#
# Both halves still matter: the ENTRY POINT list catches an excuse nobody reviewed, the TREE list
# catches an excuse whose premise nobody sweeps, and the checker itself carries the third link —
# an exemption's declared tree must be named by a command of that recipe which actually runs the
# tests, so a decoy `echo <tree>` elsewhere no longer passes.
checker_exemptions="$("$PGWIRING" -mode print-exemptions -workflow x 2>/dev/null | sort)"
battery_exemptions="$(printf '%s\t%s\n' \
	"test:cloud" "cloud/control-plane" \
	"test:cloud:norace" "cloud/control-plane" \
	"test:release" "core/license" | sort)"
[ -n "$checker_exemptions" ] && [ "$checker_exemptions" = "$battery_exemptions" ]
check "the checker EMITS exactly the exemptions this battery sweeps" "entry points and trees, both ways" $?

# THE FOUR ESCAPES THE NINTH CONTRAST BUILT, pinned so they stay closed.
#
# Three of them walked around a guard whose comment claimed more than it read, and the fourth ran
# in a part of a target this program was not inspecting at all. That last one is the serious one:
# `deps:` runs BEFORE the recipe, so an unwrapped `go test` there is not an edge case, it is the
# load-bearing layer looking at half of what a target does.
cat "$TASKFILE" >"$WORK/tf-deps.yml"
cat >>"$WORK/tf-deps.yml" <<'YAML'

  ninth:unwrapped:
    cmds:
      - bash -c 'cd modules && go test -count=1 ./eventing/...'
YAML
sed -i 's|^  test:race-hot:modules:$|  test:race-hot:modules:\n    deps: [ninth:unwrapped]|' "$WORK/tf-deps.yml"
if require_mutation "a deps: target running an unwrapped go test" "$TASKFILE" "$WORK/tf-deps.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-deps.yml" >/dev/null 2>"$WORK/deps.err"
	rc_deps=$?
	[ "$rc_deps" -eq 1 ] && grep -q "WITHOUT with-pg-env.sh" "$WORK/deps.err"
	check "an unwrapped go test reached through deps: turns this red" "exit $rc_deps" $?
else
	unexercised "an unwrapped go test reached through deps: turns this red"
fi

cat "$WORKFLOW" >"$WORK/wf-export.yml"
printf '\n  race-export-local:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          export OLIVARES_PG_LOCAL_DEFAULTS=1\n          task test:race-hot:modules\n' \
	>>"$WORK/wf-export.yml"
if require_mutation "a job exporting the LOCAL opt-in" "$WORKFLOW" "$WORK/wf-export.yml"; then
	pg_wiring "$WORK/wf-export.yml" "$TASKFILE" >/dev/null 2>"$WORK/export.err"
	rc_export=$?
	[ "$rc_export" -eq 1 ] && grep -q "OLIVARES_PG_LOCAL_DEFAULTS" "$WORK/export.err"
	check "an \`export\` of the LOCAL opt-in turns this red" "exit $rc_export" $?
else
	unexercised "an \`export\` of the LOCAL opt-in turns this red"
fi

# The decoy: the exempt recipe mentions its declared tree in an `echo` while running elsewhere.
sed 's|bash scripts/private-leg.sh test:cloud cloud/control-plane go test|echo cloud/control-plane >/dev/null; cd modules \&\& go test|' \
	"$TASKFILE" >"$WORK/tf-decoy.yml"
if require_mutation "an exempt recipe naming its tree only in a decoy" "$TASKFILE" "$WORK/tf-decoy.yml"; then
	pg_wiring "$WORKFLOW" "$WORK/tf-decoy.yml" >/dev/null 2>"$WORK/decoy.err"
	rc_decoy=$?
	[ "$rc_decoy" -eq 1 ] && grep -q "no longer mentions" "$WORK/decoy.err"
	check "a decoy mention of the exempt tree turns this red" "exit $rc_decoy" $?
else
	unexercised "a decoy mention of the exempt tree turns this red"
fi

# THE SAME LEG, SPELLED EVERY OTHER WAY WE COULD FIND (2026-08-02). Closing the case above left a narrower
# hole of exactly the same shape, twice over. Discovery first took only the token immediately
# after a bare `task`, and six spellings escaped. The rule that replaced it — any race-prefixed
# token in a shell that mentions `task` — closed those and opened two more holes: the FIFTH
# contrast measured four further escapes (the target held in the job's `env`, the command written
# `\task`, the binary named by path, the target concatenated as `test:"race"-hot:modules`), each
# verified by executing the shell against a stub and watching the exact argv arrive, AND a false
# positive where a job that merely printed a target name was reported as racing it.
#
# Both rules failed for one reason: guessing at a shell instead of reading it. Discovery now scans
# shell words — quoting, escapes, `$VAR` against the workflow's own env maps, operator boundaries
# — and attributes a target only to the argv of a command whose resolved name is `task`.
#
# EACH CASE DECLARES THE EXIT CODE IT EXPECTS, not merely "not zero". A finding (1) and an
# UNVERIFIED (2) are different answers, and a case that accepts either cannot tell you which one
# regressed. The control matters as much: a fixture that simply failed to parse would also turn a
# row red, for the wrong reason — so where the forced -job/-task lookup can prove the job real and
# unwired, it must return 1 before discovery's own answer is measured.
spelling_case() { # spelling_case <label> <expected-exit> <yaml-job-body>
	printf '%s' "$(cat "$WORKFLOW")" >"$WORK/wf-spell.yml"
	printf '\n%s\n' "$3" >>"$WORK/wf-spell.yml"
	pg_wiring "$WORK/wf-spell.yml" "$TASKFILE" race-undetected test:race-hot:modules \
		>/dev/null 2>"$WORK/spell-forced.err"
	if [ "$?" -ne 1 ]; then
		unexercised "an unwired leg written as \`$1\` gives exit $2"
		return
	fi
	pg_wiring "$WORK/wf-spell.yml" "$TASKFILE" >/dev/null 2>"$WORK/spell.err"
	rc_spell=$?
	[ "$rc_spell" -eq "$2" ] && grep -q "race-undetected" "$WORK/spell.err"
	check "an unwired leg written as \`$1\` gives exit $2" "exit $rc_spell, names the job" $?
}

spelling_case 'task --output group X' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: task --output group test:race-hot:modules'

spelling_case 'task -v X' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: task -v test:race-hot:modules'

spelling_case 'task -d . X' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: task -d . test:race-hot:modules'

spelling_case 'task \ <newline> X' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: |
          task \
            test:race-hot:modules'

spelling_case 'bash -lc "task X"' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: bash -lc "task test:race-hot:modules"'

spelling_case 'the target held in the job env' 1 '  race-undetected:
    runs-on: ubuntu-latest
    env:
      TARGET: test:race-hot:modules
    steps:
      - run: task $TARGET'

spelling_case 'the command written \task' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: \task test:race-hot:modules'

spelling_case 'the target split by quotes' 1 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: task test:"race"-hot:modules'

# The UNVERIFIED cases. Exit 2 is the honest answer when the target or the runner cannot be
# resolved statically: claiming "not wired" would assert something this program cannot see, and
# claiming clean would be the one answer it must never give wrongly.
spelling_case 'the binary named by an unresolved path' 2 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: |
          "$HOME/go/bin/task" test:race-hot:modules'

spelling_case 'a target from a variable nothing declares' 2 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: task $MYSTERY_TARGET'

spelling_case 'a target from a ${{ }} expression' 2 '  race-undetected:
    runs-on: ubuntu-latest
    steps:
      - run: task ${{ matrix.target }}'

spelling_case 'a uses: job with no steps key' 2 '  race-undetected:
    uses: ./.github/workflows/e2e-operator-kind.yml'

# A -RACE LEG INSIDE A LOCAL COMPOSITE ACTION, directly and then one hop further. The direct case
# closes a premise that used to be assumed rather than checked: that no action in this repository
# invokes a race target. The transitive case is the fifth contrast's — reading an action's inline
# `run` and stopping there is reading one link of a chain.
cat "$WORKFLOW" >"$WORK/wf-action.yml"
printf '\n  race-undetected:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ./.github/actions/pr-failure-report\n' \
	>>"$WORK/wf-action.yml"
# The fixture root must carry the repository scripts a race recipe names, or the checker answers
# UNVERIFIED about the ROOT rather than about the composite action this row is asking after.
mkdir -p "$WORK/fake-root/.github/actions/pr-failure-report" "$WORK/fake-root/scripts"
# ⛔ EL ARBOL DE GUIONES ENTERO, no una pareja elegida a mano. Medido el 2026-08-18: un
# paso NUEVO del workflow que sourcea `scripts/lib/exec-workdir.sh` dejo CINCO casos de
# esta bateria en rojo — y no porque el paso estuviera mal, sino porque el root falso no
# llevaba ese fichero y el comprobador contesta UNVERIFIED sobre el ROOT en vez de sobre
# lo que la fila pregunta. Una lista escrita a mano de dependencias envejece cada vez que
# alguien toca el workflow, y el rojo aparece lejos de la causa.
cp -R "$ROOT/scripts/." "$WORK/fake-root/scripts/"
printf 'name: x\nruns:\n  using: composite\n  steps:\n    - run: task test:race-hot:modules\n      shell: bash\n' \
	>"$WORK/fake-root/.github/actions/pr-failure-report/action.yml"
"$PGWIRING" -workflow "$WORK/wf-action.yml" -taskfile "$TASKFILE" -root "$WORK/fake-root" \
	>/dev/null 2>"$WORK/action.err"
rc_action=$?
[ "$rc_action" -eq 1 ] && grep -q "race-undetected" "$WORK/action.err"
check "a -race leg inside a LOCAL composite action is seen" "exit $rc_action" $?

mkdir -p "$WORK/nested-root/.github" "$WORK/nested-root/scripts"
cp -r "$ROOT/.github/actions" "$WORK/nested-root/.github/actions"
# export-closure: fixture scripts/run-race.sh — the bytes below are WRITTEN into a
# disposable action.yml under $WORK; the composite action they describe is the subject of
# this case, never a dependency of the published tree. The real file is created two lines
# down, inside that same throwaway root.
mkdir -p "$WORK/nested-root/.github/actions/run-race"
printf 'name: run-race\nruns:\n  using: composite\n  steps:\n    - run: bash scripts/run-race.sh\n      shell: bash\n' \
	>"$WORK/nested-root/.github/actions/run-race/action.yml"
printf '#!/usr/bin/env bash\ntask test:race-hot:modules\n' >"$WORK/nested-root/scripts/run-race.sh"
# ⛔ EL ARBOL DE GUIONES ENTERO, no una pareja elegida a mano. Medido el 2026-08-18: un
# paso NUEVO del workflow que sourcea `scripts/lib/exec-workdir.sh` dejo CINCO casos de
# esta bateria en rojo — y no porque el paso estuviera mal, sino porque el root falso no
# llevaba ese fichero y el comprobador contesta UNVERIFIED sobre el ROOT en vez de sobre
# lo que la fila pregunta. Una lista escrita a mano de dependencias envejece cada vez que
# alguien toca el workflow, y el rojo aparece lejos de la causa.
cp -R "$ROOT/scripts/." "$WORK/nested-root/scripts/"
cat "$WORKFLOW" >"$WORK/wf-nested.yml"
printf '\n  race-undetected:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: ./.github/actions/run-race\n' \
	>>"$WORK/wf-nested.yml"
"$PGWIRING" -workflow "$WORK/wf-nested.yml" -taskfile "$TASKFILE" -root "$WORK/nested-root" \
	>/dev/null 2>"$WORK/nested.err"
rc_nested=$?
[ "$rc_nested" -eq 1 ] && grep -q "race-undetected" "$WORK/nested.err"
check "a -race leg one hop further, in a script the action runs" "exit $rc_nested" $?

# THE FALSE POSITIVE THE BROAD RULE PRODUCED, kept as a case so the narrow rule cannot drift back.
# A job that merely PRINTS a target name is not racing anything, and saying it does asserts a leg
# that does not exist — loud, but false.
cat "$WORKFLOW" >"$WORK/wf-phantom.yml"
printf '\n  race-phantom:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          task lint:spdx\n          echo "see test:race-hot:modules for the racing leg"\n' \
	>>"$WORK/wf-phantom.yml"
"$PGWIRING" -workflow "$WORK/wf-phantom.yml" -taskfile "$TASKFILE" -root "$ROOT" >/dev/null 2>&1
check "a job that only PRINTS a target name is not a leg" "exit 0, no phantom finding" $?

# EVERY LEG THE WORKFLOW RUNS IS INVENTORIED, and the absence guard is worth exactly that
# completeness. The table listed two while the workflow invoked four; deleting an uninventoried
# leg's step left the program at exit 0. The fifth contrast measured that, which is why the
# written refusal to complete the table was retracted rather than defended.
sed '/run: task test:race-hot:root/d' "$WORKFLOW" >"$WORK/wf-noroot.yml"
if require_mutation "an uninventoried leg deleted" "$WORKFLOW" "$WORK/wf-noroot.yml"; then
	pg_wiring "$WORK/wf-noroot.yml" "$TASKFILE" >/dev/null 2>"$WORK/noroot.err"
	rc_noroot=$?
	[ "$rc_noroot" -eq 1 ] && grep -q "test:race-hot:root" "$WORK/noroot.err"
	check "a leg disappearing from the workflow turns this red" "exit $rc_noroot, names the leg" $?
else
	unexercised "a leg disappearing from the workflow turns this red"
fi

# And the mirror: an inventoried leg that VANISHES. Discovery alone cannot see an absence, so
# the compiled table stays as an assertion of presence rather than as the source of coverage.
sed 's|^  race-modules:|  race-modules-renamed:|' "$WORKFLOW" >"$WORK/wf-leggone.yml"
if require_mutation "an inventoried leg renamed away" "$WORKFLOW" "$WORK/wf-leggone.yml"; then
	pg_wiring "$WORK/wf-leggone.yml" "$TASKFILE" >/dev/null 2>"$WORK/leggone.err"
	rc_leggone=$?
	[ "$rc_leggone" -eq 1 ] && grep -q "no longer invoked anywhere" "$WORK/leggone.err"
	check "an inventoried leg disappearing turns this red" "exit 1, names the lost leg" $?
else
	unexercised "an inventoried leg disappearing turns this red"
fi

# THE THIRD ANSWER. Everything above distinguishes wired from not-wired; this proves the
# gate also distinguishes "I could not look", and that it is not silently a pass. A missing
# file, a job that is absent under the name given, and a recipe with nothing to inspect all
# land here.
pg_wiring "$WORK/does-not-exist.yml" "$TASKFILE" race-modules test:race-hot:modules >/dev/null 2>&1
rc_missing=$?
[ "$rc_missing" -eq 2 ]
check "an unreadable workflow is UNVERIFIED, never a pass" "exit 2" $?

pg_wiring "$WORKFLOW" "$TASKFILE" no-such-job test:race-hot:modules >/dev/null 2>&1
rc_nojob=$?
[ "$rc_nojob" -eq 2 ]
check "a renamed/absent job is UNVERIFIED, never a pass" "exit 2" $?

pg_wiring "$WORKFLOW" "$TASKFILE" race-modules no-such-task >/dev/null 2>&1
rc_notask=$?
[ "$rc_notask" -eq 2 ]
check "a renamed/absent task is UNVERIFIED, never a pass" "exit 2" $?

# --- WIRING: the decision must live at the canonical entry points ------------------------
# The 14 original cases proved the helper in isolation, so deleting the hook's eval would
# have left every one of them green while the gate silently skipped Postgres again. This
# asserts the call sites themselves, and is fail-closed: a NEW `go test` entry point must
# either use the wrapper or be added to the reviewed exemption list below, with a reason.
#
#   test:release       — core/license only; no Postgres-backed package is reachable.
#   test:cloud[:norace]— cloud/control-plane is a separate module built with GOWORK=off and
#                        contains no pgtest reference and no OLIVARES_TEST_POSTGRES_* read.
#   test:integration   — `-run Conformance` selects connector conformance against external
#                        endpoints; it selects no pgtest suite.
#   hookpar selftest   — scripts/hookpar is a standalone GOWORK=off module importing only
#                        go/ast, go/parser, go/token and stdlib; its tests parse Go source inside
#                        t.TempDir() and open no database. Wrapping it is WORSE than useless:
#                        with-pg-env.sh refuses without a resolved DSN, so the wrapper turns a
#                        passing suite into a hard failure on any box with no Postgres — measured
#                        2026-08-25, wrapped rc=1 / unwrapped rc=0. Its premise is ASSERTED below
#                        with the same sweep that proves the other two.
#
# test:race-hot:modules was the FOURTH entry on this list until 2026-08-01, exempted
# because ./modules was Postgres-free. It is not exempt any more, and the exemption line
# is deleted rather than left inert: with the wrapper back, the `with-pg-env.sh` arm below
# already lets it through, so a stale `cd modules && go test` arm would sit here matching
# nothing — until the day somebody unwrapped that recipe again, when it would silently
# excuse exactly the regression this scan exists to catch. An exemption whose reason has
# expired is a hole with a comment on it.
unwrapped=0
seen_entries=0
while IFS= read -r line; do
	seen_entries=$((seen_entries + 1))
	case "$line" in
	*with-pg-env.sh*) continue ;;
	*"-tags release"*) continue ;;
	*"-tags integration -run Conformance"*) continue ;;
	*"go test -race -count=1 -timeout 10m ./..."*) continue ;;
	*"go test -count=1 -timeout 10m ./..."*) continue ;;
	*"cd scripts/hookpar"*) continue ;;
	esac
	unwrapped=$((unwrapped + 1))
	printf '        unwrapped go-test entry point: %s\n' "$line"
done < <(grep -E '^[[:space:]]*-.*go test' "$ROOT/Taskfile.yml")
# The assertion needs BOTH halves. Counting only `unwrapped -eq 0` meant the row went
# green when the scan saw NOTHING — a Taskfile renamed to .yaml, a recipe reformatted so
# `go test` no longer follows a leading `-`, a grep that failed outright — because the
# loop then ran zero times and unwrapped stayed 0. The one check that keeps this helper
# from being quietly unwired passed precisely when it could no longer observe anything,
# and it runs in the pre-push gate as `task lint:pg-env`.
[ "$seen_entries" -gt 0 ] && [ "$unwrapped" -eq 0 ]
check "every canonical go-test entry point decides the Postgres env (${seen_entries} seen)" \
	"no unwrapped entry points, and the scan saw some" $?

# AN EXEMPTION MUST NOT OUTLIVE ITS REASON (2026-08-01). The case list above excuses the
# entry points from the wrapper, and two of those excuses are PREMISES about a tree:
# cloud/control-plane and core/license reference no pgtest and read no OLIVARES_TEST_POSTGRES_*,
# so with-pg-env.sh would hand them nothing. Both premises are true today — measured here, not
# assumed. So was "./modules is Postgres-free" on 2026-07-30, and it was one commit from false;
# this entire guard exists because that premise expired while its comment did not. The comment
# the comment on the removed test:race-hot:modules exemption says it outright: an exemption whose reason has expired is a hole with a
# comment on it. These two now ASSERT their reason with the same sweep that proves the detector,
# so the day either tree acquires a Postgres test the exemption goes red instead of quietly
# excusing a suite that skips.
#
# WHAT THIS DELIBERATELY DOES NOT CLAIM. The third exemption, test:integration, rests on a
# SELECTION claim — `-run Conformance` selects no pgtest suite — not on an absence, and a tree
# sweep cannot decide it. It stays a reviewed comment. Naming the gap is the point: the failure
# mode here is an unverified premise that reads as a verified one.
#
# The trees are the ones each entry point actually runs (`./core/license/...`, and
# `dir: cloud/control-plane`), not their parents. A wider sweep would fail for trees the
# exemption never covered, which is a different bug wearing this one's clothes.
#
# AND THE PATTERN SET IS NARROWER THAN modules_pg_sweep's, deliberately. Reusing the wide one
# here produces a FALSE RED, not a finding: cloud/control-plane is a genuinely Postgres-backed
# service — it imports jackc/pgx directly and has real store packages — and the exemption never
# claimed otherwise. What it claims is that the tree does not participate in the SHARED TEST
# HARNESS, and the patterns below are precisely the things with-pg-env.sh exports, i.e.
# precisely what the wrapper could have supplied and did not. Measured on 2026-08-01: the wide
# sweep fails cloud on five store files and one direct pgx line, none of which the exemption
# was ever about. An assertion that measures something other than the claim it is checking is
# the same defect as no assertion, wearing a green tick.
EXEMPT_HARNESS_PATTERNS=(
	-e 'pgtest'
	-e 'OLIVARES_TEST_POSTGRES'
	-e 'OLIVARES_TEST_VECTOR_DSN'
)
exempt_harness_sweep() {
	# $1 = tree. Nonzero when the tree reads the shared Postgres test harness. Hits on stdout.
	harness_hits="$(grep -rln --include='*.go' "${EXEMPT_HARNESS_PATTERNS[@]}" "$1" 2>/dev/null)"
	if [ -n "$harness_hits" ]; then
		printf '        shared Postgres test harness referenced under %s:\n' "$1"
		printf '%s\n' "$harness_hits" | sed 's/^/          /'
		return 1
	fi
	return 0
}

# ⛔ `cloud/control-plane` SALIÓ de esta lista el 2026-08-30. Estaba aquí porque no leía el
# arnés de Postgres, y eso dejó de ser cierto con `privilege_posture_test.go`, que pide una
# base REAL para interrogar `pg_proc.proacl`. Una exención es la declaración de un HECHO, no
# un permiso: cuando el hecho cambia se RETIRA, y su entry point pasa por `with-pg-env.sh`
# como los demás (Taskfile.yml, task test:cloud). Ampliarla habría dejado un árbol que lee
# el arnés excusado de decidir su entorno, que es exactamente el agujero que el caso
# «an exempt tree that STARTS reading the harness is DETECTED» existe para cerrar.
for exempt in "core/license:test:release" "scripts/hookpar:lint:test-hook-parallelism:selftest"; do
	exempt_tree="${exempt%%:*}"
	exempt_entry="${exempt#*:}"
	if [ -d "$ROOT/$exempt_tree" ]; then
		exempt_harness_sweep "$ROOT/$exempt_tree"
		rc_exempt=$?
		[ "$rc_exempt" -eq 0 ]
		check "the exemption for ${exempt_entry} still holds (${exempt_tree})" \
			"no pgtest, no OLIVARES_TEST_POSTGRES_*/VECTOR read" $?
	else
		# The tree moved or was renamed. That is not a pass: the exemption now excuses an
		# entry point whose premise nothing can be read against.
		unexercised "the exemption for ${exempt_entry} still holds (${exempt_tree})"
	fi
done

# RED first, for both halves — a detector proven only on the trees it currently answers for is
# a gate whose red half is imaginary. This is the same discipline the modules detector gets
# above, and it is what would have caught the too-wide pattern set on the first run instead of
# the second.
EXEMPT_RED="$WORK/exempt-red"
mkdir -p "$EXEMPT_RED"
cat >"$EXEMPT_RED/harness_test.go" <<'EOF'
package red

import "os"

var dsn = os.Getenv("OLIVARES_TEST_POSTGRES_DSN")
EOF
exempt_harness_sweep "$EXEMPT_RED" >/dev/null
rc_exempt_red=$?
[ "$rc_exempt_red" -ne 0 ]
check "an exempt tree that STARTS reading the harness is DETECTED" "nonzero" $?

EXEMPT_GREEN="$WORK/exempt-green"
mkdir -p "$EXEMPT_GREEN"
cat >"$EXEMPT_GREEN/own_test.go" <<'EOF'
package green

import "os"

// A tree with its OWN Postgres, on its own variable, is what the exemption describes.
var dsn = os.Getenv("DATABASE_URL")
EOF
exempt_harness_sweep "$EXEMPT_GREEN" >/dev/null
check "a tree with its OWN Postgres, not the harness, stays exempt" "the detector can say no" $?

grep -q 'scripts/pg-test-env.sh' "$ROOT/.githooks/pre-push"
check "the pre-push hook still makes the decision too" "hook wired" $?

echo
# THREE numbers, because there are three answers. A summary that folds skips into `passed` is
# the same fail-open the helper it tests refuses to commit: `passed` must mean "ran a predicate
# and it held". Under OLIVARES_GATE_STRICT_PG=1 a skip is already counted as a failure by
# skip(), so this line reads `0 skipped` there by construction.
echo "pg-test-env: ${pass} passed, ${skips} skipped, ${fail} failed"

# THE COUNT IS PART OF THE VERDICT (2026-08-01). See EXPECTED_CASES at the top: a green run
# that measured fewer cases than this battery declares is not a green run, it is a run whose
# coverage silently shrank. Checked BEFORE the failure count so the diagnosis names the right
# defect — "you measured N of EXPECTED_CASES" is a different bug report from "N cases failed".
# THE ACCOUNTING ITSELF, checked rather than trusted. Every declared case reports through
# exactly one of three terms, so this sum is an identity — and an identity that fails is a bug
# in the reporting, which is the bug that made `75 passed, 0 failed` mean 71 measurements.
# Setup failures are deliberately outside it: they raise `fail` without declaring a case.
if [ "$cases" -ne "$((pass + skips + case_fail))" ]; then
	echo "pg-test-env: FAILING — the case accounting does not add up: ${cases} declared but" >&2
	echo "pg-test-env: ${pass} passed + ${skips} skipped + ${case_fail} failed = $((pass + skips + case_fail))." >&2
	echo "pg-test-env: a case reported through two counters, or a term is missing. Fix the" >&2
	echo "pg-test-env: reporting before reading anything else in this run." >&2
	echo "pg-test-env: before reading anything else in this run. (${setup_fail} setup failures" >&2
	echo "pg-test-env: are counted apart, by design: they raise no case.)" >&2
	exit 1
fi

if [ "$cases" -ne "$EXPECTED_CASES" ]; then
	echo "pg-test-env: FAILING — this battery declares ${EXPECTED_CASES} cases and ran ${cases}." >&2
	echo "pg-test-env: a run that measures less than it claims to measure is not a pass. If you" >&2
	echo "pg-test-env: added or removed a case on purpose, update EXPECTED_CASES at the top of" >&2
	echo "pg-test-env: this file in the same commit." >&2
	exit 1
fi

[ "$fail" -eq 0 ] || exit 1
