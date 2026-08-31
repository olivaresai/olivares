#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Que el `timeout-minutes` de un JOB sea mayor que la SUMA de los de sus pasos.
#
# Por que existe, con el caso que lo motivo escrito por el propio workflow: el comentario de
# `control-plane` dice que su techo de job es «absolute backstop only» porque al dispararse
# «a JOB-level timeout cancels remaining steps, so the failure reporter never posts — SILENT RED»,
# y que las guardas de verdad son los `timeout-minutes` de PASO. El 2026-08-22 ese backstop era 75
# y sus pasos sumaban 150: tenia GARANTIZADO dispararse primero. Resultado medido: CERO exitos en
# catorce corridas, nueve clavadas en 75,4 min, sobre uno de los CUATRO contextos que la proteccion
# de rama EXIGE. La proteccion de `main` era insatisfacible y nada lo decia.
#
# No es una heuristica: los pasos de un job corren en SERIE, asi que su suma es el peor caso. Un
# techo por debajo de esa suma no es un backstop, es el limite efectivo — y el que no reporta.
#
# ⛔ LA LECTURA DE LOS WORKFLOWS VIVE EN `scripts/ci-timeouts.py`, Y NO ES UN REFACTOR (2026-08-23).
# Este guion tenia `import yaml` como unica via y salia 2 en cuanto PyYAML faltaba. Medido hoy en la
# un contenedor de desarrollo SIN PyYAML, sobre `origin/main` puro: `rc=2`, y
# `task lint:ci-timeout-arithmetic` -> 201. `.githooks/pre-push:1205` invoca esa tarea y la 1218 de
# ese mismo fichero es `# --- FAST LOCAL GATE ENDS HERE ---`, o sea que el gate vive en el carril
# RAPIDO ⇒ **rechazaba cualquier push, de cualquier rama, desde una caja sin PyYAML**, y aqui no hay
# `pip` en ninguna forma. Es el MISMO defecto que `taskfile-shape.py:14-27` diagnostico y cerro el
# 2026-08-20, dos dias antes de que naciera este gate; la leccion estaba escrita y no se aplico.
# Y no se veia porque el gate NO ES REPRODUCIBLE ENTRE CAJAS: el mismo commit da CLEAN donde PyYAML
# esta y 2 donde no — el 2026-08-23 el integrador publico `CLEAN` mientras esta caja daba `rc=2`
# sobre el mismo arbol.
#
# Tres respuestas: 0 limpio · 1 hallazgo · 2 NO PUDE MIRAR.
set -uo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="${OLIVARES_CI_WORKFLOWS_DIR:-$RAIZ/.github/workflows}"
MARGEN="${OLIVARES_CI_TIMEOUT_MARGIN:-0}"

# ⛔ EL MARGEN SE VALIDA ANTES DE USARLO, y no es defensivo: sin esto DESACTIVA la invariante.
# Contraste Codex `sol max`, 2026-08-23, medido sobre un job con techo 30 y dos pasos de 20:
#   por defecto                       -> rc=1, hallazgo 30 contra 40
#   OLIVARES_CI_TIMEOUT_MARGIN=-20    -> rc=0, CLEAN
# El margen entra en `techo <= suma + margen`, asi que uno NEGATIVO relaja el predicado hasta
# certificar un arbol defectuoso — heredado del entorno o puesto a mano, da igual. Un margen
# existe para ser MAS estricto (exigir holgura por encima de la suma), nunca menos.
#
# Y un valor no entero tampoco es un hallazgo: `int()` reventaba con un traceback y el shell
# devolvia 1, o sea «hay un defecto» cuando lo que hay es una configuracion invalida. Las dos
# cosas son «no he podido mirar» y salen 2, que es lo que el contrato de este guion promete.
case "$MARGEN" in
  ''|*[!0-9]*)
    echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — OLIVARES_CI_TIMEOUT_MARGIN=$MARGEN no es un entero no negativo. Un margen negativo RELAJA la invariante hasta certificar un arbol defectuoso, y uno ilegible no es un hallazgo: es una configuracion que no se puede leer." >&2
    exit 2 ;;
esac
AYUDANTE="$RAIZ/scripts/ci-timeouts.py"

