#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05 full-refund unique leftover unique vs #891 (original OPEN product
# PR; stale vs origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-full-refund-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-full-refund-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C05REFP_JSON:-design/c05-full-refund-prep-2026-08-20.json}"
DOC="${OLIVARES_C05REFP_DOC:-design/C05-FULL-REFUND-PREP-2026-08-20.md}"
DODO="${OLIVARES_C05REFP_DODO:-cloud/control-plane/internal/billing/dodo.go}"

for f in "$JSON" "$DOC" "$DODO"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#891`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #891"
grep -F -q 'Unique leftover unique vs `hub-comercio/c05-full-refund`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'ClassifyDodoRefundBody not landed' "$DOC" \
  || fail "prepare doc lost classify HOLD"
grep -F -q 'Does not copy stale `#891`' "$DOC" \
  || fail "prepare doc lost stale-branch HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|ClassifyDodoRefundBody landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -F -q 'case "payment.succeeded", "refund.succeeded":' "$DODO" \
  || fail "refund is no longer lumped with payment.succeeded"
if grep -q 'ClassifyDodoRefundBody' "$DODO"; then
  fail "ClassifyDodoRefundBody landed — this HOLD lote does not apply #891"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-full-refund-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-full-refund-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-full-refund-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("classify_dodo_refund_body_landed") is not False:
    fail("classify_dodo_refund_body_landed must stay false")
if data.get("refund_lumped_with_payment") is not True:
    fail("refund_lumped_with_payment must stay true")
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

say "check-c05-full-refund-prep: CLEAN — ClassifyDodoRefundBody HOLD; refund lumped with payment; overlay remasure not in this gate."
exit 0
