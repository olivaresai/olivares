#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-check-pr-suite-shards.sh — the mutation battery of scripts/check-pr-suite-shards.sh.
#
# ⛔ THE FIXTURE IS SYNTHETIC ON PURPOSE. The mutants this gate exists to catch are
# "a package is in two shards" and "a package is in no shard", and proving those on the
# real tree would mean creating and deleting packages. The gate takes its universe from a
# file when OLIVARES_PR_SUITE_PACKAGES is set, so the battery can build exactly the tree it
# needs to describe. The no-fire control is the same fixture unmutated: without it, a gate
# that refused everything would pass every mutant and look perfect.
#
# ⛔ AND ONE CASE IS THE REAL TREE, because a battery that only ever reads its own fixtures
# proves the gate parses ITS OWN dialect. Case 1 runs the gate against this repository with
# no override at all and requires CLEAN: it is the only case that can notice the spec and
# the workflow drifting apart in the tree that ships.
#
# ⛔ TWO WORKFLOWS RUN THE SHARDS, and the gate pins both: pr-ci.yml on pull requests and
# mainline-ci.yml on main. The fixture carries a miniature of each ($T/wf.yml and
# $T/wf-main.yml); cases 43-46 break the mainline one alone, so a gate that only read
# pr-ci.yml would pass everything above and fail exactly there.
#
# ⛔ THE NAME GROUPS (cases 21-30) NEED A SECOND SYNTHETIC UNIVERSE. A `group` record splits
# ONE package across shards by test NAME, so the mutants are "a test in no group" and "a test
# in two groups" — the same two defects as the package partition, one level down, and with a
# worse failure: `go test -run` over an expression that matches nothing EXITS 0. A shard would
# report success having run no test at all. OLIVARES_PR_SUITE_TESTS gives the gate its test
# universe from a file, exactly as OLIVARES_PR_SUITE_PACKAGES gives it its packages, so the
# battery describes the tree it needs without writing a single Go file.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || { echo "test-check-pr-suite-shards: cannot enter $ROOT" >&2; exit 2; }

GATE="$ROOT/scripts/check-pr-suite-shards.sh"
[ -x "$GATE" ] || { echo "test-check-pr-suite-shards: $GATE is not executable" >&2; exit 2; }

# Resolved BEFORE any stub directory goes on the PATH, because the case that breaks `grep`
# still needs the real one for every other call it makes.
REAL_GREP="$(command -v grep)" ||
  { echo "test-check-pr-suite-shards: no grep on PATH" >&2; exit 2; }

T="$(mktemp -d "${TMPDIR:-/tmp}/pr-suite-battery.XXXXXX")" ||
  { echo "test-check-pr-suite-shards: cannot create a scratch directory" >&2; exit 2; }
trap 'rm -rf "$T"' EXIT

PASS=0; FAIL=0

# ── the fixture ───────────────────────────────────────────────────────────────────────
# A miniature of the real thing: three shards, a surgical pattern under a broad one (so
# the longest-match rule is exercised), an optional pattern for a module that is not in
# this universe, and one leg.
base_pkgs() {
  cat > "$T/pkgs.txt" <<'EOF'
example.test/tree/cmd/helper
example.test/tree/cmd/tool
example.test/tree/core/api
example.test/tree/core/internal/store
example.test/tree/modules/one
example.test/tree/modules/two
EOF
}

# The test universe: `<import path> <top-level function>`, one per line. Only the packages a
# `group` record names are ever read from it, so the cases that partition nothing ignore it.
# FuzzDelta is here on purpose: `-run` selects fuzz targets and examples too, and a partition
# written as if only `Test*` existed would drop them without a word.
base_tests() {
  cat > "$T/tests.txt" <<'EOF'
example.test/tree/cmd/tool TestAlpha
example.test/tree/cmd/tool TestAlphaSecond
example.test/tree/cmd/tool TestBeta
example.test/tree/cmd/tool TestGamma
example.test/tree/cmd/tool FuzzDelta
EOF
}

base_spec() {
  cat > "$T/spec.txt" <<'EOF'
shard a
shard b
pkg a example.test/tree/core/internal/...
pkg b example.test/tree/core/...
pkg b example.test/tree/modules/...
pkg a example.test/tree/cmd/...
pkg a example.test/tree/absent/...
optional example.test/tree/absent/...
leg b test:example-leg
go-timeout-minutes 35
step-ceiling-minutes 45
job-ceiling-minutes 55
EOF
}

