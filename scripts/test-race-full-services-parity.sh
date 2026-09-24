#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-race-full-services-parity.sh — race-full must export every OLIVARES_TEST_POSTGRES_*
# variable that a mainline-ci race job exports.
#
# CENSUS-SUBJECT: internal
#   Subject: .github/workflows/mainline-ci.yml and .github/workflows/race-full.yml.
#
# Why this gate exists, measured: olivares.preprod race-full run 35147072374, GitHub-hosted,
# job race-workspace (heavy-stores#a), step "race this group":
#   --- FAIL: TestDRDestinationUnrelatedCluster (0.00s)
#   drdestinationauthority_pg_test.go:187: a second owned real cluster is required
# The same test already recorded fail in run 35136307725's heavy-stores artifact, masked
# then by the shard's timeout. Two databases on one server share system_identifier, so the
# test needs a second SERVER. mainline-ci.yml provisions services.postgres_other and
# exports OLIVARES_TEST_POSTGRES_OTHER_DSN (control-plane inline; race-core and
# race-sessions via scripts/ci-postgres-service.sh resolve + PGPORT_OTHER). race-full.yml
# provisioned one postgres service and never exported OTHER_DSN. Class: sibling workflow
# service inventories diverged.
#
# What it checks: the UNION of OLIVARES_TEST_POSTGRES_* actually exported by mainline-ci
# jobs whose id starts with `race` must all be exported by race-full's race-workspace job
# (the job that races core/internal/store/sqlstore). A comment is not an export. Calling
# ci-postgres-service.sh resolve exports the helper's unconditional POSTGRES_* set, and
# OTHER_DSN only when the job binds PGPORT_OTHER (or declares services.postgres_other and
# the helper is used — same decision the helper itself documents).
#
# race-root is not required to export OTHER_DSN: cmd/olivares does not read it. Putting
# OTHER_DSN only on race-root must still fail, because sqlstore runs in race-workspace.
#
# Answers: 0 clean · 1 finding · 2 could not look.
#
# Default run: fixture battery first (proves the gate can go red and green), then the
# real tree. OLIVARES_RACE_FULL_SERVICES_TREE_ONLY=1 skips the battery.
set -uo pipefail
export LC_ALL=C

HERE="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(CDPATH= cd -- "$HERE/.." && pwd)"
HELPER="$ROOT/scripts/ci-postgres-service.sh"
MAINLINE="${OLIVARES_RACE_FULL_PARITY_MAINLINE:-$ROOT/.github/workflows/mainline-ci.yml}"
RACEFULL="${OLIVARES_RACE_FULL_PARITY_RACEFULL:-$ROOT/.github/workflows/race-full.yml}"

blind() {
	printf 'race-full-services-parity: NO HE PODIDO MIRAR — %s\n' "$*" >&2
	exit 2
}

command -v python3 >/dev/null 2>&1 || blind "python3 is not on PATH"
[ -r "$HELPER" ] || blind "cannot read $HELPER"

# shellcheck source=lib/exec-workdir.sh
. "$HERE/lib/exec-workdir.sh" || blind "missing scripts/lib/exec-workdir.sh"
WORK="$(olivares_pick_exec_workdir race-full-svc-parity)" || blind "no executable work directory"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0

