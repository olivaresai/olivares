#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# apply-github-presence.sh — apply the repo's GitHub presence-as-code.
#
# Applies, idempotently, to the target repository:
#   - the "About" description and homepage   (canonical strings live HERE)
#   - the topics list                        (canonical list lives HERE)
#   - the labels defined in .github/labels.json (create-or-update; never deletes)
#
# What it deliberately does NOT do (no API exists, or owned elsewhere):
#   - social preview upload   -> manual: Settings > Social preview
#                                (repo: .github/assets/social/social-preview-mix2-1280x640.png;
#                                 org:  .github/assets/social/social-preview-org-1280x640.png)
#   - org profile             -> the olivaresai/.github repo, from .github/org-profile/README.md
#   - branch protection / repo settings -> scripts/apply-branch-protection.sh (settings-as-code)
#
# Usage:  scripts/apply-github-presence.sh [owner/repo]   (default: olivaresai/olivares)
# Needs:  gh (authenticated with admin on the target repo), python3.
set -euo pipefail

REPO="${1:-olivaresai/olivares}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LABELS_FILE="$ROOT/.github/labels.json"

# Canonical visitor-facing copy: docs/launch/GITHUB-ABOUT.md (keep in lockstep).
DESCRIPTION="Ground truth for enterprise AI. One self-hosted Go binary to integrate, manage and secure the AI you already run — Claude Code, Codex, Grok Build and the rest. Work items, leases, inventory, policy, signed evidence. AGPL open core. Beta."
HOMEPAGE="https://olivares.ai"
TOPICS="ai-governance ai-agents agentic-ai mcp model-context-protocol claude grok llm self-hosted open-core least-privilege drift-detection audit observability finops security golang kubernetes"

echo "==> ${REPO}: description + homepage"
gh api -X PATCH "repos/${REPO}" \
  -f description="${DESCRIPTION}" \
  -f homepage="${HOMEPAGE}" >/dev/null

echo "==> ${REPO}: topics"
# shellcheck disable=SC2086  # TOPICS is a deliberate word-split list
printf '%s\n' ${TOPICS} \
  | python3 -c 'import sys,json;print(json.dumps({"names":[l.strip() for l in sys.stdin if l.strip()]}))' \
  | gh api -X PUT "repos/${REPO}/topics" --input - >/dev/null

echo "==> ${REPO}: labels (create-or-update from .github/labels.json)"
python3 -c '
import json,sys
for l in json.load(open(sys.argv[1]))["labels"]:
    print("\t".join((l["name"], l["color"], l["description"])))
' "$LABELS_FILE" | while IFS="$(printf '\t')" read -r name color desc; do
  if gh api "repos/${REPO}/labels/$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$name")" >/dev/null 2>&1; then
    gh api -X PATCH "repos/${REPO}/labels/$(python3 -c 'import sys,urllib.parse;print(urllib.parse.quote(sys.argv[1]))' "$name")" \
      -f new_name="$name" -f color="$color" -f description="$desc" >/dev/null
    echo "    updated: $name"
  else
    gh api -X POST "repos/${REPO}/labels" \
      -f name="$name" -f color="$color" -f description="$desc" >/dev/null
    echo "    created: $name"
  fi
done

echo "==> done. Manual steps that have no API: social preview (Settings > Social preview),"
echo "    org profile (olivaresai/.github <- .github/org-profile/README.md)."