[ -d "$DIR" ] || { echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — sin $DIR" >&2; exit 2; }
[ -f "$AYUDANTE" ] || { echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — sin $AYUDANTE" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — sin python3" >&2; exit 2; }

# El ayudante nunca sale distinto de 0: dice lo que sabe, o dice NOPUEDO en una linea. Distinguir
# «no imprimio nada» de «fallo» importa, porque son 2 y 2 por razones distintas y el mensaje cambia.
FILAS="$(python3 "$AYUDANTE" "$DIR" 2>&1)" || {
  echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — el ayudante murio: $FILAS" >&2; exit 2; }

case "$FILAS" in
  NOPUEDO*)
    echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — ${FILAS#NOPUEDO	}" >&2; exit 2 ;;
esac

[ -n "$FILAS" ] || { echo "check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — el ayudante no devolvio ningun job" >&2; exit 2; }

MARGEN="$MARGEN" FILAS="$FILAS" python3 - <<'PY'
import os, sys

margen = int(os.environ["MARGEN"])
hallazgos, jobs_vistos, jobs_totales = [], 0, 0
for linea in os.environ["FILAS"].split("\n"):
    if not linea.strip():
        continue
    campos = linea.split("\t")
    if campos[0] != "JOB" or len(campos) != 7:
        print("check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — fila ilegible del ayudante: %r"
              % linea[:120], file=sys.stderr)
        raise SystemExit(2)
    fich, nombre = campos[1], campos[2]
    try:
        techo, suma, pasos, conguarda = (int(campos[3]), int(campos[4]),
                                         int(campos[5]), int(campos[6]))
    except ValueError:
        print("check-ci-timeout-arithmetic: 2 NO PUDE MIRAR — fila con campos no numericos: %r"
              % linea[:120], file=sys.stderr)
        raise SystemExit(2)
    # Un job sin NINGUNA guarda de paso no entra en esta invariante: no hay suma que comparar.
    # Es un hueco REAL y con nombre — el techo del job es entonces el unico que puede morder, y
    # es el que no reporta — pero es OTRA invariante, con su propia linea base, y mezclarlas aqui
    # convertiria este gate en rojo para trece jobs el dia que aterrice.
    jobs_totales += 1
    if not suma:
        continue
    jobs_vistos += 1
    if techo <= suma + margen:
        hallazgos.append((fich, nombre, techo, suma, pasos - conguarda))

if hallazgos:
    print("check-ci-timeout-arithmetic: ⛔ %d job(s) con el techo POR DEBAJO de la suma de sus pasos." % len(hallazgos))
    print("  Esto NO predice que el job vaya a tardar: dice QUE GUARDA disparara si tarda, y esa")
    print("  distincion es el defecto. Los pasos corren en SERIE, asi que su suma es el peor caso;")
    print("  con el techo por debajo, el que muerde primero es SIEMPRE el del job — y el del job es")
    print("  el unico que no sabe reportar, porque cancela los pasos que quedan y el reportero de")
    print("  fallos no llega a publicar. El rojo sale MUDO, y GitHub lo etiqueta `cancelled`, que es")
    print("  la palabra de la supersesion.")
    print("  ⇒ Un job puede llevar años sin acercarse a su techo y seguir siendo un hallazgo: lo que")
    print("  esta roto no es su velocidad, es su modo de fallo el dia que falle.")
    for fich, nombre, techo, suma, sin in hallazgos:
        print("    %-22s %-18s techo %-4d suma de pasos %-4d  (+%d paso(s) sin guarda)"
              % (fich, nombre, techo, suma, sin))
    print("  Reparacion: sube el techo del job por encima de la suma, o baja los de sus pasos.")
    raise SystemExit(1)

# ⛔ El recuento va en el veredicto, y con SUS DOS NUMEROS. Un «CLEAN» que no dice sobre cuantos
# sujetos se pronuncio es la forma mas barata de un gate ciego: sale 0, nadie lo mira y certifica
# un arbol que no ha visto. Aqui la ceguera de verdad —que el ayudante no devuelva NINGUN job— ya
# la corta el shell antes de llegar; lo que queda es la distincion entre «no habia sujetos» y «la
# lectura se rompio», y esos dos casos se escriben distinto en vez de confundirse en un 0 mudo.
if jobs_vistos == 0:
    print("check-ci-timeout-arithmetic: CLEAN — 0 de %d job(s) con techo tienen guardas de paso, "
          "asi que esta invariante no tiene sujeto aqui. NO es un veredicto sobre esos %d jobs: "
          "que un job no tenga ninguna guarda de paso es un hueco con nombre propio, y lo mira "
          "otra invariante." % (jobs_totales, jobs_totales))
    raise SystemExit(0)

print("check-ci-timeout-arithmetic: CLEAN — %d de %d job(s) con techo tienen guardas de paso, "
      "todos por debajo de su techo." % (jobs_vistos, jobs_totales))
PY