base_wf() {
  cat > "$T/wf.yml" <<'EOF'
name: fixture

jobs:
  pr-test-shard:
    if: github.repository == 'example/repo'
    strategy:
      fail-fast: false
      matrix:
        shard: [a, b]
    runs-on: ubuntu-latest
    timeout-minutes: 55
    steps:
      - name: one shard
        timeout-minutes: 45
        run: task test:functional:shard
EOF
  # The mainline miniature: the shard matrix beside a required aggregate job with ceilings of
  # its own, so the reader has to attribute each ceiling to the job that declares it.
  cat > "$T/wf-main.yml" <<'EOF'
name: fixture-mainline

jobs:
  classify:
    runs-on: ubuntu-latest
    steps:
      - run: echo classify
  functional-shard:
    needs: [classify]
    strategy:
      fail-fast: false
      matrix:
        shard: [a, b]
    runs-on: ubuntu-latest
    timeout-minutes: 55
    steps:
      - name: one shard
        timeout-minutes: 45
        run: task test:functional:shard
  control-plane:
    needs: [classify, functional-shard]
    if: ${{ always() }}
    runs-on: ubuntu-latest
    timeout-minutes: 320
    steps:
      - name: a lint with a ceiling of its own
        timeout-minutes: 15
        run: echo lint
EOF
}

base_taskfile() {
  cat > "$T/Taskfile.yml" <<'EOF'
version: '3'
tasks:
  test:example-leg:
    cmds:
      - echo leg
EOF
}

reset_fixture() { base_pkgs; base_spec; base_wf; base_taskfile; base_tests; }

# The same fixture with cmd/tool split by test NAME across two shards: one named group and
# one remainder. `pkg a example.test/tree/cmd/...` stays declared and stays honest — it still
# owns cmd/helper — which is the shape the real tree has once the root package is partitioned.
group_fixture() {
  reset_fixture
  {
    printf 'shard t1\n'
    printf 'group a example.test/tree/cmd/tool alpha A\n'
    printf 'group t1 example.test/tree/cmd/tool rest *\n'
  } >> "$T/spec.txt"
  sed -i 's|shard: \[a, b\]|shard: [a, b, t1]|' "$T/wf.yml" "$T/wf-main.yml"
}

run_gate() {
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" \
  OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  OLIVARES_PR_CI_WF="$T/wf.yml" \
  OLIVARES_MAINLINE_CI_WF="$T/wf-main.yml" \
  OLIVARES_TASKFILE="$T/Taskfile.yml" \
  bash "$GATE" > "$T/out.txt" 2> "$T/err.txt"
  echo "$?"
}

# case <name> <expected rc> [<text that must appear in the output>]
check() {
  local name="$1" want="$2" needle="${3:-}" got
  got="$(run_gate)"
  if [ "$got" != "$want" ]; then
    printf '  ✗ %s — expected rc %s, got %s\n' "$name" "$want" "$got" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if [ -n "$needle" ] && ! grep -qF -- "$needle" "$T/out.txt" "$T/err.txt"; then
    printf '  ✗ %s — rc %s is right but the reason is not said: no %q in the output\n' \
      "$name" "$want" "$needle" >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (rc %s)\n' "$name" "$got"
  PASS=$((PASS + 1))
}


# ── the runner, not the gate ──────────────────────────────────────────────────────────
# Four of the cases below run scripts/pr-suite-shards.sh itself, because the last line of
# defense against a shard that selects nothing is in the RUNNER: the gate can be skipped, a
# fork can carry a spec the gate never read, and `go list` can answer differently on the
# machine that runs the suite than on the one that reviewed it. `go` and `task` are replaced
# by stubs that record the call and fail, so a runner that reaches them cannot pass by
# exiting 1 for some other reason: the case checks the REASON and the marker, not the number.
#
# A case that WANTS the toolchain reached says so with STUB_TASK_EXIT, and checks the log with
# check_runner_log instead of the marker. Every stub is rewritten on each call, so no case
# inherits another's PATH.
stub_tools() {
  mkdir -p "$T/stub"
  # A case can ask for a `grep` that cannot look, to prove the runner tells that apart from a
  # count of zero. It is removed on every call, so no case inherits another's PATH.
  rm -f "$T/stub/grep"
  if [ -n "${STUB_GREP_EXIT:-}" ]; then
    # The single quotes are the point: `$1` and `$@` are written into the stub.
    # shellcheck disable=SC2016
    {
      printf '#!/usr/bin/env bash\n'
      printf '# Only the counting call is broken; every other call is the real grep.\n'
      printf 'if [ "$1" = "-cE" ]; then echo "grep: fixture: cannot look" >&2; exit %s; fi\n' \
        "$STUB_GREP_EXIT"
      printf 'exec %s "$@"\n' "$REAL_GREP"
    } > "$T/stub/grep"
    chmod +x "$T/stub/grep"
  fi
  local tool code
  for tool in go task; do
    code=9
    [ "$tool" = task ] && code="${STUB_TASK_EXIT:-9}"
    {
      printf '#!/usr/bin/env bash\n'
      printf 'printf "%%s\\\\n" "%s $*" >> %s\n' "$tool" "$T/stub-was-called.txt"
      printf 'exit %s\n' "$code"
    } > "$T/stub/$tool"
    chmod +x "$T/stub/$tool"
  done
  : > "$T/stub-was-called.txt"
}

