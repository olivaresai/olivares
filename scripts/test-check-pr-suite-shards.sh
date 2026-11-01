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
# ⛔ THE NAME GROUPS NEED A SECOND SYNTHETIC UNIVERSE. A `group` record splits
# ONE package across shards by generated test NAME assignment. The behavior under test is
# exact-once coverage, repeatability, addition without a spec edit, and refusal of an empty
# selection: `go test -run` over an expression that matches nothing EXITS 0, so a shard would
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
example.test/tree/cmd/tool ExampleDelta
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
  : > "$T/export-public.sh"
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
  # The candidate miniature: the shard job of candidate-shards.yml, the push-triggered copy of
  # pr-ci's shard job. Its ceilings and its matrix are pinned the same way as the other two.
  cat > "$T/wf-candidate.yml" <<'EOF'
name: fixture-candidate

on:
  push:
    branches: ['release/*-candidate']

jobs:
  pr-test-shard:
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

# The same fixture with cmd/tool split by generated test NAME assignment across two shards.
# `pkg a example.test/tree/cmd/...` stays declared and stays honest — it still owns
# cmd/helper — which is the shape the real tree has once the root package is partitioned.
group_fixture() {
  reset_fixture
  {
    printf 'shard t1\n'
    printf 'group a example.test/tree/cmd/tool alpha\n'
    printf 'group t1 example.test/tree/cmd/tool rest\n'
  } >> "$T/spec.txt"
  sed -i 's|shard: \[a, b\]|shard: [a, b, t1]|' "$T/wf.yml" "$T/wf-main.yml" "$T/wf-candidate.yml"
}