# compare_pair <mainline.yml> <race-full.yml>
# Prints the finding (or "clean") on stdout. Exit status is 0 / 1 / 2.
compare_pair() {
	python3 - "$1" "$2" "$HELPER" <<'PY'
import re, sys

mainline_path, racefull_path, helper_path = sys.argv[1], sys.argv[2], sys.argv[3]

def read(path):
    try:
        with open(path, encoding="utf-8") as f:
            return f.read().replace("\r\n", "\n").replace("\r", "\n")
    except OSError as e:
        print("cannot read %s: %s" % (path, e), file=sys.stderr)
        sys.exit(2)

def jobs_map(text):
    m = re.search(r"(?m)^jobs:\s*$", text)
    if not m:
        return {}
    rest = text[m.end():]
    top = re.search(r"(?m)^[A-Za-z]", rest)
    if top:
        rest = rest[: top.start()]
    parts = re.split(r"(?m)^(?=  [A-Za-z0-9_-]+:\s*$)", rest)
    out = {}
    for part in parts:
        part = part.strip("\n")
        if not part.strip():
            continue
        first = part.split("\n", 1)[0].strip()
        if first.startswith("#"):
            continue
        name = first.rstrip(":").strip()
        if re.fullmatch(r"[A-Za-z0-9_-]+", name):
            out[name] = part
    return out

def uncommented_lines(block):
    for line in block.splitlines():
        s = line.lstrip()
        if not s or s.startswith("#"):
            continue
        yield line, s

def has_key(block, key):
    pat = re.compile(r"^\s+" + re.escape(key) + r":(?:\s|$)")
    for line, _s in uncommented_lines(block):
        if pat.search(line):
            return True
    return False

def echo_postgres_vars(block):
    found = set()
    for _, s in uncommented_lines(block):
        found.update(re.findall(r'echo\s+"?(OLIVARES_TEST_POSTGRES_[A-Z0-9_]+)=', s))
    return found

def helper_sets(helper_text):
    all_vars = set(re.findall(r'echo\s+"?(OLIVARES_TEST_POSTGRES_[A-Z0-9_]+)=', helper_text))
    other = "OLIVARES_TEST_POSTGRES_OTHER_DSN"
    always = set(v for v in all_vars if v != other)
    return always, other

def job_exports(block, always, other):
    exported = echo_postgres_vars(block)
    if "ci-postgres-service.sh resolve" in block:
        exported |= always
        if has_key(block, "PGPORT_OTHER") or has_key(block, "postgres_other"):
            exported.add(other)
    return exported

helper_text = read(helper_path)
always, other = helper_sets(helper_text)
if not always:
    print("helper exports no OLIVARES_TEST_POSTGRES_* assignment", file=sys.stderr)
    sys.exit(2)

main_jobs = jobs_map(read(mainline_path))
full_jobs = jobs_map(read(racefull_path))
if "race-workspace" not in full_jobs:
    print("race-full.yml has no race-workspace job", file=sys.stderr)
    sys.exit(2)

main_race = {n: b for n, b in main_jobs.items() if n.startswith("race")}
if not main_race:
    print("mainline-ci.yml has no race* job", file=sys.stderr)
    sys.exit(2)

exporters = {}
union = set()
for name, block in sorted(main_race.items()):
    got = job_exports(block, always, other)
    if got:
        exporters[name] = got
        union |= got

workspace = job_exports(full_jobs["race-workspace"], always, other)
missing = sorted(union - workspace)
if missing:
    print("MISSING " + " ".join(missing))
    for var in missing:
        who = [n for n, vs in exporters.items() if var in vs]
        print("  %s exported by mainline-ci race jobs: %s" % (var, " ".join(who) or "(none)"))
    sys.exit(1)
print("clean")
sys.exit(0)
PY
}

ok() {
	pass=$((pass + 1))
	printf '  ok    %s\n' "$1"
}

mal() {
	fail=$((fail + 1))
	printf '  FAIL  %s — %s\n' "$1" "$2"
}

expect_rc() {
	local label="$1" want="$2" got="$3" out="$4"
	if [ "$got" = "$want" ]; then
		ok "$label"
		return
	fi
	mal "$label" "rc=$got want=$want out=$(printf '%s' "$out" | tr '\n' ' ' | cut -c1-180)"
}

if [ -z "${OLIVARES_RACE_FULL_SERVICES_TREE_ONLY:-}" ]; then
	echo "race-full-services-parity: fixture battery"
	FIX="$WORK/fix"
	mkdir -p "$FIX"

	# Shared always-set that the helper emits. Fixtures that do not call the helper
	# must echo these or they are a different finding than OTHER_DSN.
	ALWAYS_ECHO='          echo "OLIVARES_TEST_POSTGRES_DSN=postgres://x"
          echo "OLIVARES_TEST_POSTGRES_ADMIN_DSN=postgres://x"
          echo "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN=postgres://x"
          echo "OLIVARES_TEST_POSTGRES_REQUIRED=1"'

	write_pair() {
		local tag="$1" main="$2" full="$3"
		local d="$FIX/$tag"
		mkdir -p "$d"
		printf '%s\n' "$main" >"$d/mainline.yml"
		printf '%s\n' "$full" >"$d/race-full.yml"
		printf '%s' "$d"
	}

	MAIN_WITH_OTHER='jobs:
  control-plane:
    steps:
      - run: |
          echo "OLIVARES_TEST_POSTGRES_OTHER_DSN=postgres://x"
  race-core:
    services:
      postgres_other:
        image: example
    steps:
      - env:
          PGPORT_OTHER: "1"
        run: bash scripts/ci-postgres-service.sh resolve
  race-rest:
    steps:
      - run: bash scripts/ci-postgres-service.sh resolve
'

	FULL_NO_OTHER="jobs:
  race-workspace:
    steps:
      - run: |
${ALWAYS_ECHO}
  race-root:
    steps:
      - run: |
${ALWAYS_ECHO}
"

	FULL_WITH_ECHO="jobs:
  race-workspace:
    steps:
      - run: |
${ALWAYS_ECHO}
          echo \"OLIVARES_TEST_POSTGRES_OTHER_DSN=postgres://x\"
  race-root:
    steps:
      - run: |
${ALWAYS_ECHO}
"

	FULL_COMMENT_ONLY="jobs:
  race-workspace:
    steps:
      - run: |
