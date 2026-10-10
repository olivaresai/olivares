#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
source "$root/scripts/lib/exec-workdir.sh"
work=$(olivares_pick_exec_workdir first-hour-evidence)
trap 'rm -rf "$work"' EXIT
cat > "$work/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
# Verify the query binds the candidate SHA and the dedicated workflow.
if [[ $* == *'/workflows/'* ]]; then
  [[ $* == *"head_sha=$GITHUB_SHA"* && $* == *'.head_sha == '* && $* == *'/workflows/release-first-hour.yml/runs?'* && $* == *'.conclusion == "success"'* ]] || exit 2
  [[ ${API_ERROR:-0} == 0 ]] || exit 2
  printf '%s\n' "${RUNS:-}"
else
  [[ $* == *'select(.name == "release-first-hour")'* ]] || exit 2
  printf '%s\n' "${JOB:-}"
fi
STUB
chmod +x "$work/gh"
export PATH="$work:$PATH" GITHUB_REPOSITORY=example/product GITHUB_SHA=1111111111111111111111111111111111111111
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
printf 'PASS: 8 first-hour release evidence cases\n'