run_gate() {
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" \
  OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  OLIVARES_PR_CI_WF="$T/wf.yml" \
  OLIVARES_MAINLINE_CI_WF="$T/wf-main.yml" \
  OLIVARES_CANDIDATE_SHARDS_WF="$T/wf-candidate.yml" \
  OLIVARES_EXPORT_SCRIPT="$T/export-public.sh" \
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
      # A leg's task output must leave the JSON stream in JSON mode; this makes it visible.
      if [ "$tool" = task ] && [ -n "${STUB_TASK_STDOUT:-}" ]; then
        printf 'printf "%%s\\\\n" "%s"\n' "$STUB_TASK_STDOUT"
      fi
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
  OLIVARES_CANDIDATE_SHARDS_WF="$T/wf-candidate.yml" \
  OLIVARES_EXPORT_SCRIPT="$T/export-public.sh" \
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
    printf 'func FuzzDelta(f *testing.F) { _ = f }\n'
    printf 'func ExampleFixture() {}\n'
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
  OLIVARES_CANDIDATE_SHARDS_WF="$T/wf-candidate.yml" \
  OLIVARES_EXPORT_SCRIPT="$T/export-public.sh" \
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
sed -i 's|shard: \[a, b\]|shard: [a, b, c]|' "$T/wf.yml" "$T/wf-main.yml" "$T/wf-candidate.yml"
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

# GENERATED NAME GROUPS ─────────────────────────────────────────────────────────────
# The assignment is computed from the test inventory. These cases prove that no remainder or
# hand-written family list is needed, while empty selections and invalid records still fail.
group_fixture
check "case 21 — a package split by generated test name is CLEAN" 0 "CLEAN"

group_fixture
# Renaming the second group needs no remainder marker: every test still has exactly one owner.
sed -i 's|^group t1 example.test/tree/cmd/tool rest$|group t1 example.test/tree/cmd/tool beta|' "$T/spec.txt"
check "case 22 — generated coverage needs no remainder group" 0 "CLEAN"

group_fixture
# A hand-written family is no longer part of the grammar; refusing it prevents silent drift.
printf 'group t1 example.test/tree/cmd/tool alpha-again Alpha\n' >> "$T/spec.txt"
check "case 23 — a hand-written family is rejected" 1 "not a record this reader knows"

group_fixture
# The package is split by name AND sent whole to a shard: every test of it runs twice, and
# the shard that the file says bounds the suite is not the one that does.
printf 'pkg b example.test/tree/cmd/tool\n' >> "$T/spec.txt"
check "case 24 — a group for a package a pkg record also sends whole" 1 "sends it whole"

group_fixture
sed -i 's|shard: \[a, b, t1\]|shard: [a, b]|' "$T/wf.yml"
check "case 25 — the matrix does not list the new group shard" 1 "disagree"

group_fixture
# TestGamma is the only member of alpha in this two-group fixture, so rest is empty.
printf 'example.test/tree/cmd/tool TestGamma\n' > "$T/tests.txt"
check "case 26 — a generated group that selects nothing" 1 "selects no test"

group_fixture
printf 'group t1 example.test/tree/cmd/tool rest\n' >> "$T/spec.txt"
check "case 27 — two groups with the same name" 1 "declares two groups called"

group_fixture
sed -i 's|^group t1 example.test/tree/cmd/tool rest$|group zz example.test/tree/cmd/tool rest|' "$T/spec.txt"
sed -i 's|shard: \[a, b, t1\]|shard: [a, b]|' "$T/wf.yml"
sed -i '/^shard t1$/d' "$T/spec.txt"
check "case 28 — a group naming a shard that is never declared" 1 "which is never declared"

group_fixture
sed -i 's|^group a example.test/tree/cmd/tool alpha$|group a example.test/tree/cmd/absent alpha|' "$T/spec.txt"
check "case 29 — a group naming a package this tree does not have" 1 "this tree does not have"

group_fixture
# Exact-name expressions can outgrow what the kernel carries in one argument. The input names
# also exceed ARG_MAX: enumeration must reach the declared refusal rather than failing to exec
# the reader with its test list in an environment variable.
python3 - "$T/tests.txt" <<'PYGEN'
import os, sys
count = max(100000, os.sysconf("SC_ARG_MAX")) // len("TestBeyondWhatOneArgumentOfTheKernelWillCarry00000000\n") + 1
with open(sys.argv[1], "a", encoding="utf-8") as fh:
    for i in range(count):
        fh.write("example.test/tree/cmd/tool TestBeyondWhatOneArgumentOfTheKernelWillCarry%08d\n" % i)
PYGEN
check "case 30 — a -run expression over the argument limit" 1 "MAX_ARG_STRLEN"

# NOTHING SELECTED IS NOT NOTHING WRONG ───────────────────────────────────────────────
group_fixture
printf 'example.test/tree/cmd/tool TestGamma\n' > "$T/tests.txt"
check_runner "case 33 — the runner refuses a generated group that selects nothing" t1 1 "selects no test"

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
check_tests "case 39 — real Test, Fuzz and Example declarations are enumerated whole" "$T/enum" example.test/enum \
  "ExampleFixture FuzzDelta TestAsciiOne TestÜberSync"

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
  "group alpha of example.test/tree/cmd/tool: 2 of 6 test(s)" \
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

# CANDIDATE-SHARDS ─────────────────────────────────────────────────────────────────────
# The push-triggered shard job of candidate-shards.yml: a copy of pr-ci's battery that runs on
# release candidate branches, so it is pinned to the same list and the same ceilings.
reset_fixture
sed -i 's|shard: \[a, b\]|shard: [a]|' "$T/wf-candidate.yml"
check "case 46a — the candidate workflow's matrix lost a shard that pr-ci still runs" 1 "wf-candidate.yml and the shard list disagree"

reset_fixture
sed -i 's|^    timeout-minutes: 55$|    timeout-minutes: 90|' "$T/wf-candidate.yml"
check "case 46b — the candidate workflow's shard job ceiling is not the declared one" 1 "wf-candidate.yml declares timeout-minutes"

reset_fixture
rm -f "$T/wf-candidate.yml"
check "case 46c — an absent candidate workflow in a tree with the export script is COULD NOT LOOK" 2 "missing"

# The trigger is what makes the battery run at all: an edited glob, a dropped `push` or an added
# `pull_request` leaves every shard green on a file nothing ever starts.
reset_fixture
sed -i "s|branches: \['release/\*-candidate'\]|branches: ['release/*-cand']|" "$T/wf-candidate.yml"
check "case 46e — the candidate workflow's branch glob is not the candidate branches" 1 "wf-candidate.yml triggers on"

reset_fixture
sed -i "s|^  push:\$|  pull_request:|" "$T/wf-candidate.yml"
check "case 46f — the candidate workflow no longer triggers on a push" 1 "wf-candidate.yml triggers on"

# An extra trigger beside the push is as wrong as a missing one: it starts the battery where the spec
# says it starts, and the exact compare must refuse a loosened `on:` block, not only a changed one.
reset_fixture
sed -i "s|^  push:\$|  pull_request:\n  push:|" "$T/wf-candidate.yml"
check "case 46g — the candidate workflow triggers on a push and something else" 1 "wf-candidate.yml triggers on"

# A public tree has no export script and no candidate workflow: the pin does not apply there, and
# the gate says so instead of failing the public pull-request check that runs it.
reset_fixture
rm -f "$T/export-public.sh" "$T/wf-candidate.yml"
check "case 46d — a public tree with no candidate workflow passes with a NOTICE" 0 "its pin does not apply here"

# The export must keep the dev-only workflow out of the public tree. Dropping it from GITHUB_BLOCK
# would ship a dev battery publicly, and the 46d NOTICE branch would then hide the difference.
# The export script is hub-only (SCRIPTS_BLOCK) and this selftest is exported: public pr-ci runs it on a
# tree without that script, so there the exclusion is checked on the development tree, as the lint does.
# export-closure: hub-only scripts/export-public.sh — the exporter is in SCRIPTS_BLOCK and does not travel;
# the whole check sits inside the presence test for it, so a public tree takes the else branch.
if [ -f "$ROOT/scripts/export-public.sh" ]; then
  if awk '/^GITHUB_BLOCK=\(/{f=1} f&&!/^[[:space:]]*#/&&/candidate-shards\.yml/{hit=1} f&&/^\)/{exit} END{exit !hit}' "$ROOT/scripts/export-public.sh"; then
    printf '  ✓ case 46h — the public export lists the candidate workflow in GITHUB_BLOCK\n'
    PASS=$((PASS + 1))
  else
    printf '  ✗ case 46h — scripts/export-public.sh does not exclude candidate-shards.yml from the public tree\n' >&2
    FAIL=$((FAIL + 1))
  fi
else
  printf '  ✓ case 46h — no export script in this tree; the candidate workflow exclusion is checked on the development tree\n'
  PASS=$((PASS + 1))
fi

# THE CLOCK A `go test` IS GIVEN ──────────────────────────────────────────────────────────
# The batteries (merge-gate-check) run the shards under a multiple of the declared clocks, and
# no `go test` may be given more time than the step has left: Go's timeout is the one that names
# the test that hung, and the step ceiling only cancels. The cases read the `-timeout` that
# reached the wrapper, not the line the runner prints about it. The first call sleeps two
# seconds so that the second has less of the step to be given.
fake_pg_env_root_recording() {
  local root="$T/fakeroot"
  rm -rf "$root"; mkdir -p "$root/scripts"; : > "$T/pg-args.txt"
  # The single quotes are the point: `$*` and `$(wc …)` are written into the stub, not expanded.
  # shellcheck disable=SC2016
  {
    printf '#!/usr/bin/env bash\n'
    printf 'printf "%%s\\n" "$*" >> %s\n' "$T/pg-args.txt"
    printf 'printf "%%s\\n" "with-pg-env $*" >> %s\n' "$T/stub-was-called.txt"
    printf 'if [ "$(wc -l < %s)" -eq 1 ]; then sleep 2; fi\n' "$T/pg-args.txt"
    printf 'exit 0\n'
  } > "$root/scripts/with-pg-env.sh"
  chmod +x "$root/scripts/with-pg-env.sh"
}

# The -timeout values the wrapper was handed, in call order, one line.
given_clocks() {
  sed -n 's/.* -timeout \([0-9]*[ms]\) .*/\1/p' "$T/pg-args.txt" | paste -sd' ' -
}

# check_clocks <name> <shard> <expected rc> <expected -timeout values, in call order>
check_clocks() {
  local name="$1" shard="$2" want="$3" clocks="$4" got given
  : > "$T/pg-args.txt"
  got="$(run_runner "$shard")"
  given="$(given_clocks)"
  if [ "$got" != "$want" ] || [ "$given" != "$clocks" ]; then
    printf '  ✗ %s — expected rc %s and clocks "%s", got rc %s and "%s"\n' \
      "$name" "$want" "$clocks" "$got" "$given" >&2
    sed 's/^/      /' "$T/err.txt" | head -4 >&2
    FAIL=$((FAIL + 1)); return
  fi
  printf '  ✓ %s (rc %s, clocks %s)\n' "$name" "$got" "$given"
  PASS=$((PASS + 1))
}

echo
group_fixture
fake_pg_env_root_recording
export OLIVARES_ROOT="$T/fakeroot"
unset OLIVARES_SHARD_CLOCK_FACTOR
check_clocks "case 48 — no factor: the declared clock, for the packages and for the group" a 0 "35m 35m"

export OLIVARES_SHARD_CLOCK_FACTOR=2
check_clocks "case 49 — factor 2 doubles the clock of every go test of the shard" a 0 "70m 70m"
unset OLIVARES_SHARD_CLOCK_FACTOR

# A step of 36 minutes leaves 36 - 2 of grace = 34 minutes, under the 35-minute clock. Each
# go test is given what is LEFT when it starts, so the second is given less than the first by
# at least the two seconds the first one took, and not by much more than that.
sed -i 's|^step-ceiling-minutes 45$|step-ceiling-minutes 36|' "$T/spec.txt"
fake_pg_env_root_recording
got="$(run_runner a)"
read -r first second <<< "$(sed -n 's/.* -timeout \([0-9]*\)s .*/\1/p' "$T/pg-args.txt" | paste -sd' ' -)"
if [ "$got" = 0 ] && [ -n "${first:-}" ] && [ -n "${second:-}" ] &&
   [ "$first" -le 2040 ] && [ "$first" -gt 2010 ] && [ "$second" -le $((first - 2)) ] &&
   [ "$second" -gt $((first - 30)) ]; then
  printf '  ✓ case 50 — a go test is never given more than the step has left (%ss, then %ss)\n' "$first" "$second"
  PASS=$((PASS + 1))
else
  printf '  ✗ case 50 — expected rc 0 and two clocks in seconds, the first at most 2040 and the second at least two seconds under it; got rc %s and "%s"\n' \
    "$got" "$(given_clocks)" >&2
  FAIL=$((FAIL + 1))
fi

# A factor that is not a positive whole number is a refusal that names it, before anything runs.
group_fixture
n=51
for bad in 0 2x -1; do
  export OLIVARES_SHARD_CLOCK_FACTOR="$bad"
  check_runner "case $n — the clock factor '$bad' is refused" a 2 "OLIVARES_SHARD_CLOCK_FACTOR"
  n=$((n + 1))
done
n=55
for bad in 100 99999999999999999999999; do
  export OLIVARES_SHARD_CLOCK_FACTOR="$bad"
  check_runner "case $n — the clock factor '$bad' is refused (1 to 99 only)" a 2 "OLIVARES_SHARD_CLOCK_FACTOR"
  n=$((n + 1))
done
unset OLIVARES_SHARD_CLOCK_FACTOR OLIVARES_ROOT

# A step with nothing left still gives each go test a clock: Go reads -timeout 0 as none, so
# the floor is 60 seconds. A 2-minute step ceiling is all grace, which leaves nothing.
group_fixture
sed -i 's|^step-ceiling-minutes 45$|step-ceiling-minutes 2|' "$T/spec.txt"
fake_pg_env_root_recording
export OLIVARES_ROOT="$T/fakeroot"
check_clocks "case 57 — a step with nothing left still gives each go test 60s, never 0" a 0 "60s 60s"

# `08` is eight. Bash reads it as an octal error unless the number is read as decimal.
group_fixture
fake_pg_env_root_recording
export OLIVARES_SHARD_CLOCK_FACTOR=08
check_clocks "case 58 — the factor '08' is 8, not an octal error" a 0 "280m 280m"
unset OLIVARES_SHARD_CLOCK_FACTOR OLIVARES_ROOT

# THE JSON MODE OF THE SCHEDULED BATTERY ──────────────────────────────────────────────────
# main-suite.yml appends the runner's stdout to the artifact main-suite-report.py reads, so
# in JSON mode the wrapper's test output stays on stdout and every progress line goes to
# stderr. One prose line on stdout is one artifact error in the report.
fake_pg_env_json() {
  local root="$T/fakeroot"
  rm -rf "$root"; mkdir -p "$root/scripts"; : > "$T/pg-args.txt"
  cat > "$root/scripts/with-pg-env.sh" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$T/pg-args.txt"
printf '%s\n' '{"Action":"pass","Package":"example.test/tree/cmd/tool"}'
printf '%s\n' 'with-pg-env: progress' >&2
exit 0
EOF
  chmod +x "$root/scripts/with-pg-env.sh"
}

group_fixture
fake_pg_env_json
export OLIVARES_ROOT="$T/fakeroot" OLIVARES_SHARD_JSON=1
got="$(run_runner a)"
json_ok=0
python3 - "$T/out.txt" <<'PY' || json_ok=$?
import json, sys
lines = [line for line in open(sys.argv[1], encoding="utf-8") if line.strip()]
assert lines, "no event on stdout"
for line in lines:
    json.loads(line)
PY
if [ "$got" = 0 ] && [ "$json_ok" -eq 0 ] &&
   [ "$(grep -c '"Action":"pass"' "$T/out.txt")" -eq 2 ] &&
   ! grep -q 'pr-suite-shards:' "$T/out.txt" &&
   grep -q 'pr-suite-shards: shard a' "$T/err.txt" &&
   [ "$(grep -c -- ' -json ' "$T/pg-args.txt")" -eq 2 ]; then
  printf '  ✓ case 59 — json mode: stdout carries only events, progress goes to stderr, both go tests get -json\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 59 — json mode: rc %s, parse rc %s, %s event line(s), %s progress line(s) on stdout, %s on stderr, %s of 2 calls with -json\n' \
    "$got" "$json_ok" "$(grep -c '"Action":"pass"' "$T/out.txt")" \
    "$(grep -c 'pr-suite-shards:' "$T/out.txt")" \
    "$(grep -c 'pr-suite-shards:' "$T/err.txt")" \
    "$(grep -c -- ' -json ' "$T/pg-args.txt")" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT OLIVARES_SHARD_JSON

group_fixture
export OLIVARES_SHARD_JSON=yes
check_runner "case 60 — an OLIVARES_SHARD_JSON that is not 0 or 1 is refused" a 2 "OLIVARES_SHARD_JSON"
unset OLIVARES_SHARD_JSON

# PARTITION: NAME GROUPS COME FROM THIS FILE, NOT A SECOND LIST ─────────────────
# `partition <package>` lists the shard groups that split one package. A
# subcommand with no battery case is a contract nothing pins, so: declaration order on the
# happy path, a refusal when no group splits the package, and a usage refusal with no
# argument.
group_fixture
# Declare t1 BEFORE a, while the group records still name a first: only the shard
# declaration order can produce t1 then a, which is the order the matrix inherits.
sed -i '/^shard t1$/d' "$T/spec.txt"
sed -i '1i shard t1' "$T/spec.txt"
printf 't1\na\n' > "$T/partition-want.txt"
got=0
OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" partition example.test/tree/cmd/tool \
  > "$T/partition-got.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 0 ] && cmp -s "$T/partition-want.txt" "$T/partition-got.txt"; then
  printf '  ✓ case 61 — partition prints the shards that split a package, in declaration order\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 61 — partition exited %s and printed %s line(s), expected t1 then a\n' \
    "$got" "$(grep -c . "$T/partition-got.txt")" >&2
  FAIL=$((FAIL + 1))
