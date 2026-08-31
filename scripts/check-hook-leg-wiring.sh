#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
#
# ¿Toda pata que el gancho LLAMA esta declarada tambien donde se declara?
#
# ⛔ POR QUE EXISTE, con la medida que lo pide: el 2026-08-31 el commit `8f2edda97` añadio
# `lint:mute-pipefail` a las LLAMADAS del gancho y a ningun otro sitio. Consecuencias medidas:
#
#   · `lint:prepush-refclass` —que ES pata del gancho— se puso ROJA, asi que **todo push de toda
#     caja moria ahi**. Estaba en la linea 1735, y ese dia nadie la alcanzo porque otra pata rota
#     (la 915) mataba antes: un rojo temprano es una CORTINA y lo de detras va sin medir.
#   · Los 8 casos que fallaban decian `rc=0` **y no nombraban la causa**. Hicieron falta tres
#     experimentos (el mismo banco sobre el arbol de 6 h antes, media cura, cura entera) para
#     saber que faltaba y donde.
#   · Y el mismo olvido dejo `lint:gate-parity` en rojo por su fila del registro.
#
# **Una pata cableada vive en TRES sitios** —la llamada, el banner que la anuncia y la lista
# declarada del banco— y el defecto es SIEMPRE el mismo: se actualiza uno. Esto no añade una cuarta
# lista: **verifica la coherencia de las tres que ya existen**, en sub-segundo, y NOMBRA la pata y
# el sitio que le falta. El banco ya cubre el caso, pero mal (58 s), tarde (posicion 1735) y mudo.
#
# ⛔ FAIL-CLOSED, y no es adorno: si un extractor deja de casar —porque alguien reformatea el
# gancho o renombra la lista— este gate encontraria CERO incumplimientos y saldria VERDE. Un cero
# por no haber mirado se parece demasiado a un cero por estar limpio, asi que si cualquiera de las
# dos listas sale vacia, esto dice NO HE PODIDO MIRAR y sale 2.
set -euo pipefail

GANCHO="${OLIVARES_WIRING_HOOK:-.githooks/pre-push}"
BANCO="${OLIVARES_WIRING_BENCH:-scripts/test-prepush-refclass.sh}"

for f in "$GANCHO" "$BANCO"; do
	[ -r "$f" ] || {
		echo "check-hook-leg-wiring: NO HE PODIDO MIRAR: no leo $f." >&2
		exit 2
	}
done

OLIVARES_WIRING_HOOK="$GANCHO" OLIVARES_WIRING_BENCH="$BANCO" python3 - <<'PY'
import os, re, sys, io

hook = io.open(os.environ["OLIVARES_WIRING_HOOK"], encoding="utf-8", errors="replace").read()
banco = io.open(os.environ["OLIVARES_WIRING_BENCH"], encoding="utf-8", errors="replace").read()

# 1 · las patas que el gancho LLAMA de verdad (no las que menciona su prosa).
#     El prefijo `VAR=1 task lint:x` cuenta: es una llamada con entorno, no otra cosa.
llamadas = []
for linea in hook.split("\n"):
    s = linea.strip()
    if not s or s.startswith("#"):
        continue
    m = re.match(r'^(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*task\s+(lint:[A-Za-z0-9:_.\-]+)$', s)
    if m:
        llamadas.append(m.group(1))

# 2 · el banner que las anuncia. Ojo con la forma: una de sus lineas acaba en '+' SIN espacio
#     detras, asi que exigir ' + ' declara ausente una pata que esta (me paso con channel-parity).
banner = " + ".join(
    l for l in hook.split("\n")
    if (l.startswith('echo "pre-push:') or l.startswith("gate_heavy_list=")) and "+" in l
)

# 3 · la lista declarada del banco son las DOS: el carril rapido y el pesado. Comparar solo contra
#     FAST_LINTS marca las patas del gate pesado como si faltaran (me paso con format-ratchet,
#     guide-docs y raw-palette).
# ⛔ SE LEE POR LINEAS Y SIN COMENTARIOS, y las dos cosas por una medida: con `re.S` hasta el
# primer `\n)` el bloque de HEAVY_TASKS se comia el CODIGO de debajo (433 tokens, 220 que no son
# tareas), y los cuatro comentarios de dentro de FAST_LINTS metian 68 palabras de PROSA en el
# universo. Una pata citada en un comentario habria contado como declarada: falso negativo, que en
# un gate es peor que un falso positivo porque no se ve.
declaradas = set()
lineas = banco.split("\n")
for nombre in ("FAST_LINTS", "HEAVY_TASKS"):
    try:
        i = next(k for k, l in enumerate(lineas) if l.startswith(nombre + "=("))
    except StopIteration:
        continue
    # El array puede venir en UNA linea (`X=(a b c)`) o en varias. Las dos formas existen en este
    # banco: FAST_LINTS es multilinea y HEAVY_TASKS es de una sola, y leer solo desde la linea
    # siguiente se saltaba HEAVY_TASKS entera — un universo incompleto marca como ausentes las patas
    # que SI estan, que es el falso positivo que apunta a un carril ajeno.
    resto = lineas[i].split("=(", 1)[1]
    for l in [resto] + lineas[i + 1:]:
        if l.lstrip().startswith("#"):
            continue
        declaradas |= set(l.replace(")", " ").split())
        if ")" in l:
            break

if not llamadas or not declaradas:
    print("check-hook-leg-wiring: NO HE PODIDO MIRAR: los extractores no casaron nada "
          "(llamadas=%d, declaradas=%d). El gancho o el banco han cambiado de forma."
          % (len(llamadas), len(declaradas)), file=sys.stderr)
    sys.exit(2)

sin_banner = [t for t in llamadas if t.replace("lint:", "", 1) not in banner]
sin_lista = [t for t in llamadas if t not in declaradas]

print("check-hook-leg-wiring: %d pata(s) lint: llamadas por el gancho · %d declarada(s) en el banco"
      % (len(llamadas), len(declaradas)))

if not sin_banner and not sin_lista:
    print("check-hook-leg-wiring: limpio — toda pata llamada esta tambien anunciada y declarada.")
    sys.exit(0)

print("check-hook-leg-wiring: ⛔ HALLAZGO — pata(s) cableada(s) en un sitio y no en los otros:")
for t in sorted(set(sin_banner) | set(sin_lista)):
    faltan = []
    if t in sin_banner:
        faltan.append("el banner del gancho")
    if t in sin_lista:
        faltan.append("la lista declarada del banco (FAST_LINTS/HEAVY_TASKS)")
    print("    %s — falta en %s" % (t, " y en ".join(faltan)))
print("  Una pata cableada vive en TRES sitios. Añadela donde falte, en la MISMA posicion de la")
print("  secuencia en que el gancho la llama: el banco compara el ORDEN, no solo la pertenencia.")
sys.exit(1)
PY
