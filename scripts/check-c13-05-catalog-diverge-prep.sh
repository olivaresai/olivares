#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-05 unique leftover unique vs overlay-gated
# check-c13-05-overlay-catalog-diverge.sh: hub-safe half so
# lint:addon-sets does not LOOK 2 without OLIVARES_ENT_DIR.
# Three answers: 0 CLEAN · 1 finding · 2 could not look.
#
# ⛔ THIS GATE REQUIRED THE DIVERGENCE TO EXIST, AND REQUIRED THE HOLD TO NAME TWO SLUGS. Both were
# pins, and on 2026-09-03 both were cured: the sold map became a derivation of the pricing canon and
# grew to the 30 modules the overlay catalog already had, so `overlay_matches_sold: false` and
# `hold == {caeptransmit, circuit-breaker}` were asserting a state that no longer exists. A gate that
# demands a defect stay present goes red when somebody fixes it — measured the same day:
# check-c13-05-overlay-catalog-diverge.sh failed with "overlay catalog equals sold map; HOLD is
# stale", which is the cure being reported as a regression.
#
# What it asserts now is the property, not the state: the sold map IS the canon derivation, the
# packaging HOLD set is DERIVED (canon slugs the map does not carry — today none), and this gate
# still refuses to say anything about the overlay, which it does not read.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-05-catalog-diverge-prep: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-05-catalog-diverge-prep: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1305P_JSON:-design/c13-05-catalog-diverge-prep-2026-08-20.json}"
DOC="${OLIVARES_C1305P_DOC:-design/C13-05-CATALOG-DIVERGE-PREP-2026-08-20.md}"
SOLD="${OLIVARES_C1305P_SOLD:-commercial/module-slug-package.json}"
HOLD="${OLIVARES_C1305P_HOLD:-design/HOLD-AIRS-AR-CRITERIOS-2026-08-18.md}"

[ -r "$JSON" ] || cannot "missing $JSON"
[ -r "$DOC" ] || cannot "missing $DOC"
[ -r "$SOLD" ] || cannot "missing $SOLD"
[ -r "$HOLD" ] || cannot "missing $HOLD"
command -v python3 >/dev/null || cannot "no python3"

# ── the canonical rendering decides the source-tree half ─────────────────────────────────────────────────
#
# rc 2 and rc 1 are propagated exactly as they arrive: turning a 2 into a 1 claims a finding nobody
# measured, and turning it into a 0 is the silent green this repair removes. The scratch files live
# in TMPDIR, never in the working tree, or `check-tree-untouched` reports them mid-push as "a gate
# modified the working tree".
ERR="$(mktemp "${TMPDIR:-/tmp}/c1305.XXXXXX")" || cannot "cannot create a scratch file"
trap 'rm -f "$ERR" "$ERR.out"' EXIT
set +e
[ -r "$ROOT/scripts/module-catalog-go.sh" ] || cannot "falta scripts/module-catalog-go.sh: sin el envoltorio del derivador no hay con qué comparar (un 127 no es un veredicto)"
bash "$ROOT/scripts/module-catalog-go.sh" check >"$ERR.out" 2>"$ERR"
rc=$?
set -e
# ⛔ UN rc DESCONOCIDO NO ES ÉXITO. Medido por el contraste `sol max`: con un binario inyectado que
# devuelve 125 —o matado, que da 128+señal— estas ramas sólo miraban 1 y 2, así que «distinto de 1 y
# 2» caía en la rama de derivación correcta y el gate anunciaba CLEAN. La tercera respuesta se
# reclama por defecto: todo lo que no sea 0, 1 o 2 es «no pude mirar».
case "$rc" in
0 | 1 | 2) ;;
*)
	say "check-c13-05-catalog-diverge-prep: COULD NOT LOOK — la derivación salió con un código que su contrato no define ($rc):" >&2
	cat "$ERR" >&2 || true
	exit 2
	;;
esac
if [ "$rc" -eq 2 ]; then
	say "check-c13-05-catalog-diverge-prep: COULD NOT LOOK — the canon derivation did not run:" >&2
	cat "$ERR" >&2 || true
	exit 2
fi
if [ "$rc" -eq 1 ]; then
	say "check-c13-05-catalog-diverge-prep: FAIL — a projection differs from the canon derivation:" >&2
	cat "$ERR" >&2 || true
	exit 1
fi
DERIVED=1
if grep -qx 'MODULE-CATALOG-NOT-APPLICABLE' "$ERR.out" 2>/dev/null; then
	DERIVED=0
fi

grep -F -q 'Unique leftover unique vs `check-c13-05-overlay-catalog-diverge.sh`' "$DOC" \
  || fail "prepare doc lost uniqueness vs overlay-gated diverge check"
