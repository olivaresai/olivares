#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C03-21: lts is not a self-serve channel. Three answers: 0 / 1 / 2.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c03-21-channel-right: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c03-21-channel-right: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
DOC=design/C03-21-CHANNEL-RIGHT-2026-08-19.md
[ -r "$DOC" ] || cannot "missing $DOC"
grep -q 'Channel vs right: present.' "$DOC" \
  || fail "census no longer says channel vs right is present"

MAN=commercial/license-worker/src/download/manifests.ts
GATE=commercial/license-worker/src/download/gate.ts
[ -r "$MAN" ] || cannot "missing $MAN"
[ -r "$GATE" ] || cannot "missing $GATE"

grep -q 'export function channelIsSelfServe' "$MAN" \
  || fail "channelIsSelfServe missing"
grep -q 'SELF_SERVE_CHANNELS' "$MAN" \
  || fail "SELF_SERVE_CHANNELS missing"
grep -q 'channelIsSelfServe(channel)' "$GATE" \
  || fail "serveManifest does not consult channelIsSelfServe"
grep -q 'lts is order-form only' "$GATE" \
  || fail "named lts 403 missing"

TST=commercial/license-worker/test/c03-21-channel-right.test.ts
[ -r "$TST" ] || cannot "missing $TST"
grep -q 'cheapest active licence cannot fetch the lts' "$TST" \
  || fail "billing-hole test missing"

say "check-c03-21-channel-right: CLEAN — lts is not a self-serve channel."
exit 0
