#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02-09 prepare. Live R2 sizes are written. The 266 MB figure stays
# cited. The ~4.8 GB figure is another matrix. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-09-cost: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-09-cost: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC=design/C02-09-MATRIZ-COSTE-2026-08-19.md
[ -r "$DOC" ] || cannot "missing $DOC"

grep -Fq '4412' "$DOC" \
  || fail "C02-09 lost the live sandbox total (4412 B)"
grep -Fq '4096' "$DOC" \
  || fail "C02-09 lost the live stub size (4096 B)"
grep -Fq '2026-08-19T02:36Z' "$DOC" \
  || fail "C02-09 lost the remasure timestamp"
grep -qiE 'other matrix|otra matriz' "$DOC" \
  || fail "C02-09 no longer marks 4,8 GB as another matrix"

# Present-tense only. "does not weigh a 266 MB object" is the
# constraint, not a claim that R2 holds 266 MB.
# Sin la tuberia final a grep -q: bajo pipefail devuelve 141 CUANDO ACIERTA.
_hits0="$(grep -iE 'live (tarball|object|r2).{0,40}266|266.{0,40}(re-?weighed|remasured live|on r2)' "$DOC" \
  | grep -viE 'does not|not re-weigh|cited, not' || true)"
if [ -n "$_hits0" ]; then
  fail "C02-09 claimed 266 MB was remasured live on R2 — it was not"
fi
# Sin la tuberia final a grep -q: bajo pipefail devuelve 141 CUANDO ACIERTA.
_hits1="$(grep -iE '4,?8 GB.{0,40}(this matrix|current matrix|esta matriz)|this matrix.{0,40}4,?8' "$DOC" \
  | grep -viE 'not |no |other|otra' || true)"
if [ -n "$_hits1" ]; then
  fail "C02-09 treated 4,8 GB as this matrix"
fi
if grep -qiE 'prod.{0,30}(has|holds|contains).{0,20}(tarball|artifact|266)|production R2 .{0,20}(has|holds) [1-9]' "$DOC"; then
  fail "C02-09 claimed prod R2 has artifacts — live count is 0"
fi

say "check-c02-09-cost: CLEAN — live 4412 B; 266 MB cited; 4,8 GB other matrix."
exit 0
