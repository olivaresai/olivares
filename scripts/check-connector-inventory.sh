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
# ⛔ LO QUE ESTE GATE **NO** COMPRUEBA, dicho aquí para que su verde no se lea como una bendición
#    del documento entero: el bloque «Summary» lleva **once métricas más** (alias de kind, binarios
#    de plugin, conectores de salida, proveedores de roster, fuentes de contenido…) y este gate
#    **sólo** comprueba «Connector directories». Re-derivar las otras once aquí sería una SEGUNDA
#    implementación de lo que `check-public-counts.sh` ya deriva, y dos copias de un predicado
#    divergen — que es la trampa que `web-bundle-source-digest.sh` documenta con su medida. Si
#    alguna de esas once importa, se deriva UNA vez y se consume, no se copia.
#
# Salida: 0 la tabla cubre el árbol · 1 falta o sobra alguna fila · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-connector-inventory: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
DOC="${OLIVARES_CONNECTOR_INVENTORY:-docs/ai-context/CONNECTORS.md}"
DIR="${OLIVARES_CONNECTOR_DIR:-connectors}"

[ -r "$DOC" ] || {
	echo "check-connector-inventory: ⛔ NO HE PODIDO MIRAR: no se puede leer $DOC" >&2
	exit 2
}
[ -d "$DIR" ] || {
	echo "check-connector-inventory: ⛔ NO HE PODIDO MIRAR: no existe $DIR/" >&2
	exit 2
}

DOC="$DOC" DIR="$DIR" python3 - <<'PY'
import os, re, sys

doc = os.environ["DOC"]
raiz = os.environ["DIR"]

def ciego(msg):
    # «No he podido mirar» NO es «está limpio»: 2, nunca 1 y nunca 0.
    print(f"check-connector-inventory: ⛔ NO HE PODIDO MIRAR: {msg}", file=sys.stderr)
    sys.exit(2)

try:
    texto = open(doc, encoding="utf8").read()
except OSError as exc:
    ciego(f"{doc} ilegible ({exc})")

# La tabla vive bajo «## Truth Table» y termina en el siguiente encabezado de nivel 2. Si el
# documento se reestructura, esto NO adivina: dice que no ha podido mirar.
try:
    i = texto.index("## Truth Table")
except ValueError:
    ciego(f"{doc} no tiene una sección «## Truth Table» — la tabla puede haberse renombrado")
j = texto.find("\n## ", i + 5)
bloque = texto[i:] if j == -1 else texto[i:j]

filas = set(re.findall(r"^\| `([^`]+)` \|", bloque, re.M))
if not filas:
    ciego(f"{doc}: la tabla no tiene ni una fila reconocible — el barrido mediría cero contra todo")

# ⛔ EL DENOMINADOR ES «TIENE CÓDIGO GO», NO «ES UN DIRECTORIO». `connectors/backstage` son
#    plugins TypeScript y NO lleva fila a propósito; es el mismo «−1 non-Go» que
#    `check-public-counts.sh` descuenta al derivar la cifra pública. Exigirle fila haría rojo un
#    árbol correcto, que es la forma más rápida de que un gate se desactive.
dirs, sin_go = set(), set()
try:
    for d in sorted(os.listdir(raiz)):
        ruta = os.path.join(raiz, d)
        if not os.path.isdir(ruta):
            continue
        tiene_go = any(
            f.endswith(".go")
            for _, _, fs in os.walk(ruta)
            for f in fs
        )
        (dirs if tiene_go else sin_go).add(d)
except OSError as exc:
    ciego(f"no se pudo recorrer {raiz}/ ({exc})")

if not dirs:
    ciego(f"cero conectores con código Go bajo {raiz}/ — el árbol no es el que este gate espera")

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
        f"check-connector-inventory: OK — {len(filas)} fila(s) cubren los {len(dirs)} conector(es) "
        f"con Go ({len(sin_go)} sin Go, exento(s) a propósito: {', '.join(sorted(sin_go)) or 'ninguno'}). "
        f"⚠ Las otras 11 métricas del bloque Summary NO están cubiertas por este gate."
    )
    sys.exit(0)

for c in faltan:
    print(
        f"check-connector-inventory: ⛔ `{c}` tiene código Go y NO tiene fila en {doc} — "
        f"un conector sin fila desaparece del inventario y no lo ve ningún gate, sólo una persona.",
        file=sys.stderr,
    )
for c in sobran:
    print(
        f"check-connector-inventory: ⛔ {doc} tiene fila para `{c}` y no existe {raiz}/{c} — "
        f"la tabla nombra algo que el árbol no tiene.",
        file=sys.stderr,
    )
if cifra_mal:
    print(
        f"check-connector-inventory: ⛔ el resumen dice «Connector directories | {cifra_mal[0]}» y "
        f"hay {cifra_mal[1]} — la cifra se re-deriva, no se transcribe.",
        file=sys.stderr,
    )
sys.exit(1)
PY
