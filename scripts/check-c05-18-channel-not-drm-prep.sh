#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C05-18 unique leftover unique vs check-c05-18-channel-not-drm.sh
# (LOOK 2 on origin/main without the 2026-08-19 census doc) and unique
# leftover unique vs #1061. 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c05-18-channel-not-drm-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c05-18-channel-not-drm-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C0518P_JSON:-design/c05-18-channel-not-drm-prep-2026-08-20.json}"
DOC="${OLIVARES_C0518P_DOC:-design/C05-18-CHANNEL-NOT-DRM-PREP-2026-08-20.md}"
GATE="${OLIVARES_C0518P_GATE:-commercial/license-worker/src/download/gate.ts}"
TOKENS="${OLIVARES_C0518P_TOKENS:-commercial/license-worker/src/download/tokens.ts}"
STRAT="${OLIVARES_C0518P_STRAT:-design/ENTERPRISE-PROTECTION-AND-TRUST-STRATEGY.md}"

for f in "$JSON" "$DOC" "$GATE" "$TOKENS" "$STRAT"; do
  [ -r "$f" ] || cannot "missing $f"
done
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `check-c05-18-channel-not-drm.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original channel-not-drm check"
grep -F -q 'Unique leftover unique vs `#1061`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #1061"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Channel-not-DRM wording not landed' "$DOC" \
  || fail "prepare doc lost channel-not-DRM HOLD"
grep -F -q 'Does not write docs-site' "$DOC" \
  || fail "prepare doc lost docs-site HOLD"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|channel-not-DRM landed' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

if grep -q 'not DRM' "$GATE"; then
  fail "gate.ts gained not DRM — this HOLD lote does not apply C05-18"
fi
if grep -q 'not consumed' "$GATE"; then
  fail "gate.ts gained not consumed — this HOLD lote does not apply C05-18"
fi
grep -q 'watermarking por cliente' "$STRAT" \
  || fail "strategy no longer presents watermarking por cliente — remasure drifted"

python3 - "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c05-18-channel-not-drm-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c05-18-channel-not-drm-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c05-18-channel-not-drm-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("channel_not_drm_landed") is not False:
    fail("channel_not_drm_landed must stay false")
if data.get("watermarking_still_presented") is not True:
    fail("watermarking_still_presented must stay true")
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

say "check-c05-18-channel-not-drm-prep: CLEAN — channel-not-DRM HOLD; watermarking still presented; overlay remasure not in this gate."
exit 0