fi

got=0
OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" partition example.test/tree/core/api \
  > "$T/out.txt" 2> "$T/err.txt" || got=$?
usage_rc=0
OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" partition \
  > "$T/out.txt" 2> "$T/err-usage.txt" || usage_rc=$?
if [ "$got" -eq 1 ] && grep -qF 'split by no name group' "$T/err.txt" &&
   [ "$usage_rc" -eq 2 ] && grep -qF 'usage:' "$T/err-usage.txt"; then
  printf '  ✓ case 62 — partition refuses an unsplit package and a missing argument\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 62 — unsplit package rc %s (want 1), missing argument rc %s (want 2)\n' \
    "$got" "$usage_rc" >&2
  FAIL=$((FAIL + 1))
fi

# A GROUP THAT CANNOT BE READ IS NOT AN EMPTY GROUP ──────────────────────────────────────
# `partition` derives the scheduled matrix and `run` decides what a shard executes. Both
# used producers whose failure looked like "no rows": partition would omit a shard, so the
# matrix starts no job for its tests, and run would skip a shard's name group while its
# whole packages still made it exit 0. A stub awk that fails only for one shard proves each
# refusal without editing the reader.
awk_fails_for_shard() {
  local shard="$1" real
  real="$(command -v awk)" || { echo "test-check-pr-suite-shards: no awk on PATH" >&2; exit 2; }
  mkdir -p "$T/badawk"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'if [ "${2:-}" = "s=%s" ]; then exit 2; fi\n' "$shard"
    printf 'exec %q "$@"\n' "$real"
  } > "$T/badawk/awk"
  chmod +x "$T/badawk/awk"
}

