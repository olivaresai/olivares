#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Reference demo E2E for governed RAG defaults: semantic embedder status, live
# content-source provenance, MCP retrieval, source scope and deny-closed clearance.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
START="$(date +%s)"

echo "==> Running governed RAG demo E2E (semantic + scope + deny + source_mode)"
(
  cd "$ROOT/cmd/olivares"
  go test -count=1 -tags e2e -run '^TestE2E_GovernedRAGDefaultsLive$' -v .
)

END="$(date +%s)"
echo "==> GOVERNED_RAG_TIMING wall_clock=$((END - START))s"
