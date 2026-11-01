#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
#
# Generated test-name assignment, through the same reader commands CI calls.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 2
TMP="$(mktemp -d "${TMPDIR:-/tmp}/generated-shards.XXXXXX")" || exit 2
trap 'rm -rf "$TMP"' EXIT
PASS=0
FAIL=0
PKG="example.test/tree/cmd/tool"

pass() { printf '  ✓ %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  ✗ %s\n' "$1" >&2; FAIL=$((FAIL + 1)); }
show_failure() {
  sed 's/^/      /' "$TMP/out" "$TMP/err" | head -6 >&2
}

cat > "$TMP/tests.txt" <<'EOF'
example.test/tree/cmd/tool TestAlpha
example.test/tree/cmd/tool TestAlphaSecond
example.test/tree/cmd/tool TestBeta
example.test/tree/cmd/tool TestGamma
example.test/tree/cmd/tool FuzzDelta
example.test/tree/cmd/tool ExampleDelta
github.com/olivaresai/olivares/cmd/olivares TestAlpha
github.com/olivaresai/olivares/cmd/olivares TestAlphaSecond
github.com/olivaresai/olivares/cmd/olivares TestBeta
github.com/olivaresai/olivares/cmd/olivares TestGamma
github.com/olivaresai/olivares/cmd/olivares FuzzDelta
github.com/olivaresai/olivares/cmd/olivares ExampleDelta
github.com/olivaresai/olivares/core/auth TestAlpha
github.com/olivaresai/olivares/core/auth TestAlphaSecond
github.com/olivaresai/olivares/core/auth TestBeta
github.com/olivaresai/olivares/core/auth TestGamma
github.com/olivaresai/olivares/core/auth FuzzDelta
github.com/olivaresai/olivares/core/auth ExampleDelta
EOF

cat > "$TMP/pr.txt" <<'EOF'
shard alpha
shard rest
group alpha example.test/tree/cmd/tool alpha
group rest example.test/tree/cmd/tool rest
go-timeout-minutes 45
step-ceiling-minutes 55
job-ceiling-minutes 65
EOF

python3 -I -B - "$ROOT/scripts/race-groups.json" "$TMP/race.json" <<'PY'
import json
import sys

spec = json.load(open(sys.argv[1], encoding="utf-8"))
spec["root_shards"] = [{"name": "root-a"}, {"name": "root-b"}]
spec["max_shard_share"] = 1.0
for group in spec["groups"]:
    if group.get("shards"):
        group["shards"] = [{"name": "a"}, {"name": "b"}]
json.dump(spec, open(sys.argv[2], "w", encoding="utf-8"))
PY

pr_expression() {
  local rc=0
  OLIVARES_PR_SUITE_SHARDS="$TMP/pr.txt" OLIVARES_PR_SUITE_TESTS="$TMP/tests.txt" \
    bash "$ROOT/scripts/pr-suite-shards.sh" run-expr "$PKG" "$1" \
    > "$TMP/out" 2> "$TMP/err" || rc=$?
  return "$rc"
}

check_pr() {
  local name="$1" group="$2" want="$3" got
  if ! pr_expression "$group"; then
    fail "$name — reader refused: $(head -1 "$TMP/err")"
    return
  fi
  got="$(cat "$TMP/out")"
  if [ "$got" != "$want" ]; then
    fail "$name — got $got, expected $want"
    return
  fi
  pass "$name"
}

race_expression() {
  local rc=0 spec="${3:-$TMP/race.json}"
  OLIVARES_RACE_GROUPS="$spec" OLIVARES_PR_SUITE_TESTS="$TMP/tests.txt" \
    OLIVARES_RACE_DRYRUN=1 bash "$ROOT/scripts/race-groups.sh" "$1" "$2" \
    > "$TMP/out" 2> "$TMP/err" || rc=$?
  return "$rc"
}

check_race() {
  local name="$1" command="$2" shard="$3" want="$4" got
  if ! race_expression "$command" "$shard"; then
    fail "$name — reader refused: $(head -1 "$TMP/err")"
    return
  fi
  got="$(python3 -I -B -c 'import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
match = re.search(r"-run (\S+)", text)
print(match.group(1) if match else "")' "$TMP/out")"
  if [ "$got" != "$want" ]; then
    fail "$name — got $got, expected $want"
    return
  fi
  pass "$name"
}