# The shard names and the legs are read through sed programs. A stub sed that fails for one
# program proves those reads are refused, without editing the reader for the test.
sed_fails_for() {
  local program="$1" real
  real="$(command -v sed)" || { echo "test-check-pr-suite-shards: no sed on PATH" >&2; exit 2; }
  mkdir -p "$T/badsed"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'if [ "${2:-}" = %q ]; then exit 2; fi\n' "$program"
    printf 'exec %q "$@"\n' "$real"
  } > "$T/badsed/sed"
  chmod +x "$T/badsed/sed"
}

# awk failing for one -v shape, the same way. partition matches with `-v p=<package>`;
# a stub that fails on only that argument proves the read is refused without touching other awk calls.
awk_fails_for_var() {
  local prefix="$1" dir="$2" real
  real="$(command -v awk)" || { echo "test-check-pr-suite-shards: no awk on PATH" >&2; exit 2; }
  mkdir -p "$T/$dir"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'case "${2:-}" in %s*) exit 2 ;; esac\n' "$prefix"
    printf 'exec %q "$@"\n' "$real"
  } > "$T/$dir/awk"
  chmod +x "$T/$dir/awk"
}

group_fixture
# b splits the package too, so omitting b would be a smaller matrix, not an empty one.
printf 'group b example.test/tree/cmd/tool b\n' >> "$T/spec.txt"
awk_fails_for_shard b
got=0
PATH="$T/badawk:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" partition example.test/tree/cmd/tool \
  > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot read the name groups of shard b' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 63 — partition refuses when a shard group read fails\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 63 — partition exited %s; expected refusal rc 2 and no derived shard list\n' \
    "$got" >&2
  FAIL=$((FAIL + 1))