${ALWAYS_ECHO}
          # echo \"OLIVARES_TEST_POSTGRES_OTHER_DSN=postgres://x\"
  race-root:
    steps:
      - run: |
${ALWAYS_ECHO}
"

	FULL_ROOT_ONLY="jobs:
  race-workspace:
    steps:
      - run: |
${ALWAYS_ECHO}
  race-root:
    steps:
      - run: |
${ALWAYS_ECHO}
          echo \"OLIVARES_TEST_POSTGRES_OTHER_DSN=postgres://x\"
"

	FULL_HELPER_OTHER="jobs:
  race-workspace:
    services:
      postgres_other:
        image: example
    steps:
      - env:
          PGPORT_OTHER: \"2\"
        run: bash scripts/ci-postgres-service.sh resolve
  race-root:
    steps:
      - run: bash scripts/ci-postgres-service.sh resolve
"

	MAIN_NO_RACE_OTHER='jobs:
  control-plane:
    steps:
      - run: |
          echo "OLIVARES_TEST_POSTGRES_OTHER_DSN=postgres://x"
  race-rest:
    steps:
      - run: bash scripts/ci-postgres-service.sh resolve
'

	d="$(write_pair gap "$MAIN_WITH_OTHER" "$FULL_NO_OTHER")"
	out="$(compare_pair "$d/mainline.yml" "$d/race-full.yml" 2>&1)"
	rc=$?
	expect_rc "mainline race-core exports OTHER_DSN, race-workspace does not: FINDING (1)" 1 "$rc" "$out"
	grep -q 'OLIVARES_TEST_POSTGRES_OTHER_DSN' <<<"$out" || mal "gap names OTHER_DSN" "out=$out"

	d="$(write_pair echo-ok "$MAIN_WITH_OTHER" "$FULL_WITH_ECHO")"
	out="$(compare_pair "$d/mainline.yml" "$d/race-full.yml" 2>&1)"
	rc=$?
	expect_rc "race-workspace echoes OTHER_DSN: CLEAN (0)" 0 "$rc" "$out"

	d="$(write_pair helper-ok "$MAIN_WITH_OTHER" "$FULL_HELPER_OTHER")"
	out="$(compare_pair "$d/mainline.yml" "$d/race-full.yml" 2>&1)"
	rc=$?
	expect_rc "race-workspace helper+PGPORT_OTHER: CLEAN (0)" 0 "$rc" "$out"

	d="$(write_pair comment "$MAIN_WITH_OTHER" "$FULL_COMMENT_ONLY")"
	out="$(compare_pair "$d/mainline.yml" "$d/race-full.yml" 2>&1)"
	rc=$?
	expect_rc "comment-only OTHER_DSN is not an export: FINDING (1)" 1 "$rc" "$out"

	d="$(write_pair root-only "$MAIN_WITH_OTHER" "$FULL_ROOT_ONLY")"
	out="$(compare_pair "$d/mainline.yml" "$d/race-full.yml" 2>&1)"
	rc=$?
	expect_rc "OTHER_DSN only on race-root: FINDING (1)" 1 "$rc" "$out"

	d="$(write_pair control-only "$MAIN_NO_RACE_OTHER" "$FULL_NO_OTHER")"
	out="$(compare_pair "$d/mainline.yml" "$d/race-full.yml" 2>&1)"
	rc=$?
	expect_rc "OTHER_DSN only on control-plane (not a race job): CLEAN (0)" 0 "$rc" "$out"

	out="$(compare_pair "$FIX/gap/mainline.yml" "$WORK/no-such-race-full.yml" 2>&1)"
	rc=$?
	expect_rc "missing race-full file: COULD NOT LOOK (2)" 2 "$rc" "$out"

	printf 'jobs:\n  setup:\n    runs-on: x\n' >"$FIX/no-workspace.yml"
	out="$(compare_pair "$FIX/gap/mainline.yml" "$FIX/no-workspace.yml" 2>&1)"
	rc=$?
	expect_rc "race-full without race-workspace: COULD NOT LOOK (2)" 2 "$rc" "$out"

	echo "race-full-services-parity: battery $pass passed, $fail failed"
	if [ "$fail" -ne 0 ]; then
		echo "race-full-services-parity: battery is red; the tree check still runs so both signals are visible" >&2
	fi
fi

echo "race-full-services-parity: real tree"
[ -r "$MAINLINE" ] || blind "cannot read $MAINLINE"
[ -r "$RACEFULL" ] || blind "cannot read $RACEFULL"
tree_out="$(compare_pair "$MAINLINE" "$RACEFULL" 2>&1)"
tree_rc=$?
printf '%s\n' "$tree_out"
echo "race-full-services-parity: TREE rc=$tree_rc"

if [ "$tree_rc" -eq 2 ]; then
	exit 2
fi
if [ "${fail:-0}" -ne 0 ] || [ "$tree_rc" -ne 0 ]; then
	exit 1
fi
exit 0
