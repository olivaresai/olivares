#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Run under the assigned heavy-job wrapper. PostgreSQL uses an isolated database
# with the production application, owner and administration roles.
set -euo pipefail

test_name=TestTaskIdentityMCPAndInferenceLifetime
case "${1:---sqlite}" in
  --sqlite) backend=sqlite ;;
  --native-sqlite)
    backend=sqlite
    test_name=TestTaskIdentityNativeMCPAndInference
    ;;
  --postgres)
    backend=postgres
    export OLIVARES_TEST_POSTGRES_REQUIRED=1
    export OLIVARES_TEST_PG_EXPECT_MAJOR=16
    ;;
  --native-postgres)
    backend=postgres
    test_name=TestTaskIdentityNativeMCPAndInference
    export OLIVARES_TEST_POSTGRES_REQUIRED=1
    export OLIVARES_TEST_PG_EXPECT_MAJOR=16
    ;;
  *) echo 'Usage: test-task-identity-lifetime.sh [--sqlite|--postgres|--native-sqlite|--native-postgres]' >&2; exit 64 ;;
esac

cd "$(dirname "$0")/.."
exec go test -p 1 -parallel 2 ./cmd/olivares -count=1 -timeout=3m \
  -run "^${test_name}/${backend}\$" -v
