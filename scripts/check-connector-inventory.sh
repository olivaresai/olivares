#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-connector-inventory.sh — la tabla de conectores de `docs/ai-context/CONNECTORS.md` cubre
# TODO conector con código Go, y no nombra ninguno que no exista.
#
# ⛔ POR QUÉ EXISTE, con el caso que lo trajo. El 2026-08-18 aterrizó `connectors/grok` con su
#    código, sus celdas, su registro en `sources.go` y los tres gates de Go en verde — y **sin fila
#    en el inventario**. Ningún gate lo vio: `check-connectors.sh` deriva del CÓDIGO y dice
#    explícitamente que lo hace «rather than trusting docs/ai-context/CONNECTORS.md», que es la
#    decisión correcta para clasificar y deja la tabla sin vigilancia. Lo cazó una persona leyendo.
#
#    Un inventario que sólo revisa una persona no es un inventario: es una foto que envejece. Y
#    envejece hacia el lado peligroso — lo que falta no se ve, mientras que una fila de más chirría.
#
# ⛔ WHAT THIS GATE DOES **NOT** CHECK, stated here so its green is not read as a blessing of
#    the whole document: the "Summary" block carries more metrics, and this gate checks **only**
#    "Connector directories". The metrics derivable from the code (in-process source kind aliases
#    and source directories, plugin binaries, outbound connectors, roster providers, content
#    sources, libraries, integrations, directories without Go) are derived ONCE by
#    `check-public-counts.sh` (check_connectors_context) and compared with that table and with the
#    canonical sentence. Re-deriving them here would be a SECOND implementation, and two copies of
#    a predicate diverge: the trap `web-bundle-source-digest.sh` documents with its measurement.
#    Still unchecked here and there: the no-op Gather list, the source scaffolds, the Git-host
#    write adapter and the unclassified directories.
#
# Salida: 0 la tabla cubre el árbol · 1 falta o sobra alguna fila · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-connector-inventory: ⛔ COULD NOT CHECK: missing $RAIZ" >&2
	exit 2
}
DOC="${OLIVARES_CONNECTOR_INVENTORY:-docs/ai-context/CONNECTORS.md}"
DIR="${OLIVARES_CONNECTOR_DIR:-connectors}"

[ -r "$DOC" ] || {
	echo "check-connector-inventory: ⛔ COULD NOT CHECK: cannot read $DOC" >&2
	exit 2
}
[ -d "$DIR" ] || {
	echo "check-connector-inventory: ⛔ COULD NOT CHECK: missing $DIR/" >&2
	exit 2
}

DOC="$DOC" DIR="$DIR" python3 - <<'PY'
import os, re, sys

doc = os.environ["DOC"]
raiz = os.environ["DIR"]

def ciego(msg):
    # «No he podido mirar» NO es «está limpio»: 2, nunca 1 y nunca 0.
    print(f"check-connector-inventory: ⛔ COULD NOT CHECK: {msg}", file=sys.stderr)
    sys.exit(2)

try:
    texto = open(doc, encoding="utf8").read()
except OSError as exc:
    ciego(f"{doc} unreadable ({exc})")

# La tabla vive bajo «## Truth Table» y termina en el siguiente encabezado de nivel 2. Si el
# documento se reestructura, esto NO adivina: dice que no ha podido mirar.
try:
    i = texto.index("## Truth Table")
except ValueError:
    ciego(f"{doc} has no «## Truth Table» section — the table may have been renamed")
j = texto.find("\n## ", i + 5)
bloque = texto[i:] if j == -1 else texto[i:j]

filas = set(re.findall(r"^\| `([^`]+)` \|", bloque, re.M))
if not filas:
    ciego(f"{doc}: the table has no recognizable rows — the scan would compare everything against zero")

