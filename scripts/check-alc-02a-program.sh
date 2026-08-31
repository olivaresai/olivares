#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ALC-02a unique leftover unique vs #956: the reliability PROGRAM is on
# main. This CHECK pins it. Does not apply AWS.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-alc-02a-program: FAIL — $*" >&2; exit 1; }
cannot() { say "check-alc-02a-program: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

PROG="${OLIVARES_ALC02A_PROG:-design/PROGRAMA-ALC-FIABILIDAD-CLOUD-2026-08-18.md}"
DOC="${OLIVARES_ALC02A_DOC:-design/ALC-02A-PROGRAM-PREP-2026-08-20.md}"

[ -r "$PROG" ] || cannot "missing $PROG"
[ -r "$DOC" ] || cannot "missing $DOC"

grep -q 'Unique leftover unique vs `#956`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #956"
grep -q 'Roadmap declarado, no construido' "$DOC" \
  || fail "prepare doc lost Roadmap declarado, no construido"

grep -q 'Roadmap declarado, no construido' "$PROG" \
  || fail "program lost Roadmap declarado, no construido"
grep -q 'ALC-02-F1' "$PROG" || fail "program lost ALC-02-F1"
grep -q 'ALC-02-F2' "$PROG" || fail "program lost ALC-02-F2"
grep -q 'ALC-02-F3' "$PROG" || fail "program lost ALC-02-F3"
grep -q 'ALC-02-F4' "$PROG" || fail "program lost ALC-02-F4"
grep -Fq 'programa no lanza `tofu apply`' "$PROG" \
  || fail "program lost programa no lanza tofu apply"

say "check-alc-02a-program: CLEAN — reliability program on main; F1..F4 named; not applied."
exit 0
