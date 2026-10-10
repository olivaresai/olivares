#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# The cluster, image tags and manager port are shared across refs on a runner host.
# Check the scheduler contract before any job can mutate those resources.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
policy="$(sed -n '/^concurrency:$/,/^[^[:space:]#]/p' \
    "$root/.github/workflows/e2e-operator-kind.yml")"
failures=0
check() {
    if grep -Eq "$1" <<< "$policy"; then
        printf 'PASS: %s\n' "$2"
    else
        printf 'FAIL: %s\n' "$2" >&2
        failures=$((failures + 1))
    fi
}

# A literal group makes PRs, scheduled runs and manual runs contend for one slot.
check '^  group: e2e-operator-kind[[:space:]]*$' 'all refs serialize shared kind resources'
check '^  cancel-in-progress: false[[:space:]]*$' 'active qualification runs finish'
check '^  queue: max[[:space:]]*$' 'pending qualifications queue instead of replacing each other'
# A rejected scheduling policy must not reach the unconditional cluster deletion.
policy="$(sed -n '/^      - name: Tear down$/,$p' \
    "$root/.github/workflows/e2e-operator-kind.yml")"
check "^        if: always\(\) && steps.scheduling.outcome == 'success'[[:space:]]*$" \
    'rejected scheduling cannot delete another run’s cluster'
test "$failures" -eq 0
