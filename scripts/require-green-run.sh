#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# CI proves behavior; release consumes successful push/dispatch evidence, never reruns it.
# Usage: require-green-run.sh <workflow.yml> <sha> [--tree-equal-ok]
set +x
set -euo pipefail
cannot() { echo "release evidence: COULD NOT CHECK — $*" >&2; exit 2; }
workflow=${1:-} sha=${2:-} tree_equal=${3:-}
[[ $# == 2 || ( $# == 3 && $tree_equal == --tree-equal-ok && ( $workflow == release-first-hour.yml || $workflow == race-full.yml ) ) ]] || cannot 'invalid arguments'
[[ $workflow =~ ^[A-Za-z0-9_-]+\.ya?ml$ && $sha =~ ^[0-9a-f]{40}$ ]] || cannot 'workflow and full candidate SHA required'
[[ ${GITHUB_REPOSITORY:-} =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || cannot 'repository required'
[[ ${GH_TOKEN:-} =~ ^[A-Za-z0-9_.-]+$ ]] || cannot 'GitHub token required'
for tool in curl jq git; do command -v "$tool" >/dev/null || cannot "missing $tool"; done
candidate_tree=$(git rev-parse "${sha}^{tree}") || cannot 'candidate commit unavailable'
api() {
  # The header is read from stdin; the token never appears in curl's argv or logs.
  printf 'header = "Authorization: Bearer %s"\n' "$GH_TOKEN" |
    curl --config - --fail --silent --show-error --connect-timeout 10 --max-time 60 \
      --header 'Accept: application/vnd.github+json' --url "https://api.github.com/repos/$GITHUB_REPOSITORY/$1"
}
first_hour_job() {
  local run=$1 page response count found=0 qualified=0
  for ((page=1; page<=100; page++)); do
    response=$(api "actions/runs/$run/jobs?per_page=100&page=$page") || cannot 'job API failed'
    jq -e '.jobs | type == "array" and all(.[]; (.name | type == "string") and
      (.conclusion == null or (.conclusion | type == "string")))' <<< "$response" >/dev/null || cannot 'malformed jobs'
    count=$(jq '.jobs | length' <<< "$response")
    found=$((found + $(jq '[.jobs[] | select(.name == "release-first-hour")] | length' <<< "$response")))
    qualified=$((qualified + $(jq '[.jobs[] | select(.name == "release-first-hour" and .conclusion == "success")] | length' <<< "$response")))
    if ((count < 100)); then [[ $found == 1 && $qualified == 1 ]]; return; fi
  done
  cannot 'job pagination limit reached'
}
comparison_unavailable=false
for ((page=1; page<=100; page++)); do
  response=$(api "actions/workflows/$workflow/runs?status=success&per_page=100&page=$page") || cannot 'run API failed'
  jq -e '.workflow_runs | type == "array" and all(.[];
    (.id | type == "number" and . > 0 and floor == .) and
    (.head_sha | type == "string" and test("^[0-9a-f]{40}$")) and
    (.event | type == "string") and (.status | type == "string") and
    (.conclusion | type == "string"))' <<< "$response" >/dev/null || cannot 'malformed workflow runs'
  runs=$(jq -r '.workflow_runs[] | select(.status == "completed" and .conclusion == "success" and
    (.event == "push" or .event == "workflow_dispatch")) | [.id, .head_sha] | @tsv' <<< "$response")
  while IFS=$'\t' read -r run green; do
    [[ -n $run ]] || continue
    if [[ $green != "$sha" ]]; then
      [[ $tree_equal == --tree-equal-ok ]] || continue
      if ! git cat-file -e "${green}^{commit}" 2>/dev/null; then
        # Never add --depth: GoReleaser needs the full history for previous-tag metadata.
        if ! git fetch -q origin "$green"; then comparison_unavailable=true; continue; fi
      fi
      green_tree=$(git rev-parse "${green}^{tree}") || cannot 'evidence tree unavailable'
      [[ $green_tree == "$candidate_tree" ]] || continue
    fi
    if [[ $workflow == release-first-hour.yml ]] && ! first_hour_job "$run"; then continue; fi
    if [[ $green != "$sha" ]]; then echo "release evidence: green run on $green; tree identical to $sha"; fi
    echo "release evidence: PASS $workflow $sha (run $run, head $green)"
    exit 0
  done <<< "$runs"
  count=$(jq '.workflow_runs | length' <<< "$response")
  if ((count < 100)); then
    [[ $comparison_unavailable == false ]] || cannot 'no qualifying run; an evidence commit was unavailable'
    echo "release evidence: FAIL — no qualifying $workflow run on $sha${tree_equal:+ or an identical tree}" >&2
    exit 1
  fi
done
cannot 'run pagination limit reached'