fi

group_fixture
fake_pg_env_json
awk_fails_for_shard a
export OLIVARES_ROOT="$T/fakeroot" OLIVARES_SHARD_JSON=1
got=0
PATH="$T/badawk:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" run a > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot read the name groups of shard a' "$T/err.txt" &&
   [ ! -s "$T/pg-args.txt" ]; then
  printf '  ✓ case 64 — run refuses a group read failure before it runs a test\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 64 — run exited %s; expected refusal rc 2 with no go test call\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT OLIVARES_SHARD_JSON

# A shard name is a matrix value, an artifact path and a sed pattern. A name carrying a
# slash or a sed command must be refused by the file's reader, not carried into all three.
group_fixture
printf 'shard bad/name\n' >> "$T/spec.txt"
got=0
OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" shards > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 1 ] && grep -qF 'shard name' "$T/err.txt" &&
   grep -qF 'is not a plain name' "$T/err.txt"; then
  printf '  ✓ case 65 — a shard name the matrix and paths cannot carry is refused\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 65 — an unsafe shard name was not refused (rc %s)\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The scheduled job always sets the clock factor and nine of its ten shards own no whole
# package, so those two progress lines must be exercised in JSON mode: one prose line on
# stdout is one artifact error in the report.
group_fixture
fake_pg_env_json
export OLIVARES_ROOT="$T/fakeroot" OLIVARES_SHARD_JSON=1 OLIVARES_SHARD_CLOCK_FACTOR=2
got="$(run_runner t1)"
json_ok=0
python3 - "$T/out.txt" <<'PY' || json_ok=$?
import json, sys
lines = [line for line in open(sys.argv[1], encoding="utf-8") if line.strip()]
assert lines, "no event on stdout"
for line in lines:
    json.loads(line)
PY
if [ "$got" = 0 ] && [ "$json_ok" -eq 0 ] &&
   ! grep -q 'pr-suite-shards:' "$T/out.txt" &&
   grep -q 'clock factor 2' "$T/err.txt" &&
   grep -q 'owns no whole package in THIS tree' "$T/err.txt" &&
   grep -q -- ' -json ' "$T/pg-args.txt"; then
  printf '  ✓ case 66 — the factor and empty-package progress lines stay off the JSON stream\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 66 — json mode leaked progress (rc %s, parse rc %s) or lost a progress line\n' \
    "$got" "$json_ok" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT OLIVARES_SHARD_JSON OLIVARES_SHARD_CLOCK_FACTOR

# A leg's task output is not a Go test event. In JSON mode it must go to stderr, or the
# report reads it as an invalid JSON artifact line.
group_fixture
printf 'leg a test:example-leg\n' >> "$T/spec.txt"
fake_pg_env_json
export OLIVARES_ROOT="$T/fakeroot" OLIVARES_SHARD_JSON=1 STUB_TASK_EXIT=0
export STUB_TASK_STDOUT='leg prose'
got="$(run_runner a)"
json_ok=0
python3 - "$T/out.txt" <<'PY' || json_ok=$?
import json, sys
lines = [line for line in open(sys.argv[1], encoding="utf-8") if line.strip()]
assert lines, "no event on stdout"
for line in lines:
    json.loads(line)
PY
if [ "$got" = 0 ] && [ "$json_ok" -eq 0 ] &&
   ! grep -q 'leg prose' "$T/out.txt" && grep -q 'leg prose' "$T/err.txt"; then
  printf '  ✓ case 67 — a leg task output leaves the JSON stream in JSON mode\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 67 — leg output reached stdout (rc %s, parse rc %s)\n' "$got" "$json_ok" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT OLIVARES_SHARD_JSON STUB_TASK_EXIT STUB_TASK_STDOUT

# The shard names and the legs are read through sed. A failed read there is not "none": a
# shard-name read that fails must stop partition before it derives a smaller matrix, and a
# legs read that fails must stop run before it executes the shard's groups and exits 0.
group_fixture
sed_fails_for 's/^SHARD //p'
got=0
PATH="$T/badsed:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" partition example.test/tree/cmd/tool \
  > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot read the shard names' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 68 — partition refuses when the shard-name read fails\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 68 — partition exited %s; expected refusal rc 2 and no derived matrix\n' \
    "$got" >&2
  FAIL=$((FAIL + 1))
fi

