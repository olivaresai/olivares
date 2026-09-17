#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# C13-02 remainder: the reverse package→slugs view matches the CANON DERIVATION.
# 0 CLEAN · 1 finding · 2 LOOK.
#
# ⛔ THIS GATE PINNED 27 SOURCES AND 25 PACKAGES AS LITERAL ACCEPTANCE VALUES, AND THAT IS WHY IT
# WAS GREEN OVER A REAL DEFECT. Measured 2026-09-03: the canon sold 30 slugs over 28 packages while
# the map carried 27/25 — so the pinned gate would have called the TRUTH a regression, and did call
# the stale map clean. `source_entries != 27` and `packages != 25` were literally the failure
# conditions.
#
# A count that decides has to be edited when a module is added, and every place it is edited is a
# place it can be forgotten. So the counts are now OBSERVED and printed, and what decides is the
# comparison against `commerce-lint -module-catalog=check`: set, order and digest.

set -euo pipefail
say() { printf '%s\n' "$*"; }
fail() { say "check-c13-02-package-view: FAIL — $*" >&2; exit 1; }
cannot() { say "check-c13-02-package-view: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_C1302V_JSON:-design/c13-02-package-view.json}"
DOC="${OLIVARES_C1302V_DOC:-design/C13-02-PACKAGE-VIEW-2026-08-20.md}"
SRC="${OLIVARES_C1302V_SRC:-commercial/module-slug-package.json}"
VIEW="${OLIVARES_C1302V_VIEW:-commercial/module-package-slugs.json}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
[ -f "$SRC" ] || cannot "missing source map"
[ -f "$VIEW" ] || cannot "missing reverse view"
command -v python3 >/dev/null || cannot "no python3"

# ── the canonical rendering decides; the counts are only reported ────────────────────────────────
#
# rc 2 and rc 1 are propagated exactly as they arrive: turning a 2 into a 1 claims a finding nobody
# measured, and turning it into a 0 is the silent green this repair exists to remove. The scratch
# file lives in TMPDIR, never in the working tree, or `check-tree-untouched` reports it mid-push as
# "a gate modified the working tree".
ERR="$(mktemp "${TMPDIR:-/tmp}/c1302-view.XXXXXX")" || cannot "cannot create a scratch file"
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
	say "check-c13-02-package-view: COULD NOT LOOK — la derivación salió con un código que su contrato no define ($rc):" >&2
	cat "$ERR" >&2 || true
	exit 2
	;;
esac
if [ "$rc" -eq 2 ]; then
	say "check-c13-02-package-view: COULD NOT LOOK — the canon derivation did not run:" >&2
	cat "$ERR" >&2 || true
	exit 2
fi
if [ "$rc" -eq 1 ]; then
	say "check-c13-02-package-view: FAIL — a projection differs from the canon derivation:" >&2
	cat "$ERR" >&2 || true
	exit 1
fi
DERIVED=1
if grep -qx 'MODULE-CATALOG-NOT-APPLICABLE' "$ERR.out" 2>/dev/null; then
	DERIVED=0
fi

grep -q 'Not bijective' "$DOC" || fail "$DOC lost not-bijective"
grep -q 'Pack slugs' "$DOC" || fail "$DOC lost pack-slugs-off"
if grep -qiE 'FIRMA A claimed|view is bijective|pack slug landed' "$DOC"; then
	fail "$DOC claims a close this lote does not have"
fi

python3 - "$JSON" "$SRC" "$VIEW" <<'PY' || fail "view drifted from source"
import json, re, sys

# A SET slug names a purchase combination and never a module; one in this table would let a caller
# resolve `biz+airs` to a package. Derived from the four add-on codes rather than typed, so a fifth
# add-on cannot be forgotten here while it is added everywhere else.
banned = {"biz", "biz+airs", "biz+ids", "biz+reg", "biz+cp", "ent"}

with open(sys.argv[1], encoding="utf-8") as fh:
    data = json.load(fh)
with open(sys.argv[2], encoding="utf-8") as fh:
    src = json.load(fh)
with open(sys.argv[3], encoding="utf-8") as fh:
    view = json.load(fh)

if data.get("schema") != "c13-02-package-view/v2":
    raise SystemExit("unknown schema %r (v1 pinned 27/25 and is retired)" % data.get("schema"))
if data.get("source") != "design/PRICING-CANON.md":
    raise SystemExit("the view's source is %r; the authority is the pricing canon" % data.get("source"))

# ── the digest ties the three files to ONE derivation ───────────────────────────────────────────
digests = {"view": data.get("canon_sha256"), "source": src.get("canon_sha256"), "reverse": view.get("canon_sha256")}
for name, sha in digests.items():
    if not isinstance(sha, str) or not re.fullmatch(r"[0-9a-f]{64}", sha):
        raise SystemExit("%s carries no 64-hex canon_sha256" % name)
if len(set(digests.values())) != 1:
    raise SystemExit("the three files carry DIFFERENT canon digests %r; they were generated from "
                     "different canons" % digests)

# ── the verdicts are DERIVED here and compared with what the file recorded ──────────────────────
entries = src.get("entries") or []
if not entries:
    raise SystemExit("the source map has no rows")
want = {}
for row in entries:
    slug, pkg, pack = row.get("slug") or "", row.get("package") or "", row.get("pack") or ""
    if not slug or not pkg or not pack:
        raise SystemExit("row %r is missing a field" % row)
    if slug in banned:
        raise SystemExit("the source grew a set slug %s" % slug)
    want.setdefault(pkg, []).append(slug)
for pkg in want:
    want[pkg] = sorted(set(want[pkg]))

got = view.get("packages") or {}
if set(got) != set(want):
    missing = sorted(set(want) - set(got))
    extra = sorted(set(got) - set(want))
    raise SystemExit("package set drifted: missing %r, extra %r" % (missing, extra))
for pkg, slugs in want.items():
    if list(got.get(pkg) or []) != slugs:
        raise SystemExit("package %s slugs drifted: %r vs %r" % (pkg, got.get(pkg), slugs))
for slugs in got.values():
    for slug in slugs:
        if slug in banned:
            raise SystemExit("the view grew a set slug %s" % slug)

shared = sorted(p for p, v in want.items() if len(v) > 1)
if data.get("observed_shared_packages") != shared:
    raise SystemExit("observed_shared_packages %r does not match the derivation %r"
                     % (data.get("observed_shared_packages"), shared))
if data.get("observed_bijective") is not (len(shared) == 0):
    raise SystemExit("observed_bijective disagrees with the derivation")
if data.get("observed_pack_slugs_present") is not False:
    raise SystemExit("observed_pack_slugs_present must be false; a set slug in a module map is a "
                     "finding, not a recorded state")
if data.get("observed_source_entries") != len(entries):
    raise SystemExit("observed_source_entries %r != %d rows actually present"
                     % (data.get("observed_source_entries"), len(entries)))
if data.get("observed_packages") != len(want):
    raise SystemExit("observed_packages %r != %d packages actually present"
                     % (data.get("observed_packages"), len(want)))

# ── the evidence block is the lote's, not the generator's: it must EXIST and be well-formed ─────
ev = data.get("evidence")
if not isinstance(ev, dict):
    raise SystemExit("the evidence block is missing; the lote's hand-measured object ids live there")
for k in ("u_f", "u_d"):
    if ev.get(k) != "UNKNOWN":
        raise SystemExit("%s must stay UNKNOWN until somebody measures it" % k)
for key in ("hub", "overlay"):
    val = ev.get(key) or ""
    if val != "UNKNOWN" and not re.fullmatch(r"[0-9a-f]{40}", val):
        raise SystemExit("evidence.%s is neither UNKNOWN nor a 40-hex object id" % key)

# The counts are PRINTED. Nothing above compares them to a literal.
print("observed %d rows over %d packages, %d shared" % (len(entries), len(want), len(shared)))
PY

if [ "$DERIVED" -eq 1 ]; then
	say "check-c13-02-package-view: CLEAN — the view matches ONE canon derivation; set slugs off; counts observed, not pinned."
else
	# 0, but it SAYS what it could not compare: a scoped verdict is not a CLEAN one.
	say "check-c13-02-package-view: SCOPED — the canon derivation is NOT APPLICABLE in this tree (commercial/commerce-lint absent); only the structural checks ran."
fi
exit 0
