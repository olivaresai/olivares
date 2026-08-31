#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# cpu-quota.sh — cuántas CPU tenemos DE VERDAD, que no es lo que dice `nproc`.
#
# ⛔ POR QUÉ EXISTE. `nproc` lee la afinidad del proceso, no la cuota del cgroup. En esta caja dice
#    **16** y la cuota es **4,0** (`cpu.max` = 400000/100000). Eso no es un detalle de presentación:
#
#    · `with-pg-env.sh` acotaba su paralelismo de paquetes con `nproc` bajo el comentario «nunca más
#      CPUs de las que hay». Con nproc=16 esa cota NO MUERDE: lo único que acotaba era la memoria.
#      Salía bien por casualidad (14 GiB / 4 GiB = 3 ≤ 4), y en una caja con más memoria y la misma
#      cuota habría pedido más paralelismo que núcleos sin que nadie lo viera.
#    · `time-fast-lints.sh` imprime «load X on N cpu(s) — cite these WITH the numbers below», y el
#      canon las cita. Con el divisor equivocado, una carga de 9,4 se lee como 0,59x de la caja
#      cuando es **2,35x de sobrecarga**. Las duraciones publicadas no quedan invalidadas —llevan
#      sus condiciones, que es lo que se pide— pero su LECTURA se invierte: los 41m45s del 25-08 no
#      eran un pesimista raro, eran la carga normal de cuatro pushes en una caja de 4 CPU.
#
# Uso:  cpus="$(bash scripts/cpu-quota.sh)"     # imprime un entero >= 1
#
# ORDEN DE FUENTES, de la más específica a la más general:
#   1. cgroup v2  /sys/fs/cgroup/cpu.max        → "<cuota> <periodo>"  o  "max <periodo>"
#   2. cgroup v1  cpu.cfs_quota_us / cpu.cfs_period_us  (quota -1 = sin límite)
#   3. nproc                                     ← último recurso, y sólo cuando NO hay cuota
#
# El redondeo es HACIA ARRIBA a propósito: con cuota 2,5 hay dos núcleos completos y medio más, y
# quedarse en 2 desaprovecha; pasarse a 3 sólo cuesta contención marginal. Lo que no se hace nunca
# es redondear a 0: el mínimo es 1.
set -uo pipefail

_cq_ceil_div() { # $1/$2 hacia arriba, en enteros
	local n="$1" d="$2"
	[ "$d" -gt 0 ] 2>/dev/null || { printf '1'; return; }
	printf '%s' $(( (n + d - 1) / d ))
}

cpu_quota() {
	local quota period v

	# 1 · cgroup v2
	if [ -r /sys/fs/cgroup/cpu.max ]; then
		read -r quota period < /sys/fs/cgroup/cpu.max 2>/dev/null || true
		if [ "${quota:-max}" != "max" ] && [ -n "${period:-}" ]; then
			case "$quota$period" in
			*[!0-9]*) ;; # basura: cae al siguiente
			*)
				v="$(_cq_ceil_div "$quota" "$period")"
				[ "${v:-0}" -ge 1 ] 2>/dev/null && { printf '%s' "$v"; return 0; }
				;;
			esac
		fi
	fi

	# 2 · cgroup v1
	if [ -r /sys/fs/cgroup/cpu/cpu.cfs_quota_us ] && [ -r /sys/fs/cgroup/cpu/cpu.cfs_period_us ]; then
		quota="$(cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us 2>/dev/null || echo -1)"
		period="$(cat /sys/fs/cgroup/cpu/cpu.cfs_period_us 2>/dev/null || echo 0)"
		if [ "${quota:--1}" -gt 0 ] 2>/dev/null && [ "${period:-0}" -gt 0 ] 2>/dev/null; then
			v="$(_cq_ceil_div "$quota" "$period")"
			[ "${v:-0}" -ge 1 ] 2>/dev/null && { printf '%s' "$v"; return 0; }
		fi
	fi

	# 3 · sin cuota: la afinidad ES la respuesta correcta
	v="$(nproc 2>/dev/null || echo 1)"
	case "$v" in
	'' | *[!0-9]*) v=1 ;;
	esac
	[ "$v" -lt 1 ] && v=1
	printf '%s' "$v"
}

# Ejecutado directamente: imprime el número. Sourceado: sólo define la función.
case "${0##*/}" in
cpu-quota.sh) cpu_quota; printf '\n' ;;
esac
