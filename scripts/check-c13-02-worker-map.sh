#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-c13-02-worker-map.sh — the Worker's copy of the slug→package map equals the SINGLE canonical
# rendering. 0 CLEAN · 1 finding · 2 could not look.
#
# ⛔ WHAT THIS GATE USED TO DO, AND WHY IT WAS GREEN OVER A REAL DEFECT.
#
# It compared the two copies TO EACH OTHER and accepted any length ≥ 20. Measured on 2026-09-03:
# both files hashed to d0862a62… — byte-identical, and both carrying 27 rows over 25 packages while
# the canon sold 30 over 28. Two equally stale copies agree perfectly, so the gate said CLEAN about
# a map missing `circuit-breaker`, `caeptransmit` and `credential-minter`.
#
# The `>= 20` floor is the same defect in miniature: a threshold whose only job is to be survivable
# tells you nothing when it is met.
#
# ⇒ Both copies are now compared against ONE rendering derived from the canon, which is the only
# comparison that can fail when both drift together. Equality between the copies is still checked —
# it is now a consequence rather than the whole verdict, and it is the case a hand edit to one copy
# produces.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-02-worker-map: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-02-worker-map: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

SRC="${OLIVARES_C1302_SRC:-commercial/module-slug-package.json}"
COPY="${OLIVARES_C1302_COPY:-commercial/license-worker/src/catalog/module-slug-package.json}"
TS="${OLIVARES_C1302_TS:-commercial/license-worker/src/catalog/slug-package.ts}"

[ -f "$SRC" ] || cannot "missing C13-02 source $SRC"
[ -f "$COPY" ] || cannot "missing Worker copy $COPY"
[ -f "$TS" ] || cannot "missing $TS"
command -v python3 >/dev/null || cannot "no python3"

# ── the canonical rendering decides ──────────────────────────────────────────────────────────────
#
# The wrapper answers NOT APPLICABLE (0) in a curated public tree where commercial/ was exported
# away; it answers 2 when it could not derive. Both are propagated exactly as they arrive: turning
# a 2 into a 1 would claim a finding nobody measured, and turning it into a 0 would be the silent
# green this gate is being repaired for.
# ⛔ THE SCRATCH FILE GOES TO TMPDIR, NEVER INTO $ROOT. A gate that drops a file in the working tree
# is caught by `check-tree-untouched` mid-push and reported as "a gate modified the working tree",
# which sends the reader hunting for someone else's residue.
ERR="$(mktemp "${TMPDIR:-/tmp}/c1302-worker.XXXXXX")" || cannot "cannot create a scratch file"
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
	say "check-c13-02-worker-map: COULD NOT LOOK — la derivación salió con un código que su contrato no define ($rc):" >&2
	cat "$ERR" >&2 || true
	exit 2
	;;
esac
if [ "$rc" -eq 2 ]; then
	say "check-c13-02-worker-map: COULD NOT LOOK — the canon derivation did not run:" >&2
	cat "$ERR" >&2 || true
	exit 2
fi
if [ "$rc" -eq 1 ]; then
	say "check-c13-02-worker-map: FAIL — a projection differs from the canon derivation:" >&2
	cat "$ERR" >&2 || true
	exit 1
fi
DERIVED=1
if grep -qx 'MODULE-CATALOG-NOT-APPLICABLE' "$ERR.out" 2>/dev/null; then
	DERIVED=0
fi

# ── and the two copies must still be the same bytes ──────────────────────────────────────────────
if ! cmp -s "$SRC" "$COPY"; then
	fail "the Worker copy is not byte-identical to $SRC; they are written from ONE rendering, so a difference means one of them was edited by hand"
fi

python3 - "$SRC" <<'PY' || fail "the source map is not the generated schema"
import json, sys

with open(sys.argv[1], encoding="utf-8") as fh:
    a = json.load(fh)

# The SCHEMA and the SOURCE are asserted; the row COUNT is only printed. A gate that pinned the
# count is what this repair removes: the day the canon sells one more module, a pinned count calls
# the truth a regression.
if a.get("schema") != "module-slug-package/v2":
    raise SystemExit("schema is %r, not the generated module-slug-package/v2" % a.get("schema"))
if a.get("source") != "design/PRICING-CANON.md":
    raise SystemExit("source is %r; the authority is the pricing canon" % a.get("source"))
sha = a.get("canon_sha256") or ""
if len(sha) != 64 or any(c not in "0123456789abcdef" for c in sha):
    raise SystemExit("canon_sha256 is not a 64-hex digest")
entries = a.get("entries")
if not isinstance(entries, list) or not entries:
    raise SystemExit("entries is empty; a map with no rows resolves nothing")
for row in entries:
    for field in ("slug", "pack", "package"):
        if not row.get(field):
            raise SystemExit("row %r is missing %s" % (row, field))
print("observed %d rows (an observation; this gate does not compare it)" % len(entries))
PY

grep -q 'packageForSlug' "$TS" || fail "$TS does not export packageForSlug"
grep -q 'packForSlug' "$TS" || fail "$TS does not export packForSlug; the pack travels with the package now"
grep -q 'module-slug-package.json' "$TS" || fail "$TS does not import the map"

if [ "$DERIVED" -eq 1 ]; then
	say "check-c13-02-worker-map: CLEAN — both copies equal ONE canon-derived rendering."
else
	# 0, but it SAYS what it could not compare: a scoped verdict is not a CLEAN one.
	say "check-c13-02-worker-map: SCOPED — the canon derivation is NOT APPLICABLE in this tree (commercial/commerce-lint absent); only the structural checks ran."
fi
exit 0
