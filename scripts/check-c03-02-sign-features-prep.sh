#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-02 unique leftover unique vs check-c03-02-sign-features.sh
# (on main, not in lint:addon-sets) and unique leftover unique vs #1408.
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-02-sign-features-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-02-sign-features-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0302P_JSON:-design/c03-02-sign-features-prep-2026-08-20.json}"
DOC="${OLIVARES_C0302P_DOC:-design/C03-02-SIGN-FEATURES-PREP-2026-08-20.md}"
SRC="${OLIVARES_C0302P_SRC:-cmd/olivares/cmd_license.go}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$SRC" ] || cannot "missing $SRC"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-02-sign-features.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original sign-features check"
grep -q 'informational; never a gate' "$DOC" \
  || fail "prepare doc lost informational never-a-gate pin"
if grep -qiE 'addongate reads --features|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi
grep -q 'featuresCSV' "$SRC" || fail "sign command lost the --features variable"
grep -q 'StringVar(&featuresCSV, "features"' "$SRC" \
  || fail "sign command has no --features flag"
grep -q 'parseLicenseFeatures(featuresCSV)' "$SRC" \
  || fail "sign does not parse --features"
grep -q 'Features: feats' "$SRC" \
  || fail "sign does not assign Features on Claims — Sign would mint an empty list"
grep -q 'func parseLicenseFeatures' "$SRC" || fail "parseLicenseFeatures missing"
grep -q 'informational; never a gate' "$SRC" \
  || fail "sign --features lost the informational never-a-gate wording"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-02-sign-features-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-02-sign-features-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "c03-02-sign-features-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("features_flag_present") is not True:
    fail("features_flag_present must stay true")
if data.get("claims_features_assigned") is not True:
    fail("claims_features_assigned must stay true")
if data.get("features_is_informational") is not True:
    fail("features_is_informational must stay true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c03-02-sign-features-prep: CLEAN — --features reaches Claims.Features; never a gate."
exit 0