# OLIVARES_PR_SUITE_RUNNER names the script these cases drive, and it has a name of its own
# rather than sharing the gate's OLIVARES_PR_SUITE_READER, which case 37 sets and unsets for
# its own purpose. Defaulting to the tree's copy keeps every case honest; overriding it is
# what makes a mutation of the runner testable WITHOUT editing the runner in the worktree —
# which is how the case below was shown to fail when the line it pins is put back.
run_runner() {
  stub_tools
  PATH="$T/stub:$PATH" \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" \
  OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  bash "${OLIVARES_PR_SUITE_RUNNER:-$ROOT/scripts/pr-suite-shards.sh}" run "$1" \
    > "$T/out.txt" 2> "$T/err.txt"
  echo "$?"
}

# check_runner <name> <shard> <expected rc> <text that must appear>
check_runner() {
  local name="$1" shard="$2" want="$3" needle="$4" got
  got="$(run_runner "$shard")"
  if [ "$got" != "$want" ]; then
    printf '  ✗ %s — expected rc %s, got %s\n' "$name" "$want" "$got" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if ! grep -qF -- "$needle" "$T/out.txt" "$T/err.txt"; then
    printf '  ✗ %s — rc %s is right but the reason is not said: no %q in the output\n' \
      "$name" "$want" "$needle" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if [ -s "$T/stub-was-called.txt" ]; then
    printf '  ✗ %s — the runner reached the toolchain before refusing: %s\n' \
      "$name" "$(head -1 "$T/stub-was-called.txt")" >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (rc %s, nothing was run)\n' "$name" "$got"
  PASS=$((PASS + 1))
}

# check_runner_log <name> <shard> <expected rc> <text that must appear>…
# The opposite question to check_runner's: not "did it refuse before running anything" but
# "did it run everything it owes and still report the red". One assertion per line of the log
# that has to be there, because the defect this catches is a MISSING line and not a wrong one.
check_runner_log() {
  local name="$1" shard="$2" want="$3"
  shift 3
  local got needle
  got="$(run_runner "$shard")"
  if [ "$got" != "$want" ]; then
    printf '  ✗ %s — expected rc %s, got %s\n' "$name" "$want" "$got" >&2
    sed 's/^/      /' "$T/out.txt" "$T/err.txt" | head -8 >&2
    FAIL=$((FAIL + 1)); return
  fi
  for needle in "$@"; do
    if grep -qF -- "$needle" "$T/out.txt" "$T/err.txt"; then
      continue
    fi
    printf '  ✗ %s — rc %s is right but the log does not carry %q\n' "$name" "$want" "$needle" >&2
    sed 's/^/      /' "$T/out.txt" | head -8 >&2
    FAIL=$((FAIL + 1)); return
  done
  printf '  ✓ %s (rc %s, %d log line(s) confirmed)\n' "$name" "$got" "$#"
  PASS=$((PASS + 1))
}

# A root whose scripts/with-pg-env.sh answers instead of running Go: RED for the whole-package
# invocation and GREEN for the one that carries a -run. The runner resolves that wrapper
# relative to OLIVARES_ROOT, so the fixture replaces it without touching the tree's own copy
# and without a compiler anywhere near the case.
fake_pg_env_root() {
  local root="$T/fakeroot"
  rm -rf "$root"; mkdir -p "$root/scripts"
  # The single quotes are the point: `$@` and `$a` are written into the stub, not expanded.
  # shellcheck disable=SC2016
  {
    printf '#!/usr/bin/env bash\n'
    printf 'for a in "$@"; do\n'
    printf '  if [ "$a" = "-run" ]; then echo "ok  the name group ran"; exit 0; fi\n'
    printf 'done\n'
    printf 'echo "FAIL example.test/tree/cmd/helper"\n'
    printf 'exit 1\n'
  } > "$root/scripts/with-pg-env.sh"
  chmod +x "$root/scripts/with-pg-env.sh"
}

