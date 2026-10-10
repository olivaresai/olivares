#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-console-route-docs.sh — a repository gate, console coverage: count public documentation mentions.
#
# Measure literal route mentions in English docs-site pages, not explanations of each
# screen. A table mention alone does not document a route; an unnamed route cannot be
# documented, so the reported set is an upper bound on actual documentation coverage.
#
# The scope is this repository's docs-site source, not user reachability. CFG-16 measured
# 2026-08-18 that docs.olivares.ai did not resolve; the separate live web repository
# had 65 `olivares.ai/docs/reference/...` URLs in its sitemap. The product decision
# about that split was pending: “38 of 58” meant source coverage here. Retarget this
# gate after that decision rather than make the product decision inside the checker.
#
# Derive the denominator from `web/src/features/route-census.json`, shared by other
# gates. `OLIVARES_ROUTE_DOC_FLOOR` defaults to the measured level: coverage may rise
# but must not fall. Do not adjust the floor to excuse a new unmentioned route.
set -euo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

CENSUS="web/src/features/route-census.json"
DOCS="docs-site/src/content/docs"
SUELO="${OLIVARES_ROUTE_DOC_FLOOR:-38}"

[ -f "$CENSUS" ] || { echo "check-console-route-docs: COULD NOT CHECK: missing $CENSUS" >&2; exit 2; }
[ -d "$DOCS" ]  || { echo "check-console-route-docs: COULD NOT CHECK: missing $DOCS" >&2; exit 2; }

OUTPUT="$(python3 - "$CENSUS" "$DOCS" <<'PY'
import json, os, re, sys
census, docs = sys.argv[1], sys.argv[2]
d = json.load(open(census, encoding="utf-8"))
rutas = [p if isinstance(p, str) else p.get("path", "") for p in d.get("paths", [])]
rutas = [r for r in rutas if r]
# Las traducciones NO cuentan: una ruta mencionada solo en la version japonesa no esta
# mencionada en la fuente, y la paridad de idiomas ya tiene su propio gate.
salta = re.compile(r'/(de|es|fr|ja|ru|zh[a-z-]*|2026-06)(/|$)')
paginas = []
for root, _, fs in os.walk(docs):
    if salta.search(root):
        continue
    for f in fs:
        if f.endswith((".md", ".mdx")):
            paginas.append(os.path.join(root, f))
texto = "\n".join(open(p, encoding="utf-8", errors="replace").read() for p in paginas)
con = [r for r in rutas if r in texto]
sin = [r for r in rutas if r not in texto]
print(len(rutas)); print(len(paginas)); print(len(con))
print("\n".join(sin))
PY
)"
N_RUTAS="$(printf '%s\n' "$OUTPUT" | sed -n 1p)"
N_PAGS="$(printf '%s\n' "$OUTPUT" | sed -n 2p)"
N_CON="$(printf '%s\n' "$OUTPUT" | sed -n 3p)"
SIN="$(printf '%s\n' "$OUTPUT" | tail -n +4)"

# CONTROL POSITIVO: un censo vacío o unas páginas vacías no aprueban nada.
if [ "${N_RUTAS:-0}" -lt 5 ] || [ "${N_PAGS:-0}" -lt 5 ]; then
	echo "check-console-route-docs: COULD NOT CHECK: scan=${N_RUTAS:-0} routes, ${N_PAGS:-0} page(s)." >&2
	echo "                          An empty denominator would make any numerator look like full coverage." >&2
	exit 2
fi

echo "check-console-route-docs: ${N_CON} of ${N_RUTAS} console route(s) appear in ${N_PAGS} English pages (minimum ${SUELO})"
if [ "$N_CON" -lt "$SUELO" ]; then
	echo "check-console-route-docs: ⛔ coverage decreased: ${N_CON} < ${SUELO}. Routes without a mention:" >&2
	printf '%s\n' "$SIN" | sed 's/^/    /' >&2
	echo "                          Do not lower the minimum to accommodate a new undocumented route." >&2
	exit 1
fi
if [ -n "$SIN" ]; then
	echo "check-console-route-docs: still unmentioned ($((N_RUTAS - N_CON))), listed for visibility:"
	printf '%s\n' "$SIN" | sed 's/^/    /'
fi
echo "check-console-route-docs: OK"
