#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# One build path for Task, PR generation and GoReleaser. No generated Git commits.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
dist=core/internal/webui/dist
stamp=core/internal/webui/bundle-source.stamp
# Refuse a redirected output before clearing the fixed generated directory.
for path in core core/internal core/internal/webui "$dist" "$stamp"; do
  if [ -L "$path" ]; then
    echo "build:web: refusing symlink at $path" >&2
    exit 1
  fi
done
rm -f "$stamp"
before=$(bash scripts/web-bundle-source-digest.sh)
pnpm --dir web install --frozen-lockfile
rm -rf "$dist"
mkdir -p "$dist"
# Keep the compile-only placeholder even when the build fails after clearing dist.
trap 'cp web/public/PLACEHOLDER "$dist/PLACEHOLDER"' EXIT
pnpm --dir web run build
after=$(bash scripts/web-bundle-source-digest.sh)
if [ "$before" != "$after" ]; then
  echo "build:web: source inputs changed during the build" >&2
  exit 1
fi
printf '%s\n' "$after" > "$stamp"
if ! bash scripts/check-web-bundle-freshness.sh --built; then
  rm -f "$stamp"
  exit 1
fi