# The doc must keep SAYING which half it does not look at. That sentence is the whole reason a
# hub-safe gate is allowed to exist next to an overlay-gated one, and losing it would turn a scoped
# verdict into an unscoped-looking one.
grep -q 'no lee el overlay' "$DOC" \
  || fail "prepare doc lost the sentence that says it does not read the overlay"
grep -q 'HOLD' "$DOC" || fail "prepare doc lost the HOLD history"
if grep -qiE 'C13-05 (closed|cerrado)|FIRMA A claimed' "$DOC"; then
  fail "prepare doc claims a close this hub-safe gate cannot certify"
fi

python3 - "$JSON" "$SOLD" "$HOLD" <<'PY' || exit $?
import json, re, sys

def fail(msg):
    print(f"check-c13-05-catalog-diverge-prep: FAIL — {msg}", file=sys.stderr)
    sys.exit(1)

def cannot(msg):
    print(f"check-c13-05-catalog-diverge-prep: COULD NOT LOOK — {msg}", file=sys.stderr)
    sys.exit(2)

try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
    sold = json.load(open(sys.argv[2], encoding="utf-8"))
    hold_doc = open(sys.argv[3], encoding="utf-8").read()
except Exception as e:
    cannot(f"inputs not readable: {e}")

if data.get("schema") != "c13-05-catalog-diverge-prep/v2":
    fail("unknown schema %r (v1 pinned the divergence and the two HOLD names; both are retired)"
         % data.get("schema"))
if data.get("source") != "design/PRICING-CANON.md":
    fail("the record still names a source that is not the canon: %r" % data.get("source"))
if data.get("sold_map_is_canon_derived") is not True:
    fail("sold_map_is_canon_derived must be true; that is what this lote delivered")
if data.get("packaging_hold_retired") is not True:
    fail("packaging_hold_retired must be true")
if data.get("hub_tier_card_is_not_overlay") is not True:
    fail("hub_tier_card_is_not_overlay must stay true")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
if "overlay_matches_sold" in data:
    fail("overlay_matches_sold is back; a hub-safe gate must not carry a verdict about a subject "
         "it never reads")
ev = data.get("evidence")
if not isinstance(ev, dict):
    fail("the evidence block is missing")
for key in ("overlay_main_sha", "hub"):
    val = ev.get(key) or ""
    if not re.fullmatch(r"[0-9a-f]{40}", val):
        fail("evidence.%s is not 40-hex" % key)
for k in ("u_f", "u_d"):
    if ev.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN until somebody measures it" % k)

try:
    entries = sold["entries"]
    slugs = {e["slug"] for e in entries}
except Exception as e:
    cannot(f"sold map is not readable: {e}")

# The map must BE the derivation, and carry its digest. This is the "hash/slug-pack equality" the
# plan asks for on the source-tree side; the byte comparison itself is done by
# `commerce-lint -module-catalog=check`, run above.
if sold.get("schema") != "module-slug-package/v2":
    fail("the sold map is %r, not the generated module-slug-package/v2" % sold.get("schema"))
if sold.get("source") != "design/PRICING-CANON.md":
    fail("the sold map's source is %r; the authority is the pricing canon" % sold.get("source"))
if not re.fullmatch(r"[0-9a-f]{64}", sold.get("canon_sha256") or ""):
    fail("the sold map carries no 64-hex canon_sha256")
for row in entries:
    for field in ("slug", "pack", "package"):
        if not row.get(field):
            fail("sold row %r is missing %s" % (row, field))
if "iso42001" not in slugs:
    fail("sold map lost iso42001")

# ⛔ THE HOLD SET IS DERIVED, NOT PINNED — the same cure applied to the 27/25 literals. A packaging
# HOLD is a canon-assigned slug the sold map does not carry; today that set is empty and the doc
# must name none. Naming one that is not a hole is theatre, and the HOLD doc says so itself.
hold = set(re.findall(r"^hold-slug:\s*([a-z0-9-]+)\s*$", hold_doc, flags=re.M))
stale = sorted(s for s in hold if s in slugs)
if stale:
    fail("the HOLD doc still names %s as a packaging hole while the sold map carries it; a HOLD "
         "whose condition is cured is theatre" % ", ".join(stale))

print("json-ok — observed %d sold rows, %d packaging HOLDs (both observations)"
      % (len(entries), len(hold)))
PY

if [ "$DERIVED" -eq 1 ]; then
	say "check-c13-05-catalog-diverge-prep: CLEAN — sold map IS the canon derivation; packaging HOLD derived (none); hub-safe, overlay NOT read here."
else
	# 0, but it SAYS what it could not compare: a scoped verdict is not a CLEAN one.
	say "check-c13-05-catalog-diverge-prep: SCOPED — the canon derivation is NOT APPLICABLE in this tree (commercial/commerce-lint absent); only the structural checks ran."
fi
exit 0
