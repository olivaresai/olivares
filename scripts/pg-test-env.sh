#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# pg-test-env.sh — decide whether the Postgres-backed tests can RUN, and say so out loud
# either way.
#
# WHY THIS EXISTS. The core Postgres suites self-skip unless a DSN is set, and until
# 2026-07-25 nothing in the local gate ever set one. The pre-push hook even carried a
# comment asserting, as settled fact, that there was no Postgres server on the development
# machine — a sentence inherited from an earlier session's notes that nobody had ever
# tested. There has been a PostgreSQL 15.18 running the whole time.
#
# CORRECTION (2026-07-29). An earlier version of this comment went on to assert that the
# `olivares` database was provisioned here too. Measured false: this host has exactly one
# non-template database, `olv_hub_iso`. That unverified sentence is what pointed the
# maintenance DSN at a database that does not exist — the same class of inherited,
# untested claim the paragraph above is about.
#
# So the local gate was silently skipping the exact class of test that let a total Postgres
# boot outage sit on `main` for 17 days behind a green check. Two failures compounded: an
# unverified "cannot", and a skip that looked identical to a pass.
#
# WHICH VARIABLE ACTUALLY GATES WHAT — verified, because the first version of this script
# said "every Postgres-backed test" and that was too broad to be true:
#
#   * core/internal/pgtest gates on the SUPERUSER DSN. `classify()` returns gateRun iff
#     OLIVARES_TEST_POSTGRES_SUPERUSER_DSN is non-empty, because each test provisions its
#     own isolated database (core/internal/pgtest/pgtest.go — the Env*DSN consts and classify()).
#     Setting the app
#     or admin DSN WITHOUT it is deliberately a LOUD misconfiguration, not a silent skip.
#   * the pgvector integration test gates SEPARATELY on OLIVARES_TEST_VECTOR_DSN
#     (connectors/vectorindex/pgvector_test.go — TestPgvectorIntegration) and needs the `vector`
#     extension.
#     This helper does NOT enable it, and says so: `pg_available_extensions` has no
#     `vector` row on this machine (verified 2026-07-25), so claiming otherwise would put
#     back exactly the kind of unearned green this script exists to remove.
#
# WHAT THIS SCRIPT GUARANTEES — THREE REGIMES. This paragraph is spelled out because it
# has been wrong twice by stating one regime unconditionally, each time after a change that
# added another.
#
#   FAIL-CLOSED (the DEFAULT, 2026-07-30). Without OLIVARES_PG_LOCAL_DEFAULTS=1 the script
#   refuses to synthesise any DSN and refuses a fixed-port 5432 DSN: in CI the Postgres
#   service lives on an EPHEMERAL host port, so 127.0.0.1:5432 on a shared runner host is
#   another job's cluster, and pgtest CREATE/DROP DATABASEs through the maintenance DSN.
#
#   LOCAL (OLIVARES_PG_LOCAL_DEFAULTS=1, whose one sanctioned call site is
#   .githooks/pre-push). Given VALID inputs, the absence of a server alone does not fail the
#   gate — a machine without Postgres must still be able to push. It is not a blanket "never
#   fails": a malformed input still exits 2 here, and PG_PROBE_TIMEOUT or OLIVARES_PG_PROBE
#   with a value this helper refuses to interpret does the same, in every regime.
#
#   PROMOTION (OLIVARES_GATE_STRICT_PG=1). An unreachable server IS a hard failure (the
#   block at the end of this file), because the suites that would silently skip are exactly
#   the ones a promotion gate exists to enforce.
#
# What it must never do is stay quiet about coverage it did not get: when the server is absent
# AND the inputs are valid, it names on stderr exactly which coverage the run does NOT have. The
# earlier version of this sentence said "in ANY of the three", and that is false in the
# fail-closed default with empty DSNs: the configuration refusal below exits first and never
# reaches the coverage list. Refusing IS the loud answer there, so nothing is hidden — but the
# promise had to be narrowed to what the code does.
#
# USAGE. Prefer scripts/with-pg-env.sh, which checks this script's EXIT STATUS before
# evaluating its output. `eval "$(bash scripts/pg-test-env.sh)"` does not: eval reports its
# own status, so a helper that died still left the caller at 0 under `set -e` (measured
# 2026-07-25 — a producer exiting 42 reached the next command with status 0).
#
# stdout: shell `export` lines, or nothing.  stderr: the human-readable verdict.
#
# OLIVARES_PG_PROBE forces the reachability answer so the absent-server branch can be
# exercised on a machine that has a server. It accepts ONLY `true` or `false`: an earlier
# revision ran its value through `eval`, which is arbitrary code execution through an
# environment variable for no benefit.
set -euo pipefail

