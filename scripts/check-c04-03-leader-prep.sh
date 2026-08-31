#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C04-03 unique leftover unique vs check-c04-03-leader.sh (LOOK 2 on
# origin/main without leader.go) and unique leftover unique vs #1053.
# 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c04-03-leader-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c04-03-leader-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0403P_JSON:-design/c04-03-leader-prep-2026-08-20.json}"
DOC="${OLIVARES_C0403P_DOC:-design/C04-03-LEADER-PREP-2026-08-20.md}"
SWEEPER="${OLIVARES_C0403P_SWEEPER:-cloud/control-plane/internal/lifecycle/sweeper.go}"
NOTIFIER="${OLIVARES_C0403P_NOTIFIER:-cloud/control-plane/internal/lifecycle/notifier.go}"
POLLER="${OLIVARES_C0403P_POLLER:-cloud/control-plane/internal/metering/poller.go}"
MAIN="${OLIVARES_C0403P_MAIN:-cloud/control-plane/cmd/cloud-cp/main.go}"
LEADER="${OLIVARES_C0403P_LEADER:-cloud/control-plane/internal/leader/leader.go}"

for f in "$JSON" "$DOC" "$SWEEPER" "$NOTIFIER" "$POLLER" "$MAIN"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c04-03-leader.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original leader check"
grep -F -q 'Unique leftover unique vs `#1053`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1053"
grep -F -q 'NOT APPLIED' "$DOC" \
  || fail "prepare doc lost NOT APPLIED"
grep -F -q 'Does not apply AWS' "$DOC" \
  || fail "prepare doc lost AWS HOLD"
if grep -qiE 'loops fenced|FIRMA A claimed|leader landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

[ ! -e "$LEADER" ] || fail "leader package landed — this HOLD lote does not apply C04-03"
if grep -q 'IsLeader' "$SWEEPER"; then fail "sweeper consults IsLeader — C04-03 must not apply here"; fi
if grep -q 'IsLeader' "$NOTIFIER"; then fail "notifier consults IsLeader — C04-03 must not apply here"; fi
if grep -q 'IsLeader' "$POLLER"; then fail "poller consults IsLeader — C04-03 must not apply here"; fi
if grep -q 'leader.Dial' "$MAIN"; then fail "main dials the elector — C04-03 must not apply here"; fi
if grep -q 'GET /readyz' "$MAIN"; then fail "main serves GET /readyz — C04-03 must not apply here"; fi

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c04-03-leader-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c04-03-leader-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c04-03-leader-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("c04_03_applied") is not False:
    fail("c04_03_applied must stay false")
if data.get("leader_package_present") is not False:
    fail("leader_package_present must stay false")
if data.get("loops_fenced") is not False:
    fail("loops_fenced must stay false")
if data.get("aws_applied") is not False:
    fail("aws_applied must stay false")
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

say "check-c04-03-leader-prep: CLEAN — three loops unfenced; C04-03 not applied; original CHECK stays LOOK 2."
exit 0
