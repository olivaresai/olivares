#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# with-pg-env.sh — run a test command with the Postgres test DSNs decided, and with the
# helper's EXIT STATUS actually checked.
#
# WHY THIS EXISTS RATHER THAN AN INLINE eval. Two separate defects, both measured:
#
#  1. `eval "$(bash scripts/pg-test-env.sh)"` reports the status of `eval`, not of the
#     command substitution. Measured 2026-07-25:
#         $ bash -c 'set -euo pipefail; eval "$(bash -c "echo :; exit 42")"; echo "$?"'
#         REACHED NEXT COMMAND with status 0
#     So a helper that died left every DSN unset and the suite carried on skipping the
#     Postgres legs behind a green check — reinstating the exact defect the helper was
#     written to close.
#
#  2. Only the pre-push hook evaluated the helper. A developer running `task test`
#     directly got no DSNs at all, so the canonical local entry point was still silently
#     partial. The decision belongs where the tests are run, not only in the hook.
#
# The warning the helper writes to stderr is deliberately NOT captured: it must reach the
# operator even when stdout is consumed by the caller.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

[ "$#" -gt 0 ] || {
	echo "usage: $0 <command> [args...]" >&2
	exit 2
}

if ! pg_exports="$(bash "$ROOT/scripts/pg-test-env.sh")"; then
	echo "::error::with-pg-env: scripts/pg-test-env.sh failed; refusing to run tests with an" >&2
	echo "unknown Postgres posture — an unset DSN is indistinguishable from a passing skip." >&2
	exit 1
fi

eval "$pg_exports"

# --- PARALELISMO DE PAQUETES BAJO -race, DERIVADO DEL CGROUP ----------------------------
#
# ⛔ EL PROBLEMA, medido el 2026-08-12 y mal atribuido durante meses. `go test` paraleliza
# POR PAQUETE hasta GOMAXPROCS. Esta caja declara 16 CPUs y el cgroup la capa a 9 GiB; un
# paquete instrumentado con -race pica ~1 GiB, así que el valor por defecto pide 16 GiB para
# una caja de 9 y el OOM killer siega paquetes ENTEROS — que es de dónde salía «el leg -race
# mata 19 paquetes con `signal: killed` y CERO asserts». No era el detector: era el ancho.
#
# Acotado, el leg entero de `./modules/...` —dos tercios del leg de workspace, que estaba
# diferido a un barrido SEMANAL— pasa aquí: **31 paquetes, 0 data races, 0 fallos y el
# contador `oom_kill` del cgroup sin moverse.**
#
# POR QUÉ AQUÍ Y NO EN EL Taskfile: la batería de `lint:pg-env` fija las líneas de comando
# del Taskfile como LITERALES ENTEROS (`scripts/test-pg-test-env.sh`), así que insertar un
# flag ahí ciega 42 de sus aserciones sin romper ningún invariante. Este envoltorio ya
# envuelve 7 de las 8 invocaciones -race y la batería no lo fija.
#
# POR QUÉ DERIVADO Y NO `-p 2`: 2 es el número de ESTA caja. Metido en un fichero
# compartido, envejece mal el día que alguien corra en una máquina con memoria de sobra.
# Se deriva del tope real y se acota por CPUs.
#
# ⚠ SÓLO cuando el comando lleva -race: los legs sin detector no piden esa memoria y
# estrecharlos los haría mucho más lentos sin ganar nada. Y NUNCA pisa un GOFLAGS explícito.
case " $* " in
	*" -race "*)
		if [ -z "${GOFLAGS:-}" ] && [ -r /sys/fs/cgroup/memory.max ]; then
			_wpe_cap="$(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo max)"
			case "$_wpe_cap" in
				''|max) : ;;  # sin tope: el defecto de go ya es correcto
				*[!0-9]*) : ;;  # ilegible: no adivino
				*)
					# ~4 GiB de holgura por paquete: reproduce el 2 medido en 9 GiB y
					# escala solo. Nunca menos de 1, nunca más CPUs de las que hay.
					_wpe_p=$(( _wpe_cap / 4294967296 ))
					[ "$_wpe_p" -lt 1 ] && _wpe_p=1
					# ⛔ NO `nproc`: lee la AFINIDAD, no la cuota. En esta caja dice 16 y la cuota es 4,
					#    asi que este tope —«nunca mas CPUs de las que hay»— NO MORDIA: lo unico que
					#    acotaba era la memoria de arriba. Salia bien por casualidad (14/4 = 3 <= 4).
					_wpe_cpus="$(bash "$(dirname "${BASH_SOURCE[0]:-$0}")/cpu-quota.sh" 2>/dev/null || nproc 2>/dev/null || echo 1)"
					[ "$_wpe_p" -gt "$_wpe_cpus" ] && _wpe_p="$_wpe_cpus"
					export GOFLAGS="-p=$_wpe_p"
					echo "with-pg-env: -race bajo un cgroup de $(( _wpe_cap / 1073741824 )) GiB → GOFLAGS=$GOFLAGS" >&2
					;;
			esac
		fi
		;;