PROBE="${OLIVARES_PG_PROBE:-}"
case "$PROBE" in
"" | true | false) ;;
*)
	echo "::error::pg-test-env: OLIVARES_PG_PROBE accepts only 'true' or 'false' (got: ${PROBE})." >&2
	exit 2
	;;
esac

# ORDER, deliberate (merge of two independent guards, 2026-07-31). Both landed in the same
# release and neither replaces the other: the one below refuses a MALFORMED duration, the
# one after it refuses a DSN that would point at somebody else's database. Input validation
# is grouped with its sibling OLIVARES_PG_PROBE check above; the policy gate follows.
#
# PG_PROBE_TIMEOUT is a DURATION, and it used to reach `timeout` unvalidated as that
# command's FIRST argument — where a leading dash is an OPTION, not a duration.
#
# Measured 2026-07-30, with no server reachable anywhere: `PG_PROBE_TIMEOUT=--help` made
# `timeout` print its usage and exit 0 WITHOUT running psql, so can_connect reported
# success, the reachable branch was taken, and the helper exited 0 emitting all three
# DSNs — under OLIVARES_GATE_STRICT_PG=1, which exists precisely to make a server-less run
# fatal. Control run: exit 1, zero exports. That is a green in the promotion regime with
# no PostgreSQL at all, reachable from an environment variable.
#
# Validated deny-closed, like OLIVARES_PG_PROBE above: what timeout(1) documents as a
# duration is a number with an optional s/m/h/d suffix, and nothing else is accepted. The
# invocation also gains `--`, so even a future edit that loosened this cannot let a value
# be read as an option.
TIMEOUT_SPEC="${PG_PROBE_TIMEOUT:-}"
case "$TIMEOUT_SPEC" in
"") ;;
*[!0-9smhd.]* | [!0-9]* | *[smhd]?*)
	echo "::error::pg-test-env: PG_PROBE_TIMEOUT must be a duration — digits with an optional s/m/h/d suffix (got: ${TIMEOUT_SPEC})." >&2
	exit 2
	;;
esac

