#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Banco ACOTADO de `classify()` en check-spdx.sh para la extensión `.awk`.
#
# ⛔ POR QUE PRUEBA LA FUNCION Y NO EL GATE ENTERO, que fue mi primer intento y estaba mal.
# `check-spdx.sh` lleva un suelo fail-closed (`SPDX_MIN_CHECKED=4200`): sobre un árbol de juguete
# responde **2 — CANNOT LOOK**, y con ese 2 mis filas «pasaban» por el motivo equivocado. Un rc
# correcto por otra causa es indistinguible de uno correcto, así que el banco iría en verde sin
# acreditar nada. Aquí se extrae `classify()` y se le pregunta directamente, que es exactamente lo
# que este claim cambia; la fila final comprueba además el gate REAL de punta a punta.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GUION="$ROOT/scripts/check-spdx.sh"
[ -r "$GUION" ] || { echo "COULD NOT LOOK: missing $GUION"; exit 2; }

pass=0; fail=0
ok() { printf 'ok    %-58s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
no() { printf 'FAIL  %-58s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# `classify()` se extrae por sus fronteras exactas y se evalúa aquí. Si la extracción falla, el
# banco REHUSA: un banco que no encuentra a su sujeto no puede decir que está bien.
FN="$(awk '/^classify\(\)/{f=1} f{print} f&&/^}$/{exit}' "$GUION")"
case "$FN" in
	*"classify()"*"esac"*) ;;
	*) echo "COULD NOT LOOK: could not extract classify() from $GUION" >&2; exit 2 ;;
esac
eval "$FN"

esperado() { # <ruta> <bucket esperado>
	r="$(classify "$1" 2>/dev/null)"
	[ "$r" = "$2" ] && ok "$1 -> $2" || no "$1: expected «$2» and got «${r:-<empty>}»"
}
# lo que este claim añade
esperado 'scripts/lib/sigpipe-m2.awk' source
esperado 'scripts/otro.awk'           source
# y lo que NO debe moverse: las vecinas de su bucket y un dato
esperado 'scripts/x.sh'               source
esperado 'core/x.go'                  source
esperado 'docs/x.csv'                 data

# ⛔ LA FILA QUE ACREDITA EL CAMBIO: sin `.awk` en el bucket, `classify()` no contesta ninguno de
# los dos —ni source ni data— y por eso el gate sale 2. Es la clase «el primero que estrena una
# forma destapa la forma»: el fichero nuevo no rompió nada, hizo visible una pregunta sin contestar.
FN_SIN="$(printf '%s\n' "$FN" | sed 's/|\*\.awk)/)/')"
case "$FN_SIN" in
	*'*.awk)'*) no "mutation was not applied — an unbuilt mutant reports a pass" ;;
	*)
		( eval "$FN_SIN"; r="$(classify 'scripts/lib/sigpipe-m2.awk' 2>/dev/null)"
		  [ "$r" != "source" ] && exit 0 || exit 1 ) &&
			ok "without classification, the SAME .awk is no longer source (so the gate returns 2)" ||
			no "classification has no effect: without it, the result is still source" ;;
esac

# integración, una sola vez porque cuesta ~75 s: el gate REAL sobre el árbol de verdad
if [ "${OLIVARES_SPDX_AWK_SKIP_E2E:-0}" = "1" ]; then
	ok "e2e skipped by request (OLIVARES_SPDX_AWK_SKIP_E2E=1)"
else
	out="$(sh "$GUION" 2>&1)"; rc=$?
	case "$rc$out" in
		0*"SPDX check OK"*) ok "real gate on the tree: rc 0" ;;
		*) no "gate real: rc=$rc — $(printf '%s' "$out" | tail -1)" ;;
	esac
fi

printf '\ntest-spdx-awk: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = 0 ] || exit 1