# The gate with the REAL test enumerator — no OLIVARES_PR_SUITE_TESTS — so that the `go list`
# path itself is under test. GOPROXY=off because the fixture package paths are not resolvable
# anywhere: without it `go list` would try to fetch the module it cannot find, and a battery
# that reaches the network is a battery that fails on a machine that has none.
run_gate_real_enum() {
  GOPROXY=off GOFLAGS='' \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" \
  OLIVARES_PR_CI_WF="$T/wf.yml" \
  OLIVARES_MAINLINE_CI_WF="$T/wf-main.yml" \
  OLIVARES_TASKFILE="$T/Taskfile.yml" \
  bash "$GATE" > "$T/out.txt" 2> "$T/err.txt"
  echo "$?"
}

check_real_enum() {
  local name="$1" want="$2" needle="${3:-}" got
  got="$(run_gate_real_enum)"
  if [ "$got" != "$want" ]; then
    printf '  ✗ %s — expected rc %s, got %s\n' "$name" "$want" "$got" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if [ -n "$needle" ] && ! grep -qF -- "$needle" "$T/out.txt" "$T/err.txt"; then
    printf '  ✗ %s — rc %s is right but the reason is not said: no %q in the output\n' \
      "$name" "$want" "$needle" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (rc %s)\n' "$name" "$got"
  PASS=$((PASS + 1))
}

# A miniature Go WORKSPACE on disk: three modules, the middle one with a package that does
# not parse. It exists for one question only — what the gate does when it cannot finish
# reading the universe — and it is the cheapest tree that can ask it. No module imports
# anything outside the standard library, so nothing is fetched.
work_fixture() {
  local root="$T/work" m
  rm -rf "$root"; mkdir -p "$root"
  {
    printf 'go 1.26.6\n\n'
    printf 'use (\n\t./mod-a\n\t./mod-broken\n\t./mod-c\n)\n'
  } > "$root/go.work"
  for m in mod-a mod-c; do
    mkdir -p "$root/$m"
    printf 'module example.test/%s\n\ngo 1.26.6\n' "$m" > "$root/$m/go.mod"
    printf 'package %s\n' "${m//-/}" > "$root/$m/pkg.go"
    printf 'package %s\n\nimport "testing"\n\nfunc TestThing(t *testing.T) { _ = t }\n' \
      "${m//-/}" > "$root/$m/pkg_test.go"
  done
  mkdir -p "$root/mod-broken"
  printf 'module example.test/mod-broken\n\ngo 1.26.6\n' > "$root/mod-broken/go.mod"
  # Valid go.work, valid go.mod, unparsable Go: `go work edit -json` still lists the module,
  # and `go list ./...` inside it fails. That is the shape the walk has to survive.
  printf 'package\n' > "$root/mod-broken/broken.go"
}

# A package on disk whose test file carries a name the old enumerator could not read whole.
# `Ü` is not a lower-case letter, so Go's own rule accepts TestÜberSync as a test.
enum_fixture() {
  local dir="$T/enum"
  rm -rf "$dir"; mkdir -p "$dir"
  printf 'module example.test/enum\n\ngo 1.26.6\n' > "$dir/go.mod"
  printf 'package enum\n' > "$dir/enum.go"
  {
    printf 'package enum\n\nimport "testing"\n\n'
    printf 'func TestAsciiOne(t *testing.T) { _ = t }\n'
    printf 'func Test\xc3\x9cberSync(t *testing.T) { _ = t }\n'
  } > "$dir/enum_test.go"
}

# The same, with a `func Test…` line whose name the reader must refuse rather than guess: a
# type parameter list is not a test signature, and a reader that silently took `TestGeneric`
# would report a test `go test` will never run.
enum_bad_fixture() {
  local dir="$T/enumbad"
  rm -rf "$dir"; mkdir -p "$dir"
  printf 'module example.test/enumbad\n\ngo 1.26.6\n' > "$dir/go.mod"
  printf 'package enumbad\n' > "$dir/enumbad.go"
  {
    printf 'package enumbad\n\nimport "testing"\n\n'
    printf 'func TestPlain(t *testing.T) { _ = t }\n'
    printf 'func TestGeneric[T any](t *testing.T) { _ = t }\n'
  } > "$dir/enumbad_test.go"
}


# The gate with NO package override at all, so that the walk over the go.work modules is the
# thing under test. OLIVARES_ROOT and OLIVARES_PR_SUITE_READER come from the caller.
run_gate_no_pkg_override() {
  GOPROXY=off GOFLAGS='' \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_CI_WF="$T/wf.yml" \
  OLIVARES_MAINLINE_CI_WF="$T/wf-main.yml" \
  OLIVARES_TASKFILE="$T/Taskfile.yml" \
  bash "$GATE" > "$T/out.txt" 2> "$T/err.txt"
  echo "$?"
}

