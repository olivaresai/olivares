#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-02 unique leftover unique vs check-c05-first-owner.sh and unique
# leftover unique vs #1380. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-02-mailer-required-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-02-mailer-required-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0502P_JSON:-design/c05-02-mailer-required-prep-2026-08-20.json}"
DOC="${OLIVARES_C0502P_DOC:-design/C05-02-MAILER-REQUIRED-PREP-2026-08-20.md}"
MGR="${OLIVARES_C0502P_MGR:-cloud/control-plane/internal/tenant/manager.go}"
BOOT="${OLIVARES_C0502P_BOOT:-cloud/control-plane/cmd/cloud-cp/main.go}"
TEST="${OLIVARES_C0502P_TEST:-cloud/control-plane/internal/tenant/manager_test.go}"

for f in "$JSON" "$DOC" "$MGR" "$BOOT" "$TEST"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c05-first-owner.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original first-owner check"
grep -F -q 'Unique leftover unique vs `#1380`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1380"
grep -F -q 'mailer not wired refuses Provision' "$DOC" \
  || fail "prepare doc lost deny-closed HOLD"
if grep -qiE 'FIRMA A claimed|bytes are real|stub gone' "$DOC"; then
  fail "prepare doc claims a close this lote does not have"
fi

grep -q 'mailer not wired' "$MGR" \
  || fail "inviteFirstOwner no longer refuses a nil mailer"
grep -q 'WithFirstOwnerMailer' "$BOOT" \
  || fail "boot lost WithFirstOwnerMailer"
grep -q 'SendEmail' "$BOOT" \
  || fail "boot lost Resend SendEmail"
if grep -q 'buyer cannot enter until C05-02 mailer is attached' "$MGR"; then
  fail "inviteFirstOwner still logs-and-continues when mailer is nil"
fi
grep -q 'TestProvision_RefusesWhenMailerNotWired' "$TEST" \
  || fail "lost the refuse-without-mailer test"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-02-mailer-required-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-02-mailer-required-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-02-mailer-required-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("mailer_required") is not True:
    fail("mailer_required must stay true")
if data.get("mailer_wired_in_boot") is not True:
    fail("mailer_wired_in_boot must stay true")
if data.get("create_user_before_mailer") is not False:
    fail("create_user_before_mailer must stay false")
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

say "check-c05-02-mailer-required-prep: CLEAN — mailer required before CreateUser; boot still wires Resend."
exit 0
