#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
source "$root/scripts/lib/git-env.sh"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
source "$root/scripts/lib/exec-workdir.sh"
work=$(olivares_pick_exec_workdir first-hour-evidence)
trap 'rm -rf "$work"' EXIT
cat > "$work/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
# Stub only GitHub; all commit and tree comparisons use real Git objects.
if [[ $* == *'/workflows/'* ]]; then
  [[ $* == *'/workflows/release-first-hour.yml/runs?'* && $* == *'.conclusion == "success"'* ]] || exit 2
  [[ ${API_ERROR:-0} == 0 ]] || exit 2
  if [[ $* == *"head_sha=$GITHUB_SHA"* ]]; then
    # The old gate cannot see evidence on a different commit.
    [[ ${GREEN:-$GITHUB_SHA} == "$GITHUB_SHA" ]] || exit 0
    printf '%s\n' "${RUNS:-}"
  else
    for run in ${RUNS:-}; do
      green=${GREEN:-$GITHUB_SHA}
      if [[ $run == 124 && -n ${GREEN_SECOND:-} ]]; then green=$GREEN_SECOND; fi
      printf '%s\t%s\n' "$run" "$green"
    done
  fi
else
  [[ $* == *'select(.name == "release-first-hour")'* ]] || exit 2
  [[ ${JOB_API_ERROR:-0} == 0 ]] || exit 2
  printf '%s\n' "${JOB:-}"
fi
STUB
chmod +x "$work/gh"
export PATH="$work:$PATH" GITHUB_REPOSITORY=example/product
export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.invalid
git init -q "$work/origin"
cd "$work/origin"
printf 'base\n' > product
git add product
git commit -qm base
base=$(git rev-parse HEAD)
printf 'candidate\n' > product
git add product
git commit -qm candidate
pr=$(git rev-parse HEAD)
tree=$(git rev-parse HEAD^{tree})
merge=$(git commit-tree "$tree" -p "$base" -p "$pr" -m merge)
git update-ref refs/heads/main "$merge"
# An orphan with the same tree models a PR head absent from the release checkout.
missing=$(git commit-tree "$tree" -m missing-pr-head)
git update-ref refs/heads/release "$missing"
mkdir .github
printf 'changed\n' > .github/workflow
git add .github
git commit -qm workflow-change
different=$(git rev-parse HEAD)
git clone -q --no-local --single-branch --branch main "$work/origin" "$work/checkout"
cd "$work/checkout"
export GITHUB_SHA=$merge
check() {
  local expected=$1; shift
  local rc=0
  env "$@" bash "$root/scripts/check-first-hour-evidence.sh" > "$work/out" 2>&1 || rc=$?
  [[ $rc == "$expected" ]] || { cat "$work/out"; echo "expected $expected got $rc"; exit 1; }
}
check 1 RUNS= JOB=success
check 1 RUNS=123 JOB=skipped
check 1 RUNS=123 JOB=failure
check 1 RUNS=123 JOB=
check 2 RUNS=invalid JOB=success
check 2 API_ERROR=1
check 0 RUNS=123 JOB=success
check 2 GITHUB_SHA=not-a-sha
check 0 RUNS=123 JOB=success GREEN="$pr"
grep -F "tree identical to $merge" "$work/out"
check 1 RUNS=123 JOB=success GREEN="$different"
check 1 RUNS=123 JOB=skipped GREEN="$pr"
check 1 RUNS=123 JOB=failure GREEN="$pr"
check 2 RUNS=123 JOB=success GREEN=not-a-sha
check 2 RUNS=123 JOB=success JOB_API_ERROR=1
if git cat-file -e "$missing^{commit}" 2>/dev/null; then
  echo "fixture unexpectedly contains the missing PR head" >&2; exit 1
fi
check 0 RUNS=123 JOB=success GREEN="$missing"
[[ $(git rev-parse --is-shallow-repository) == false ]]
check 1 RUNS=123 JOB=success GREEN=2222222222222222222222222222222222222222
check 1 RUNS=123 JOB=success GREEN="$pr" GITHUB_SHA="$different"
check 0 RUNS="123 124" JOB=success GREEN="$different" GREEN_SECOND="$pr"
check 2 RUNS=123 JOB=success GITHUB_REPOSITORY=invalid
# The first-hour gate is advisory only for 0.1; 0.2 and later must still block.
preflight=$(awk '
  /^      - name: / { step = "" }
  { step = step $0 ORS }
  /run: bash scripts\/check-first-hour-evidence.sh/ { printf "%s", step; exit }
' "$root/.github/workflows/release.yml")
grep -Fx '        continue-on-error: ${{ github.ref_name == '\''0.1'\'' }}' <<< "$preflight"
printf 'PASS: 19 first-hour release evidence cases; only 0.1 is advisory\n'
