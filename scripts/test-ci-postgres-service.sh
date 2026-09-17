#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Finite causal tests for scripts/ci-postgres-service.sh. No server, no apt install.
# Fixture commands only. exit 0 all held · 1 a case failed · 2 could not look.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HELPER="$ROOT/scripts/ci-postgres-service.sh"
WF_MAIN="$ROOT/.github/workflows/mainline-ci.yml"
WF_PROF="$ROOT/.github/workflows/race-package-profile.yml"
SQL="$ROOT/deploy/postgres/01-app-role.sql"

pass=0
fail=0
skip=0

could_not_look() {
	echo "test-ci-postgres-service: NO HE PODIDO MIRAR — $*" >&2
	exit 2
}

[ -f "$HELPER" ] || could_not_look "helper absent: $HELPER"
[ -f "$WF_MAIN" ] || could_not_look "mainline-ci.yml absent"
[ -f "$WF_PROF" ] || could_not_look "race-package-profile.yml absent"
[ -f "$SQL" ] || could_not_look "01-app-role.sql absent"
[ -r /proc/sys/net/ipv4/ip_local_port_range ] || could_not_look "no kernel ephemeral range"

pick_exec_dir() {
	local base d
	for base in "${RUNNER_TEMP:-}" "${TMPDIR:-}" /workspace/.olivares-tmptest /tmp; do
		[ -n "$base" ] || continue
		mkdir -p "$base" 2>/dev/null || continue
		d="$(mktemp -d "$base/pgsvc.XXXXXX" 2>/dev/null)" || continue
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

WORK="$(pick_exec_dir)" || could_not_look "no executable work directory"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

check() {
	local label="$1" want="$2" rc="$3"
	if [ "$rc" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok    %s (%s)\n' "$label" "$want"
	else
		fail=$((fail + 1))
		printf '  FAIL  %s (%s)\n' "$label" "$want"
	fi
}

read -r LOPORT HIPORT < /proc/sys/net/ipv4/ip_local_port_range
VALID="$LOPORT"
if [ "$VALID" -eq 5432 ]; then
	VALID=$((LOPORT + 1))
fi
if [ "$VALID" -lt "$LOPORT" ] || [ "$VALID" -gt "$HIPORT" ]; then
	could_not_look "could not pick a valid ephemeral port in $LOPORT-$HIPORT"
fi
LOW_OUT=1
if [ "$LOW_OUT" -ge "$LOPORT" ] && [ "$LOW_OUT" -le "$HIPORT" ]; then
	LOW_OUT=$((HIPORT + 1))
	if [ "$LOW_OUT" -gt 65535 ]; then
		LOW_OUT=0
	fi
fi

# --- resolve: valid / absent / non-numeric / fixed / out of range -----------------
CALLER_ENV="$WORK/caller.env"
: >"$CALLER_ENV"
JOB_ENV="$WORK/job.env"

run_resolve() {
	local port="${1-}"
	: >"$JOB_ENV"
	if [ "$#" -eq 0 ]; then
		env -u PGPORT_HOST GITHUB_ENV="$JOB_ENV" bash "$HELPER" resolve
		return
	fi
	PGPORT_HOST="$port" GITHUB_ENV="$JOB_ENV" bash "$HELPER" resolve
}

set +e
run_resolve "$VALID" >"$WORK/valid.out" 2>"$WORK/valid.err"
rc=$?
set -e
[ "$rc" -eq 0 ]
check "resolve accepts a kernel-ephemeral port" "rc=0" $?
grep -q "^PGHOSTPORT=127.0.0.1:${VALID}$" "$JOB_ENV"
check "resolve writes PGHOSTPORT with that port" "job env" $?
grep -q "^OLIVARES_TEST_POSTGRES_DSN=postgres://olivares_app:apppw@127.0.0.1:${VALID}/olivares?sslmode=disable$" "$JOB_ENV"
check "app DSN targets /olivares on the ephemeral port" "app" $?
grep -q "^OLIVARES_TEST_POSTGRES_ADMIN_DSN=postgres://olivares_admin:adminpw@127.0.0.1:${VALID}/olivares?sslmode=disable$" "$JOB_ENV"
check "admin DSN targets /olivares" "admin" $?
grep -q "^OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=postgres://postgres:postgres@127.0.0.1:${VALID}/postgres?sslmode=disable$" "$JOB_ENV"
check "maintenance DSN targets /postgres not /olivares" "superuser" $?
grep -q "^OLIVARES_TEST_POSTGRES_REQUIRED=1$" "$JOB_ENV"
check "REQUIRED is declared by the provisioner" "required" $?
grep -q "^OLIVARES_TEST_VECTOR_DSN=postgres://olivares_app:apppw@127.0.0.1:${VALID}/olivares?sslmode=disable$" "$JOB_ENV"
check "vector DSN is the app DSN" "vector" $?
! grep -q 5432 "$JOB_ENV"
check "resolve does not synthesise 5432 when given an ephemeral port" "no 5432" $?
[ ! -s "$CALLER_ENV" ]
check "resolve does not write the caller decoy command file" "caller.env empty" $?

set +e
run_resolve >"$WORK/absent.out" 2>"$WORK/absent.err"
rc=$?
set -e
[ "$rc" -ne 0 ]
check "absent PGPORT_HOST fails" "nonzero" $?
grep -q "empty or non-numeric" "$WORK/absent.err"
check "absent port names the cause" "message" $?

set +e
run_resolve "abc" >"$WORK/nonum.out" 2>"$WORK/nonum.err"
rc=$?
set -e
[ "$rc" -ne 0 ]
check "non-numeric PGPORT_HOST fails" "nonzero" $?

set +e
run_resolve "5432" >"$WORK/fixed.out" 2>"$WORK/fixed.err"
rc=$?
set -e
[ "$rc" -ne 0 ]
check "fixed port 5432 fails (not an ephemeral mapping)" "nonzero" $?

set +e
run_resolve "$LOW_OUT" >"$WORK/oor.out" 2>"$WORK/oor.err"
rc=$?
set -e
[ "$rc" -ne 0 ]
check "port outside the kernel ephemeral range fails" "nonzero" $?
grep -q "ephemeral range" "$WORK/oor.err"
check "out-of-range names the kernel range" "message" $?

set +e
env -u GITHUB_ENV PGPORT_HOST="$VALID" bash "$HELPER" resolve >"$WORK/noenv.out" 2>"$WORK/noenv.err"
rc=$?
set -e
[ "$rc" -ne 0 ]
check "resolve without GITHUB_ENV fails closed" "nonzero" $?

# --- provision: fixture psql, each critical command abort -------------------------
BIN="$WORK/bin"
mkdir -p "$BIN"
COUNT="$WORK/psql.count"
LOG="$WORK/psql.log"
FAIL_AT_FILE="$WORK/fail.at"
echo 0 >"$COUNT"
: >"$LOG"
echo 0 >"$FAIL_AT_FILE"

cat >"$BIN/psql" <<'EOF'
#!/bin/sh
count_file="${PGSVC_COUNT:?}"
log_file="${PGSVC_LOG:?}"
fail_at="${PGSVC_FAIL_AT:?}"
n=$(( $(cat "$count_file") + 1 ))
printf '%s\n' "$n" >"$count_file"
{
	printf 'N=%s\n' "$n"
	printf 'ARGC=%s\n' "$#"
	i=1
	for a in "$@"; do
		printf 'ARG%s=%s\n' "$i" "$a"
		i=$((i + 1))
	done
	printf 'END\n'
} >>"$log_file"
want="$(cat "$fail_at")"
if [ "$want" -gt 0 ] && [ "$n" -eq "$want" ]; then
	exit 17
fi
exit 0
EOF
chmod +x "$BIN/psql"

# A sudo/apt-get that must never run for real.
cat >"$BIN/sudo" <<'EOF'
#!/bin/sh
echo "sudo invoked: $*" >&2
exit 99
EOF
chmod +x "$BIN/sudo"
cat >"$BIN/apt-get" <<'EOF'
#!/bin/sh
echo "apt-get invoked: $*" >&2
exit 99
EOF
chmod +x "$BIN/apt-get"

run_provision() {
	echo 0 >"$COUNT"
	: >"$LOG"
	echo "${1:-0}" >"$FAIL_AT_FILE"
	env PATH="$BIN:/usr/bin:/bin" \
		PGSVC_COUNT="$COUNT" PGSVC_LOG="$LOG" PGSVC_FAIL_AT="$FAIL_AT_FILE" \
		PGHOSTPORT="127.0.0.1:${VALID}" \
		PGPASSWORD=postgres \
		bash "$HELPER" provision
}

set +e
run_provision 0 >"$WORK/prov.out" 2>"$WORK/prov.err"
rc=$?
set -e
[ "$rc" -eq 0 ]
check "provision with fixture psql runs to completion" "rc=0" $?
n="$(cat "$COUNT")"
[ "$n" -eq 5 ]
check "provision issues exactly five psql commands" "5" $?
grep -q "01-app-role.sql" "$LOG"
check "first command applies deploy/postgres/01-app-role.sql" "sql file" $?
grep -q "ON_ERROR_STOP=1" "$LOG"
check "ON_ERROR_STOP=1 is present" "flag" $?
grep -q "/postgres" "$LOG"
check "maintenance URLs include /postgres" "postgres db" $?
grep -q "/olivares" "$LOG"
check "app-database URLs include /olivares" "olivares db" $?
grep -q "NOSUPERUSER BYPASSRLS" "$LOG"
check "admin role is NOSUPERUSER BYPASSRLS" "admin posture" $?
grep -q "GRANT CONNECT ON DATABASE olivares TO olivares_admin" "$LOG"
check "admin CONNECT is granted on olivares" "connect" $?
grep -q "CREATE EXTENSION IF NOT EXISTS vector" "$LOG"
check "vector extension is created on the app database" "vector" $?
grep -q "NOBYPASSRLS" "$SQL"
check "app-role SQL still declares NOBYPASSRLS" "app file" $?

for at in 1 2 3 4 5; do
	set +e
	run_provision "$at" >"$WORK/fail$at.out" 2>"$WORK/fail$at.err"
	rc=$?
	set -e
	[ "$rc" -eq 17 ]
	check "provision command $at failure preserves fixture RC 17" "rc=17" $?
	n="$(cat "$COUNT")"
	[ "$n" -eq "$at" ]
	check "provision command $at failure does not continue" "count=$at" $?
done

# Missing psql: PATH has fixture sudo/apt-get and not psql. bash is invoked by
# absolute path because `env PATH=... bash` would search bash on that PATH.
# Host /usr/bin is not on PATH, so the real client and apt-get are not visible.
NOPSQL="$WORK/nopsql-bin"
mkdir -p "$NOPSQL"
cp "$BIN/sudo" "$BIN/apt-get" "$NOPSQL/"
BASH_ABS="$(command -v bash)"
set +e
env PATH="$NOPSQL" PGHOSTPORT="127.0.0.1:${VALID}" "$BASH_ABS" "$HELPER" provision \
	>"$WORK/nopsql.out" 2>"$WORK/nopsql.err"
rc=$?
set -e
[ "$rc" -ne 0 ]
check "missing psql does not succeed and does not install" "nonzero" $?
grep -q "sudo invoked" "$WORK/nopsql.err"
check "install fallback used the fixture sudo, not the host" "fixture" $?

grep -q "scripts/ci-postgres-service.sh resolve" "$WF_MAIN"
check "mainline-ci calls resolve" "present" $?
grep -q "scripts/ci-postgres-service.sh provision" "$WF_MAIN"
check "mainline-ci calls provision" "present" $?
grep -q "scripts/ci-postgres-service.sh resolve" "$WF_PROF"
check "profile workflow calls resolve" "present" $?
grep -q "scripts/ci-postgres-service.sh provision" "$WF_PROF"
check "profile workflow calls provision" "present" $?

set +e
python3 - <<'PY' "$WF_MAIN"
import re, sys

# The caller scope of scripts/ci-postgres-service.sh, in document order. HR1 split the
# hot-race work out into its own job (mainline-ci.yml race-hot-manifest); that job owns a
# Postgres service of its own, so it resolves and provisions exactly like the other three.
# This list is the contract: a caller that is not on it, or one on it that stopped calling,
# is the finding.
WANT = ["race-rest", "race-core", "race-sessions", "race-hot-manifest"]

text = open(sys.argv[1], encoding="utf-8").read()
jobs = re.split(r"\n  (?=[A-Za-z0-9_-]+:)", text)
hits = []
# Job membership alone was too coarse to defend the contract: a job that kept `resolve`
# and lost `provision` still contains the helper's name, so it stayed on the list and the
# missing role/extension sequence went unnoticed. Each declared caller owns a service, so
# each must do BOTH, once.
calls = {}
for block in jobs:
    first = block.split(":", 1)[0].strip()
    if "ci-postgres-service.sh" in block:
        hits.append(first)
        calls[first] = (
            block.count("ci-postgres-service.sh resolve"),
            block.count("ci-postgres-service.sh provision"),
        )
print(" ".join(hits))
unpaired = [(j, calls[j]) for j in WANT if j in calls and calls[j] != (1, 1)]
if hits == WANT and not unpaired:
    raise SystemExit(0)
# This was a bare SystemExit(1). Under the errexit in force here that killed the WHOLE
# battery on the line above: the reader got the actual list and nothing else - no FAIL
# line, no summary, and no way to tell "a declared job stopped calling the helper" from
# "a new job started calling it". Name both sides and the difference; the caller below
# turns the verdict into a recorded FAIL instead of an abort.
missing = [j for j in WANT if j not in hits]
extra = [j for j in hits if j not in WANT]
out = sys.stderr
out.write("helper caller inventory mismatch in mainline-ci.yml\n")
out.write("  expected (in order): %s\n" % " ".join(WANT))
out.write("  actual   (in order): %s\n" % (" ".join(hits) or "(none)"))
if missing:
    out.write("  declared here but no longer calls the helper: %s\n" % " ".join(missing))
if extra:
    out.write("  calls the helper but is not declared here: %s\n" % " ".join(extra))
if not missing and not extra and not unpaired:
    out.write("  same jobs, different document order\n")
for job, (r, pv) in unpaired:
    out.write("  %s calls resolve x%d and provision x%d; want one of each\n" % (job, r, pv))
raise SystemExit(1)
PY
rc=$?
set -e
check "exactly race-rest, race-core, race-sessions and race-hot-manifest call the helper, each resolving and provisioning once" "job scoped" "$rc"

DIGEST="pgvector/pgvector:pg16@sha256:a36250871de0833b8757561c72f2477ef1ddd1101afa4e617fb552e0de514c6b"
python3 - <<'PY' "$WF_MAIN" "$WF_PROF" "$DIGEST"
import re, sys
main, prof, digest = sys.argv[1], sys.argv[2], sys.argv[3]

def job_block(path, name):
    text = open(path, encoding="utf-8").read().splitlines()
    start = None
    for i, line in enumerate(text):
        if line == f"  {name}:":
            start = i
            break
    if start is None:
        raise SystemExit(f"missing job {name}")
    lines = [text[start]]
    for line in text[start + 1 :]:
        if re.match(r"  [A-Za-z0-9_-]+:\s*$", line):
            break
        lines.append(line)
    return "\n".join(lines)

rest = job_block(main, "race-rest")
core = job_block(main, "race-core")
sess = job_block(main, "race-sessions")
# HR1's hot-race job owns the same kind of service and must meet the same image, port,
# limit and STRICT_PG contract; omitting it here would have let it drift alone.
hot = job_block(main, "race-hot-manifest")
profb = job_block(prof, "package-profile")
def ports_entries(block):
    lines = block.splitlines()
    out = []
    in_ports = False
    for line in lines:
        if re.match(r"^\s+ports:\s*$", line):
            in_ports = True
            continue
        if in_ports:
            if re.match(r"^\s+- ", line):
                out.append(line.strip())
            elif re.match(r"^\s+\S", line) and not line.strip().startswith("#"):
                in_ports = False
    return out

for label, block in (
    ("race-rest", rest),
    ("race-core", core),
    ("race-sessions", sess),
    ("race-hot-manifest", hot),
    ("package-profile", profb),
):
    if digest not in block:
        raise SystemExit(f"{label} missing digest")
    if "OLIVARES_GATE_STRICT_PG" not in block:
        raise SystemExit(f"{label} missing STRICT_PG")
    if "--memory=1g" not in block or "--cpus=2" not in block or "--pids-limit=512" not in block:
        raise SystemExit(f"{label} missing service limits")
    ports = ports_entries(block)
    if any(":" in p for p in ports):
        raise SystemExit(f"{label} binds a host port: {ports}")
    # EVERY mapping must be the bare container port. This used to read `!= ["- 5432"]`,
    # which also asserted "exactly one service" - true when it was written, never the
    # property being defended. control-plane and race-core now own a second cluster for
    # TestDRDestinationUnrelatedCluster, so the arity moved; the defence did not.
    if not ports or any(p.strip() != "- 5432" for p in ports):
        raise SystemExit(f"{label} ports={ports!r}, want every mapping bare 5432")
print("parity ok")
PY
check "PG digest, bare 5432, limits and STRICT_PG match across race-rest, race-core, race-sessions, race-hot-manifest and package-profile" "parity" $?


# --- resolve: the SECOND cluster, presence vs emptiness ---------------------------------
#
# PGPORT_OTHER is BOUND by the jobs that declare services.postgres_other. GitHub hands a
# missing mapping over as the EMPTY STRING, not as unset, so the two states mean opposite
# things and the resolver must not read them alike:
#   UNSET          the job owns no second service   -> proceed, previous behaviour
#   PRESENT EMPTY  a required mapping is missing    -> refuse BEFORE any command-file write
# Measured at e51125f4e3, before this contract existed: present-empty exited 0 and wrote the
# primary DSNs. Every refusal below also asserts the caller's pre-existing sentinel survived,
# because "refuses" and "refuses without having written first" are different guarantees.
OTHER_A="$VALID"
OTHER_B=$((VALID + 1))
[ "$OTHER_B" -le "$HIPORT" ] || OTHER_B=$((VALID - 1))

run_other() { # run_other <primary> [other|__unset__]
	: >"$JOB_ENV"
	printf 'SENTINEL=pre-existing\n' >"$JOB_ENV"
	if [ "${2-__unset__}" = "__unset__" ]; then
		env -u PGPORT_OTHER PGPORT_HOST="$1" GITHUB_ENV="$JOB_ENV" bash "$HELPER" resolve
	else
		PGPORT_HOST="$1" PGPORT_OTHER="$2" GITHUB_ENV="$JOB_ENV" bash "$HELPER" resolve
	fi
}
sentinel_intact() { # the command file holds the sentinel and nothing else
	[ "$(wc -l <"$JOB_ENV")" -eq 1 ] && grep -qx 'SENTINEL=pre-existing' "$JOB_ENV"
}

if run_other "$OTHER_A" __unset__ >"$WORK/o-unset.out" 2>"$WORK/o-unset.err"; then rc=0; else rc=$?; fi
if [ "$rc" -eq 0 ] && ! grep -q OLIVARES_TEST_POSTGRES_OTHER_DSN "$JOB_ENV" &&
	grep -q OLIVARES_TEST_POSTGRES_SUPERUSER_DSN "$JOB_ENV"; then res=0; else res=1; fi
check "resolve with PGPORT_OTHER UNSET proceeds and emits no second DSN" "rc=0, primary intact" "$res"

if run_other "$OTHER_A" "" >"$WORK/o-empty.out" 2>"$WORK/o-empty.err"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ] && grep -q 'set but empty' "$WORK/o-empty.err" && sentinel_intact; then res=0; else res=1; fi
check "resolve with PGPORT_OTHER PRESENT-EMPTY refuses before writing the command file" "rc!=0, sentinel alone" "$res"

if run_other "$OTHER_A" "$OTHER_B" >"$WORK/o-ok.out" 2>"$WORK/o-ok.err"; then rc=0; else rc=$?; fi
if [ "$rc" -eq 0 ] && grep -q "OLIVARES_TEST_POSTGRES_OTHER_DSN=.*:${OTHER_B}/postgres" "$JOB_ENV"; then res=0; else res=1; fi
check "resolve with a distinct valid second port emits the second maintenance DSN" "rc=0, other DSN" "$res"

if run_other "$OTHER_A" "$OTHER_A" >"$WORK/o-same.out" 2>"$WORK/o-same.err"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ] && grep -q 'two distinct servers' "$WORK/o-same.err" && sentinel_intact; then res=0; else res=1; fi
check "resolve refuses when both services resolve to the same host port" "rc!=0, sentinel alone" "$res"