check_no_pkg_override() {
  local name="$1" want="$2" needle="${3:-}" got
  got="$(run_gate_no_pkg_override)"
  if [ "$got" != "$want" ]; then
    printf '  ✗ %s — expected rc %s, got %s\n' "$name" "$want" "$got" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if [ -n "$needle" ] && ! grep -qF -- "$needle" "$T/out.txt" "$T/err.txt"; then
    printf '  ✗ %s — rc %s is right but the reason is not said: no %q in the output\n' \
      "$name" "$want" "$needle" >&2
    sed 's/^/      /' "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (rc %s)\n' "$name" "$got"
  PASS=$((PASS + 1))
}

# The enumerator on its own, against a package on disk. `tests` is the subcommand the gate
# and the runner both go through, so what it prints is what both of them will believe.
#
# ⛔ LC_ALL=C, AND THAT IS THE POINT. Which names a character class matches depends on the
# locale and on which `grep` the machine has: the same file enumerated one way here and
# another on a runner would mean the gate certifies a partition the suite does not run. The
# battery pins the answer under the C locale, which is what a hosted runner has.
# check_tests <name> <root> <package> <the names, in order, space separated>
check_tests() {
  local name="$1" root="$2" pkg="$3" want="$4" got rc=0
  LC_ALL=C GOPROXY=off GOFLAGS='' OLIVARES_ROOT="$root" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
    bash "$ROOT/scripts/pr-suite-shards.sh" tests "$pkg" > "$T/out.txt" 2> "$T/err.txt" || rc=$?
  got="$(tr '\n' ' ' < "$T/out.txt")"
  got="${got% }"
  if [ "$rc" != 0 ]; then
    printf '  ✗ %s — the reader refused (rc %s)\n' "$name" "$rc" >&2
    sed 's/^/      /' "$T/err.txt" | head -4 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if [ "$got" != "$want" ]; then
    printf '  ✗ %s — enumerated %q, expected %q\n' "$name" "$got" "$want" >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (%s)\n' "$name" "$got"
  PASS=$((PASS + 1))
}

# check_tests_refused <name> <root> <package> <text that must name file and line>
check_tests_refused() {
  local name="$1" root="$2" pkg="$3" needle="$4" rc=0
  rc=0
  LC_ALL=C GOPROXY=off GOFLAGS='' OLIVARES_ROOT="$root" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
    bash "$ROOT/scripts/pr-suite-shards.sh" tests "$pkg" > "$T/out.txt" 2> "$T/err.txt" || rc=$?
  if [ "$rc" != 1 ]; then
    printf '  ✗ %s — expected rc 1, got %s\n' "$name" "$rc" >&2
    sed 's/^/      /' "$T/out.txt" "$T/err.txt" | head -6 >&2
    FAIL=$((FAIL + 1)); return
  fi
  if ! grep -qF -- "$needle" "$T/err.txt"; then
    printf '  ✗ %s — rc 1 is right but it does not name where: no %q\n' "$name" "$needle" >&2
    sed 's/^/      /' "$T/err.txt" | head -4 >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (rc 1, named)\n' "$name"
  PASS=$((PASS + 1))
}

echo "test-check-pr-suite-shards: the real tree"
if out="$(bash "$GATE" 2>&1)"; then
  printf '  ✓ case 1 — this repository is CLEAN\n'; PASS=$((PASS + 1))
else
  rc=$?
  printf '  ✗ case 1 — the gate does not pass on this repository (rc %s)\n' "$rc" >&2
  printf '%s\n' "$out" | sed 's/^/      /' | head -10 >&2
  FAIL=$((FAIL + 1))
fi

echo "test-check-pr-suite-shards: the fixture"

reset_fixture
check "case 2 — the unmutated fixture is CLEAN (no-fire control)" 0 "CLEAN"

reset_fixture
check "case 3 — an optional pattern matching nothing is a NOTICE, not a finding" 0 "notice"

# THE THREE THE SPLIT EXISTS TO CATCH ─────────────────────────────────────────────────
reset_fixture
# Two patterns of the same length in two shards: the assignment rule does not decide.
sed -i 's|^pkg b example.test/tree/modules/\.\.\.$|pkg b example.test/tree/modules/...\npkg a example.test/tree/modules/...|' "$T/spec.txt"
check "case 4 — a package in TWO shards" 1 "declared in shard"

reset_fixture
printf 'example.test/tree/payments/ledger\n' >> "$T/pkgs.txt"
check "case 5 — a package in NO shard" 1 "belong to NO shard"

reset_fixture
sed -i 's|^pkg a example.test/tree/cmd/\.\.\.$|pkg a example.test/tree/cmd/...\npkg a example.test/tree/nowhere/...|' "$T/spec.txt"
check "case 6 — a shard naming packages that do not exist" 1 "is stale"

