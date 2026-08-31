#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-21 unique leftover unique vs check-c05-21-fulfillment.sh (LOOK 2
# on origin/main without fulfillmentBlock) and unique leftover unique
# vs #892. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-21-fulfillment-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-21-fulfillment-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0521P_JSON:-design/c05-21-fulfillment-prep-2026-08-20.json}"
DOC="${OLIVARES_C0521P_DOC:-design/C05-21-FULFILLMENT-PREP-2026-08-20.md}"
ENVF="${OLIVARES_C0521P_ENV:-commercial/license-worker/src/env.ts}"
DELIV="${OLIVARES_C0521P_DELIV:-commercial/license-worker/src/delivery/deliver.ts}"
WH="${OLIVARES_C0521P_WH:-commercial/license-worker/src/dodo/webhook.ts}"

for f in "$JSON" "$DOC" "$ENVF" "$DELIV" "$WH"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c05-21-fulfillment.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original fulfillment check"
grep -F -q 'Unique leftover unique vs `#892`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #892"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'fulfillmentBlock not landed' "$DOC" \
  || fail "prepare doc lost fulfillmentBlock HOLD"
grep -F -q 'C05-26 manifesto already on origin/main' "$DOC" \
  || fail "prepare doc lost C05-26 remasure"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|fulfillmentBlock landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

grep -q 'return env.FULFILLMENT_ENABLED === "true"' "$ENVF" \
  || fail "fulfillmentEnabled is no longer flag-only"
if grep -q 'export function fulfillmentBlock' "$ENVF"; then
  fail "fulfillmentBlock landed — this HOLD lote does not apply C05-21"
fi
if grep -q 'ENTERPRISE_VERSION_PLACEHOLDER' "$ENVF"; then
  fail "ENTERPRISE_VERSION_PLACEHOLDER landed — this HOLD lote does not apply C05-21"
fi
grep -q 'C05-26: the channel manifesto is the authority' "$DELIV" \
  || fail "C05-26 manifesto authority comment drifted"
grep -q 'ENTERPRISE_VERSION is not a fallback' "$DELIV" \
  || fail "C05-26 no longer says ENTERPRISE_VERSION is not a fallback"
grep -q 'fulfillmentEnabled(env)' "$WH" \
  || fail "webhook no longer consults fulfillmentEnabled"
if grep -q 'fulfillmentBlock(env)' "$WH"; then
  fail "webhook consults fulfillmentBlock — this HOLD lote does not apply C05-21"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-21-fulfillment-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-21-fulfillment-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-21-fulfillment-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("fulfillment_block_landed") is not False:
    fail("fulfillment_block_landed must stay false")
if data.get("fulfillment_enabled_flag_only") is not True:
    fail("fulfillment_enabled_flag_only must stay true")
if data.get("c05_26_manifesto_authority") is not True:
    fail("c05_26_manifesto_authority must stay true")
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

say "check-c05-21-fulfillment-prep: CLEAN — fulfillmentBlock HOLD; C05-26 manifesto authority; overlay remasure not in this gate."
exit 0
