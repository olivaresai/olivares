#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# Ejercita la guarda de frescura de hub-hygiene.sh. La guarda solo CONSERVA: nada que el censo
# retirase antes deja de retirarse por ella.
#
# ⛔ NO SE TECLEA NI UNA RUTA. La primera version enumeraba arboles concretos y tenia dos defectos
#    a la vez: filtraba rutas privadas al export (lo canto `lint:export`: "founder bare name /
#    private org-or-domain / dev-process language", mas el trinquete de vocabulario 23 -> 24) y se
#    rompia sola en cuanto uno de esos arboles dejara de existir, que es exactamente lo que pasa
#    aqui todos los dias. Ahora los sujetos se DERIVAN del propio censo y se juzgan por su edad.
set -uo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)" || exit 2
cd "$ROOT" || exit 2

UMBRAL="${OLIVARES_HYGIENE_FRESH_HOURS:-168}"
fallos=0
ok() { printf '  ok    %-46s %s\n' "$1" "$2"; }
mal() { printf '  FAIL  %-46s %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

# La copia exacta de la guarda que vive en hub-hygiene.sh.
demasiado_fresco() {
	local wt="$1" head_ts ahora horas
	head_ts="$(git -C "$wt" log -1 --format=%ct HEAD 2>/dev/null)" || return 1
	case "$head_ts" in '' | *[!0-9]*) return 1 ;; esac
	ahora="$(date +%s)"
	horas=$(( (ahora - head_ts) / 3600 ))
	[ "$horas" -lt "$UMBRAL" ]
}

# Sujetos DERIVADOS: el mas joven y el mas viejo de los arboles que el censo veria.
edades="$(git worktree list --porcelain 2>/dev/null | awk '/^worktree /{print $2}' | while IFS= read -r d; do
	[ -d "$d" ] || continue
	t="$(git -C "$d" log -1 --format=%ct HEAD 2>/dev/null)" || continue
	case "$t" in '' | *[!0-9]*) continue ;; esac
	printf '%s\t%s\n' "$t" "$d"
done | sort -n)"

n="$(printf '%s\n' "$edades" | grep -c .)"
if [ "$n" -lt 2 ]; then
	echo "test-hygiene-freshness: NO HE PODIDO MIRAR — solo $n arbol(es) legible(s); hacen falta 2" >&2
	exit 2
fi
viejo="$(printf '%s\n' "$edades" | head -1 | cut -f2)"
joven="$(printf '%s\n' "$edades" | tail -1 | cut -f2)"
h_viejo=$(( ( $(date +%s) - $(printf '%s\n' "$edades" | head -1 | cut -f1) ) / 3600 ))
h_joven=$(( ( $(date +%s) - $(printf '%s\n' "$edades" | tail -1 | cut -f1) ) / 3600 ))

if demasiado_fresco "$joven"; then ok "el arbol MAS JOVEN se conserva" "${h_joven}h < ${UMBRAL}h"
else mal "el arbol MAS JOVEN se conserva" "${h_joven}h y no lo conserva"; fi

if [ "$h_viejo" -ge "$UMBRAL" ]; then
	if demasiado_fresco "$viejo"; then mal "el arbol MAS VIEJO sigue siendo candidato" "${h_viejo}h y lo conserva"
	else ok "el arbol MAS VIEJO sigue siendo candidato" "${h_viejo}h >= ${UMBRAL}h"; fi
else
	ok "el arbol MAS VIEJO sigue siendo candidato" "OMITIDO: el mas viejo tiene ${h_viejo}h, por debajo del umbral"
fi

# El umbral se puede bajar por entorno: con 0 h nada se conserva.
if OLIVARES_HYGIENE_FRESH_HOURS=0 UMBRAL=0 demasiado_fresco "$joven"; then
	mal "el umbral es ajustable por entorno" "con 0 h sigue conservando"
else ok "el umbral es ajustable por entorno" "con 0 h no conserva nada"; fi

# Fail-closed: lo ilegible NO se enmascara como fresco.
if demasiado_fresco "/no/existe/en/ninguna/parte"; then
	mal "una ruta ilegible NO se enmascara" "la dio por fresca"
else ok "una ruta ilegible NO se enmascara" "no conserva lo que no puede leer"; fi

echo "test-hygiene-freshness: $((4 - fallos)) pasan, $fallos fallan  (sobre $n arbol(es) derivado(s))"
[ "$fallos" -eq 0 ] || exit 1