# THE MATRIX ──────────────────────────────────────────────────────────────────────────
reset_fixture
sed -i 's|shard: \[a, b\]|shard: [a]|' "$T/wf.yml"
check "case 7 — a shard declared and NOT in the matrix" 1 "disagree"

reset_fixture
sed -i 's|shard: \[a, b\]|shard: [a, b, c]|' "$T/wf.yml"
check "case 8 — a matrix value with no shard" 1 "disagree"

reset_fixture
sed -i 's|shard: \[a, b\]|shard: [b, a]|' "$T/wf.yml"
check "case 9 — the same shards in another ORDER" 1 "disagree"

reset_fixture
sed -i '/^    strategy:$/,/^        shard: \[a, b\]$/d' "$T/wf.yml"
check "case 10 — no job declares a shard matrix at all" 1 "nothing runs in parts"

# THE CLOCKS ──────────────────────────────────────────────────────────────────────────
reset_fixture
sed -i 's|^go-timeout-minutes 35$|go-timeout-minutes 45|' "$T/spec.txt"
check "case 11 — the Go timeout reaches the step ceiling" 1 "not under the step ceiling"

reset_fixture
sed -i 's|^step-ceiling-minutes 45$|step-ceiling-minutes 55|' "$T/spec.txt"
check "case 12 — the step ceiling reaches the job ceiling" 1 "not under the job ceiling"

reset_fixture
sed -i 's|^    timeout-minutes: 55$|    timeout-minutes: 90|' "$T/wf.yml"
check "case 13 — the workflow job ceiling is not the declared one" 1 "declares timeout-minutes"

reset_fixture
sed -i 's|^        timeout-minutes: 45$|        timeout-minutes: 30|' "$T/wf.yml"
check "case 14 — no step carries the declared step ceiling" 1 "step ceiling"

# THE LEGS AND THE SHAPE ──────────────────────────────────────────────────────────────
reset_fixture
sed -i 's|^leg b test:example-leg$|leg b test:no-such-task|' "$T/spec.txt"
check "case 15 — a leg that is not a task" 1 "not a task of"

reset_fixture
printf 'shard c\n' >> "$T/spec.txt"
sed -i 's|shard: \[a, b\]|shard: [a, b, c]|' "$T/wf.yml" "$T/wf-main.yml"
check "case 16 — a shard that owns nothing and declares no leg" 1 "runs nothing and reports success"

reset_fixture
printf 'pkgs a example.test/tree/core/...\n' >> "$T/spec.txt"
check "case 17 — a spec line the reader cannot classify" 1 "not a record this reader knows"

# COULD NOT LOOK ≠ CLEAN ──────────────────────────────────────────────────────────────
reset_fixture
: > "$T/pkgs.txt"
check "case 18 — an empty package universe is COULD NOT LOOK" 2 "certifies nothing"

reset_fixture
rm -f "$T/spec.txt"
check "case 19 — an absent spec is COULD NOT LOOK" 2 "missing"

reset_fixture
rm -f "$T/wf.yml"
check "case 20 — an absent workflow is COULD NOT LOOK" 2 "missing"

# THE NAME GROUPS ─────────────────────────────────────────────────────────────────────
# One package, its tests split across shards by name. Everything above proves the gate can
# see a package that belongs to no shard; these prove it can see a TEST that belongs to no
# group — the same silence one level down, and the one `go test` answers 0 to.
group_fixture
check "case 21 — a package split by test name is CLEAN (no-fire control)" 0 "CLEAN"

group_fixture
# `beta B` instead of the remainder: TestGamma and FuzzDelta are then named by nothing, and
# no group claims what is left. This is the mutant the remainder exists for.
sed -i 's|^group t1 example.test/tree/cmd/tool rest \*$|group t1 example.test/tree/cmd/tool beta B|' "$T/spec.txt"
check "case 22 — a test of a partitioned package that NO group names" 1 "belong to NO name group"

group_fixture
# `Alpha` is a longer prefix of the same names `A` already takes: both groups select them.
printf 'group t1 example.test/tree/cmd/tool alpha-again Alpha\n' >> "$T/spec.txt"
check "case 23 — a test named by TWO groups" 1 "are named by TWO name groups"

group_fixture
# The package is split by name AND sent whole to a shard: every test of it runs twice, and
# the shard that the file says bounds the suite is not the one that does.
printf 'pkg b example.test/tree/cmd/tool\n' >> "$T/spec.txt"
check "case 24 — a group for a package a pkg record also sends whole" 1 "sends it whole"

group_fixture
sed -i 's|shard: \[a, b, t1\]|shard: [a, b]|' "$T/wf.yml"
check "case 25 — the matrix does not list the new group shard" 1 "disagree"