check_race "race root: generated exact names" root-run root-a '^(TestAlphaSecond|TestBeta)$'
check_race "race root: sibling covers the other tests" root-run root-b '^(TestAlpha|TestGamma)$'
check_race "race group: generated exact names" run 'core-hot#a' '^(TestGamma)$'
check_race "race group: sibling covers the other tests" run 'core-hot#b' '^(TestAlpha|TestAlphaSecond|TestBeta)$'
check_pr "PR suite: generated exact names" alpha '^(ExampleDelta|TestGamma)$'
check_pr "PR suite: fuzz targets stay covered" rest '^(FuzzDelta|TestAlpha|TestAlphaSecond|TestBeta)$'
check_pr "PR suite: assignment repeats byte for byte" alpha '^(ExampleDelta|TestGamma)$'

cat >> "$TMP/tests.txt" <<'EOF'
example.test/tree/cmd/tool TestFuture
github.com/olivaresai/olivares/cmd/olivares TestFuture
github.com/olivaresai/olivares/core/auth TestFuture
EOF
check_race "race root: an added test needs no spec edit" root-run root-a '^(TestAlphaSecond|TestBeta|TestFuture)$'
check_race "race group: an added test needs no spec edit" run 'core-hot#a' '^(TestFuture|TestGamma)$'
check_pr "PR suite: an added test needs no spec edit" rest '^(FuzzDelta|TestAlpha|TestAlphaSecond|TestBeta|TestFuture)$'

# The setup gate must certify the assignment the runner executes: `check` and `run` build
# their domain strings separately, so a drift between them would certify a split nobody runs.
# The runner puts TestAlpha and TestGamma in root-b, so a fixture of only those two leaves
# root-a empty; it puts TestAlpha, TestAlphaSecond and TestBeta in core-hot#b, so a fixture of
# only those three leaves core-hot#a empty. Each is one bit: the real `check` must agree with it.
grep -hE '^(example.test/tree/cmd/tool|github.com/olivaresai/olivares/(cmd/olivares|core/auth)) Test(Alpha|Gamma)$' \
  "$TMP/tests.txt" > "$TMP/tests-root.txt"
rc=0
OLIVARES_RACE_GROUPS="$TMP/race.json" OLIVARES_PR_SUITE_TESTS="$TMP/tests-root.txt" \
  bash "$ROOT/scripts/race-groups.sh" check > "$TMP/out" 2> "$TMP/err" || rc=$?
if [ "$rc" = 1 ] && grep -qF 'shard root-a selects no test' "$TMP/err"; then
  pass "race root: check leaves the shard the runner leaves empty"
else
  fail "race root: check assigned differently from the runner (rc=$rc)"
  show_failure
fi
grep -hE '^(example.test/tree/cmd/tool|github.com/olivaresai/olivares/(cmd/olivares|core/auth)) Test(Alpha|AlphaSecond|Beta)$' \
  "$TMP/tests.txt" > "$TMP/tests-alpha.txt"
rc=0
OLIVARES_RACE_GROUPS="$TMP/race.json" OLIVARES_PR_SUITE_TESTS="$TMP/tests-alpha.txt" \
  bash "$ROOT/scripts/race-groups.sh" check > "$TMP/out" 2> "$TMP/err" || rc=$?
if [ "$rc" = 1 ] && grep -qF 'shard a of core-hot selects no test' "$TMP/err"; then
  pass "race group: check leaves the shard the runner leaves empty"
else
  fail "race group: check assigned differently from the runner (rc=$rc)"
  show_failure
fi

cat > "$TMP/tests.txt" <<'EOF'
example.test/tree/cmd/tool TestGamma
github.com/olivaresai/olivares/cmd/olivares TestGamma
github.com/olivaresai/olivares/core/auth TestGamma
EOF
printf '%s\n' "$PKG" > "$TMP/pkgs.txt"

if race_expression root-run root-a; then
  fail "race root: an empty generated shard must refuse"
