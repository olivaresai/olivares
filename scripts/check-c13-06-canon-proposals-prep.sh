#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-06 unique leftover unique vs check-c13-06-canon-proposals.sh
# (named on main, CHECK not in lint:addon-sets) and unique leftover
# unique vs #1400. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-06-canon-proposals-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-06-canon-proposals-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1306P_JSON:-design/c13-06-canon-proposals-prep-2026-08-20.json}"
DOC="${OLIVARES_C1306P_DOC:-design/C13-06-CANON-PROPOSALS-PREP-2026-08-20.md}"
CANON="${OLIVARES_C1306P_CANON:-design/PRICING-CANON.md}"
WIRE="${OLIVARES_C1306P_WIRE:-cmd/olivares/wire_noenterprise.go}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$CANON" ] || cannot "missing $CANON"
[ -r "$WIRE" ] || cannot "missing $WIRE"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c13-06-canon-proposals.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original canon-proposals check"
grep -F -q 'Unique leftover unique vs `#1400`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1400"
grep -q 'NO ELEGIDO' "$DOC" || fail "prepare doc lost NO ELEGIDO"
grep -q 'NO APLICADO' "$DOC" || fail "prepare doc lost NO APLICADO"
if grep -qiE 'aplicamos la propuesta|applied proposal|canon rewritten|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'modules_day_one:' "$CANON" || fail "$CANON lost modules_day_one: — C13-06 must not apply proposal 1"
grep -q 'modules_growth:' "$CANON" || fail "$CANON lost modules_growth: — C13-06 must not apply proposal 1"
grep -q 'modules_on:' "$CANON" || fail "$CANON lost modules_on: — C13-06 must not apply proposal 1"
grep -q 'modules_hold_gated:' "$CANON" || fail "$CANON lost modules_hold_gated: — C13-06 must not apply proposal 1"
grep -q '    modules:' "$CANON" || fail "$CANON lost modules:"
grep -q 'retrieval-scan' "$CANON" || fail "$CANON lost retrieval-scan — C13-06 must not unify spelling"
grep -q 'enterprise/computeruse' "$WIRE" || fail "$WIRE lost enterprise/computeruse comment — C13-06 must not apply proposal 3"

if ! awk '
  /^  self_hosted.enterprise:/ { p=1; next }
  p && /^  [a-z]/ { exit (found ? 0 : 1) }
  p && /scope:/ { found=1 }
  END { exit (found ? 0 : 1) }
' "$CANON"
then
  fail "$CANON self_hosted.enterprise lost scope: — C13-06 must not apply proposal 2"
fi

python3 - "$JSON" "$CANON" "$WIRE" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c13-06-canon-proposals-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c13-06-canon-proposals-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    canon = open(sys.argv[2], encoding="utf-8").read()
    wire = open(sys.argv[3], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

want = [
    "modules:",
    "modules_day_one:",
    "modules_growth:",
    "modules_on:",
    "modules_hold_gated:",
]
if data.get("schema") != "c13-06-canon-proposals-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("five_keys") != want:
    fail("five_keys drifted from signed canon labels")
if data.get("retrieval_scan_present") is not True:
    fail("retrieval_scan_present must stay true")
if data.get("computeruse_comment_present") is not True:
    fail("computeruse_comment_present must stay true")
if data.get("enterprise_has_scope") is not True:
    fail("enterprise_has_scope must stay true")
if data.get("enterprise_has_modules_key") is not False:
    fail("enterprise_has_modules_key must stay false")
if data.get("proposals_applied") is not False:
    fail("proposals_applied must stay false")
if data.get("no_elegido") is not True:
    fail("no_elegido must stay true")
if data.get("no_aplicado") is not True:
    fail("no_aplicado must stay true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)

if "retrieval-scan" not in canon:
    fail("canon lost retrieval-scan")
if "enterprise/computeruse" not in wire:
    fail("wire lost enterprise/computeruse comment")
print("json-ok")
PY

say "check-c13-06-canon-proposals-prep: CLEAN — five keys and enterprise scope still in the canon; proposals presented, none applied."
exit 0
