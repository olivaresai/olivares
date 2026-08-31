#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-helm-chart-prep.sh — CFG-18. Prepare the first chart release.
# Does NOT cut chart-v* and does NOT publish to olivaresai/*. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-helm-chart-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-helm-chart-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

CHART=deploy/helm/olivares/Chart.yaml
WF=.github/workflows/release-chart.yml
DOC=design/CFG-18-HELM-CHART-PREP-2026-08-19.md
[ -r "$CHART" ] || cannot "missing $CHART"
[ -r "$WF" ] || cannot "missing $WF"
[ -r "$DOC" ] || cannot "missing $DOC"

grep -q 'NO publicado' "$DOC" \
  || fail "prepare doc no longer says it does not publish"

python3 - "$CHART" <<'PY' || exit $?
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
def fail(m):
    print(f"check-helm-chart-prep: FAIL — {m}", file=sys.stderr)
    sys.exit(1)
ver = re.search(r"(?m)^version:\s*([0-9]+\.[0-9]+\.[0-9]+)\s*$", text)
app = re.search(r'(?m)^appVersion:\s*"?(2[6-9]|[3-9][0-9])\.(?:[1-9]|1[0-2])\.[0-9]+"?\s*$', text)
if not ver:
    fail("Chart.yaml version is not SemVer X.Y.Z")
if not app:
    fail("Chart.yaml appVersion is not project CalVer (YY.M.PATCH, year>=26)")
print(f"chart {ver.group(1)} appVersion ok")
PY

grep -q 'tags: \["chart-v\*"\]' "$WF" \
  || fail "release-chart.yml is no longer gated on chart-v* tags"
if grep -E '^[[:space:]]*workflow_dispatch:' "$WF" >/dev/null; then
  fail "release-chart.yml has a workflow_dispatch trigger — that is a publish path this lote must not add"
fi
grep -q 'oci://ghcr.io/olivaresai/charts' "$WF" \
  || fail "release-chart.yml lost the OCI repo"

# Local tags only when the selftest plants one. The shared clone's tag
# namespace is not this lote. Origin had zero chart-v* (measured 2026-08-19).
if [ "${OLIVARES_HELM_PREP_LOCAL_TAGS:-}" = "1" ] && command -v git >/dev/null; then
  tags="$(git -C "$ROOT" tag -l 'chart-v*' 2>/dev/null || true)"
  if [ -n "$tags" ]; then
    fail "this tree has chart-v* tags — CFG-18 must not cut the tag (the owner publishes)"
  fi
fi

say "check-helm-chart-prep: CLEAN — chart pinned; no dispatch; no chart-v* tag; NO publicado."
exit 0
