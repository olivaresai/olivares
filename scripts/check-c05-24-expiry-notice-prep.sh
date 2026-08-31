#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-24 unique leftover unique vs #1085 (original OPEN product PR;
# no original check on origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-24-expiry-notice-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-24-expiry-notice-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0524P_JSON:-design/c05-24-expiry-notice-prep-2026-08-20.json}"
DOC="${OLIVARES_C0524P_DOC:-design/C05-24-EXPIRY-NOTICE-PREP-2026-08-20.md}"
IDX="${OLIVARES_C0524P_IDX:-commercial/license-worker/src/index.ts}"
WRA="${OLIVARES_C0524P_WRA:-commercial/license-worker/wrangler.jsonc}"
NOTICE="${OLIVARES_C0524P_NOTICE:-commercial/license-worker/src/expiry-notice.ts}"
MIG="${OLIVARES_C0524P_MIG:-commercial/license-worker/migrations/0032_expiry_notice.sql}"

for f in "$JSON" "$DOC" "$IDX" "$WRA"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#1085`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1085"
grep -F -q 'Unique leftover unique vs `hub-comercio/c05-24-expiry-notice`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Daily expiry notice not landed' "$DOC" \
  || fail "prepare doc lost daily-notice HOLD"
grep -F -q 'Does not add 0032' "$DOC" \
  || fail "prepare doc lost 0032 HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|expiry notice landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if [ -e "$NOTICE" ]; then
  fail "expiry-notice.ts landed — this HOLD lote does not apply C05-24"
fi
if [ -e "$MIG" ]; then
  fail "0032_expiry_notice.sql landed — this HOLD lote does not apply C05-24"
fi
if grep -q 'runExpiryNotices' "$IDX"; then
  fail "runExpiryNotices imported — this HOLD lote does not apply C05-24"
fi
if grep -F -q '0 12 * * *' "$WRA"; then
  fail "daily noon cron landed — this HOLD lote does not apply C05-24"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-24-expiry-notice-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-24-expiry-notice-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-24-expiry-notice-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("expiry_notice_landed") is not False:
    fail("expiry_notice_landed must stay false")
if data.get("cron_landed") is not False:
    fail("cron_landed must stay false")
if data.get("migration_0032_added") is not False:
    fail("migration_0032_added must stay false")
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

say "check-c05-24-expiry-notice-prep: CLEAN — expiry notice HOLD; no cron; no 0032; overlay remasure not in this gate."
exit 0
