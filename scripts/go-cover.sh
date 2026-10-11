#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# go-cover.sh — measure total test coverage once for every go.work module.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# Third answer: without go.work no coverage was measured (wrong cwd or partial
# clone). rc 1 would falsely judge the code. Zero modules is likewise not proof of
# an empty codebase: report an unperformed check.
[ -f go.work ] || { echo "go-cover: ⛔ COULD NOT LOOK — no go.work in ${ROOT}; no coverage was measured." >&2; exit 2; }

mapfile -t MODULES < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
[ "${#MODULES[@]}" -gt 0 ] || { echo "go-cover: ⛔ COULD NOT LOOK — go.work declares no modules; no coverage was measured." >&2; exit 2; }

COVER_DIR="$(mktemp -d "${TMPDIR:-/tmp}/olivares-cover.XXXXXX")"
trap 'rm -rf "${COVER_DIR}"' EXIT

index=0
rc=0
for module in "${MODULES[@]}"; do
  profile="${COVER_DIR}/module-${index}.cover.out"
  index=$((index + 1))

  echo "==> ${module}: coverage"
  if ! (cd "${module}" && go test -covermode=atomic -coverprofile="${profile}" ./...); then
    echo "coverage tests failed for ${module}; total may be incomplete" >&2
    rc=1
  fi

  if [ ! -s "${profile}" ] || ! grep -q -v '^mode:' "${profile}"; then
    echo "total: no coverprofile (no test files)"
    continue
  fi
  go tool cover -func="${profile}" | tail -1
done

exit "${rc}"