else
  if grep -qF 'selects no test' "$TMP/err"; then
    pass "race root: an empty generated shard refuses"
  else
    fail "race root: empty shard failed for the wrong reason"
    show_failure
  fi
fi
if race_expression run 'core-hot#b'; then
  fail "race group: an empty generated shard must refuse"
else
  if grep -qF 'selects no test' "$TMP/err"; then
    pass "race group: an empty generated shard refuses"
  else
    fail "race group: empty shard failed for the wrong reason"
    show_failure
  fi
fi

rc=0
OLIVARES_PR_SUITE_SHARDS="$TMP/pr.txt" OLIVARES_PR_SUITE_TESTS="$TMP/tests.txt" \
  OLIVARES_PR_SUITE_PACKAGES="$TMP/pkgs.txt" \
  bash "$ROOT/scripts/pr-suite-shards.sh" run rest > "$TMP/out" 2> "$TMP/err" || rc=$?
if [ "$rc" = 1 ] && grep -qF 'selects no test' "$TMP/err"; then
  pass "PR suite: the runner refuses an empty generated group"
else
  fail "PR suite: empty generated group returned rc=$rc for the wrong reason"
  show_failure
fi

# The setup gate must reject the same empty root and group assignments before it provisions
# services. TestGamma leaves root-a empty; adding TestFuture fills both root shards while
# leaving core-hot#b empty. max_shard_share is 1.0 in this fixture to isolate these guards.
rc=0
OLIVARES_RACE_GROUPS="$TMP/race.json" OLIVARES_PR_SUITE_TESTS="$TMP/tests.txt" \
  bash "$ROOT/scripts/race-groups.sh" check > "$TMP/out" 2> "$TMP/err" || rc=$?
if [ "$rc" = 1 ] && grep -qF 'shard root-a selects no test' "$TMP/err"; then
  pass "race root: check refuses an empty generated shard"
else
  fail "race root: check empty shard returned rc=$rc for the wrong reason"
fi

cat >> "$TMP/tests.txt" <<'EOF'
example.test/tree/cmd/tool TestFuture
github.com/olivaresai/olivares/cmd/olivares TestFuture
github.com/olivaresai/olivares/core/auth TestFuture
EOF
rc=0
OLIVARES_RACE_GROUPS="$TMP/race.json" OLIVARES_PR_SUITE_TESTS="$TMP/tests.txt" \
  bash "$ROOT/scripts/race-groups.sh" check > "$TMP/out" 2> "$TMP/err" || rc=$?
if [ "$rc" = 1 ] && grep -qF 'shard b of core-hot selects no test' "$TMP/err"; then
  pass "race group: check refuses an empty generated shard"
else
  fail "race group: check empty shard returned rc=$rc for the wrong reason"
fi

# A pre-created predictable path must never become a write target or an inventory input.
check_symlink() {
  local name="$1" command="$2" shard="$3" leaf="$4" rc=0
  printf 'SAFE\n' > "$TMP/symlink-target"
  OLIVARES_RACE_GROUPS="$TMP/race.json" OLIVARES_PR_SUITE_TESTS="$TMP/tests.txt" \
    OLIVARES_RACE_DRYRUN=1 TMPDIR="$TMP" \
    bash -c 'ln -s "$1/symlink-target" "$1/$2$$"; source "$3" "$4" "$5"' \
      "$ROOT/scripts/race-groups.sh" "$TMP" "$leaf" \
      "$ROOT/scripts/race-groups.sh" "$command" "$shard" \
    > "$TMP/out" 2> "$TMP/err" || rc=$?
  if [ "$rc" -eq 0 ] && [ "$(cat "$TMP/symlink-target")" = SAFE ]; then
    pass "$name"
  else
    fail "$name (rc=$rc, target=$(cat "$TMP/symlink-target"))"
  fi
}
check_symlink "race group: inventory allocation ignores a pre-created symlink" \
  run 'core-hot#a' 'race-group-tests.'
check_symlink "race root: inventory allocation ignores a pre-created symlink" \
  root-run root-a 'race-root-tests-list.'

