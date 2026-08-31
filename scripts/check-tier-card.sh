#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-tier-card.sh — C13-05. Canon add-on-set slugs (scripts/addon-sets.sh)
# must be in the sold slug map, except the HOLDs C13-07 names. Three answers.
# Does NOT invent packaging: a HOLD stays a named hole, never a sold row.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-tier-card: FAIL — $*" >&2; exit 1; }
cannot() { say "check-tier-card: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"
# Sanctioned absence vs broken checkout — see the same guard in check-c13-cur-map-prep.sh
# and the marker's definition in check-public-counts.sh. This gate compares the PRICING
# canon against the sold slug map; both live in roots the public export curates out, so in
# an exported tree its inputs are absent BY DESIGN and rc 2 would reject the push.
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
for f in scripts/addon-sets.sh commercial/module-slug-package.json \
         design/PRICING-CANON.md design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md; do
  [ -r "$f" ] && continue
  [ "$PUBLIC_EXPORT" -eq 1 ] || cannot "missing $f"
  curated_out="$curated_out $f"
done
if [ -n "$curated_out" ]; then
  say "check-tier-card: SCOPED — public export; hub-only input(s) curated out:$curated_out"
  exit 0
fi
[ -x scripts/addon-sets.sh ] || cannot "addon-sets.sh not executable"

command -v python3 >/dev/null || cannot "no python3"

sets="$(bash scripts/addon-sets.sh design/PRICING-CANON.md)" || cannot "addon-sets.sh failed"
[ -n "$sets" ] || cannot "addon-sets.sh produced no rows"

python3 - "$sets" <<'PY'
import json, re, sys

rows = sys.argv[1].splitlines()
canon = {}
for line in rows:
    if not line.strip():
        continue
    parts = line.split("\t")
    if len(parts) != 2:
        print(f"bad addon-sets row {line!r}", file=sys.stderr)
        sys.exit(2)
    code, slug = parts
    canon.setdefault(code, set()).add(slug)

data = json.load(open("commercial/module-slug-package.json", encoding="utf-8"))
sold = {e["slug"] for e in data["entries"]}

# Named HOLDs only. Appendix A of PRICING-CANON assigns these two to airs;
# they SHIP in the artifact (addon-sets includes them) and VOCABULARIO does
# not package them. C13-07 writes the AR-1..AR-4 bar. Adding them here as
# sold rows would invent packaging (C09-10).
hold_doc = open("design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md", encoding="utf-8").read()
hold = set(re.findall(r"^hold-slug:\s*([a-z0-9-]+)\s*$", hold_doc, flags=re.M))
if hold != {"caeptransmit", "circuit-breaker"}:
    print(
        "HOLD doc must name exactly caeptransmit and circuit-breaker "
        f"(got {sorted(hold)})",
        file=sys.stderr,
    )
    sys.exit(1)

canon_slugs = {s for slugs in canon.values() for s in slugs}
unexpected = sorted(s for s in canon_slugs if s not in sold and s not in hold)
if unexpected:
    print("canon slugs absent from the sold map and not a declared HOLD:", file=sys.stderr)
    for s in unexpected:
        print("  " + s, file=sys.stderr)
    sys.exit(1)

stale = sorted(s for s in hold if s not in canon_slugs)
if stale:
    print("declared HOLD no longer in addon-sets (stale C13-07):", file=sys.stderr)
    for s in stale:
        print("  " + s, file=sys.stderr)
    sys.exit(1)

named = sorted(s for s in hold if s in canon_slugs and s not in sold)
print(
    f"ok {len(canon_slugs)} canon slugs, {len(sold)} sold, "
    f"HOLD holes={','.join(named)}"
)
sys.exit(0)
PY
rc=$?
[ "$rc" -eq 2 ] && cannot "could not compare the two tables"
[ "$rc" -ne 0 ] && fail "tier card drifted from the sold slug map (or HOLD list)"
say "check-tier-card: CLEAN — shipping slugs are sold; Appendix A HOLDs stay named holes."
exit 0
