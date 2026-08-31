#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-03: the four add-on ids in Claims.Features ARE the fused canon
# keys (PRICING-CANON catalog-v8, now on main — not only #467).
# Three answers: 0 / 1 / 2.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-03-addon-ids: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-03-addon-ids: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
CANON=design/PRICING-CANON.md
SRC=cmd/olivares/cmd_license.go
[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$SRC" ] || cannot "missing $SRC"

command -v python3 >/dev/null || cannot "no python3"

mapfile -t canon_ids < <(python3 - "$CANON" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
ids = re.findall(r"(?m)^\s*self_hosted\.business\.addons\.([a-z0-9-]+):\s*$", text)
# Unique, stable order of first appearance.
seen = []
for i in ids:
    if i not in seen:
        seen.append(i)
print("\n".join(seen))
PY
) || cannot "could not parse add-on ids from the canon"

[ "${#canon_ids[@]}" -eq 4 ] || fail "canon has ${#canon_ids[@]} add-on ids, want 4: ${canon_ids[*]}"

want="regulated ai-runtime-security compliance-packs identity-scale"
got="${canon_ids[*]}"
[ "$got" = "$want" ] || fail "fused canon ids are [$got], not [$want]"

grep -q 'catalog-v8' "$CANON" || fail "canon lost catalog-v8 — the fused source this lote measured"
grep -q 'sales_lane:' "$CANON" || fail "canon lost sales_lane — MATRIZ said that lived only on #467"

# The CLI allowlist is the same four, no extras, no invented fifth.
for id in "${canon_ids[@]}"; do
  grep -q "\"${id}\"" "$SRC" || fail "cmd_license.go does not name canon id ${id}"
done
grep -q 'isFusedCanonAddonID' "$SRC" || fail "parseLicenseFeatures no longer refuses unknown ids"
if grep -n '"business-max"\|"enterprise-max"\|"addon_reg"' "$SRC" >/dev/null; then
  fail "cmd_license.go invented an id that is not a fused-canon add-on key"
fi

say "check-c03-03-addon-ids: CLEAN — four fused-canon add-on ids, catalog-v8 on main."
exit 0
