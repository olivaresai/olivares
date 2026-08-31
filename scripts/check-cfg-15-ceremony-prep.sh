#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-15 unique leftover unique vs check-cfg-15-ceremony.sh
# (named on main, CHECK not in lint:addon-sets) and unique leftover
# unique vs #1388 / #1401. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg-15-ceremony-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg-15-ceremony-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_CFG15P_JSON:-design/cfg-15-ceremony-prep-2026-08-20.json}"
DOC="${OLIVARES_CFG15P_DOC:-design/CFG-15-CEREMONIA-REMAINDER-PREP-2026-08-20.md}"
REG="${OLIVARES_CFG15P_REG:-design/REGISTRO-DECISIONES-2026-08-01.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$REG" ] || cannot "missing $REG"
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-cfg-15-ceremony.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original ceremony check"
grep -F -q 'Unique leftover unique vs `#1388`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1388"
grep -F -q 'Unique leftover unique vs `#1401`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1401"
grep -F -q 'Private keys NEVER enter this session' "$DOC" \
  || fail "prepare doc lost private-keys HOLD"
if grep -qiE 'the ceremony is complete|ceremonia (está )?cerrada|custody is complete|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims the ceremony is complete"
fi

required=(
  move-private-keys-off-clone
  encrypted-backup-1
  encrypted-backup-2
  restore-from-backup
  smoke-sign-verify
  rev-parse-HEAD-at-ceremony
)
for r in "${required[@]}"; do
  grep -qE "^remainder:[[:space:]]*${r}[[:space:]]*$" "$DOC" \
    || fail "remainder missing: $r"
done

grep -q 'YA-74' "$REG" || fail "REGISTRO lost YA-74"
grep -q 'siguen pendientes' "$REG" || fail "REGISTRO YA-74 lost «siguen pendientes»"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-cfg-15-ceremony-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-cfg-15-ceremony-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

want = [
    "move-private-keys-off-clone",
    "encrypted-backup-1",
    "encrypted-backup-2",
    "restore-from-backup",
    "smoke-sign-verify",
    "rev-parse-HEAD-at-ceremony",
]
if data.get("schema") != "cfg-15-ceremony-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("remainders") != want:
    fail("remainders drifted")
if data.get("ceremony_complete") is not False:
    fail("ceremony_complete must stay false")
if data.get("ya74_pending") is not True:
    fail("ya74_pending must stay true")
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

say "check-cfg-15-ceremony-prep: CLEAN — 6 remainders named; ceremony not claimed complete."
exit 0
