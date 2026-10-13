#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Selftest for the private-suite guards of scripts/docs-captures.sh and scripts/web-e2e-demo.sh.
#
# A Community build serves the access graph and the identity inventory with the public specs.
# Any other edition that serves them must bring its private capture and E2E suites, or the script
# refuses. The guard was dropped once for every edition and nothing failed, so this test runs the
# REAL guard text, cut out of each script, over every edition, status and suite-presence case, and
# also runs two weakened copies that the matrix must catch.
set -u -o pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT
ok=0
fail=0
pass() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
bad() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }

# guard_of <script> <first line of the guard>: that line up to the first top-level `fi`.
guard_of() {
  awk -v start="$2" 'index($0, start) == 1 { on = 1 } on { print } on && $0 == "fi" { exit }' "$1"
}

# refuses <guard file> <edition> <graph> <identity> <spec file>...: exit status of the guard in a
# directory that holds exactly the named spec files under e2e/.
refuses() {
  local guard=$1 edition=$2 graph=$3 identity=$4 dir
  shift 4
  dir="$(mktemp -d "$T/case.XXXXXX")"
  mkdir "$dir/e2e"
  for spec in "$@"; do : >"$dir/e2e/$spec"; done
  (cd "$dir" && BUILD_EDITION=$edition GRAPH_STATUS=$graph IDENTITY_STATUS=$identity bash "$guard" >/dev/null 2>&1)
  return $?
}

CAPTURES_SPEC=docs-captures-business.spec.ts
GRAPH_SPEC=demo-graph-business.spec.ts
IDENTITY_SPEC=console-func-l4-business.spec.ts

# mismatches <captures|demo> <guard file>: how many cases the guard answers differently from the rule.
# Rule: only a non-Community build that serves a capability (status 200) without that capability's
# private suite is refused; a 501 or an installed suite always passes.
mismatches() {
  local kind=$1 guard=$2 n=0 edition graph identity have_a have_b want got
  local specs
  for edition in community enterprise business; do
    for graph in 200 501; do
      for identity in 200 501; do
        for have_a in 0 1; do
          for have_b in 0 1; do
            specs=()
            want=0
            if [ "$kind" = captures ]; then
              [ "$have_a" = 1 ] && specs+=("$CAPTURES_SPEC")
              [ "$edition" != community ] && [ "$graph" = 200 ] && [ "$have_a" = 0 ] && want=1
            else
              [ "$have_a" = 1 ] && specs+=("$GRAPH_SPEC")
              [ "$have_b" = 1 ] && specs+=("$IDENTITY_SPEC")
              if [ "$edition" != community ]; then
                [ "$graph" = 200 ] && [ "$have_a" = 0 ] && want=1
                [ "$identity" = 200 ] && [ "$have_b" = 0 ] && want=1
              fi
            fi
            refuses "$guard" "$edition" "$graph" "$identity" ${specs[@]+"${specs[@]}"}
            got=$?
            [ "$got" -ne 0 ] && got=1
            [ "$got" = "$want" ] || n=$((n + 1))
          done
        done
      done
    done
  done
  printf '%s' "$n"
}

check_script() {
  local kind=$1 script=$2 start=$3 guard n
  guard="$T/$kind.guard.sh"
  guard_of "$ROOT/scripts/$script" "$start" >"$guard"
  if [ ! -s "$guard" ] || ! grep -q 'exit 1' "$guard"; then
    bad "$script: the guard block was not found (or has no refusal): the test would pass on nothing"
    return
  fi
  pass "$script: the guard block is present"

  n="$(mismatches "$kind" "$guard")"
  if [ "$n" = 0 ]; then
    pass "$script: the guard refuses exactly the non-Community builds that serve a capability without its private suite"
  else
    bad "$script: the guard disagrees with the rule in $n cases"
  fi

  sed 's/!= community/= community/' "$guard" >"$T/$kind.mut-edition.sh"
  n="$(mismatches "$kind" "$T/$kind.mut-edition.sh")"
  if [ "$n" != 0 ]; then pass "$script: a guard that inverts the edition test is caught"; else bad "$script: an inverted edition test goes unnoticed"; fi

  sed 's/exit 1/true/' "$guard" >"$T/$kind.mut-exit.sh"
  n="$(mismatches "$kind" "$T/$kind.mut-exit.sh")"
  if [ "$n" != 0 ]; then pass "$script: a guard that never refuses is caught"; else bad "$script: a guard that never refuses goes unnoticed"; fi
}

check_script captures docs-captures.sh 'if [ "$BUILD_EDITION" != community ] && [ "$GRAPH_STATUS" = 200 ]; then'
check_script demo web-e2e-demo.sh 'if [ "$BUILD_EDITION" != community ]; then'

# The suites the guards demand must be the ones the scripts really add to the Playwright run.
for pair in "docs-captures.sh $CAPTURES_SPEC" "web-e2e-demo.sh $GRAPH_SPEC" "web-e2e-demo.sh $IDENTITY_SPEC"; do
  set -- $pair
  if [ "$(grep -c "e2e/$2" "$ROOT/scripts/$1")" -ge 2 ]; then
    pass "$1: e2e/$2 is both required by the guard and added to the run"
  else
    bad "$1: e2e/$2 is not both required and run"
  fi
done

printf '%s ok, %s failed\n' "$ok" "$fail"
[ "$fail" = 0 ]