group_fixture
printf 'leg a test:example-leg\n' >> "$T/spec.txt"
fake_pg_env_json
sed_fails_for 's/^LEG a //p'
export OLIVARES_ROOT="$T/fakeroot" OLIVARES_SHARD_JSON=1
got=0
PATH="$T/badsed:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" run a > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot read the legs of shard a' "$T/err.txt" &&
   [ ! -s "$T/pg-args.txt" ]; then
  printf '  ✓ case 69 — run refuses a legs read failure before it runs a test\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 69 — run exited %s; expected refusal rc 2 with no go test call\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT OLIVARES_SHARD_JSON

# Outside JSON mode the runner keeps the output every existing caller reads: progress and a
# leg's task output stay on stdout. Moving them to stderr unconditionally would break them.
group_fixture
printf 'leg a test:example-leg\n' >> "$T/spec.txt"
fake_pg_env_json
export OLIVARES_ROOT="$T/fakeroot" STUB_TASK_EXIT=0 STUB_TASK_STDOUT='leg prose'
got="$(run_runner a)"
if [ "$got" = 0 ] &&
   grep -q 'pr-suite-shards: shard a' "$T/out.txt" &&
   grep -q 'leg prose' "$T/out.txt" &&
   ! grep -q -- ' -json ' "$T/pg-args.txt"; then
  printf '  ✓ case 70 — legacy mode keeps progress and leg output on stdout, without -json\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 70 — legacy output moved or -json leaked (rc %s, %s progress, %s leg prose, %s -json)\n' \
    "$got" "$(grep -c 'pr-suite-shards:' "$T/out.txt")" \
    "$(grep -c 'leg prose' "$T/out.txt")" "$(grep -c -- ' -json ' "$T/pg-args.txt")" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT STUB_TASK_EXIT STUB_TASK_STDOUT

# A partial universe is not a universe. The module walk appends into a scratch file and
# sorts it out; when the sort fails after writing some of it, the old code removed the
# scratch file and returned 0, so the shard ran a subset and certified it.
group_fixture
mkdir -p "$T/badwalk"
cat > "$T/badwalk/go" <<'EOF'
#!/usr/bin/env bash
if [ "$1 $2" = "work edit" ]; then
  printf '%s\n' '{"Use": [{"DiskPath": "."}]}'
  exit 0
fi
printf '%s\n' 'example.test/tree/cmd/tool' 'example.test/tree/core/api'
EOF
cat > "$T/badwalk/sort" <<'EOF'
#!/usr/bin/env bash
head -1
exit 2
EOF
chmod +x "$T/badwalk/go" "$T/badwalk/sort"
got=0
PATH="$T/badwalk:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" run a > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'partial universe cannot be certified' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 71 — a partial universe write is refused before a shard runs\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 71 — run exited %s; expected refusal rc 2 and no shard output\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The spec's findings are read through sed too. A failed read there must not turn the
# findings into "none": an unknown record would then read as a clean spec.
group_fixture
printf 'legs a test:example-leg\n' >> "$T/spec.txt"
sed_fails_for 's/^BAD //p'
got=0
PATH="$T/badsed:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" shards > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot inspect the spec records' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 72 — a failed read of the spec findings is refused, not called clean\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 72 — shards exited %s; expected refusal rc 2 and no shard list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The last reads the reviewer found unchecked. Each one can fail while the code still has
# an answer: an empty CANNOT list, an empty group list, or "this shard does not split the
# package" all look the same as a clean read. The stub fails only the one read under test.
group_fixture
awk_fails_for_var p= badawkpkg
got=0
PATH="$T/badawkpkg:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" partition example.test/tree/cmd/tool \
  > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'counting the name groups of shard a for example.test/tree/cmd/tool failed' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 73 — a failed partition match is refused, not read as "this shard does not split it"\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 73 — partition exited %s; expected refusal rc 2 and no shard list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

group_fixture
sed_fails_for 's/^SHARD //p'
got=0
PATH="$T/badsed:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" packages a > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot read the shard names' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 75 — a failed shard-name read is refused, not answered as an unknown shard\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 75 — packages exited %s; expected refusal rc 2 and no package list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

group_fixture
enum_fixture
sed_fails_for 's/^CANNOT //p'
got=0
LC_ALL=C GOPROXY=off GOFLAGS='' PATH="$T/badsed:$PATH" OLIVARES_ROOT="$T/enum" \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" tests example.test/enum > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot inspect the test enumeration of example.test/enum' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 76 — a failed read of the enumerator findings is refused, not called clean\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 76 — tests exited %s; expected refusal rc 2 and no test list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The BAD read has the same sed program as the top-level spec read, so a stub keyed only
# on the program would be answered by line 350 first and never reach package_tests. The
# spec records carry `^SHARD ` lines and the enumerator's output does not, so the stub
# fails only when the input is the enumerator's.
group_fixture
enum_fixture
REAL_SED="$(command -v sed)" || { echo "test-check-pr-suite-shards: no sed on PATH" >&2; exit 2; }
mkdir -p "$T/badenumbad"
{
  printf '#!/usr/bin/env bash\n'
  printf 'if [ "${2:-}" = %q ]; then\n' 's/^BAD //p'
  printf '  input="$(cat)"\n'
  printf '  if ! printf "%%s\\n" "$input" | grep -q "^SHARD "; then exit 2; fi\n'
  printf '  printf "%%s\\n" "$input" | exec %q "$@"\n' "$REAL_SED"
  printf 'fi\n'
  printf 'exec %q "$@"\n' "$REAL_SED"
} > "$T/badenumbad/sed"
chmod +x "$T/badenumbad/sed"
got=0
LC_ALL=C GOPROXY=off GOFLAGS='' PATH="$T/badenumbad:$PATH" OLIVARES_ROOT="$T/enum" \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" tests example.test/enum > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] &&
   grep -qF 'cannot inspect the test enumeration of example.test/enum for refused names' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 77 — a failed read of the enumerator BAD findings is refused, not called clean\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 77 — tests exited %s; expected refusal rc 2 and no test list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The blank-line filter over go list's test files. Exit 1 means "all blank"; exit 2 is that
