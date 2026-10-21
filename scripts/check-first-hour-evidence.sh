#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Run before creating a stable tag; release.yml repeats it before building a draft.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
exec bash "$root/scripts/require-green-run.sh" release-first-hour.yml "${GITHUB_SHA:-}" --tree-equal-ok
