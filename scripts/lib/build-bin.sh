# shellcheck shell=bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Source this helper for a static, path-trimmed development binary.
# Version and reproducible commit time come from the shared build stamps.
build_olivares_bin() {
  if [ "$#" -ne 1 ]; then
    echo "build_olivares_bin: exactly one argument (the output path) is required; got $#" >&2
    return 2
  fi
  local root flags
  root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)" || return
  OLIVARES_ROOT="$root" bash "$root/scripts/module-catalog-go.sh" pack-check || return
  flags="$(sh "$root/scripts/build-ldflags.sh")" || return
  ( cd "$root" && CGO_ENABLED=0 go build -trimpath \
      -ldflags "$flags" -o "$1" ./cmd/olivares )
}
