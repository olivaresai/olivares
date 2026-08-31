#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-08 unique leftover unique vs check-c05-08-legal-entities.sh
# (named on main, CHECK not in lint:addon-sets) and unique leftover
# unique vs #1391 / #1405. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-08-legal-entities-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-08-legal-entities-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0508P_JSON:-design/c05-08-legal-entities-prep-2026-08-20.json}"
DOC="${OLIVARES_C0508P_DOC:-design/C05-08-LEGAL-ENTITIES-PREP-2026-08-20.md}"
MIG="${OLIVARES_C0508P_MIG:-commercial/commerce/migrations/001_entities_orders.up.sql}"
COMMERCE="${OLIVARES_C0508P_COMMERCE:-commercial/commerce}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$MIG" ] || cannot "missing $MIG"
[ -d "$COMMERCE" ] || cannot "missing $COMMERCE"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c05-08-legal-entities.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original legal-entities check"
grep -F -q 'Unique leftover unique vs `#1391`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1391"
grep -F -q 'Unique leftover unique vs `#1405`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1405"
grep -q 'NO ELEGIDO' "$DOC" || fail "prepare doc lost NO ELEGIDO"
grep -q 'Operator insert' "$DOC" && grep -q 'Dodo as evidence' "$DOC" \
  && grep -q 'Dedicated verifier' "$DOC" \
  || fail "prepare doc no longer names the three options"
if grep -qiE 'elegido:|aplicamos la opción|the webhook writes verified|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a decision or a writer this lote does not have"
fi

grep -q "verification_state IN ('unverified', 'pending', 'verified', 'rejected')" "$MIG" \
  || fail "CHECK no longer lists unverified/pending/verified/rejected"
grep -q "DEFAULT 'unverified'" "$MIG" \
  || fail "DEFAULT is no longer unverified"
grep -q 'require_verified_entity' "$MIG" \
  || fail "lost require_verified_entity — checkout would sell to an unverified row"
grep -q "IS DISTINCT FROM 'verified'" "$MIG" \
  || fail "trigger no longer refuses a non-verified entity"

if command grep -R --include='*.go' --exclude='*_test.go' -n \
  'INSERT INTO commerce.legal_entities' "$COMMERCE" >/dev/null 2>&1; then
  fail "a production .go file INSERTs commerce.legal_entities — C05-08 does not add a writer"
fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-08-legal-entities-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-08-legal-entities-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

want = ["unverified", "pending", "verified", "rejected"]
if data.get("schema") != "c05-08-legal-entities-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("four_states") != want:
    fail("four_states drifted")
if data.get("default_unverified") is not True:
    fail("default_unverified must stay true")
if data.get("require_verified_entity") is not True:
    fail("require_verified_entity must stay true")
if data.get("production_insert") is not False:
    fail("production_insert must stay false")
if data.get("option_chosen") is not False:
    fail("option_chosen must stay false")
if data.get("no_elegido") is not True:
    fail("no_elegido must stay true")
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

say "check-c05-08-legal-entities-prep: CLEAN — four-state CHECK; trigger still deny-closed; no production INSERT; options presented, none chosen."
exit 0
