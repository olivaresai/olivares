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

# --- TECHO DE MEMORIA DEL BINARIO DE PRUEBAS, SIN DETECTOR ------------------------------
#
# ⛔ EL PROBLEMA, medido el 2026-09-19 sobre el paquete raiz. Tres jobs alojados murieron
# dentro de esta misma pata con «The runner has received a shutdown signal» y exit 143 —dos
# turnos de `pr-test-shard (b)` y el paso funcional de `control-plane`— tras 19, 29 y 45
# minutos de silencio, sin una linea de fallo. No era disco (la propia maquina publico 85798
# MiB libres de 147718), ni procesos huerfanos (el arbol nunca paso de 3), ni CPU (el binario
# gasto 126 s de CPU en 1734 s de pared, un 7 % de UN nucleo). Era memoria, y de UN solo
# proceso: el binario de pruebas del paquete raiz crece a escalones —3,3 GiB al minuto 6,
# 6,2 GiB al minuto 18, **8,7 GiB** de pico— y no devuelve nada. Al lado de la segunda tanda
# de paquetes, de una compilacion y de los dos contenedores de base de datos que el job
# declara, eso no cabe en los 16 GiB del runner; que quepa o no depende de si el pico
# coincide con el de otro paquete, y de ahi que muera unas veces si y otras no.
#
# ⛔ Y NO ES EL PARALELISMO: con `-parallel 2` el pico es 6,3 GiB y con `-parallel 16` es 6,8
# GiB en la misma caja. Lo que crece es lo que el binario RETIENE, no cuantas pruebas corren
# a la vez, asi que estrechar el ancho no lo toca. `GOMEMLIMIT` si: es un tope BLANDO, el
# recolector trabaja mas pero nada muere por pasarse. Medido en la misma caja y a la misma
# hora, dos ejecuciones del paquete raiz una al lado de otra: sin techo **8693 MiB** de pico;
# con techo de 2 GiB **4528 MiB**, y a los 1212 s llevaban 2872 y 2836 pruebas arrancadas
# —un 1,2 % de diferencia—, asi que el coste en tiempo es marginal.
#
# ⛔ SOLO SIN `-race`: el detector tiene su propio ancho arriba y su memoria de sombra es
# legitima; apretarle el techo lo haria recolectar sin parar. Y NUNCA pisa un GOMEMLIMIT
# explicito: quien lo declara sabe mas de su caja que este fichero.
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
				# UN CUARTO del tope, con suelo de 2,5 GiB. El suelo esta POR ENCIMA del
				# proceso mas grande del propio toolchain sobre este mismo paquete —compile
				# 2282 MiB, link 1712 MiB, medidos— asi que el techo cae sobre el binario de
				# pruebas y nunca sobre la compilacion. En el runner de 16 GiB da 4 GiB.
				_wpe_limit=$(( _wpe_bytes / 4 ))
				[ "${_wpe_limit}" -lt 2684354560 ] && _wpe_limit=2684354560
				export GOMEMLIMIT="${_wpe_limit}"
				echo "with-pg-env: sin -race sobre una maquina de $(( _wpe_bytes / 1073741824 )) GiB → GOMEMLIMIT=${_wpe_limit} ($(( _wpe_limit / 1073741824 )) GiB)" >&2
			fi
		fi
		;;
esac

exec "$@"
