#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# agent-surfaces-census.sh — AGT-01: qué dan HOY las 19 superficies del patrón Anthropic/Codex.
#
# ⛔ POR QUÉ UN GUION Y NO UN DOCUMENTO. La fila pide un censo, y un censo escrito a mano envejece
#    pareciendo vigente — la lección que este repositorio ratificó hoy como convención («una cifra
#    describe el tamaño de un problema; sólo una lista lo identifica»). Esto se re-deriva del código
#    en cada corrida, así que la respuesta de AGT-01 no puede quedar desfasada sin que se note.
#
# ⚠ ES UN PARSEO, Y LO DICE. Lee el bloque `sdk.Descriptor{…}` de cada conector y resuelve las
#    constantes de sus claves de configuración. No ejecuta el binario. Dos límites medidos al
#    escribirlo, y los dos importan:
#
#    1. Un `Title:` buscado en el FICHERO ENTERO devuelve el primero que aparezca, que a menudo es
#       el texto de un mensaje y no el del descriptor: `claude` salía como «Agent SDK permission
#       mode» en vez de «Claude Code (OTEL + hooks)». Por eso se acota al bloque.
#    2. Las claves de configuración se declaran como CONSTANTES (`{Key: cfgEnableGRPC}`), así que un
#       barrido por literales cuenta CERO y concluye que el conector no configura nada. Se resuelven
#       contra los `const x = "…"` del propio paquete.
#
# Salida: una fila por superficie · rc 0 · 2 NO HE PODIDO MIRAR.
set -uo pipefail
RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ/connectors" 2>/dev/null || {
	echo "agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ/connectors" >&2
	exit 2
}

python3 - "$@" <<'PY'
import re, glob, sys, os

# ⛔ ESTA LISTA ES UN ALCANCE DELIBERADO, NO UN DESCUIDO — y por eso lleva la guarda de abajo.
#    `connectors/` tiene ~140 directorios; aquí sólo están las superficies del PATRÓN de agentes de
#    código. Enumerarlas todas convertiría el censo en un inventario y dejaría de contestar su
#    pregunta. Lo que sí puede pudrirse en silencio es el criterio: esta lista se fijó cuando el
#    TIER 1 era Claude, y la orden de del 2026-08-17 subió **Grok Build/xAI** a TIER 1.
#    Durante un día el censo contestó sin ellos: `connectors/grok` existía —«Grok Build
#    (governance)», TypeSource— y el censo decía CERO. Un instrumento que no ve a un proveedor de
#    primera clase no está midiendo el patrón, está midiendo la lista.
SUP = ['agentsmd','claude','claude-api','claude-apps-gateway','claude-batch','claude-compliance',
       'claude-config','claude-console','claude-managed-agents','claude-projects','claude-routines',
       'claude-wif','codex','codex-managed-config','cowork','cowork-analytics','grok','managedsettings',
       'mcp','mcpb','xai']

# ── GUARDA: el censo sigue a la tabla de tiers del canon, no al revés ──────────────────────
# Si sube un proveedor a TIER 1 y nadie extiende SUP, el censo contesta como si no existiera
# —que es exactamente lo que pasó con Grok Build—. Esto lo convierte en un rojo explícito.
# Falla CERRADO: si no puede leer la tabla, sale 2 («no he podido mirar»), nunca 0.
_canon = os.path.join(os.path.dirname(os.getcwd()), 'docs', 'ai-context', 'CANON-OPERATIVO.md')
try:
    _txt = open(_canon, encoding='utf8').read()
except OSError as e:
    print(f'agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: no se pudo leer el canon ({e})', file=sys.stderr)
    raise SystemExit(2)
_fila = [l for l in _txt.split('\n') if l.startswith('| **1** |')]
if len(_fila) != 1:
    print(f'agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: la fila TIER 1 del canon aparece '
          f'{len(_fila)} veces, esperaba 1', file=sys.stderr)
    raise SystemExit(2)
# Cada grupo en negrita es UN proveedor; basta que UNA de sus palabras case el prefijo de una
# superficie. «Anthropic / Claude» lo cubre `claude`; «Grok Build / xAI», `grok` o `xai`.
# La celda 2 es la de proveedores; la 1 es el número del tier, que también va en negrita —lo
# aprendí en rojo: la primera versión reclamó que «el canon pone en TIER 1 a ['1']».
_celdas = _fila[0].split('|')
if len(_celdas) < 4:
    print('agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: la fila TIER 1 no tiene tres celdas',
          file=sys.stderr)
    raise SystemExit(2)
_grupos = re.findall(r'\*\*([^*]+)\*\*', _celdas[2])
if not _grupos:
    print('agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: la fila TIER 1 no tiene proveedores en negrita',
          file=sys.stderr)
    raise SystemExit(2)
_sin = []
for _g in _grupos:
    _palabras = [w for w in re.findall(r'[a-z0-9]+', _g.lower()) if len(w) >= 3]
    if not any(sup.split('-')[0] == w or sup == w for w in _palabras for sup in SUP):
        _sin.append(_g.strip())
if _sin:
    print(f'agent-surfaces-census: ⛔ el canon pone en TIER 1 a {_sin} y el censo no tiene ninguna '
          f'superficie suya — extiende SUP o corrige el canon', file=sys.stderr)
    raise SystemExit(1)

faltan = [s for s in SUP if not os.path.isdir(s)]
if faltan:
    print(f'agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: faltan {faltan}', file=sys.stderr)
    raise SystemExit(2)

filas = []
for s in SUP:
    txt = ''
    for f in glob.glob(f'{s}/**/*.go', recursive=True):
        if f.endswith('_test.go'):
            continue
        txt += open(f, encoding='utf8', errors='replace').read() + '\n'
    consts = dict(re.findall(r'^\s*(\w+)\s*=\s*"([^"]*)"', txt, re.M))
    bloque = ''
    m = re.search(r'sdk\.Descriptor\{(.*?)\n\t\}', txt, re.S)
    if m:
        bloque = m.group(1)
    titulo = (re.search(r'Title:\s*"([^"]*)"', bloque) or [None, ''])[1]
    tipo = (re.search(r'Type:\s*sdk\.(\w+)', bloque) or [None, ''])[1]
    claves = sorted({consts.get(k, k.strip('"'))
                     for k in re.findall(r'\{Key:\s*(\w+|"[^"]*")', txt)})
    filas.append((s, tipo, titulo, claves))

# CONTROL POSITIVO: si NINGUNA superficie declara título, el parseo no ha medido nada — un censo de
# diecinueve vacíos se lee como «no configuran nada» en vez de «no supe leerlo».
if not any(t for _, _, t, _ in filas):
    print(f'agent-surfaces-census: ⛔ NO HE PODIDO MIRAR: cero títulos leídos en {len(filas)} '
          f'superficies', file=sys.stderr)
    raise SystemExit(2)

print(f'agent-surfaces-census: {len(filas)} superficies del patrón · '
      f'{sum(len(k) for _, _, _, k in filas)} campos de configuración declarados')
print()
print(f"  {'superficie':24} {'tipo':11} {'cfg':>3}  título")
for s, t, ti, ks in filas:
    print(f'  {s:24} {t:11} {len(ks):3}  {ti[:56]}')
if '--fields' in sys.argv:
    print()
    for s, _, _, ks in filas:
        if ks:
            print(f'  {s}:')
            for k in ks:
                print(f'    {k}')
PY
