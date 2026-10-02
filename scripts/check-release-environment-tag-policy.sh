#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Read-only admission check. The environment administrator changes live policies.
set -euo pipefail
repo="${GITHUB_REPOSITORY:-olivaresai/olivares}"
environment="${OLIVARES_RELEASE_ENVIRONMENT:-ota-release-ceremony}"
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ && "$environment" =~ ^[A-Za-z0-9_-]+$ ]] || {
  printf '%s\n' 'release environment policy: invalid repository or environment' >&2; exit 2;
}
if ! command -v gh >/dev/null || ! command -v jq >/dev/null; then
  printf '%s\n' 'release environment policy: gh and jq are required' >&2; exit 2;
fi
# GitHub globs match both 26.11 and 26.11.1. Strict tag grammar belongs to preflight.
want='[0-9]*.[0-9]*'
if ! policies="$(gh api --paginate "repos/$repo/environments/$environment/deployment-branch-policies" 2>/dev/null)"; then
  printf '%s\n' 'release environment policy: could not read tag admission policies' >&2; exit 2;
fi
if ! jq -s -e --arg want "$want" '
  length > 0 and all(.[]; (.branch_policies | type == "array")) and
  any(.[] | .branch_policies[]; .type == "tag" and .name == $want)
' <<<"$policies" >/dev/null; then
  printf 'release environment policy: %s needs tag policy %s for monthly and patch releases\n' "$environment" "$want" >&2
  exit 1
fi
printf 'release environment policy: %s admits monthly and patch tags\n' "$environment"
