#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# CFG-15 prepare. The four remainder steps are written. The ceremony
# is not run. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-cfg15-ceremony-prepare: FAIL — $*" >&2; exit 1; }
cannot() { say "check-cfg15-ceremony-prepare: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC=design/CFG-15-CEREMONY-REMAINDER-2026-08-19.md
[ -r "$DOC" ] || cannot "missing $DOC"

grep -q 'Move keys' "$DOC" || fail "CFG-15 lost the move-keys step"
grep -q 'Backup A' "$DOC" || fail "CFG-15 lost backup A"
grep -q 'Backup B' "$DOC" || fail "CFG-15 lost backup B"
grep -q 'Restore + smoke' "$DOC" || fail "CFG-15 lost restore+smoke"
grep -qi 'does not run the ceremony\|NOT executed' "$DOC" \
  || fail "CFG-15 no longer says the ceremony is not run"
# Present-tense closure only. Naming the forbidden claim is not a claim.
if grep -qiE 'ceremony is complete|ceremonia está cerrada|YA-74 is closed|YA-74 está cerrad' "$DOC"; then
  fail "CFG-15 claimed YA-74 is operationally closed"
fi
if grep -qE 'BEGIN (OPENSSH |RSA |EC )?PRIVATE KEY' "$DOC"; then
  fail "CFG-15 contains private key material"
fi

say "check-cfg15-ceremony-prepare: CLEAN — four remainder steps written; ceremony not run."
exit 0
