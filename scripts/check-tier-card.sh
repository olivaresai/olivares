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

# ⛔ SIN LA DERIVACIÓN, ESTE GATE DEJA QUE UN DOCUMENTO LEGITIME UNA INFRAENTREGA. Medido por el
# contraste `sol max` el 2026-09-03 (F06): con el HOLD esperado definido como `canon_slugs - sold`,
# basta borrar `content-firewall` del mapa y añadir `hold-slug: content-firewall` para que el gate
# quede limpio. La versión anterior, que fijaba los dos nombres históricos, rechazaba ese caso.
#
# Permitir un HOLD futuro es política válida —el canon crece—, pero entonces ese documento se
# convierte en una SEGUNDA autoridad capaz de justificar que algo comprado no se entregue. La cura
# es preguntar primero a la única autoridad: si el mapa ya no es la derivación del canon, no hay
# HOLD que lo arregle.
ERRTC="$(mktemp "${TMPDIR:-/tmp}/tiercard.XXXXXX")" || cannot "no puedo crear un fichero de trabajo"
trap 'rm -f "$ERRTC" "$ERRTC.out"' EXIT
set +e
[ -r "$ROOT/scripts/module-catalog-go.sh" ] || cannot "falta scripts/module-catalog-go.sh: sin el envoltorio del derivador no hay con qué comparar (un 127 no es un veredicto)"
bash "$ROOT/scripts/module-catalog-go.sh" check >"$ERRTC.out" 2>"$ERRTC"
rctc=$?
set -e
case "$rctc" in
0 | 1 | 2) ;;
*) say "check-tier-card: COULD NOT LOOK — la derivación salió con un código que su contrato no define ($rctc)" >&2; exit 2 ;;
esac
if [ "$rctc" -eq 2 ]; then
	say "check-tier-card: COULD NOT LOOK — la derivación del canon no pudo correr:" >&2
	cat "$ERRTC" >&2 || true
	exit 2
fi
if [ "$rctc" -eq 1 ]; then
	say "check-tier-card: FAIL — el mapa vendido no es la derivación del canon, así que ningún HOLD lo justifica:" >&2
	cat "$ERRTC" >&2 || true
	exit 1
fi

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

# ⛔ THE EXPECTED HOLD SET IS DERIVED, NOT PINNED. Until 2026-09-03 this compared the doc against
# the literal {caeptransmit, circuit-breaker}, which is the same disease as the 27/25 literals
# removed from the C13 gates: a pinned set does not detect drift, it FORBIDS the tree from
# changing and calls the cure a regression. Measured that day: the canon assigned both
# (`modules_assigned_2026_08_08`, decision_status decided), the sold map became a derivation of the
# canon, both slugs appeared in it — and the HOLD became vacuous while three gates still demanded
# its two names.
#
# The property that actually matters: a canon slug the sold map does not carry MUST be a declared
# HOLD, and a declared HOLD must be exactly such a slug. Today that set is empty and the doc names
# none. If a future canon assigns a module the map cannot carry, the doc has to name it — and the
# gate says so without anybody editing this file.
hold_doc = open("design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md", encoding="utf-8").read()
hold = set(re.findall(r"^hold-slug:\s*([a-z0-9-]+)\s*$", hold_doc, flags=re.M))

canon_slugs = {s for slugs in canon.values() for s in slugs}
want_hold = {s for s in canon_slugs if s not in sold}

missing_decl = sorted(want_hold - hold)
if missing_decl:
    print("canon slugs absent from the sold map and NOT declared as a packaging HOLD:", file=sys.stderr)
    for s in missing_decl:
        print("  " + s, file=sys.stderr)
    sys.exit(1)

stale = sorted(hold - want_hold)
if stale:
    print("declared packaging HOLD that is no longer a hole (the sold map carries it, or the "
          "canon no longer assigns it) — a HOLD whose condition is cured is theatre:", file=sys.stderr)
    for s in stale:
        print("  " + s, file=sys.stderr)
    sys.exit(1)

print(
    f"ok {len(canon_slugs)} canon slugs, {len(sold)} sold, "
    f"HOLD holes={','.join(sorted(want_hold)) or '(none)'}"
)
sys.exit(0)
PY
rc=$?
[ "$rc" -eq 2 ] && cannot "could not compare the two tables"
[ "$rc" -ne 0 ] && fail "tier card drifted from the sold slug map (or HOLD list)"
say "check-tier-card: CLEAN — shipping slugs are sold; Appendix A HOLDs stay named holes."
exit 0
