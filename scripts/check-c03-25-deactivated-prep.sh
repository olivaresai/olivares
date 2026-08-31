#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C03-25 unique leftover unique vs check-c03-25-deactivated.sh (LOOK 2
# on origin/main without the 2026-08-19 census doc) and unique leftover
# unique vs #1014. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-25-deactivated-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-25-deactivated-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0325P_JSON:-design/c03-25-deactivated-prep-2026-08-20.json}"
DOC="${OLIVARES_C0325P_DOC:-design/C03-25-DEACTIVATED-PREP-2026-08-20.md}"
STORE="${OLIVARES_C0325P_STORE:-commercial/license-worker/src/store/store.ts}"
DB="${OLIVARES_C0325P_DB:-commercial/license-worker/src/store/db.ts}"
GATE="${OLIVARES_C0325P_GATE:-commercial/license-worker/src/download/gate.ts}"

for f in "$JSON" "$DOC" "$STORE" "$DB" "$GATE"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c03-25-deactivated.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original deactivated check"
grep -F -q 'Unique leftover unique vs `#1014`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1014"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Access cut not landed' "$DOC" \
  || fail "prepare doc lost access-cut HOLD"
grep -F -q 'Term untouched' "$DOC" \
  || fail "prepare doc lost term HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|access cut landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if grep -q 'holderHasDeploymentAccess' "$STORE"; then
  fail "store access-cut landed — this HOLD lote does not apply C03-25"
fi
if grep -q 'holderHasDeploymentAccess' "$GATE"; then
  fail "gate access-cut landed — this HOLD lote does not apply C03-25"
fi
if grep -q 'deactivateDeployment' "$DB"; then
  fail "deactivateDeployment landed — this HOLD lote does not apply C03-25"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c03-25-deactivated-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c03-25-deactivated-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c03-25-deactivated-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("access_cut_landed") is not False:
    fail("access_cut_landed must stay false")
if data.get("term_rewritten") is not False:
    fail("term_rewritten must stay false")
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

say "check-c03-25-deactivated-prep: CLEAN — access cut HOLD; term untouched; overlay remasure not in this gate."
exit 0
