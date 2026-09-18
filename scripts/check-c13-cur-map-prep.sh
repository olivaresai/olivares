#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# C13-cur unique leftover unique vs #960 (original OPEN; map already
# on origin/main). 0 CLEAN · 1 finding · 2 could not look.
#
# ⛔ THIS GATE PINNED THE OLD SOURCE AND THE COUNT 27 TWICE, AND BOTH PINS WERE WRONG BY 2026-09-03.
# It required `map source == an internal design note (not shipped)` and `len(entries) == 27` and
# `entry_count == 27` — while the canon sold 30 modules and the authority had moved to
# an internal design note (not shipped) A gate that pins the count of a growing catalog does not detect drift; it
# FORBIDS the catalog from growing, and reports the stale copy as clean until somebody edits the
# gate. The count is gone rather than merely re-pinned: this file is a lote record, not a generated
# projection, so any number kept here would be hand-maintained and would drift again.

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
# curated public export drops both ON PURPOSE; in the full source tree the same absence is a broken
# checkout. The only honest discriminator is the marker the curation pipeline writes into
# the exported tree and never tracks in the full source tree (same one check-public-counts.sh reads).
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

# ── the canonical rendering decides ──────────────────────────────────────────────────────────────
#
# It runs only when the inputs are present in this tree: in a curated public export the wrapper
# answers NOT APPLICABLE (0) and the SCOPED branch above has already returned.
ERR="$(mktemp "${TMPDIR:-/tmp}/c13cur.XXXXXX")" || cannot "cannot create a scratch file"
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
	say "check-c13-cur-map-prep: COULD NOT LOOK — la derivación salió con un código que su contrato no define ($rc):" >&2
	cat "$ERR" >&2 || true
	exit 2
	;;
esac
if [ "$rc" -eq 2 ]; then
	say "check-c13-cur-map-prep: COULD NOT LOOK — the canon derivation did not run:" >&2
	cat "$ERR" >&2 || true
	exit 2
fi
if [ "$rc" -eq 1 ]; then
	say "check-c13-cur-map-prep: FAIL — a projection differs from the canon derivation:" >&2
	cat "$ERR" >&2 || true
	exit 1
fi
DERIVED=1
if grep -qx 'MODULE-CATALOG-NOT-APPLICABLE' "$ERR.out" 2>/dev/null; then
	DERIVED=0
fi

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

import re

if mp.get("schema") != "module-slug-package/v2":
    fail("the map is %r, not the generated module-slug-package/v2" % mp.get("schema"))
if mp.get("source") != "design/PRICING-CANON.md":
    fail("the map's source is %r; the authority is the pricing canon" % mp.get("source"))
if not re.fullmatch(r"[0-9a-f]{64}", mp.get("canon_sha256") or ""):
    fail("the map carries no 64-hex canon_sha256")
entries = mp.get("entries")
if not isinstance(entries, list) or not entries:
    fail("the map has no rows; a map that resolves nothing is not published")
for row in entries:
    for field in ("slug", "pack", "package"):
        if not row.get(field):
            fail("row %r is missing %s" % (row, field))

if data.get("schema") != "c13-cur-map-prep/v2":
    fail("unknown schema %r (v1 pinned entry_count and is retired)" % data.get("schema"))
if data.get("source") != "design/PRICING-CANON.md":
    fail("the prep record still names a source that is not the canon: %r" % data.get("source"))
if data.get("map_published") is not True:
    fail("map_published must stay true")
if "entry_count" in data:
    fail("entry_count is back in the prep record; a count kept outside the generator is a pin that "
         "will drift, which is exactly what this repair removed")
if data.get("remainder_applied") is not False:
    fail("remainder_applied must stay false")
if data.get("overlay_remeasured_in_this_gate") is not False:
    fail("overlay remasure leaked into this hub-safe gate")
ev = data.get("evidence")
if not isinstance(ev, dict):
    fail("the evidence block is missing; the lote's hand-measured object id lives there")
hub = ev.get("hub") or ""
if not re.fullmatch(r"[0-9a-f]{40}", hub):
    fail("evidence.hub is not 40-hex")
for k in ("u_f", "u_d"):
    if ev.get(k) != "UNKNOWN":
        fail("%s must stay UNKNOWN until somebody measures it" % k)

# PRINTED, never compared.
print("json-ok — observed %d rows (an observation; no literal is compared)" % len(entries))
PY

if [ "$DERIVED" -eq 1 ]; then
	say "check-c13-cur-map-prep: CLEAN — the map is the canon derivation; #960 not copied; no count pinned; overlay remasure not in this gate."
else
	# 0, but it SAYS what it could not compare: a scoped verdict is not a CLEAN one.
	say "check-c13-cur-map-prep: SCOPED — the canon derivation is NOT APPLICABLE in this tree (commercial/commerce-lint absent); only the structural checks ran."
fi
exit 0