esac

# --- SOFT GO RUNTIME MEMORY TARGET FOR COMMANDS WITHOUT -race -------------------------
#
# Hosted jobs observed on 2026-09-19 ended with a shutdown signal and exit 143,
# without a test-failure line. Those observations do not establish OOM as the cause.
# Local sampling found substantial root-package memory use, motivating this mitigation.
# The early comparisons were cut short; their sampled peaks and counts of tests started
# do not establish a completed-run memory bound or a marginal wall-clock cost.
#
# Public PR 67, run 35446229978 at 095e1ffbde44403d73534d4717ce2fae0bf3a0bb,
# completed once with GOMEMLIMIT=4191344640 (about 3.90 GiB). The root package took
# 2207.705 s under its 2700 s timeout. The witness's highest sample was 12.74 GiB
# used of 15.61 GiB for the HOST (MemTotal - MemAvailable), not root-process RSS.
# This is one completion with observed margins, not a paired causal comparison or
# evidence that future shutdowns cannot occur. Keep the witness on later candidates.
#
# GOMEMLIMIT targets memory managed by each Go runtime, not total process RSS or the
# aggregate host. It is soft: Go may exceed it to avoid excessive GC work. It does not
# prevent an OS-level OOM. See https://go.dev/doc/gc-guide#Memory_limit.
# The exported value also reaches Go toolchain subprocesses; compilation can be affected.
#
# Leave -race commands to the separate package-parallelism policy above, and preserve
# an explicit GOMEMLIMIT. Neither policy is a guarantee against memory exhaustion.
case " $* " in
	*" -race "*) : ;;  # el detector ya esta acotado por el ancho de paquetes de arriba
	*" test "*)
		if [ -z "${GOMEMLIMIT:-}" ]; then
			# Las DOS lecturas llevan su ruta en una variable, con la real por defecto. Es la
			# unica costura, y existe para que la bateria del envoltorio
			# (scripts/test-pg-test-env.sh) pueda apuntarlas a ficheros y ver la derivacion
			# —y su rama ilegible— sin un cgroup propio ni una maquina de 16 GiB.
			_wpe_cgroup_file="${OLIVARES_WPE_CGROUP_MAX:-/sys/fs/cgroup/memory.max}"
			_wpe_meminfo_file="${OLIVARES_WPE_MEMINFO:-/proc/meminfo}"
			# El tope real de la maquina: el del cgroup si lo hay, y si no la memoria fisica.
			_wpe_bytes=""
			if [ -r "${_wpe_cgroup_file}" ]; then
				_wpe_bytes="$(cat "${_wpe_cgroup_file}" 2>/dev/null || true)"
			fi
			case "${_wpe_bytes:-max}" in
				''|max|*[!0-9]*)
					# Sin cgroup con tope: MemTotal, que viene en KiB.
					#
					# ⛔ LA MULTIPLICACION SE HACE EN EL SHELL Y NO EN awk, y esto lo cazo la
					# bateria de este envoltorio, no una revision. `awk '{print $2 * 1024}'`
					# imprime **1,71799e+10** para una maquina de 16 GiB —notacion cientifica
					# por OFMT y coma decimal por el locale— y `printf "%d"` la recorta a
					# 2147483647. Las dos formas fallan la comprobacion de digitos de este
					# mismo `case`, asi que el techo NO se ponia: inerte EXACTAMENTE en la
					# maquina SIN tope de cgroup, que es el runner alojado al que va dirigido.
					# Medido 2026-09-19.
					_wpe_kib="$(awk '/^MemTotal:/ {print $2; exit}' "${_wpe_meminfo_file}" 2>/dev/null || true)"
					case "${_wpe_kib:-}" in
						''|*[!0-9]*) _wpe_bytes="" ;;
						*) _wpe_bytes=$(( _wpe_kib * 1024 )) ;;
					esac
					;;
			esac
			if [ -n "${_wpe_bytes}" ] && [ "${_wpe_bytes}" -gt 0 ]; then
				# One quarter of the detected memory, with a 2.5 GiB floor. This is a
				# per-runtime target inherited by tests and toolchain subprocesses, not
				# a reservation or an exemption for compilation. Exactly 16 GiB gives
				# 4 GiB; the diagnostic's integer GiB display truncates other values.
				_wpe_limit=$(( _wpe_bytes / 4 ))
				[ "${_wpe_limit}" -lt 2684354560 ] && _wpe_limit=2684354560
				export GOMEMLIMIT="${_wpe_limit}"
				echo "with-pg-env: sin -race sobre una maquina de $(( _wpe_bytes / 1073741824 )) GiB → GOMEMLIMIT=${_wpe_limit} ($(( _wpe_limit / 1073741824 )) GiB)" >&2
			fi
		fi
		;;
esac

exec "$@"
