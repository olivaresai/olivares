#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-13 unique leftover unique vs check-c03-13-w-hybrid.sh (LOOK 2 on
# origin/main without the 2026-08-19 census doc) and unique leftover
# unique vs #1005. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-13-w-hybrid-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-13-w-hybrid-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0313P_JSON:-design/c03-13-w-hybrid-prep-2026-08-20.json}"
DOC="${OLIVARES_C0313P_DOC:-design/C03-13-W-HYBRID-PREP-2026-08-20.md}"
SRC="${OLIVARES_C0313P_SRC:-commercial/license-worker/src/license/issue-context.ts}"

for f in "$JSON" "$DOC" "$SRC"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-13-w-hybrid.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original W-hybrid check"
grep -F -q 'Unique leftover unique vs `#1005`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1005"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'W hybrid not landed' "$DOC" \
  || fail "prepare doc lost W-hybrid HOLD"
grep -F -q 'Paid issue still hardcoded refund_window' "$DOC" \
  || fail "prepare doc lost hardcoded-refund_window remasure"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|W hybrid landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -F -q 'paidIssue ? "refund_window" : "term"' "$SRC" \
  || fail "hardcoded refund_window line drifted — remasure no longer holds"
if grep -q 'phaseForPaidIssue' "$SRC"; then
  fail "phaseForPaidIssue landed — this HOLD lote does not apply C03-13"
fi
if grep -q 'PERMISSIVE_MARGIN_DAYS' "$SRC"; then
  fail "PERMISSIVE_MARGIN_DAYS landed — this HOLD lote does not apply C03-13"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-13-w-hybrid-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-13-w-hybrid-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c03-13-w-hybrid-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("w_hybrid_landed") is not False:
    fail("w_hybrid_landed must stay false")
if data.get("paid_issue_hardcoded_refund_window") is not True:
    fail("paid_issue_hardcoded_refund_window must stay true")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
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

say "check-c03-13-w-hybrid-prep: CLEAN — W hybrid HOLD; paid issue still hardcoded refund_window; overlay remasure not in this gate."
exit 0