group_fixture
# Stale families are worse here than a stale pattern: `-run` matches nothing, `go test`
# prints ok and exits 0, and the shard publishes a success having run no test.
sed -i 's|^group a example.test/tree/cmd/tool alpha A$|group a example.test/tree/cmd/tool alpha Zeta|' "$T/spec.txt"
check "case 26 — a named group whose families match no test" 1 "names no test in this tree"

group_fixture
printf 'group b example.test/tree/cmd/tool rest-again *\n' >> "$T/spec.txt"
check "case 27 — two remainder groups for one package" 1 "two remainder groups"

group_fixture
sed -i 's|^group t1 example.test/tree/cmd/tool rest \*$|group zz example.test/tree/cmd/tool rest *|' "$T/spec.txt"
sed -i 's|shard: \[a, b, t1\]|shard: [a, b]|' "$T/wf.yml"
sed -i '/^shard t1$/d' "$T/spec.txt"
check "case 28 — a group naming a shard that is never declared" 1 "which is never declared"

group_fixture
sed -i 's|^group a example.test/tree/cmd/tool alpha A$|group a example.test/tree/cmd/absent alpha A|' "$T/spec.txt"
check "case 29 — a group naming a package this tree does not have" 1 "this tree does not have"

group_fixture
# The remainder is EXACT NAMES, so it is the one expression that can outgrow what the kernel
# will carry in a single argument. A gate that lets it through buys an exec failure on the
# runner whose message is about the argument list and never about the tests.
python3 - "$T/tests.txt" <<'PYGEN'
import sys
with open(sys.argv[1], "a", encoding="utf-8") as fh:
    for i in range(2500):
        fh.write("example.test/tree/cmd/tool TestBeyondWhatOneArgumentOfTheKernelWillCarry%04d\n" % i)
PYGEN
check "case 30 — a -run expression over the argument limit" 1 "MAX_ARG_STRLEN"

group_fixture
# ⛔ THE REMAINDER IS MANDATORY EVEN WHEN NOTHING IS ORPHANED TODAY, and this is the only case
# that says so. core/api is split by two families that between them name every test it has, so
# there is no orphan to report and case 22's finding stays quiet: what is left is a package
# whose NEXT test would match nothing. Without this case the rule could be deleted and the
# battery would stay green — measured, by removing it.
{
  printf 'example.test/tree/core/api TestOne\n'
  printf 'example.test/tree/core/api TestTwo\n'
} >> "$T/tests.txt"
{
  printf 'group a example.test/tree/core/api one One\n'
  printf 'group b example.test/tree/core/api two Two\n'
} >> "$T/spec.txt"
check "case 31 — a partitioned package with no remainder group" 1 "none of them is the remainder"

# NOTHING SELECTED IS NOT NOTHING WRONG ───────────────────────────────────────────────
# `go test -run` over an expression that matches no test prints ok and exits 0. Everything
# from here down is about the two places that can happen — the gate, which can refuse the
# spec, and the runner, which is the only thing left when the gate was never run.
group_fixture
# core/api split into two families that between them name both its tests: the remainder is
# then EMPTY, and an empty remainder is not an idle group — it is a `go test` that would
# select nothing, or be skipped, and either way report success.
{
  printf 'example.test/tree/core/api TestOne\n'
  printf 'example.test/tree/core/api TestTwo\n'
} >> "$T/tests.txt"
{
  printf 'group a example.test/tree/core/api one One\n'
  printf 'group b example.test/tree/core/api two Two\n'
  printf 'group t1 example.test/tree/core/api rest *\n'
} >> "$T/spec.txt"
check "case 32 — a remainder that selects nothing" 1 "selects no test"

group_fixture
# Shard t1 owns no whole package, so nothing can fail ahead of the group and the case is
# about the group alone.
sed -i 's|^group t1 example.test/tree/cmd/tool rest \*$|group t1 example.test/tree/cmd/tool zeta Zeta|' "$T/spec.txt"
check_runner "case 33 — the runner refuses a named group that selects nothing" t1 1 "selects no test"

group_fixture
{
  printf 'example.test/tree/core/api TestOne\n'
} >> "$T/tests.txt"
{
  printf 'shard t2\n'
  printf 'group a example.test/tree/core/api one One\n'
  printf 'group t2 example.test/tree/core/api rest *\n'
} >> "$T/spec.txt"
check_runner "case 34 — the runner refuses a remainder that selects nothing" t2 1 "selects no test"

# AN INABILITY IS NEVER AN EMPTY UNIVERSE ─────────────────────────────────────────────
group_fixture
rm -f "$T/tests.txt"
check_real_enum "case 35 — go list cannot place a split package: COULD NOT LOOK" 2 "could not locate"