# FAIL-CLOSED BY DEFAULT (2026-07-30). On a shared runner host the CI Postgres is
# published on an EPHEMERAL host port, so 127.0.0.1:5432 is either nothing or ANOTHER
# job's cluster. core/internal/pgtest runs CREATE DATABASE / DROP DATABASE against the
# maintenance DSN, so a silent fallback would create and destroy databases inside the
# neighbour's server.
#
# This helper does NOT invent a DSN unless explicitly asked to. Environment detection
# (CI=true / GITHUB_ACTIONS=true) is NOT a usable condition: a `container:` job, an
# `act` run, or an `env -i` loses it, and that is exactly where the fallback hurts.
# The one sanctioned local opt-in call site is .githooks/pre-push.
if [ "${OLIVARES_PG_LOCAL_DEFAULTS:-0}" != "1" ]; then
	for v in OLIVARES_TEST_POSTGRES_SUPERUSER_DSN OLIVARES_TEST_POSTGRES_DSN OLIVARES_TEST_POSTGRES_ADMIN_DSN; do
		eval "val=\${$v:-}"
		if [ -z "$val" ]; then
			echo "pg-test-env: FAILING — $v is empty and OLIVARES_PG_LOCAL_DEFAULTS is not 1." >&2
			echo "pg-test-env: refusing to synthesise a 127.0.0.1:5432 default. On a shared host" >&2
			echo "pg-test-env: that address is another job's Postgres. In CI, the workflow step" >&2
			echo "pg-test-env: 'resolve the ephemeral Postgres port' must run first. On a laptop," >&2
			echo "pg-test-env: export OLIVARES_PG_LOCAL_DEFAULTS=1 to opt in to the old defaults." >&2
			exit 1
		fi
		case "$val" in
		*@127.0.0.1:5432/* | *@localhost:5432/* | *@\[::1\]:5432/* | *host=127.0.0.1*port=5432*)
			echo "pg-test-env: FAILING — $v targets the FIXED port 5432." >&2
			echo "pg-test-env: CI publishes Postgres on an ephemeral port; a job that reaches 5432" >&2
			echo "pg-test-env: is reaching a neighbour. If you are migrating the jobs to \`container:\`" >&2
			echo "pg-test-env: (service reached by DNS name, e.g. postgres:5432), this guard is what" >&2
			echo "pg-test-env: blocks you: relax it deliberately, in the same PR, with the reason." >&2
			exit 1
			;;
		esac
	done
fi

# DEFAULT_SUPERUSER_DSN points at the MAINTENANCE database, not at an application one.
#
# That is a correction, and the bug it closes was live on this machine on 2026-07-29: the
# default named `/olivares`, that database did not exist here, and `pg_isready` answered
# yes anyway — it probes the SERVER, never the database. So the helper announced "will
# RUN" and every Postgres-backed test then died with `FATAL: database "olivares" does not
# exist (SQLSTATE 3D000)`. A wrong RUN is worse than a wrong SKIP: the reader goes looking
# for broken code instead of a broken DSN.
#
# `postgres` is also the only correct answer on the merits. pgtest uses this DSN purely to
# CREATE DATABASE and DROP DATABASE around each test (core/internal/pgtest/pgtest.go), and
# PostgreSQL refuses to drop the database the session is connected to. Pointing maintenance
# at an application database works only for as long as nobody tries to drop it.
DEFAULT_SUPERUSER_DSN='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable'

# PROBE_NOTE records HOW reachability was decided, so the verdict can say whether the
# database was actually opened or only the listener was pinged.
PROBE_NOTE=''

# can_connect opens a real session and runs a trivial query. `pg_isready` cannot stand in
# for this: it completes the startup handshake far enough to learn the server is alive and
# reports success for a database that does not exist, a role that cannot log in, and a
# password that is wrong.
#
# It is bounded three ways, because a helper that HANGS is worse than one that guesses —
# it stalls a pre-push with no output at all:
#
#   -w              never prompt for a password. Without it an interactive pre-push can
#                   sit waiting on a terminal read that nobody is watching.
#   connect_timeout libpq imposes NO limit when this is absent or zero, so a route that
#                   drops packets waits indefinitely.
#   timeout(1)      bounds a session that connects and then stops answering, which
#                   connect_timeout does not cover. Absent on some minimal images, so it
#                   is used only when present.
can_connect() {
	local dsn="$1"
	local sql='SELECT 1'
	if command -v timeout >/dev/null 2>&1; then
		# `--` so the duration can never be parsed as an option. See the validation of
		# PG_PROBE_TIMEOUT at the top: without both, `--help` made timeout exit 0 without
		# running psql, and an unreachable server was reported as reachable.
		timeout -- "${PG_PROBE_TIMEOUT:-10}" psql -X -q -w -At \
			-d "$dsn" -v ON_ERROR_STOP=1 -c "$sql" >/dev/null 2>&1
		return $?
	fi
	PGCONNECT_TIMEOUT="${PG_PROBE_TIMEOUT:-10}" psql -X -q -w -At \
		-d "$dsn" -v ON_ERROR_STOP=1 -c "$sql" >/dev/null 2>&1
}

reachable() {
	case "$PROBE" in
	true)
		PROBE_NOTE='forced'
		return 0
		;;
	false)
		PROBE_NOTE='forced'
		return 1
		;;
	esac

	# Prefer a REAL connection. Probe the CONFIGURED server when there is one: always
	# probing localhost meant a developer pointed at another host was told every Postgres
	# test would skip while their inherited DSNs stayed set and the tests actually ran —
	# a warning that is wrong in the safe direction is still a warning nobody will trust.
	#
	# BUG FIX (2026-07-30, pre-existing): both branches below used to carry a dead
	# `[ "$PROBE_NOTE" = "no-probe-tool" ]` arm — comparing against a value the line
	# just above had ruled out — whose echos went to STDOUT, the very stream
	# with-pg-env.sh feeds to `eval`. The no-probe-tool report already lives, once and
	# on stderr, in the verdict block at the bottom; the dead arms are simply removed.
	if command -v psql >/dev/null 2>&1; then
		export PGCONNECT_TIMEOUT="${PG_PROBE_TIMEOUT:-10}"
		PROBE_NOTE='connected'
		if [ -n "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ]; then
			can_connect "$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN"
			return $?
		fi
		# The fixed-port default probe is the LOCAL-DEFAULTS path only. Without the
		# opt-in the fail-closed guard above guarantees the DSN is set, so reaching
		# here means the guard was bypassed — treat 127.0.0.1:5432 as unreachable
		# rather than probe a port that may belong to a neighbouring job.
		[ "${OLIVARES_PG_LOCAL_DEFAULTS:-0}" = "1" ] || return 1
		can_connect "$DEFAULT_SUPERUSER_DSN"
		return $?
	fi

	# No psql: fall back to the listener probe and SAY that is all that was checked.
	if ! command -v pg_isready >/dev/null 2>&1; then
		# NEITHER tool. This is not "no server" — nothing was measured at all, and
		# reporting an unmeasured absence as a measured one is the same dishonesty,
		# pointed the other way.
		PROBE_NOTE='no-probe-tool'
		return 1
	fi
	PROBE_NOTE='listener-only'
	if [ -n "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ]; then
		pg_isready -q -d "$OLIVARES_TEST_POSTGRES_SUPERUSER_DSN" >/dev/null 2>&1
		return $?
	fi
	# Same rule as the psql branch: the fixed-port listener probe only under opt-in.
	[ "${OLIVARES_PG_LOCAL_DEFAULTS:-0}" = "1" ] || return 1
	pg_isready -q -h 127.0.0.1 -p 5432 >/dev/null 2>&1
}

# The pgvector leg is never enabled by this helper; it is reported in BOTH branches so a
# reader is never left to infer that "Postgres tests run" includes it.
pgvector_note() {
	echo "pg-test-env: NOT enabled here in either case — the pgvector integration test, gated"
	echo "pg-test-env:   separately on OLIVARES_TEST_VECTOR_DSN and needing the 'vector' extension"
	echo "pg-test-env:   (absent on this machine). Set that DSN yourself to cover it locally."
	echo "pg-test-env:   CI DOES run it (2026-07-30): mainline-ci and race-full ship a pgvector"
	echo "pg-test-env:   service image, create the extension, and export that DSN."
}

if reachable; then
	# The roles and database the workflow provisions in CI (deploy/postgres/01-app-role.sql)
	# are the same ones this container already has. Existing values win, so a developer can
	# point the gate at another server without editing this file.
	printf 'export OLIVARES_TEST_POSTGRES_DSN=%s\n' \
		"\"\${OLIVARES_TEST_POSTGRES_DSN:-postgres://olivares_app:apppw@127.0.0.1:5432/olivares?sslmode=disable}\""
	printf 'export OLIVARES_TEST_POSTGRES_ADMIN_DSN=%s\n' \
		"\"\${OLIVARES_TEST_POSTGRES_ADMIN_DSN:-postgres://olivares_admin:adminpw@127.0.0.1:5432/olivares?sslmode=disable}\""
	printf 'export OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=%s\n' \
		"\"\${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-${DEFAULT_SUPERUSER_DSN}}\""
	{
		echo "pg-test-env: Postgres reachable — the core suites gated on"
		echo "pg-test-env:   OLIVARES_TEST_POSTGRES_SUPERUSER_DSN will RUN: RLS and tenant binding,"
		echo "pg-test-env:   HA leader election and failover, expand-contract migrations and the boot path."
		if [ "$PROBE_NOTE" = "listener-only" ]; then
			# Do not let this pass for a verified posture. pg_isready reports success for
			# a database that does not exist, which is precisely how a "will RUN" turned
			# into a whole suite failing to connect.
			echo "pg-test-env: CAVEAT — psql is not installed, so only the LISTENER was probed."
			echo "pg-test-env:   A missing database or a bad credential would still fail every suite."
		fi
		pgvector_note
	} >&2
	exit 0
fi

# NOT a failure IN LOCAL — but never a silent one either. Under OLIVARES_GATE_STRICT_PG=1 the
# block at the end of this file turns this same state into exit 1; the sentence used to read as
# an unqualified "not a failure", which is the opposite of what the promotion regime does.
{
	if [ "$PROBE_NOTE" = "no-probe-tool" ]; then
		echo "pg-test-env: WARNING — neither psql nor pg_isready is installed, so reachability"
		echo "pg-test-env: was NOT measured. This is not a report that PostgreSQL is absent; it is"
		echo "pg-test-env: a report that the helper could not look. No DSN is exported, so the core"
		echo "pg-test-env: suites will SKIP."
	elif [ -n "${OLIVARES_TEST_POSTGRES_SUPERUSER_DSN:-}" ]; then
		# NEVER echo the DSN: it carries a password. And be accurate about what happens —
		# an inherited-but-unreachable DSN stays exported, so the suites gated on it do
		# NOT skip, they FAIL on connection. Saying "will SKIP" there would send the
		# reader looking for a missing server instead of a broken one.
		echo "pg-test-env: WARNING — the CONFIGURED superuser DSN is not reachable."
		echo "pg-test-env: (value withheld: it carries a password. It came from your environment.)"
		echo "pg-test-env: it stays exported, so the core suites will FAIL to connect rather than"
		echo "pg-test-env: skip. Unset it to get a clean skip, or start/point it at a live server."
	else
		echo "pg-test-env: WARNING — no Postgres reachable on 127.0.0.1:5432."
		echo "pg-test-env: the core suites are gated on OLIVARES_TEST_POSTGRES_SUPERUSER_DSN, so they"
		echo "pg-test-env: will SKIP."
		if [ "$PROBE_NOTE" = "connected" ]; then
			# Be exact about WHICH of the two it was. A live server whose maintenance
			# database is missing, or whose superuser credential is wrong, looks nothing
			# like an absent server to the person fixing it.
			echo "pg-test-env: (the check opened a real session, so this covers a server that is down,"
			echo "pg-test-env:  a maintenance database that does not exist, and a credential that is"
			echo "pg-test-env:  refused — all three land here.)"
		fi
	fi
	echo "pg-test-env: This run does NOT cover:"
	echo "pg-test-env:   * FORCE ROW LEVEL SECURITY and tenant binding"
	echo "pg-test-env:   * HA leader election and failover"
	echo "pg-test-env:   * expand-contract online migrations and the boot path"
	pgvector_note
	echo "pg-test-env: a green result here is therefore PARTIAL. The 2026-07-08 boot outage"
	echo "pg-test-env: reached main through exactly this gap. Start a server, or rely on CI."
} >&2

# THREE regimes, one helper — not two, as this said until 2026-08-01 while the header above
# had already listed three. FAIL-CLOSED is the default: with no DSN configured and no opt-in,
# the helper refuses to synthesise one and exits 1. LOCAL (OLIVARES_PG_LOCAL_DEFAULTS=1) is the
# opt-in a PG-less machine uses: the loud warning above and a partial run, a usable local loop.
# PROMOTION (release, race-full, mainline CI) is the third: a partial run is not a run, because
# the suites this gate exists to enforce are exactly the ones that just skipped. Those callers
# export OLIVARES_GATE_STRICT_PG=1 and an unreachable server becomes a hard failure.
if [ "${OLIVARES_GATE_STRICT_PG:-0}" = "1" ]; then
	{
		echo "pg-test-env: FAILING — OLIVARES_GATE_STRICT_PG=1 and PostgreSQL is not reachable."
		echo "pg-test-env: In the promotion regime a PG-less pass is a silent hole, not a pass."
	} >&2
	exit 1
fi
exit 0
