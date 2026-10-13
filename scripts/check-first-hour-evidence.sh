#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Run before creating a stable tag; release.yml repeats it before building a draft.
set -euo pipefail
[[ ${GITHUB_SHA:-} =~ ^[0-9a-f]{40}$ ]] || { echo 'full candidate GITHUB_SHA required' >&2; exit 2; }
[[ ${GITHUB_REPOSITORY:-} =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || exit 2
# Fail closed on API errors, malformed evidence and skipped jobs. Only exact commits or
# identical Git trees qualify; an ancestor or workflow-only diff is not sufficient.
runs=$(gh api --paginate "repos/$GITHUB_REPOSITORY/actions/workflows/release-first-hour.yml/runs?status=success&per_page=100" --jq '.workflow_runs[] | select(.conclusion == "success") | [.id, .head_sha] | @tsv')
while IFS=$'\t' read -r run green extra; do
  [[ -n $run ]] || continue
  [[ $run =~ ^[0-9]+$ && $green =~ ^[0-9a-f]{40}$ && -z $extra ]] || exit 2
  if [[ $green != "$GITHUB_SHA" ]]; then
    if ! git cat-file -e "${green}^{commit}" 2>/dev/null; then
      # Never shallow the release checkout: GoReleaser needs the full tag history.
      git fetch -q origin "$green" || continue
    fi
    candidate_tree=$(git rev-parse "${GITHUB_SHA}^{tree}")
    green_tree=$(git rev-parse "${green}^{tree}")
    [[ $green_tree == "$candidate_tree" ]] || continue
  fi
  result=$(gh api --paginate "repos/$GITHUB_REPOSITORY/actions/runs/$run/jobs?per_page=100" --jq '.jobs[] | select(.name == "release-first-hour") | .conclusion')
  if [[ $result == success ]]; then
    if [[ $green != "$GITHUB_SHA" ]]; then
      echo "first-hour evidence: green run on $green; tree identical to $GITHUB_SHA"
    fi
    echo "first-hour release qualification: PASS $GITHUB_SHA (release-first-hour run $run)"
    exit 0
  fi
done <<< "$runs"
echo "FAIL: no successful candidate release-first-hour job on $GITHUB_SHA or an identical tree; run release-first-hour before tagging" >&2
exit 1
