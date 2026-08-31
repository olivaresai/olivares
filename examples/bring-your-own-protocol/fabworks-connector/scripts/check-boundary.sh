#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: Apache-2.0
#
# check-boundary.sh — license boundary check for this out-of-tree connector.
#
# This connector links the Apache-2.0 SDK (github.com/olivaresai/olivares/sdk)
# and must NEVER link the upstream AGPL-3.0 engine module: a permissively
# licensed connector that transitively imports the AGPL engine would put its
# users under copyleft obligations they did not choose. This is the SAME
# boundary the upstream repo enforces in its own CI (scripts/check-boundary.sh
# there) — run this script in YOUR CI on every push.
#
# HOW: ask the Go toolchain for the FULL transitive import set of every
# package (`go list -deps ./...`) and fail if anything under the engine module
# appears. This is the real build graph, not a textual grep that misses
# indirect imports. Standalone on purpose: bash + go, no other dependencies.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# This connector is a standalone module; never resolve through an ambient
# workspace (a go.work in a parent tree — e.g. an upstream checkout above this
# repo — would make `go list` fail with a workspace error instead of giving a
# boundary verdict, and set -e would abort).
export GOWORK=off

CORE_PREFIX='github.com/olivaresai/olivares/core'

# A go list failure (broken module) aborts the script via set -e — a tree that
# cannot be analyzed must not pass the check.
deps="$(go list -deps ./...)"

if grep -E "^${CORE_PREFIX}(/|\$)" <<<"${deps}"; then
  echo
  echo "BOUNDARY VIOLATION: this connector transitively imports ${CORE_PREFIX}."
  echo "A connector is Apache-2.0 and must never link the AGPL engine. Depend"
  echo "only on github.com/olivaresai/olivares/sdk (and sdk/plugin for the"
  echo "plugin transport)."
  exit 1
fi

echo "Boundary check OK: no ${CORE_PREFIX} package in the build graph."
