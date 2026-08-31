#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-04 unique leftover unique vs #957 and #876 (original OPEN product
# PRs; no original check on origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-04-business-fence-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-04-business-fence-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG04P_JSON:-design/cfg-04-business-fence-prep-2026-08-20.json}"
DOC="${OLIVARES_CFG04P_DOC:-design/CFG-04-BUSINESS-FENCE-PREP-2026-08-20.md}"
ENVF="${OLIVARES_CFG04P_ENV:-commercial/license-worker/src/env.ts}"
WH="${OLIVARES_CFG04P_WH:-commercial/license-worker/src/dodo/webhook.ts}"

for f in "$JSON" "$DOC" "$ENVF" "$WH"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#957`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #957"
grep -F -q 'Unique leftover unique vs `#876`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #876"
grep -F -q 'Unique leftover unique vs `hub-comercio/cfg-04-now`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'expectedDodoBusinessId not landed' "$DOC" \
  || fail "prepare doc lost fence HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|expectedDodoBusinessId landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if grep -q 'expectedDodoBusinessId' "$ENVF"; then
  fail "expectedDodoBusinessId landed — this HOLD lote does not apply CFG-04"
fi
if grep -q 'DODO_BUSINESS_ID' "$ENVF"; then
  fail "DODO_BUSINESS_ID landed — this HOLD lote does not apply CFG-04"
fi
if grep -q 'expectedDodoBusinessId' "$WH"; then
  fail "webhook consults expectedDodoBusinessId — this HOLD lote does not apply CFG-04"
fi
if grep -q 'FOREIGN_BUSINESS' "$WH"; then
  fail "FOREIGN_BUSINESS quarantine landed — this HOLD lote does not apply CFG-04"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-cfg-04-business-fence-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-cfg-04-business-fence-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "cfg-04-business-fence-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("expected_dodo_business_id_landed") is not False:
    fail("expected_dodo_business_id_landed must stay false")
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

say "check-cfg-04-business-fence-prep: CLEAN — expectedDodoBusinessId HOLD; overlay remasure not in this gate."
exit 0
