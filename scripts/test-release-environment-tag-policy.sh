#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail
root="$(cd -- "$(dirname -- "$0")/.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-policy.XXXXXX")"
trap 'rm -rf -- "$work"' EXIT
mkdir "$work/bin"
cat > "$work/bin/gh" <<'GH'
#!/usr/bin/env bash
[[ "$*" == 'api --paginate repos/olivaresai/olivares/environments/ota-release-ceremony/deployment-branch-policies' ]] || exit 90
[[ "${FIXTURE_UNAVAILABLE:-0}" == 0 ]] || exit 1
cat "$FIXTURE_POLICY"
GH
chmod +x "$work/bin/gh"
export PATH="$work/bin:$PATH" FIXTURE_POLICY="$work/policies" GITHUB_REPOSITORY=olivaresai/olivares OLIVARES_RELEASE_ENVIRONMENT=ota-release-ceremony
check() {
  local want="$1" label="$2" got=0
  bash "$root/scripts/check-release-environment-tag-policy.sh" >"$work/out" 2>"$work/err" || got=$?
  [[ "$got" == "$want" ]] || { printf 'FAIL %s rc=%s\n' "$label" "$got"; cat "$work/err"; exit 1; }
  printf 'ok - %s\n' "$label"
}
printf '%s\n' '{"branch_policies":[{"type":"tag","name":"[0-9]*.[0-9]*"},{"type":"tag","name":"v*"}]}' > "$FIXTURE_POLICY"
check 0 'monthly and patch policy, historical v policy retained'
printf '%s\n' '{"branch_policies":[{"type":"tag","name":"[0-9]*.[0-9]*.[0-9]*"}]}' > "$FIXTURE_POLICY"
check 1 'three-number policy alone refuses monthly admission'
printf '%s\n' '{"branch_policies":[{"type":"branch","name":"[0-9]*.[0-9]*"}]}' > "$FIXTURE_POLICY"
check 1 'branch policy never substitutes for tag policy'
printf '%s\n' '{"branch_policies":[{"type":"tag","name":"*"}]}' > "$FIXTURE_POLICY"
check 1 'unbounded wildcard does not satisfy the declared policy'
printf '%s\n' '{"branch_policies":[]}' > "$FIXTURE_POLICY"
check 1 'absent tag policy refuses'
printf '%s\n' '{}' > "$FIXTURE_POLICY"
check 1 'malformed policy response refuses'
FIXTURE_UNAVAILABLE=1 check 2 'API read failure is explicit'
python3 - <<'PY'
import fnmatch
for tag in ['26.11','26.11.1','26.12','27.1']:
 assert fnmatch.fnmatchcase(tag,'[0-9]*.[0-9]*'),tag
assert not fnmatch.fnmatchcase('26.11','[0-9]*.[0-9]*.[0-9]*')
assert not fnmatch.fnmatchcase('main','[0-9]*.[0-9]*')
print('ok - documented deployment glob monthly/patch and negative controls')
PY
