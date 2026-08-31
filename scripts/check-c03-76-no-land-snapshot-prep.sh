#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-76 unique leftover unique vs overlay-remeasuring
# check-c03-76-no-land-snapshot.sh: hub-safe HOLD so lint:addon-sets
# does not remasure overlay. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-76-no-land-snapshot-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-76-no-land-snapshot-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0376P_JSON:-design/c03-76-no-land-snapshot-prep-2026-08-20.json}"
DOC="${OLIVARES_C0376P_DOC:-design/C03-76-NO-LAND-SNAPSHOT-PREP-2026-08-20.md}"
BACKLOG="${OLIVARES_C0376P_BACKLOG:-design/BACKLOG-COMPLETITUD-2026-08-16.md}"
ROSTER="${OLIVARES_C0376P_ROSTER:-modules/governance/roster.go}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$BACKLOG" ] || cannot "missing $BACKLOG"
[ -r "$ROSTER" ] || cannot "missing $ROSTER"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-76-no-land-snapshot.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-remeasuring no-land check"
grep -q 'NOT MERGED' "$DOC" || fail "prepare doc lost NOT MERGED"
grep -q 'as-is refused' "$DOC" || fail "prepare doc lost as-is refused"
grep -q 'TestSnapshotIsDeliberatelyUngated' "$DOC" \
  || fail "prepare doc lost the overlay-main test name"
grep -q 'EntitlementFunc' "$DOC" || fail "prepare doc lost EntitlementFunc"
if grep -qiE 'landed overlay #76|snapshot now gated on overlay main|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi
grep -q 'C03-04' "$BACKLOG" || fail "backlog lost the C03-04 row"
grep -q 'ACCUMULATE AND CONTINUE' "$ROSTER" \
  || fail "hub roster lost accumulate-and-continue"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-76-no-land-snapshot-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-76-no-land-snapshot-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"JSON is not readable: {e}")

if data.get("schema") != "c03-76-no-land-snapshot-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("as_is_landable") is not False:
    fail("as_is_landable must stay false")
if data.get("merged") is not False:
    fail("merged must stay false")
if data.get("executed") is not False:
    fail("executed must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if data.get("overlay_pr") != 76:
    fail("overlay_pr must stay 76")
if data.get("also_gates_snapshot_pr") != 83:
    fail("also_gates_snapshot_pr must stay 83")
if data.get("snapshot_ungated_on_overlay_main") is not True:
    fail("Snapshot must stay ungated on overlay main")
if data.get("snapshot_gated_in_pr76") is not True:
    fail("PR 76 still gates Snapshot")
if data.get("entitlement_func_on_overlay_main") is not False:
    fail("EntitlementFunc must stay absent on overlay main")
if data.get("snapshot_is_a_read") is not True:
    fail("Snapshot must stay classified as a read")
if data.get("snapshot_op") != "snapshot-conjur-roster":
    fail("snapshot_op must stay snapshot-conjur-roster")
sha = data.get("overlay_main_sha") or ""
if len(sha) != 40 or any(c not in "0123456789abcdef" for c in sha):
    fail("overlay_main_sha is not 40-hex")
p76 = data.get("pr76_sha") or ""
if len(p76) != 40 or p76 == sha:
    fail("pr76_sha is not a distinct 40-hex")
hub = data.get("hub") or ""
if len(hub) != 40 or any(c not in "0123456789abcdef" for c in hub):
    fail("hub is not 40-hex")
for k in ("u_f", "u_d"):
    if data.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN" % k)
print("json-ok")
PY

say "check-c03-76-no-land-snapshot-prep: CLEAN — as-is refused; hub-safe; overlay not remasured."
exit 0
