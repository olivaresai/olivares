#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-cpu-quota.sh — batería de `scripts/cpu-quota.sh`.
#
# ⛔ EL CASO QUE DE VERDAD IMPORTA es el primero: en ESTA caja tiene que decir 4 y no 16. Un helper
#    que devolviera `nproc` seguiría pasando todos los casos sintéticos y no habría arreglado nada,
#    porque el defecto no era el cálculo: era la FUENTE. Por eso el caso vivo va antes que los
#    sintéticos y no se salta.
#
# ⛔ Y LOS SINTÉTICOS NO PUEDEN LEER /sys, así que prueban la aritmética a través de una copia del
#    guion con las rutas redirigidas a un directorio de prueba. Si algún día el guion deja de leer
#    esas rutas, la copia falla al construirse y el caso lo dice: un banco que no puede montar su
#    sujeto NO DICTAMINA, lo declara.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

PASS=0; FAIL=0; NOPUDE=0
ok()     { PASS=$((PASS+1));   printf 'ok   %-56s %s\n' "$1" "${2:-}"; }
malo()   { FAIL=$((FAIL+1));   printf 'FAIL %-56s %s\n' "$1" "${2:-}"; }
nopude() { NOPUDE=$((NOPUDE+1)); printf '?    %-56s %s\n' "$1" "${2:-}"; }

# ── 1 · LA CAJA VIVA. Sin esto, todo lo demás es teatro.
vivo="$(bash scripts/cpu-quota.sh 2>/dev/null)"
np="$(nproc 2>/dev/null || echo '?')"
if [ -r /sys/fs/cgroup/cpu.max ]; then
	read -r q p < /sys/fs/cgroup/cpu.max
	if [ "$q" = "max" ]; then
		if [ "$vivo" = "$np" ]; then
			ok "sin cuota en esta caja: cae a nproc" "$vivo"
		else
			malo "sin cuota en esta caja: cae a nproc" "dio $vivo, nproc $np"
		fi
	else
		esperado=$(( (q + p - 1) / p ))
		if [ "$vivo" = "$esperado" ]; then
			ok "la caja viva devuelve la CUOTA, no la afinidad" "$vivo (nproc dice $np)"
		else
			malo "la caja viva devuelve la CUOTA, no la afinidad" "dio $vivo, esperaba $esperado"
		fi
		if [ "$np" != "$esperado" ] && [ "$vivo" = "$np" ]; then
			malo "el helper NO se ha limitado a llamar a nproc" "dio $np, que es justo el defecto"
		else
			ok "el helper NO se ha limitado a llamar a nproc" "cuota=$esperado nproc=$np"
		fi
	fi
else
	nopude "la caja viva" "no hay /sys/fs/cgroup/cpu.max legible: no puedo dictaminar aqui"
fi

# ── 2 · ARITMÉTICA, sobre una copia con las rutas redirigidas.
T="$(mktemp -d "${TMPDIR:-/tmp}/cq.XXXXXX")" || { nopude "sinteticos" "sin TMPDIR escribible"; T=""; }
if [ -n "$T" ]; then
	trap 'rm -rf "$T"' EXIT
	mkdir -p "$T/cg"
	# ⛔ LA COPIA CONSERVA EL NOMBRE a proposito: el guion decide si autoejecutarse mirando
	#    `${0##*/}`, asi que una copia llamada de otra forma se sourcea y NO IMPRIME NADA — mi
	#    primera version la llamo `cq.sh` y los seis casos sinteticos dieron vacio. Un banco que
	#    renombra a su sujeto cambia su comportamiento.
	sed "s|/sys/fs/cgroup/cpu.max|$T/cg/cpu.max|g; s|/sys/fs/cgroup/cpu/cpu.cfs_quota_us|$T/cg/q|g; s|/sys/fs/cgroup/cpu/cpu.cfs_period_us|$T/cg/p|g" \
		scripts/cpu-quota.sh > "$T/cpu-quota.sh" 2>/dev/null
	if ! command grep -q "$T/cg/cpu.max" "$T/cpu-quota.sh" 2>/dev/null; then
		nopude "banco sintetico" "la copia no tiene las rutas redirigidas: el guion ya no las lee asi"
	else
		caso() { # <titulo> <contenido cpu.max> <esperado>
			printf '%s\n' "$2" > "$T/cg/cpu.max"
			got="$(bash "$T/cpu-quota.sh" 2>/dev/null)"
			if [ "$got" = "$3" ]; then ok "$1" "$got"; else malo "$1" "dio '$got', esperaba '$3'"; fi
		}
		caso "cuota exacta 400000/100000 = 4"            "400000 100000" 4
		caso "cuota de un solo nucleo"                   "100000 100000" 1
		caso "cuota fraccionaria 250000/100000 -> 3 (arriba)" "250000 100000" 3
		caso "cuota MENOR que un nucleo nunca baja de 1" "50000 100000"  1
		# `max` = sin cuota ⇒ nproc. Es el ÚNICO caso donde nproc es la respuesta correcta.
		printf 'max 100000\n' > "$T/cg/cpu.max"
		got="$(bash "$T/cpu-quota.sh" 2>/dev/null)"
		if [ "$got" = "$(nproc 2>/dev/null || echo 1)" ]; then
			ok "cpu.max = 'max' cae a nproc" "$got"
		else
			malo "cpu.max = 'max' cae a nproc" "dio '$got', nproc dice '$(nproc 2>/dev/null)'"
		fi
		# Basura: no puede devolver vacio ni 0 — un dimensionado a 0 no arranca nada.
		printf 'basura basura\n' > "$T/cg/cpu.max"
		got="$(bash "$T/cpu-quota.sh" 2>/dev/null)"
		if [ -n "$got" ] && [ "$got" -ge 1 ] 2>/dev/null; then
			ok "un cpu.max ilegible NUNCA devuelve 0 ni vacio" "$got"
		else
			malo "un cpu.max ilegible NUNCA devuelve 0 ni vacio" "dio '$got'"
		fi
	fi
fi

printf '\ncheck-cpu-quota: %s passed, %s failed, %s sin-veredicto\n' "$PASS" "$FAIL" "$NOPUDE"
[ "$FAIL" -eq 0 ] || exit 1
[ "$NOPUDE" -eq 0 ] || exit 2
exit 0