# A selection count that cannot be read is COULD NOT LOOK, not zero matches.
mkdir -p "$TMP/badgrep"
REAL_GREP="$(command -v grep)"
{
  printf '#!/usr/bin/env bash\n'
  printf 'if [ "$1" = "-cE" ]; then exit 2; fi\n'
  printf 'exec %q "$@"\n' "$REAL_GREP"
} > "$TMP/badgrep/grep"
chmod +x "$TMP/badgrep/grep"
rc=0
PATH="$TMP/badgrep:$PATH" race_expression root-run root-a || rc=$?
if [ "$rc" = 2 ] && grep -qF 'grep exited 2' "$TMP/err"; then
  pass "race root: a failed selection count is COULD NOT LOOK"
else
  fail "race root: failed selection count returned rc=$rc, expected 2"
fi

# Exact names grow with the tree. Both race readers must refuse an expression the kernel
# cannot carry instead of reaching exec with "Argument list too long".
python3 -I -B - "$TMP/race.json" "$TMP/race-limit.json" "$TMP/tests.txt" <<'PY'
import json, sys

spec = json.load(open(sys.argv[1], encoding="utf-8"))
spec["root_shards"] = [{"name": "root-a"}]
for group in spec["groups"]:
    if group["name"] == "core-hot":
        group["shards"] = [{"name": "a"}]
json.dump(spec, open(sys.argv[2], "w", encoding="utf-8"))
packages = [
    "example.test/tree/cmd/tool",
    "github.com/olivaresai/olivares/cmd/olivares",
    "github.com/olivaresai/olivares/core/auth",
]
with open(sys.argv[3], "w", encoding="utf-8") as fh:
    for pkg in packages:
        for name in ("Test" + "A" * 50000, "Test" + "B" * 50000):
            fh.write(pkg + " " + name + "\n")
PY
if race_expression root-run root-a "$TMP/race-limit.json"; then
  fail "race root: an oversized -run expression was accepted"
elif grep -qF 'MAX_ARG_STRLEN' "$TMP/err"; then
  pass "race root: an oversized -run expression refuses before exec"
else
  fail "race root: oversized expression failed for the wrong reason"
fi
if race_expression run 'core-hot#a' "$TMP/race-limit.json"; then
  fail "race group: an oversized -run expression was accepted"
elif grep -qF 'MAX_ARG_STRLEN' "$TMP/err"; then
  pass "race group: an oversized -run expression refuses before exec"
else
  fail "race group: oversized expression failed for the wrong reason"
fi

# The fixtures above use two shards; the real specs use up to 26. A modulus or index slip
# that only shows with more shards, a prefix pair like TestFoo/TestFooBar, or a non-ASCII
# name must still land every test in exactly one shard, and each expression must select
# exactly its own shard's names.
if python3 -I - "$ROOT/scripts/lib/test_name_shard.py" > "$TMP/out" 2> "$TMP/err" <<'PY'
import importlib.util, re, sys

spec = importlib.util.spec_from_file_location("test_name_shard", sys.argv[1])
sharder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sharder)

shards = ["s%d" % i for i in range(7)]
tests = sorted({"Test%d" % i for i in range(250)} | {"Test%dSecond" % i for i in range(250)} | {"TestÜber", "TestÜberSync"})
assigned = sharder.assign("domain", shards, tests)

flat = [t for s in shards for t in assigned[s]]
assert sorted(flat) == tests, "a test is in no shard or in two"
assert all(assigned[s] for s in shards), "a shard is empty"
for s in shards:
    expr = re.compile(sharder.expression(assigned[s]))
    got = sorted(t for t in tests if expr.search(t))
    assert got == assigned[s], "%s selects %d names, owns %d" % (s, len(got), len(assigned[s]))

more = sharder.assign("domain", shards, sorted(tests + ["TestAdded"]))
for s in shards:
    assert [t for t in more[s] if t != "TestAdded"] == assigned[s], "adding a test moved another"
PY
then
  pass "assignment: 7 shards, prefix pairs and a non-ASCII name land once, anchored and stable"
else
  fail "assignment: 7-shard exactly-once check"
  show_failure
fi

printf 'generated-shards selftest: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
