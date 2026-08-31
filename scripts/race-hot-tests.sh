#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# race-hot-tests.sh — emit the ANCHORED `go test -run` regex selecting the
# concurrency-hot tests of the cmd/olivares ROOT package (the push gate's -race
# leg; see Taskfile `test:race-hot` and the split rationale there).
#
# WHY a generated regex, not a hand-written allowlist: a hand list drifts (new
# tests in a hot FILE silently lose -race until the weekly sweep) and an
# unanchored list over-selects by prefix accident. This script derives the set
# from a versioned MANIFEST of hot files, anchors it, and fails CLOSED:
#   - a manifest glob that resolves no files is an ERROR (file renamed/moved);
#   - zero extracted tests is an ERROR (grep drift);
# so gate coverage can only shrink loudly, never silently.
#
# The deliberate long-poll e2e tail (…StoryE2E) is EXCLUDED here — the weekly
# race-full workflow races it, so nothing is permanently unraced.
#
# Output: the full anchored regex on stdout; the selected test count on stderr.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}/cmd/olivares"

# MANIFEST — concurrency-bearing surfaces of the root package. Keep each entry
# justified; classify new concurrency test files here (or as an explicit
# exemption in the weekly sweep) when they land.
HOT_GLOBS=(
	"inferenceproxy*_test.go"     # inline inference PEP / authorize chain
	"natsbus_integration_test.go" # cross-node bus bridge (embedded NATS)
	"*pump_test.go"               # leader pumps: eventing/orch cadence+workflow/DR
	"retentionsweep_test.go"      # retention sweep loop
	"killswitch_e2e_test.go"      # kill-switch PEP/MCP seams (StoryE2E -> weekly)
	"license_holder_test.go"      # license hot-apply — its contract REQUIRES -race
)

# Long-poll story e2e stays out of the push gate by design (weekly races it).
EXCLUDE_RE='StoryE2E'

# WHY THERE IS NO `sed` AND NO `compgen` BELOW, and it is not style (2026-08-05). This helper is on
# the reviewed recipeHelpers list, which is what lets a race recipe run it OUTSIDE with-pg-env.sh,
# and checkpgwiring enforces the premise that comes with that: every command head here must be one
# that runs no word of its own argv. `sed` and `compgen` are not: GNU sed's `e` command runs a shell
# command taken from the sed script, and `compgen -C` names a command bash runs. Both were briefly
# allowed by a reviewed table entry that NAMED that residual — and the seventeenth contrast reached
# a `go test` straight through the sed one, checker exit 0. A residual a review has written down is
# still a residual. Every command here now takes its arguments as data, so the premise is decided
# rather than asserted. Keep it that way when editing: a new head has to be classified in
# checkpgwiring's inertCommands before this file may use it.
names=""
for g in "${HOT_GLOBS[@]}"; do
	# shellcheck disable=SC2206 — glob expansion is the point.
	files=(${g})
	if [ ! -e "${files[0]}" ]; then
		echo "race-hot-tests: manifest glob '${g}' resolves NO files — renamed/moved? Update the manifest (fail-closed)." >&2
		exit 1
	fi
	# `grep -o` prints the whole match, `func TestX`; the second field is the name.
	# shellcheck disable=SC2086 — glob expansion is the point.
	found=$(grep -hoE '^func Test[A-Za-z0-9_]+' ${g} | cut -d' ' -f2) || true
	if [ -z "${found}" ]; then
		echo "race-hot-tests: no Test funcs extracted from '${g}' (fail-closed)." >&2
		exit 1
	fi
	names="${names}${found}"$'\n'
done

selected=$(printf '%s' "${names}" | sort -u | grep -vE "${EXCLUDE_RE}" || true)
count=$(printf '%s\n' "${selected}" | grep -c . || true)
if [ "${count}" -eq 0 ]; then
	echo "race-hot-tests: selection is EMPTY after exclusions (fail-closed)." >&2
	exit 1
fi

echo "race-hot-tests: ${count} hot tests selected" >&2
printf '^(%s)$\n' "$(printf '%s\n' "${selected}" | grep . | paste -sd'|')"