group_fixture
# The file exists and is readable and simply says nothing about this package. That is a
# package with no test, which has a prepared finding of its own — not an inability.
printf 'example.test/tree/core/api TestOne\n' > "$T/tests.txt"
check "case 36 — a split package with no test reaches its own finding" 1 "has no top-level test"

group_fixture
{
  printf '#!/usr/bin/env bash\n'
  printf 'echo "pr-suite-shards: COULD NOT LOOK — the fixture reader cannot look" >&2\n'
  printf 'exit 2\n'
} > "$T/reader-cannot.sh"
chmod +x "$T/reader-cannot.sh"
export OLIVARES_PR_SUITE_READER="$T/reader-cannot.sh"
check "case 37 — a reader that COULD NOT LOOK is not a finding" 2 "COULD NOT LOOK"
unset OLIVARES_PR_SUITE_READER

# THE WALK CANNOT BE CUT SHORT IN SILENCE ─────────────────────────────────────────────
reset_fixture
work_fixture
# No package override here: the point is the walk itself. The middle module of three fails,
# and the two facts that must survive it are the exit code and the module's name.
export OLIVARES_ROOT="$T/work"
export OLIVARES_PR_SUITE_READER="$ROOT/scripts/pr-suite-shards.sh"
check_no_pkg_override "case 38 — one module of the workspace cannot be listed" 2 "mod-broken"
unset OLIVARES_ROOT OLIVARES_PR_SUITE_READER

# A NAME IS ENUMERATED WHOLE OR IT IS REFUSED ─────────────────────────────────────────
reset_fixture
enum_fixture
check_tests "case 39 — a name outside ASCII is enumerated whole" "$T/enum" example.test/enum \
  "TestAsciiOne TestÜberSync"

reset_fixture
enum_bad_fixture
check_tests_refused "case 40 — a func Test line the reader cannot read whole" "$T/enumbad" \
  example.test/enumbad "enumbad_test.go:6"

# A RED PACKAGE DOES NOT HIDE THE REST OF ITS SHARD ───────────────────────────────────
group_fixture
# Shard a is the only shape in which the question can be asked: two whole packages, one name
# group, and — with this line — a leg. The wrapper the runner goes through is replaced, so the
# package run is red and the group's is green without a compiler being involved at all. The
# leg's `task` is told to succeed, so the shard's rc 1 can only have come from the package.
printf 'leg a test:example-leg\n' >> "$T/spec.txt"
fake_pg_env_root
export OLIVARES_ROOT="$T/fakeroot" STUB_TASK_EXIT=0
check_runner_log "case 41 — a red package does not hide its group or its leg" a 1 \
  "FAIL example.test/tree/cmd/helper" \
  "group alpha of example.test/tree/cmd/tool: 2 of 5 test(s)" \
  "leg test:example-leg"
unset OLIVARES_ROOT STUB_TASK_EXIT

echo
group_fixture
# Shard t1 owns no whole package, so the only thing that can happen in it is the count — and
# the count is made with a `grep` that answers 2. Two is not a count of zero.
export STUB_GREP_EXIT=2
check_runner "case 42 — a count that could not be made is not a count of zero" t1 2 "grep exited 2"
unset STUB_GREP_EXIT

# MAINLINE-CI ─────────────────────────────────────────────────────────────────────────
# The same pins on the second workflow, broken there alone: pr-ci.yml stays correct in every
# case below, so only a gate that reads mainline-ci.yml can turn them red.
echo
reset_fixture
sed -i 's|shard: \[a, b\]|shard: [a]|' "$T/wf-main.yml"
check "case 43 — mainline-ci's matrix lost a shard that pr-ci still runs" 1 "wf-main.yml and the shard list disagree"

reset_fixture
sed -i '/^    strategy:$/,/^        shard: \[a, b\]$/d' "$T/wf-main.yml"
check "case 44 — mainline-ci runs no shard matrix at all" 1 "wf-main.yml declares a"

reset_fixture
sed -i '/^  functional-shard:$/,/^  control-plane:$/ s|^    timeout-minutes: 55$|    timeout-minutes: 90|' "$T/wf-main.yml"
check "case 45 — mainline-ci's shard job ceiling is not the declared one" 1 "job 'functional-shard' in"

reset_fixture
rm -f "$T/wf-main.yml"
check "case 46 — an absent mainline-ci workflow is COULD NOT LOOK" 2 "missing"

echo
printf 'test-check-pr-suite-shards: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
# A battery that ran nothing exits 0 for free. It has to say how many it ran, and refuse
# if the number is not the one it was written with.
[ "$PASS" -eq 46 ] || {
  echo "test-check-pr-suite-shards: only $PASS of 46 cases ran — the battery was edited without its count" >&2
  exit 1
}
exit 0