# ⛔ EL DENOMINADOR ES «TIENE CÓDIGO GO», NO «ES UN DIRECTORIO». `connectors/backstage` son
#    plugins TypeScript y NO lleva fila a propósito; es el mismo «−1 non-Go» que
#    `check-public-counts.sh` descuenta al derivar la cifra pública. Exigirle fila haría rojo un
#    árbol correcto, que es la forma más rápida de que un gate se desactive.
# ⛔ EL DENOMINADOR ES «TIENE CÓDIGO GO», NO «ES UN DIRECTORIO». `connectors/backstage` son
#    plugins TypeScript y NO lleva fila a propósito; es el mismo «−1 non-Go» que
#    `check-public-counts.sh` descuenta al derivar la cifra pública. Exigirle fila haría rojo un
#    árbol correcto, que es la forma más rápida de que un gate se desactive.
#
# ⛔ Y POR ESO SE PODAN LOS DIRECTORIOS DE DEPENDENCIAS. Medido el 2026-09-01, con un push muerto
#    a los 22 minutos: un worktree con las dependencias instaladas tiene
#    `connectors/backstage/node_modules/.pnpm/flatted@3.4.2/node_modules/flatted/golang/pkg/
#    flatted/flatted.go` — un `.go` DENTRO de un paquete npm, sin trackear. Sin la poda, backstage
#    «tenía Go», perdía su exención y el gate enrojecía un árbol correcto: el MISMO commit pasaba
#    o moría según si habías corrido `pnpm install`. Un gate cuyo veredicto depende de ficheros
#    que no son del repositorio da respuestas distintas en dos cajas con el mismo árbol.
#    NO se poda `vendor/`: código Go vendorizado bajo un conector ES código Go de ese conector.
VENDOR = {"node_modules", ".git", ".pnpm-store"}

dirs, sin_go = set(), set()
try:
    for d in sorted(os.listdir(raiz)):
        ruta = os.path.join(raiz, d)
        if not os.path.isdir(ruta):
            continue
        tiene_go = False
        for _, subdirs, fs in os.walk(ruta):
            subdirs[:] = [x for x in subdirs if x not in VENDOR]
            if any(f.endswith(".go") for f in fs):
                tiene_go = True
                break
        (dirs if tiene_go else sin_go).add(d)
except OSError as exc:
    ciego(f"could not traverse {raiz}/ ({exc})")

if not dirs:
    ciego(f"no connectors with Go code under {raiz}/ — this tree does not match the expected layout")

faltan = sorted(dirs - filas)
sobran = sorted(filas - dirs - sin_go)

# La cifra del resumen, que es la ÚNICA del bloque «Summary» que este gate cubre (ver cabecera).
total_dirs = len(dirs) + len(sin_go)
m = re.search(r"^\| Connector directories \| (\d+) \|", texto, re.M)
cifra_mal = None
if m and int(m.group(1)) != total_dirs:
    cifra_mal = (int(m.group(1)), total_dirs)

if not faltan and not sobran and cifra_mal is None:
    print(
        f"check-connector-inventory: OK — {len(filas)} row(s) cover the {len(dirs)} connector(s) "
        f"with Go ({len(sin_go)} without Go, intentionally exempt: {', '.join(sorted(sin_go)) or 'none'}). "
        f"The derivable Summary metrics are compared by scripts/check-public-counts.sh; the no-op Gather, scaffold, Git-host and unclassified rows are not checked."
    )
    sys.exit(0)

for c in faltan:
    print(
        f"check-connector-inventory: ⛔ `{c}` contains Go code but has no row in {doc} — "
        f"a connector without a row disappears from the inventory and requires manual discovery.",
        file=sys.stderr,
    )
for c in sobran:
    print(
        f"check-connector-inventory: ⛔ {doc} has a row for `{c}`, but {raiz}/{c} is missing — "
        f"the table names something absent from the tree.",
        file=sys.stderr,
    )
if cifra_mal:
    print(
        f"check-connector-inventory: ⛔ the summary says «Connector directories | {cifra_mal[0]}», but "
        f"there are {cifra_mal[1]}; this count is derived from the tree, not copied.",
        file=sys.stderr,
    )
sys.exit(1)
PY
