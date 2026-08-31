#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-console-route-docs.sh — a repository gate, la mitad de CONSOLA. ¿Cuántas rutas de la consola
# aparecen en la documentación pública?
#
# ⛔ LO QUE ESTE GATE MIDE, DICHO SIN ADORNO: que la RUTA aparece literalmente en alguna página
# inglesa de `docs-site`. Eso NO es «documentada» — una ruta puede salir en una tabla y no tener
# ni una línea que explique la pantalla. Contar cadenas dice qué se MENCIONA, no qué se explica, y
# este fichero no va a fingir lo contrario. Sirve para lo que sirve: una ruta que NADIE nombra no
# puede estar documentada, así que el conjunto que este gate marca es un límite superior honesto.
#
# ⛔ Y UNA SEGUNDA COSA QUE ESTE GATE **NO** DICE, para que su numero no se lea de mas: mide el
# `docs-site/` DE ESTE REPO, que a dia de hoy **no esta publicado en ningun dominio** — CFG-16,
# medido el 2026-08-18: `docs.olivares.ai` no resuelve. Hay una SEGUNDA superficie viva en el
# repo web (`olivares.ai/docs/reference/...`, 65 URLs en su sitemap) que este gate no mira, y
# ese reparto esta pendiente de una decision de producto. Asi que «38 de 58 aparecen en la
# documentacion» significa **en la fuente de este repo**, NO «un usuario puede encontrarlas».
# Cuando la decision se tome, este gate apunta a la superficie que gane — no antes, porque
# elegirla aqui seria decidirla por la puerta de atras.
#
# ⛔ Y NO ENUMERA: el denominador sale de `web/src/features/route-census.json`, que ya es el censo
# derivado que usan otros gates. Un gate con su propia lista envejece en silencio.
#
# Suelo (`OLIVARES_ROUTE_DOC_FLOOR`, por defecto el nivel de hoy): la cobertura puede subir y
# NUNCA bajar. No se sube el suelo para acomodar una ruta nueva sin mención: eso es exactamente
# lo que el ratchet existe para impedir.
set -euo pipefail
ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

CENSO="web/src/features/route-census.json"
DOCS="docs-site/src/content/docs"
SUELO="${OLIVARES_ROUTE_DOC_FLOOR:-38}"

[ -f "$CENSO" ] || { echo "check-console-route-docs: NO HE PODIDO MIRAR: falta $CENSO" >&2; exit 2; }
[ -d "$DOCS" ]  || { echo "check-console-route-docs: NO HE PODIDO MIRAR: falta $DOCS" >&2; exit 2; }

SALIDA="$(python3 - "$CENSO" "$DOCS" <<'PY'
import json, os, re, sys
censo, docs = sys.argv[1], sys.argv[2]
d = json.load(open(censo, encoding="utf-8"))
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
N_RUTAS="$(printf '%s\n' "$SALIDA" | sed -n 1p)"
N_PAGS="$(printf '%s\n' "$SALIDA" | sed -n 2p)"
N_CON="$(printf '%s\n' "$SALIDA" | sed -n 3p)"
SIN="$(printf '%s\n' "$SALIDA" | tail -n +4)"

# CONTROL POSITIVO: un censo vacío o unas páginas vacías no aprueban nada.
if [ "${N_RUTAS:-0}" -lt 5 ] || [ "${N_PAGS:-0}" -lt 5 ]; then
	echo "check-console-route-docs: NO HE PODIDO MIRAR: censo=${N_RUTAS:-0} rutas, ${N_PAGS:-0} página(s)." >&2
	echo "                          Un denominador vacío haría que cualquier numerador pareciera un pleno." >&2
	exit 2
fi

echo "check-console-route-docs: ${N_CON} de ${N_RUTAS} ruta(s) de consola aparecen en las ${N_PAGS} páginas inglesas (suelo ${SUELO})"
if [ "$N_CON" -lt "$SUELO" ]; then
	echo "check-console-route-docs: ⛔ LA COBERTURA BAJA: ${N_CON} < ${SUELO}. Las rutas sin mención:" >&2
	printf '%s\n' "$SIN" | sed 's/^/    /' >&2
	echo "                          El suelo NO se baja para acomodar una ruta nueva sin documentar." >&2
	exit 1
fi
if [ -n "$SIN" ]; then
	echo "check-console-route-docs: sin mención todavía ($((N_RUTAS - N_CON))), nombradas para que nadie las descubra tarde:"
	printf '%s\n' "$SIN" | sed 's/^/    /'
fi
echo "check-console-route-docs: OK"
