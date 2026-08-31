#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C02-08 prepare. goreleaser v2 admits blobs.ids (OSS). The overlay
# is not applied here. Three answers.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c02-08-blobs-ids: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c02-08-blobs-ids: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

DOC=design/C02-08-GORELEASER-BLOBS-IDS-2026-08-19.md
[ -r "$DOC" ] || cannot "missing $DOC"

grep -q 'blobs\[\]\.ids\|blobs.ids\|`ids:` on `blobs:`' "$DOC" \
  || fail "C02-08 lost the blobs.ids field name"
grep -Fq '**Yes.**' "$DOC" \
  || fail "C02-08 no longer records that goreleaser v2 admits ids"
# Present-tense only. "Does not claim the overlay already publishes"
# is the constraint, not a claim that C02-03 landed.
# Sin la tuberia final a grep -q: bajo pipefail devuelve 141 CUANDO ACIERTA.
_hits0="$(grep -iE 'overlay already publishes|ent:\.goreleaser already (has|publishes)|we applied blobs|blobs (are|is) already live' "$DOC" \
  | grep -viE 'does not|not claim|not edit|not upload' || true)"
if [ -n "$_hits0" ]; then
  fail "C02-08 claimed the overlay already publishes per set — that is C02-03"
fi
# Pro is only the if: filter. A claim that ids itself needs Pro is false.
# The live note names Pro for `if:`, so require the present-tense "is Pro".
# Sin la tuberia final a grep -q: bajo pipefail devuelve 141 CUANDO ACIERTA.
_hits1="$(grep -iE 'blobs\.ids is (pro|paid)|ids:.*(is|requires|needs) (pro|paid)' "$DOC" \
  | grep -viE 'not |if:' || true)"
if [ -n "$_hits1" ]; then
  fail "C02-08 claimed blobs.ids is Pro — the primary source lists it as OSS"
fi
grep -q 'goreleaser.com/customization/publish/blob' "$DOC" \
  || fail "C02-08 lost the primary-source URL"

say "check-c02-08-blobs-ids: CLEAN — v2 admits blobs.ids; overlay not applied."
exit 0
