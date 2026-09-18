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
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || { echo "test-check-pr-suite-shards: cannot enter $ROOT" >&2; exit 2; }

GATE="$ROOT/scripts/check-pr-suite-shards.sh"
[ -x "$GATE" ] || { echo "test-check-pr-suite-shards: $GATE is not executable" >&2; exit 2; }

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
example.test/tree/cmd/tool
example.test/tree/core/api
example.test/tree/core/internal/store
example.test/tree/modules/one
example.test/tree/modules/two
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

reset_fixture() { base_pkgs; base_spec; base_wf; base_taskfile; }

run_gate() {
  OLIVARES_PR_SUITE_SHARDS="$T/spec.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$T/pkgs.txt" \
  OLIVARES_PR_CI_WF="$T/wf.yml" \
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
sed -i 's|shard: \[a, b\]|shard: [a, b, c]|' "$T/wf.yml"
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

echo
printf 'test-check-pr-suite-shards: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
# A battery that ran nothing exits 0 for free. It has to say how many it ran, and refuse
# if the number is not the one it was written with.
[ "$PASS" -eq 20 ] || {
  echo "test-check-pr-suite-shards: only $PASS of 20 cases ran — the battery was edited without its count" >&2
  exit 1
}
exit 0
