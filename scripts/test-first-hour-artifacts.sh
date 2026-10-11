#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export FIRST_HOUR_ARTIFACTS="$work/evidence"
mkdir -p "$FIRST_HOUR_ARTIFACTS"
for file in engine-sha256.txt running-image.txt candidate-image.txt; do
  printf 'identity must survive Playwright startup\n' > "$FIRST_HOUR_ARTIFACTS/$file"
done
(cd "$FIRST_HOUR_ARTIFACTS" && sha256sum *.txt) > "$work/before"
pnpm --dir "$root/web" exec playwright test --config playwright.release.config.ts surface.spec.ts --workers=1
(cd "$FIRST_HOUR_ARTIFACTS" && sha256sum --check "$work/before")
echo 'PASS: browser guards preserve container identity evidence'
