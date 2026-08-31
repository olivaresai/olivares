#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-03 unique leftover unique vs check-c03-03-addon-ids.sh
# (on main GATE only, not in lint:addon-sets) and unique leftover
# unique vs #1409. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-03-addon-ids-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-03-addon-ids-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0303P_JSON:-design/c03-03-addon-ids-prep-2026-08-20.json}"
DOC="${OLIVARES_C0303P_DOC:-design/C03-03-ADDON-IDS-PREP-2026-08-20.md}"
CANON="${OLIVARES_C0303P_CANON:-design/PRICING-CANON.md}"
SRC="${OLIVARES_C0303P_SRC:-cmd/olivares/cmd_license.go}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$SRC" ] || cannot "missing $SRC"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-03-addon-ids.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original addon-ids check"
grep -q 'regulated ai-runtime-security' "$DOC" \
  || fail "prepare doc lost the four fused-canon ids"
if grep -qiE 'addongate reads Features|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi
grep -q 'catalog-v8' "$CANON" || fail "canon lost catalog-v8"
grep -q 'sales_lane:' "$CANON" || fail "canon lost sales_lane"
grep -q 'isFusedCanonAddonID' "$SRC" \
  || fail "parseLicenseFeatures no longer refuses unknown ids"
if grep -n '"business-max"\|"enterprise-max"\|"addon_reg"' "$SRC" >/dev/null; then
  fail "cmd_license.go invented an id that is not a fused-canon add-on key"
fi

python3 - "$JSON" "$CANON" "$SRC" <<'PY' || exit $?
import json, re, sys

def fail(msg):
    print(f"check-c03-03-addon-ids-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-03-addon-ids-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    canon = open(sys.argv[2], encoding="utf-8").read()
    src = open(sys.argv[3], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

want = ["regulated", "ai-runtime-security", "compliance-packs", "identity-scale"]
if data.get("schema") != "c03-03-addon-ids-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("four_ids") != want:
    fail("four_ids drifted from fused-canon keys")
if data.get("catalog_v8") is not True:
    fail("catalog_v8 must stay true")
if data.get("sales_lane_present") is not True:
    fail("sales_lane_present must stay true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

ids = re.findall(r"(?m)^\s*self_hosted\.business\.addons\.([a-z0-9-]+):\s*$", canon)
seen = []
for i in ids:
    if i not in seen:
        seen.append(i)
if seen != want:
    fail("fused canon ids are %s, not %s" % (seen, want))
for i in want:
    if '"%s"' % i not in src:
        fail("cmd_license.go does not name canon id %s" % i)
print("json-ok")
PY

say "check-c03-03-addon-ids-prep: CLEAN — four fused-canon add-on ids; catalog-v8 on main."
exit 0
