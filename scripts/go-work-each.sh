#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# go-work-each.sh — run a command once inside every module of the go.work
# workspace.
#
# WHY: in a go.work workspace, `go build ./...` / `go test ./...` only cover the
# CURRENT module, not all workspace modules (golang/go#50745). So build, test,
# vet and govulncheck must be run per-module. This iterates the module dirs from
# go.work (no jq dependency) and runs the given command in each.
#
# Usage:
#   scripts/go-work-each.sh go build ./...
#   scripts/go-work-each.sh go test -race -count=1 ./...
#   scripts/go-work-each.sh govulncheck ./...
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

[ "$#" -ge 1 ] || { echo "usage: $0 <command...>" >&2; exit 2; }
[ -f go.work ] || { echo "go.work not found at ${ROOT}" >&2; exit 1; }

mapfile -t MODULES < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
[ "${#MODULES[@]}" -gt 0 ] || { echo "no modules found in go.work" >&2; exit 1; }

# Optional GO_WORK_EACH_SKIP: comma-separated module DiskPaths to skip (leading
# "./" optional), e.g. GO_WORK_EACH_SKIP=cmd/olivares runs the command over every
# module EXCEPT the long-poll cmd/olivares suite. The push race gate uses this to
# race every module but cmd/olivares, whose hot subset is raced separately and
# whose e2e tail is raced nightly (Taskfile test:race-hot / test).
SKIP="${GO_WORK_EACH_SKIP:-}"
skip_match() {
  local mod="${1#./}" s
  local IFS=,
  for s in ${SKIP}; do
    s="${s#./}"
    [ -n "${s}" ] && [ "${s}" = "${mod}" ] && return 0
  done
  return 1
}


# Optional GO_WORK_EACH_ONLY: the allowlist twin of SKIP — comma-separated module
# DiskPaths to run and NOTHING else, e.g. GO_WORK_EACH_ONLY=core,modules. Added
# 2026-08-31 so a caller can shard the workspace across jobs by module.
#
# GRANULARITY IS THE MODULE, AND THAT IS NOT race-full's split: the groups in
# scripts/race-groups.sh are PER PACKAGE, because the sweep's two peaks —
# core/auth 1325 s and core/api 1321 s — live in the SAME module, so splitting by
# module does not split them. This filter shards by module; it does not replace
# race-groups.sh and the race-full matrix does not use it.
#
# Declared and matching NOTHING is an ERROR, not an empty sweep: a filter with a
# typo would run zero modules and exit 0, which is the cheapest possible way to
# publish "all green" without having executed anything.
ONLY="${GO_WORK_EACH_ONLY:-}"
only_match() {
  local mod="${1#./}" s
  local IFS=,
  for s in ${ONLY}; do
    s="${s#./}"
    [ -n "${s}" ] && [ "${s}" = "${mod}" ] && return 0
  done
  return 1
}
if [ -n "${ONLY}" ]; then
  _matched=0
  for _m in "${MODULES[@]}"; do
    only_match "${_m}" && _matched=$((_matched + 1))
  done
  [ "${_matched}" -gt 0 ] || {
    echo "GO_WORK_EACH_ONLY='${ONLY}' matches no go.work module; refusing to run zero modules and exit 0" >&2
    exit 2
  }
fi

rc=0
for m in "${MODULES[@]}"; do
  if [ -n "${ONLY}" ] && ! only_match "${m}"; then
    echo "==> ${m}: SKIPPED (not in GO_WORK_EACH_ONLY)"
    continue
  fi
  if [ -n "${SKIP}" ] && skip_match "${m}"; then
    echo "==> ${m}: SKIPPED (GO_WORK_EACH_SKIP)"
    continue
  fi
  echo "==> ${m}: $*"
  ( cd "${m}" && "$@" ) || rc=1
done
exit "${rc}"
