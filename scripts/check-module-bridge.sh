#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-module-bridge.sh — the VOCABULARIO table does not CONTRADICT the canon-derived map.
# Three answers: 0 clean · 1 contradiction · 2 could not look.
#
# Until 2026-09-03 this gate required exact equality with the dated
# `an internal design note (not shipped)` table. The canon offered 30 modules but
# that table measured 27; pending row 4 already acknowledged missing Enterprise
# modules for self_hosted.enterprise. Every new canonical module forced a manual
# rewrite of an older measurement.
# Check noncontradiction: shared slugs map to the same package; no table slug may
# vanish from the derived map. Print additional map slugs without changing the verdict.
# `commerce-lint -module-catalog` derivation guarantees completeness, not a manual mirror.

set -euo pipefail

say() { printf '%s\n' "$*"; }
fail() { say "check-module-bridge: FAIL — $*" >&2; exit 1; }
cannot() { say "check-module-bridge: COULD NOT LOOK — $*" >&2; exit 2; }

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

VOC="$ROOT/design/VOCABULARIO-MODULOS-2026-08-08.md"
JSON="$ROOT/commercial/module-slug-package.json"
[ -r "$VOC" ] || cannot "cannot read $VOC"
[ -r "$JSON" ] || cannot "cannot read $JSON"

command -v python3 >/dev/null || cannot "no python3"

# ⛔ Y LA COMPLETITUD LA GARANTIZA LA DERIVACIÓN, NO ESTE FICHERO — sin esto, la sustitución
# comprobaba MENOS que el pin que retiró.
#
# La versión anterior exigía igualdad exacta con la tabla del VOCABULARIO, así que un slug INVENTADO
# en el mapa salía rojo aquí. Al pasar a «no contradicción» —necesario, porque el canon crece y esa
# tabla es una medida fechada— ese caso pasaba a REPORTARSE como crecimiento. Lo cazó el propio
# banco de este gate en la pata 277 de un push: `FAIL invented slug stayed CLEAN`.
#
# La cura no es volver al pin: es preguntar a la AUTORIDAD. Un slug que el canon no vende hace que
# el mapa deje de ser la derivación, y eso es exactamente lo que `-module-catalog=check` mide.
ERR="$(mktemp "${TMPDIR:-/tmp}/modbridge.XXXXXX")" || cannot "cannot create a scratch file"
trap 'rm -f "$ERR" "$ERR.out"' EXIT
set +e
[ -r "$ROOT/scripts/module-catalog-go.sh" ] || cannot "missing scripts/module-catalog-go.sh: the derivation wrapper is required for comparison (exit 127 is not a result)"
bash "$ROOT/scripts/module-catalog-go.sh" check >"$ERR.out" 2>"$ERR"
rcd=$?
set -e
# ⛔ UN rc DESCONOCIDO NO ES ÉXITO. Medido por el contraste `sol max`: con un binario inyectado que
# devuelve 125 —o matado, que da 128+señal— estas ramas sólo miraban 1 y 2, así que «distinto de 1 y
# 2» caía en la rama de derivación correcta y el gate anunciaba CLEAN. La tercera respuesta se
# reclama por defecto: todo lo que no sea 0, 1 o 2 es «no pude mirar».
case "$rcd" in
0 | 1 | 2) ;;
*)
	say "check-module-bridge: COULD NOT LOOK — derivation returned an exit code its contract does not define ($rcd):" >&2
	cat "$ERR" >&2 || true
	exit 2
	;;
esac
if [ "$rcd" -eq 2 ]; then
	say "check-module-bridge: COULD NOT LOOK — the canon derivation did not run:" >&2
	cat "$ERR" >&2 || true
	exit 2
fi
if [ "$rcd" -eq 1 ]; then
	say "check-module-bridge: FAIL — the map is not the canon derivation:" >&2
	cat "$ERR" >&2 || true
	exit 1
