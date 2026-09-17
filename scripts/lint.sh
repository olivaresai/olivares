#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# lint.sh — run golangci-lint on every module in the go.work workspace.
#
# WHY: `golangci-lint run ./...` from the workspace root does NOT lint
# sub-modules — it fails with
#   "directory prefix . does not contain modules listed in go.work ..."
# (verified: exit code 7, lints nothing). golangci-lint v2 has no built-in
# "whole workspace" mode, so we run it once per module. The single repo-root
# .golangci.yml is reused by each invocation (golangci-lint walks up to the
# git root to find config).
#
# Usage:
#   scripts/lint.sh          # run linters
#   scripts/lint.sh fmt      # check formatting (no writes)
#   scripts/lint.sh fmt-fix  # apply formatting
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

[ -f go.work ] || { echo "go.work not found at ${ROOT}" >&2; exit 1; }

mapfile -t MODULES < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
[ "${#MODULES[@]}" -gt 0 ] || { echo "no modules found in go.work" >&2; exit 1; }

mode="${1:-run}"
rc=0
for m in "${MODULES[@]}"; do
  echo "==> ${m}"
  case "${mode}" in
    run)     ( cd "${m}" && golangci-lint run ./... ) || rc=1 ;;
    fmt)     ( cd "${m}" && golangci-lint fmt --diff ./... ) || rc=1 ;;
    fmt-fix) ( cd "${m}" && golangci-lint fmt ./... ) || rc=1 ;;
    *)       echo "unknown mode: ${mode}" >&2; exit 2 ;;
  esac
done
exit "${rc}"
