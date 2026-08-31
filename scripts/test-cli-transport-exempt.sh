#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-cli-transport-exempt.sh — el selftest del lint, y existe porque un gate que nunca se ha
# visto enrojecer no se sabe si discrimina o solo grita.
#
# ⛔ CADA CASO VA EN LAS DOS DIRECCIONES. Un caso que solo comprueba el rojo no distingue «el
# gate caza esto» de «el gate caza todo»; y uno que solo comprueba el verde no distingue «esto
# esta bien» de «el gate esta ciego».
set -u -o pipefail
GUION="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)/check-cli-transport-exempt.sh"
ok=0; mal=0
paso(){ printf '  ok    %s\n' "$*"; ok=$((ok+1)); }
fallo(){ printf '  ⛔ FALLO %s\n' "$*"; mal=$((mal+1)); }

# Un arbol de juguete con el SUELO de poblacion satisfecho (el lint exige >=50 .go).
monta(){
	T=$(mktemp -d); mkdir -p "$T/cmd/olivares"
	for i in $(seq 1 60); do printf 'package main\n' > "$T/cmd/olivares/relleno$i.go"; done
	printf 'package main\n' > "$T/cmd/olivares/clitransport.go"
	printf '%s\n' "$T"
}
corre(){ bash "$GUION" "$1" >/dev/null 2>&1; printf '%s' "$?"; }

# --- 1 · una ruta de red SIN exencion -> HALLAZGO (rc=1) ---
T=$(monta)
printf 'package main\nfunc f(){ c := &http.Client{} ; _ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 1 ] && paso "una ruta sin exencion es HALLAZGO (rc=1)" \
	|| fallo "una ruta sin exencion NO salio 1"

# --- 2 · CONTROL INVERSO: la misma ruta CON exencion valida -> LIMPIO (rc=0) ---
printf 'package main\n// cli-transport-exempt: descarga OTA anclada a la firma del manifiesto\nfunc f(){ c := &http.Client{} ; _ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 0 ] && paso "control inverso: con exencion valida sale LIMPIO (rc=0)" \
	|| fallo "con exencion valida NO salio 0 — el gate grita por todo"

# --- 3 · marcador PELADO (sin razon) -> HALLAZGO. «Alguien tecleo la palabra magica» no vale ---
printf 'package main\n// cli-transport-exempt:\nfunc f(){ c := &http.Client{} ; _ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 1 ] && paso "marcador sin razon es HALLAZGO" || fallo "marcador pelado colo"

# --- 4 · razon DEMASIADO CORTA -> HALLAZGO (el umbral discrimina, no es decoracion) ---
printf 'package main\n// cli-transport-exempt: ota\nfunc f(){ c := &http.Client{} ; _ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 1 ] && paso "razon de menos de 10 caracteres es HALLAZGO" || fallo "razon corta colo"

# --- 5 · LA REGLA NUEVA, direccion permisiva: un comentario LARGO no agota nada ---
#     Bajo la ventana de 6 lineas esto habria FALLADO; bajo la del bloque, pasa.
{ printf 'package main\n// cli-transport-exempt: descarga OTA anclada a la firma del manifiesto\n'
  for i in $(seq 1 12); do printf '// linea de explicacion %d\n' "$i"; done
  printf 'func f(){ c := &http.Client{} ; _ = c }\n'; } > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 0 ] && paso "marcador a 13 lineas dentro del MISMO bloque: LIMPIO (la ventana de 6 lo habria roto)" \
	|| fallo "un comentario largo rompe su propia exencion — la regla del bloque no esta puesta"

# --- 6 · LA REGLA NUEVA, direccion estricta: una linea de CODIGO corta el bloque ---
#     Bajo la ventana de 6 esto habria PASADO (distancia 2); bajo la del bloque, no.
printf 'package main\n// cli-transport-exempt: descarga OTA anclada a la firma del manifiesto\nvar corta = 1\nfunc f(){ c := &http.Client{} ; _ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 1 ] && paso "una linea de CODIGO corta el bloque: HALLAZGO (la ventana de 6 lo habria dejado pasar)" \
	|| fallo "un marcador separado por codigo sigue contando — la regla es la vieja"

# --- 6b · MARCADOR EN LA PROPIA LINEA DEL CLIENTE, razon valida -> LIMPIO ---
#     El test declaredExempt (`for i := idx`) ya lo eximia. Arrancar en i-1 hacia que los dos
#     gates discreparan sobre el mismo fichero, y el mensaje de este imprimia la razon en la
#     linea de la que decia que no la tenia. Medido el 2026-08-25.
printf 'package main\nfunc f(){ c := &http.Client{} // cli-transport-exempt: descarga OTA anclada a la firma del manifiesto\n_ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 0 ] && paso "marcador en la PROPIA linea del cliente: LIMPIO (casa con declaredExempt)" \
	|| fallo "marcador en la linea del cliente sale HALLAZGO — diverge de declaredExempt"

# --- 6c · CONTROL INVERSO del anterior: misma linea, razon CORTA -> HALLAZGO ---
printf 'package main\nfunc f(){ c := &http.Client{} // cli-transport-exempt: ota\n_ = c }\n' > "$T/cmd/olivares/sujeto.go"
[ "$(corre "$T")" = 1 ] && paso "control inverso: en la linea del cliente con razon corta es HALLAZGO" \
	|| fallo "razon corta en la linea del cliente colo — el umbral no se aplica ahi"

# --- 7 · el mensaje de fallo NOMBRA la regla de adyacencia (documentado != diagnosticable) ---
printf 'package main\nfunc f(){ c := &http.Client{} ; _ = c }\n' > "$T/cmd/olivares/sujeto.go"
# ⛔ SE CAPTURA ANTES DE FILTRAR. Con `pipefail`, `guion | grep -q` devuelve el rc del GUION
#    —que aqui es 1 a proposito— y no el del grep: el caso fallaba con el mensaje correcto
#    delante. Es la trampa del `push | tail` de esta misma noche, en su cuarta forma.
MSG=$(bash "$GUION" "$T" 2>&1 || true)
case "$MSG" in
	*BLOQUE*) paso "el mensaje de fallo NOMBRA la adyacencia" ;;
	*) fallo "el fallo no dice cual es la regla: documentado no es diagnosticable" ;;
esac

# --- 8 · poblacion insuficiente -> NO HE PODIDO MIRAR (rc=2), nunca un verde ---
T2=$(mktemp -d); mkdir -p "$T2/cmd/olivares"; printf 'package main\n' > "$T2/cmd/olivares/uno.go"
[ "$(corre "$T2")" = 2 ] && paso "un glob que no ve el paquete responde 2, no 0" \
	|| fallo "poblacion insuficiente NO salio 2 — el cero comodo"

# --- 9 · ruta inexistente -> NO HE PODIDO MIRAR (rc=2) ---
[ "$(corre "$T/no-existe")" = 2 ] && paso "una raiz inexistente responde 2" || fallo "raiz inexistente no salio 2"

rm -rf "$T" "$T2"
printf '\ntest-cli-transport-exempt: %d pasan, %d fallan\n' "$ok" "$mal"
[ "$mal" = 0 ] || exit 1