fi
DERIVED=1
if grep -qx 'MODULE-CATALOG-NOT-APPLICABLE' "$ERR.out" 2>/dev/null; then
	DERIVED=0
fi

python3 - "$VOC" "$JSON" <<'PY'
import json, re, sys

voc_path, json_path = sys.argv[1], sys.argv[2]
try:
    text = open(voc_path, encoding="utf-8").read()
    data = json.load(open(json_path, encoding="utf-8"))
except OSError as e:
    print(f"unreadable: {e}", file=sys.stderr)
    sys.exit(2)

# Rows of the measured table: | `slug` | `enterprise/...` |
row = re.compile(
    r"^\|\s*`([^`]+)`\s*\|\s*`([^`]+)`[^|]*\|",
    re.M,
)
# Only the section that names sold ids (skip other tables).
start = text.find("| id vendido |")
if start < 0:
    print("VOCABULARIO has no 'id vendido' table", file=sys.stderr)
    sys.exit(2)
chunk = text[start:]
end = chunk.find("\n> ###")
if end > 0:
    chunk = chunk[:end]
voc = {}
for m in row.finditer(chunk):
    slug, pkg = m.group(1), m.group(2)
    if slug == "id vendido":
        continue
    voc[slug] = pkg.split()[0]  # drop trailing (`groupmap.go`)

entries = data.get("entries")
if not isinstance(entries, list) or not entries:
    print("JSON entries missing", file=sys.stderr)
    sys.exit(1)
js = {}
for e in entries:
    s, p = e.get("slug"), e.get("package")
    if not s or not p:
        print(f"incomplete entry {e!r}", file=sys.stderr)
        sys.exit(1)
    if s in js:
        print(f"duplicate slug {s}", file=sys.stderr)
        sys.exit(1)
    js[s] = p

drift = []
grown = []
for s in sorted(set(voc) | set(js)):
    if s not in voc:
        # The canon sells something the 2026-08-08 measurement did not carry. That is the catalog
        # growing, which is the normal direction, and it is REPORTED rather than judged.
        grown.append(s)
    elif s not in js:
        drift.append(f"VOCABULARIO measured {s} as sold and the canon-derived map does not carry it")
    elif voc[s] != js[s]:
        drift.append(f"{s}: the map says {js[s]!r} and the VOCABULARIO measurement says {voc[s]!r}")

# Non-bijective packages must stay named, not "fixed" into 1:1.
from collections import Counter
pkgs = Counter(js.values())
shared = {p: n for p, n in pkgs.items() if n > 1}
if "enterprise/federation" not in shared or "enterprise/wormretention" not in shared:
    print("expected federation and wormretention to serve two slugs each", file=sys.stderr)
    sys.exit(1)

if drift:
    for d in drift:
        print(d, file=sys.stderr)
    sys.exit(1)
print(f"ok {len(js)} slugs, {len(pkgs)} packages, shared={sorted(shared)}; "
      f"beyond the 2026-08-08 measurement: {grown or 'none'}")
sys.exit(0)
PY
rc=$?
if [ "$rc" -eq 2 ]; then
  cannot "parser could not read the table or the JSON"
fi
if [ "$rc" -ne 0 ]; then
  fail "the canon-derived map CONTRADICTS the 2026-08-08 VOCABULARIO measurement"
fi
if [ "$DERIVED" -eq 1 ]; then
	say "check-module-bridge: CLEAN — the map IS the canon derivation and does not contradict the measured VOCABULARIO table."
else
	# 0, pero DICE lo que no pudo comparar: sin el derivador esto sólo mide no-contradicción con una
	# tabla fechada, que no detecta un slug que el canon no vende.
	say "check-module-bridge: SCOPED — the canon derivation is NOT APPLICABLE in this tree (commercial/commerce-lint absent); only non-contradiction with the 2026-08-08 table was checked."
fi
exit 0
