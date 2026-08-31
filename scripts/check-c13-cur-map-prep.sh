#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-cur unique leftover unique vs #960 (original OPEN; map already
# on origin/main). 0 CLEAN · 1 finding · 2 could not look.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-cur-map-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-cur-map-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C13CURP_JSON:-design/c13-cur-map-prep-2026-08-20.json}"
DOC="${OLIVARES_C13CURP_DOC:-design/C13-CUR-MAP-PREP-2026-08-20.md}"
MAP="${OLIVARES_C13CURP_MAP:-commercial/module-slug-package.json}"
BRIDGE="${OLIVARES_C13CURP_BRIDGE:-scripts/check-module-bridge.sh}"

# Sanctioned absence vs broken checkout. This gate reads design/ and commercial/, and the
# curated public export drops both ON PURPOSE; in the hub the same absence is a broken
# checkout. The only honest discriminator is the marker the curation pipeline writes into
# the exported tree and never tracks in the hub (same one check-public-counts.sh reads).
# Measured 2026-08-31 from an exported tree with `git init`: without this, rc 2 — and the
# canon's fail-closed rule turns "I could not look" into a rejected push in public.
# NOT `[ -f .olivares-public-export ]`: hub-leg.sh:29-40 records that a bare marker is a
# PASSWORD anybody can type — a stray `cp`, a half-finished export — and adversarial review
# X-07 replaced it with two pieces of evidence a copy cannot fabricate (the sentence the
# generator stamps, AND no hub-only path present). Reuse that classifier instead of keeping
# a second, weaker copy of the criterion here.
PUBLIC_EXPORT=0
if [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
  PUBLIC_EXPORT=1
fi

curated_out=""
for f in "$JSON" "$DOC" "$MAP" "$BRIDGE"; do
  [ -r "$f" ] && continue
  [ "$PUBLIC_EXPORT" -eq 1 ] || cannot "missing $f"
  curated_out="$curated_out $f"
done
if [ -n "$curated_out" ]; then
  # 0, but it SAYS what it did not look at: a scoped verdict is not a CLEAN one.
  say "check-c13-cur-map-prep: SCOPED — public export; hub-only input(s) curated out:$curated_out"
  exit 0
fi
command -v python3 >/dev/null || cannot "no python3"

grep -F -q 'Unique leftover unique vs `#960`' "$DOC" \
  || fail "prepare doc lost uniqueness vs #960"
grep -F -q 'Unique leftover unique vs `hub-comercio/c13-cur`' "$DOC" \
  || fail "prepare doc lost uniqueness vs original branch"
grep -F -q 'HOLD. NOT APPLIED.' "$DOC" \
  || fail "prepare doc lost HOLD"
grep -F -q 'Does not copy `#960`' "$DOC" \
  || fail "prepare doc lost no-copy HOLD"
grep -F -q 'Map already on origin/main' "$DOC" \
  || fail "prepare doc lost map remasure"
if grep -qiE 'FIRMA A claimed|remainder applied on origin/main|copied #960' "$DOC"; then
  fail "prepare doc claims an application this lote does not have"
fi

python3 - "$MAP" "$JSON" <<'PY' || exit $?
import json, sys

def fail(msg):
    print(f"check-c13-cur-map-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c13-cur-map-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    mp = json.load(open(sys.argv[1], encoding="utf-8"))
    data = json.load(open(sys.argv[2], encoding="utf-8"))
except Exception as e:
    cannot(f"inputs not readable: {e}")

if mp.get("source") != "design/VOCABULARIO-MODULOS-2026-08-08.md":
    fail("map source drifted")
entries = mp.get("entries")
if not isinstance(entries, list) or len(entries) != 27:
    fail("entry_count drifted (want 27, got %r)" % (len(entries) if isinstance(entries, list) else entries,))
if data.get("schema") != "c13-cur-map-prep/v1":
    fail("unknown schema %r" % data.get("schema"))
if data.get("map_published") is not True:
    fail("map_published must stay true")
if data.get("entry_count") != 27:
    fail("json entry_count drifted")
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

say "check-c13-cur-map-prep: CLEAN — map already on origin/main; #960 not copied; overlay remasure not in this gate."
exit 0
