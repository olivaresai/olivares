#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-transport-exempt.sh — toda ruta de red de `cmd/olivares` pasa por `cliTransport`
# o DECLARA POR ESCRITO por qué no.
#
# ⛔ POR QUE EXISTE ESTE GUION SI YA HAY UN TEST QUE LO COMPRUEBA.
#
# `TestOnlyOneCLITransport` sigue existiendo y sigue siendo el testigo de integracion. Pero el
# 2026-08-25 se midio donde vive: `test:functional` NO esta en el hook de pre-push —ni en los
# rapidos ni en el pesado— y solo corre en `mainline-ci.yml:1109`, que dispara **por push a
# `main`**. ⇒ el autor de una ruta de red **no tenia forma de ver su propio rojo hasta despues
# de aterrizar**. El rojo de no fue descuido de nadie: fue geografia del control.
#
# Este escaneo es glob + comparacion de lineas: sin motor, sin BD, sin red. Nada justifica que
# viva a una hora del autor.
#
# ⛔ LA ADYACENCIA ES PARTE DEL CONTRATO, y por eso se documenta aqui y se NOMBRA en el fallo.
#
# El test original miraba una VENTANA FIJA de seis lineas por encima. Eso mordio en silencio:
# `cmd_upgrade.go:644-649` estaba a distancia 5 —a UNA linea de romperse— con la exencion
# CORRECTA puesta, y el mensaje decia que faltaba. Un limite no documentado muerde donde mas
# duele: en una exencion bien escrita.
#
# La regla de aqui NO tiene numero: **el marcador va en el BLOQUE DE COMENTARIO CONTIGUO
# inmediatamente encima del `http.Client{`**.
#
# ⛔ Y NO ES «equivalente» A SECAS, que es como lo escribi primero: es una MEJORA, no un
# refactor. Medido sobre las tres exenciones que existen HOY da el mismo veredicto en las tres
# —la distancia ES el tamano del bloque en cmd_upgrade (5), haleaderlabel (4) y mcpgateway (3)—
# pero **divergen sobre casos que aun no estan escritos**. Decirlo importa: un «es equivalente»
# se cita manana para NO re-medir cuando alguien escriba el primer caso divergente.
#
#   equivalente sobre el corpus medido (3/3) · ESTRICTAMENTE MAS EXIGENTE fuera de el ·
#   y esa exigencia extra es la intencion, no un efecto colateral.
#
# Ademas:
#   · no se agota: un comentario que crece no rompe su propia exencion;
#   · es la regla que la gente ya cree que existe («el marcador va en el comentario de este
#     cliente»), no una aproximacion con numero;
#   · y es MAS estricta donde importa: un marcador separado por una linea de CODIGO ya no
#     cuenta, aunque este a tres lineas.
#
# Tres respuestas, nunca dos:  0 limpio · 1 hallazgo · 2 no he podido mirar.
set -u -o pipefail

RAIZ="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
PKG="$RAIZ/cmd/olivares"
MARCA="cli-transport-exempt:"
RAZON_MIN=10   # un marcador pelado no es una justificacion: «alguien tecleo la palabra magica»

if [ ! -d "$PKG" ]; then
	printf 'check-cli-transport-exempt: ⛔ NO HE PODIDO MIRAR: no existe %s\n' "$PKG" >&2
	exit 2
fi

# ⛔ SUELO DE POBLACION. Sin el, un glob que no ve nada sale LIMPIO — el cero comodo. El test
# original lleva el mismo suelo y por la misma razon.
N=$(find "$PKG" -maxdepth 1 -name '*.go' ! -name '*_test.go' | wc -l)
if [ "$N" -lt 50 ]; then
	printf 'check-cli-transport-exempt: ⛔ NO HE PODIDO MIRAR: el glob ve %d ficheros .go y el\n' "$N" >&2
	printf '  paquete tiene decenas. Un escaneo que no ve el paquete no puede declararlo limpio.\n' >&2
	exit 2
fi

SALIDA=$(RAIZ="$PKG" MARCA="$MARCA" RAZON_MIN="$RAZON_MIN" python3 - <<'PY'
import glob, io, os, sys
raiz = os.environ["RAIZ"]; marca = os.environ["MARCA"]; rmin = int(os.environ["RAZON_MIN"])
malos = []
for ruta in sorted(glob.glob(os.path.join(raiz, "*.go"))):
    base = os.path.basename(ruta)
    if base.endswith("_test.go") or base == "clitransport.go":
        continue
    lineas = io.open(ruta, encoding="utf-8", errors="replace").read().split("\n")
    for i, l in enumerate(lineas):
        if "http.Client{" not in l:
            continue
        # el BLOQUE DE COMENTARIO CONTIGUO justo encima: se sube mientras la linea sea
        # comentario o este en blanco; la primera linea de CODIGO cierra el bloque.
        exento = False
        # ⛔ ARRANCA EN LA PROPIA LINEA DEL CLIENTE, no encima. Un marcador al FINAL de esa
        # linea esta pegado al enunciado tanto como el comentario de arriba, y el test
        # declaredExempt (clitransport_single_test.go:71, `for i := idx`) ya lo eximia.
        # Arrancar en i-1 hacia que los DOS gates discreparan sobre el MISMO fichero.
        # Medido el 2026-08-25 sobre el arbol real, inyectando el marcador al final de la
        # linea del cliente: su test rc=0 (lo exime) y este rc=1 (lo acusa) -- imprimiendo
        # la razon en la misma linea de la que decia que no la tenia.
        j = i
        while j >= 0:
            s = lineas[j].strip()
            if j != i and not (s.startswith("//") or s == ""):
                break
            pos = lineas[j].find(marca)
            if pos >= 0:
                razon = lineas[j][pos + len(marca):].strip()
                exento = len(razon) >= rmin
                break
            j -= 1
        if not exento:
            malos.append("%s:%d: %s" % (base, i + 1, l.strip()))
print("\n".join(malos))
PY
) || { printf 'check-cli-transport-exempt: ⛔ NO HE PODIDO MIRAR: el escaneo fallo\n' >&2; exit 2; }

if [ -n "$SALIDA" ]; then
	printf 'check-cli-transport-exempt: ⛔ %d ruta(s) de red construyen su propio http.Client sin\n' \
		"$(printf '%s\n' "$SALIDA" | grep -c .)" >&2
	printf '  pasar por cliTransport y sin declarar por que:\n' >&2
	printf '%s\n' "$SALIDA" | sed 's/^/    /' >&2
	printf '\n  Para declararlo: pon `// %s <razon de al menos %d caracteres>` DENTRO DEL BLOQUE\n' "$MARCA" "$RAZON_MIN" >&2
	printf '  DE COMENTARIO CONTIGUO inmediatamente encima del `http.Client{`. La adyacencia es\n' >&2
	printf '  parte del contrato: una linea de CODIGO entre el marcador y el cliente CORTA el\n' >&2
	printf '  bloque y la exencion no cuenta. No hay limite de lineas — el bloque entero vale.\n' >&2
	exit 1
fi

printf 'check-cli-transport-exempt: LIMPIO — %d fichero(s) escaneados, toda ruta de red pasa por\n' "$N"
printf '  cliTransport o declara su razon en el comentario contiguo.\n'
exit 0
