#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c05-33-portal.sh — C05-33. The post-purchase Cloud page names the
# paid canon plan. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-33-portal: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-33-portal: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

PAGE=commercial/license-worker/src/portal/pages/cloud.ts
TEST=commercial/license-worker/test/cloud-portal.test.ts
[ -r "$PAGE" ] || cannot "missing $PAGE"
[ -r "$TEST" ] || cannot "missing $TEST"

grep -Fq 'export function planLabel' "$PAGE" \
  || fail "planLabel is gone — the page no longer names the paid plan"
grep -Fq 'Cloud Standard' "$PAGE" \
  || fail "Cloud Standard public name is gone from the Cloud page"
grep -Fq '"cloud-standard-m"' "$PAGE" \
  || fail "cloud-standard-m (the monthly Cloud SKU tier) is unmapped"
grep -Fq 'Get started with Cloud Standard' "$PAGE" \
  || fail "empty-state CTA no longer names Cloud Standard"

if grep -Fq 'Get started with a Pro or Business plan' "$PAGE"; then
  fail "empty state still invites Pro or Business — those are not cloud SKUs"
fi
if grep -Eq 'pro:[[:space:]]*"Pro"' "$PAGE"; then
  fail "Polar leftover pro is titled as a product again"
fi
if grep -Eq 'business:[[:space:]]*"Business"' "$PAGE"; then
  fail "Polar leftover business is titled as a product again"
fi
if grep -Eq 'enterprise:[[:space:]]*"Enterprise"' "$PAGE"; then
  fail "Polar leftover enterprise is titled as a product again"
fi

grep -Fq 'planLabel("cloud-standard-m")' "$TEST" \
  || fail "tests no longer pin cloud-standard-m → Cloud Standard"
grep -Fq 'Pro or Business' "$TEST" \
  || fail "tests no longer refuse the Pro or Business CTA"

say "check-c05-33-portal: CLEAN — Cloud page names Cloud Standard; Polar leftovers stay raw."
exit 0