if run_other "$OTHER_A" "abc" >"$WORK/o-nonum.out" 2>"$WORK/o-nonum.err"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ] && grep -q 'non-numeric' "$WORK/o-nonum.err" && sentinel_intact; then res=0; else res=1; fi
check "resolve refuses a non-numeric second port" "rc!=0, sentinel alone" "$res"

if run_other "$OTHER_A" "5432" >"$WORK/o-range.out" 2>"$WORK/o-range.err"; then rc=0; else rc=$?; fi
if [ "$rc" -ne 0 ] && grep -q 'outside this host' "$WORK/o-range.err" && sentinel_intact; then res=0; else res=1; fi
check "resolve refuses a second port outside this kernel's ephemeral range" "rc!=0, sentinel alone" "$res"

# The two conditions in the helper MUST be the same predicate: a validation that fires on a
# state the emitter does not recognise (or the reverse) is two decisions wearing one name.
if [ "$(grep -c '\${PGPORT_OTHER+set}' "$HELPER")" -eq 2 ] &&
	[ "$(grep -c '\${PGPORT_OTHER:-}' "$HELPER")" -eq 0 ]; then res=0; else res=1; fi
check "validation and emission share one presence predicate (+set, never :-)" "2 and 0" "$res"

echo
echo "test-ci-postgres-service: $pass passed, $fail failed, $skip skipped"
[ "$fail" -eq 0 ] || exit 1
exit 0
