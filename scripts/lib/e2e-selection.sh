#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# e2e-selection.sh — select browser specs for scripts/web-e2e.sh, and refuse to call a run
# green that did not run them.
#
# WHY IT EXISTS. web-e2e.sh ran every spec: each first-boot spec on its own virgin engine,
# then the rest on a shared one. Its extra arguments reach the shared run only, so a
# first-boot spec could not be run alone, and nothing checked that a run had run anything.
# A spec that skipped itself (no token, no work directory) left Playwright exiting 0.
#
# WHY IT IS A LIBRARY. Like engine-boot.sh, its failure paths must be exercisable without
# pnpm, a web bundle or a Go build. It is sourced by web-e2e.sh; a caller can source it
# with fixtures and reach every answer.
#
# ANSWERS, the repository's three: 0 intended, 1 defect, 2 could not look.

# e2e_selection_parse <web-dir> <space-separated specs>
# Prints one selected spec per line, relative to <web-dir>. Exit 2 when the selection is
# empty, names a path outside e2e/*.spec.ts, or names a file that does not exist: a
# selection that selects nothing is never a run.
e2e_selection_parse() {
  local web=$1 raw=$2 spec n=0
  [ -d "$web" ] || {
    echo "e2e-selection: COULD NOT LOOK: no web directory at $web" >&2
    return 2
  }
  for spec in $raw; do
    case "$spec" in
    e2e/*.spec.ts) ;;
    *)
      echo "e2e-selection: COULD NOT LOOK: '$spec' is not an e2e/<name>.spec.ts path" >&2
      return 2
      ;;
    esac
    [ -f "$web/$spec" ] || {
      echo "e2e-selection: COULD NOT LOOK: the selection names $spec and it does not exist" >&2
      return 2
    }
    printf '%s\n' "$spec"
    n=$((n + 1))
  done
  [ "$n" -gt 0 ] || {
    echo "e2e-selection: COULD NOT LOOK: E2E_SPECS is set and selects nothing" >&2
    return 2
  }
  return 0
}

# e2e_selected <spec> <selected...> — 0 when <spec> is one of the selected specs.
e2e_selected() {
  local spec=$1 s
  shift
  for s in "$@"; do [ "$s" = "$spec" ] && return 0; done
  return 1
}

# e2e_report_gate <playwright-json-report> <label>
# Reads Playwright's own JSON report. Exit 2 when the report is missing or unreadable,
# ran no test, or skipped any (a skip is "could not look", never a pass). Exit 1 when any
# test failed or needed a retry to pass. Exit 0 only for at least one test, all expected.
e2e_report_gate() {
  local report=$1 label=$2
  [ -s "$report" ] || {
    echo "e2e-selection: COULD NOT LOOK: [$label] produced no JSON report at $report" >&2
    return 2
  }
  command -v python3 >/dev/null 2>&1 || {
    echo "e2e-selection: COULD NOT LOOK: python3 is not on PATH to read [$label]" >&2
    return 2
  }
  REPORT="$report" LABEL="$label" python3 - <<'PY'
import json, os, sys
label = os.environ["LABEL"]
try:
    stats = json.load(open(os.environ["REPORT"], encoding="utf-8"))["stats"]
    expected, skipped = int(stats["expected"]), int(stats["skipped"])
    unexpected, flaky = int(stats["unexpected"]), int(stats["flaky"])
except (OSError, ValueError, KeyError, TypeError) as exc:
    print(f"e2e-selection: COULD NOT LOOK: [{label}] report unreadable: {exc}", file=sys.stderr)
    sys.exit(2)
line = f"[{label}] expected={expected} unexpected={unexpected} flaky={flaky} skipped={skipped}"
if unexpected or flaky:
    print(f"e2e-selection: DEFECT {line}", file=sys.stderr)
    sys.exit(1)
if expected < 1 or skipped:
    print(f"e2e-selection: COULD NOT LOOK {line}: nothing ran, or something skipped", file=sys.stderr)
    sys.exit(2)
print(f"e2e-selection: PASSED {line}")
sys.exit(0)
PY
}

# e2e_gated_run <report> <label> <command...>
# One selected Playwright run and its gate. The command runs with
# PLAYWRIGHT_JSON_OUTPUT_FILE=<report>, and assignments placed before the call reach it too.
# <report> is REMOVED first, so a report left by an earlier run can never be read as this
# run's answer: a run that writes none is "could not look". Returns the worst of the command
# (0, or 1 for any failure) and the gate: 0 passed, 1 defect, 2 could not look.
e2e_gated_run() {
  local report=$1 label=$2 prc=0 grc=0
  shift 2
  rm -f -- "$report" 2>/dev/null
  [ ! -e "$report" ] || {
    echo "e2e-selection: COULD NOT LOOK: [$label] cannot clear the report path $report" >&2
    return 2
  }
  PLAYWRIGHT_JSON_OUTPUT_FILE="$report" "$@" || prc=1
  e2e_report_gate "$report" "$label" || grc=$?
  return "$(e2e_worst "$prc" "$grc")"
}

# e2e_worst <rc...> — the most severe answer: 2 over 1 over 0.
e2e_worst() {
  local worst=0 rc
  for rc in "$@"; do
    if [ "$rc" -eq 2 ]; then
      worst=2
    elif [ "$rc" -ne 0 ] && [ "$worst" -ne 2 ]; then
      worst=1
    fi
  done
  echo "$worst"
}