# grep could not look, and it used to make the package look testless.
reset_fixture
enum_fixture
mkdir -p "$T/badgrep"
REAL_GREP="$(command -v grep)" || { echo "test-check-pr-suite-shards: no grep on PATH" >&2; exit 2; }
{
  printf '#!/usr/bin/env bash\n'
  printf 'if [ "${1:-}" = %q ] && [ "${2:-}" = %q ]; then exit 2; fi\n' -v '^[[:space:]]*$'
  printf 'exec %q "$@"\n' "$REAL_GREP"
} > "$T/badgrep/grep"
chmod +x "$T/badgrep/grep"
got=0
LC_ALL=C GOPROXY=off GOFLAGS='' PATH="$T/badgrep:$PATH" OLIVARES_ROOT="$T/enum" \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" tests example.test/enum > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot filter the test files of example.test/enum' "$T/err.txt" &&
   [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 79 — a failed test-file filter is refused, not called a testless package\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 79 — tests exited %s; expected refusal rc 2 and no test list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The test universe given by OLIVARES_PR_SUITE_TESTS. Its awk read used to end with an
# unconditional return 0, so a failed read was an empty list.
group_fixture
printf 'example.test/tree/cmd/tool TestAlpha\n' > "$T/fixture-tests.txt"
awk_fails_for_var p= badspeclist
got=0
PATH="$T/badspeclist:$PATH" OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_TESTS="$T/fixture-tests.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" tests example.test/tree/cmd/tool \
  > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'cannot read OLIVARES_PR_SUITE_TESTS=' "$T/err.txt" &&
   grep -qF 'for example.test/tree/cmd/tool' "$T/err.txt" && [ ! -s "$T/out.txt" ]; then
  printf '  ✓ case 80 — a failed read of the given test universe is refused, not read as empty\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 80 — tests exited %s; expected refusal rc 2 and no test list\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# A failed status-producing read is never an empty answer. Drive the same gate with one
# sed program broken at a time; every extraction must return COULD NOT LOOK.
check_sed_refusal() {
  local name="$1" program="$2" got
  sed_fails_for "$program"
  got="$(PATH="$T/badsed:$PATH" run_gate)"
  if [ "$got" = 2 ] && grep -qF 'could not read' "$T/err.txt"; then
    printf '  ✓ %s (rc 2)\n' "$name"
    PASS=$((PASS + 1))
  else
    printf '  ✗ %s — expected rc 2, got %s\n' "$name" "$got" >&2
    FAIL=$((FAIL + 1))
  fi
}

n=81
for program in \
  's/^JOB //p' \
  's/^MATRIX //p' \
  's/^JOBCEILING //p' \
  's/^STEPCEILINGS //p' \
  's/^CANNOT //p' \
  's/^NOTICE //p' \
  's/^FINDING //p' \
  's/^DETAIL //p' \
  's/^REPARTO //p' \
  's/^GROUPS //p' \
  's/^TOTAL //p'; do
  reset_fixture
  check_sed_refusal "case $n — a failed workflow or partition record read is refused" "$program"
  n=$((n + 1))
done

# grep status 1 means NOMATRIX is absent; any higher status means the read failed.
reset_fixture
mkdir -p "$T/badstatusgrep"
REAL_GREP="$(command -v grep)"
{
  printf '#!/usr/bin/env bash\n'
  printf 'if [ "$1" = "-qx" ] && [ "$2" = "NOMATRIX" ]; then exit 2; fi\n'
  printf 'exec %q "$@"\n' "$REAL_GREP"
} > "$T/badstatusgrep/grep"
chmod +x "$T/badstatusgrep/grep"
got="$(PATH="$T/badstatusgrep:$PATH" run_gate)"
if [ "$got" = 2 ] && grep -qF 'could not inspect the workflow shape' "$T/err.txt"; then
  printf '  ✓ case 92 — a failed NOMATRIX read is refused (rc 2)\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 92 — NOMATRIX read returned %s, expected 2\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# A sort can emit a complete-looking inventory and still fail. Its status must be checked.
mkdir -p "$T/badsort"
cat > "$T/badsort/sort" <<'EOF'
#!/usr/bin/env bash
cat
exit 2
EOF
chmod +x "$T/badsort/sort"
reset_fixture
got="$(PATH="$T/badsort:$PATH" run_gate)"
if [ "$got" = 2 ] && grep -qF 'could not write the package universe' "$T/err.txt"; then
  printf '  ✓ case 93 — a failed override-universe write is refused (rc 2)\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 93 — override-universe write returned %s, expected 2\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

# Six real Go packages form the same universe as the basic fixture, with no network access.
universe_work_fixture() {
  local root="$T/work" pkg name
  rm -rf "$root"
  mkdir -p "$root/tree"
  printf 'go 1.26.6\n\nuse ./tree\n' > "$root/go.work"
  printf 'module example.test/tree\n\ngo 1.26.6\n' > "$root/tree/go.mod"
  for pkg in cmd/tool cmd/helper core/api core/internal/store modules/one modules/two; do
    name="${pkg##*/}"
    mkdir -p "$root/tree/$pkg"
    printf 'package %s\n' "$name" > "$root/tree/$pkg/pkg.go"
    printf 'package %s\n\nimport "testing"\n\nfunc TestThing(t *testing.T) { _ = t }\n' \
      "$name" > "$root/tree/$pkg/pkg_test.go"
  done
}

reset_fixture
universe_work_fixture
export OLIVARES_ROOT="$T/work" OLIVARES_PR_SUITE_READER="$ROOT/scripts/pr-suite-shards.sh"
got="$(run_gate_no_pkg_override)"
if [ "$got" = 0 ] && grep -qF 'CLEAN' "$T/out.txt"; then
  printf '  ✓ case 94 — the real workspace-universe control is CLEAN\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 94 — valid workspace returned %s, expected CLEAN\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi

got="$(PATH="$T/badsort:$PATH" run_gate_no_pkg_override)"
if [ "$got" = 2 ] && grep -qF 'could not write the package universe' "$T/err.txt"; then
  printf '  ✓ case 95 — a failed workspace-universe write is refused (rc 2)\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 95 — workspace-universe write returned %s, expected 2\n' "$got" >&2
  FAIL=$((FAIL + 1))
fi
unset OLIVARES_ROOT OLIVARES_PR_SUITE_READER

# THE WHOLE-PACKAGE LEG OF root-a ─────────────────────────────────────────────────────────
# Measured in the scheduled batteries of 2026-10-17 (runs 39002849343, 39026256760,
# 39045913341): root-a spent 14.4, 18.1 and 11.0 minutes on its 186 whole packages and only
# then began group a, which took 18.8 minutes in the run that finished and was still running
# after 40 and 36 in the two that were cut. Nothing but a name group may follow a long leg
# in the shard that carries the heaviest cmd/olivares group, so the leg lives in root-h, the
# shortest shard measured (11.1 and 10.7 minutes).
echo "test-check-pr-suite-shards: the root-a package leg"
leg_owner="$(bash "$ROOT/scripts/pr-suite-shards.sh" packages root-a 2>&1)"
leg_home="$(bash "$ROOT/scripts/pr-suite-shards.sh" packages root-h 2>/dev/null)"
leg_slow="github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
if [ -z "$leg_owner" ] && grep -qx "$leg_slow" <<< "$leg_home"; then
  printf '  ✓ case 54 — root-a runs its name group alone; its package leg runs in root-h\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 54 — root-a still owns whole packages (%s line(s)) or root-h does not carry %s\n' \
    "$(printf '%s' "$leg_owner" | grep -c .)" "$leg_slow" >&2
  FAIL=$((FAIL + 1))
fi

# A DYING DISK MUST NOT REPORT AS FAILING TESTS. Main run 40185355549 (2026-10-25) red-flagged
# seven cmd/olivares tests across two shards — a postgres data file that could not extend,
# sqlite's "database or disk is full", a TempDir mkdir — and every one of them was green on
# the same tree with free disk. The failing lines named tests and never the disk, so the
# failure went to a code lane. The runner must refuse to LOOK below its free-space floor,
# and the refusal must name the disk and the floor, before it runs a single test.
group_fixture
fake_pg_env_json
mkdir -p "$T/stubdf"
cat > "$T/stubdf/df" <<'EOF'
#!/usr/bin/env bash
# 24 MiB free, whatever is asked: a disk that is already dying.
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf '/dev/fake 4096000 4071424 24576 100%% /\n'
EOF
chmod +x "$T/stubdf/df"
got=0
PATH="$T/stubdf:$PATH" OLIVARES_ROOT="$T/fakeroot" \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" run a > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 2 ] && grep -qF 'COULD NOT LOOK' "$T/err.txt" &&
   grep -qF 'free space' "$T/err.txt" && grep -qF '8192' "$T/err.txt" &&
   [ ! -s "$T/pg-args.txt" ]; then
  printf '  ✓ case 89 — run refuses a dying disk by name before it runs a test\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 89 — run exited %s; expected refusal rc 2 naming the disk and the floor, with no go test call\n' \
    "$got" >&2
  FAIL=$((FAIL + 1))
fi

# The no-fire control of case 89, with room to run: a guard that fires on every disk passes
# the case above and is worse than no guard — it would red every healthy shard. The df stub
# is kept, so what is proven is the READING of the number, not the presence of a stub.
cat > "$T/stubdf/df" <<'EOF'
#!/usr/bin/env bash
# 150 GiB free, whatever is asked.
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf '/dev/fake 262144000 104857600 157286400 40%% /\n'
EOF
chmod +x "$T/stubdf/df"
got=0
PATH="$T/stubdf:$PATH" OLIVARES_ROOT="$T/fakeroot" \
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" OLIVARES_PR_SUITE_TESTS="$T/tests.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" run a > "$T/out.txt" 2> "$T/err.txt" || got=$?
if [ "$got" -eq 0 ] && [ -s "$T/pg-args.txt" ]; then
  printf '  ✓ case 90 — a disk with room runs the shard (the case 89 control)\n'
  PASS=$((PASS + 1))
else
  printf '  ✗ case 90 — run exited %s with the floor satisfied; the guard fired on a healthy disk\n' \
    "$got" >&2
  FAIL=$((FAIL + 1))
fi

echo
printf 'test-check-pr-suite-shards: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
# A battery that ran nothing exits 0 for free. It has to say how many it ran, and refuse
# if the number is not the one it was written with.
[ "$PASS" -eq 99 ] || {
  echo "test-check-pr-suite-shards: only $PASS of 99 cases ran — the battery was edited without its count" >&2
  exit 1
}
exit 0
