#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Run before creating a stable tag; release.yml repeats it before building a draft.
set -euo pipefail
[[ ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ ]] || { echo 'full candidate GITHUB_SHA required' >&2; exit 2; }
[[ ${GITHUB_REPOSITORY:-} =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || exit 2
# Fail closed on API errors, malformed JSON, another SHA and skipped jobs. No ancestor or workflow-only exception applies.
runs=$(gh api --paginate "repos/$GITHUB_REPOSITORY/actions/workflows/release-first-hour.yml/runs?head_sha=$GITHUB_SHA&status=success&per_page=100" --jq ".workflow_runs[] | select(.head_sha == \"$GITHUB_SHA\" and .conclusion == \"success\") | .id")
for run in $runs; do
  [[ $run =~ ^[0-9]+$ ]] || exit 2
  result=$(gh api --paginate "repos/$GITHUB_REPOSITORY/actions/runs/$run/jobs?per_page=100" --jq '.jobs[] | select(.name == "release-first-hour") | .conclusion')
  if [[ $result == success ]]; then
    echo "first-hour release qualification: PASS $GITHUB_SHA (release-first-hour run $run)"
    exit 0
  fi
done
echo "FAIL: no successful candidate release-first-hour job on $GITHUB_SHA; run release-first-hour before tagging" >&2
exit 1
