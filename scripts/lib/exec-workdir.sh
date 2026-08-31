# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# exec-workdir.sh — SOURCE THIS when a battery needs scratch space whose EXECUTE BIT IS
# REAL. Not executable, not a program: it exists to be `.`-sourced and defines one function.
#
# THE ENVIRONMENT FACT (measured 2026-08-04, container in use by every lane):
#
#     findmnt -no OPTIONS /tmp  ->  rw,nosuid,nodev,noexec,relatime,size=2097152k,…
#
# /tmp is mounted `noexec`, so the obvious `mktemp -d -t` hands back a directory where
# every execve dies. A battery that stands up a shim there runs the REAL tool instead and
# passes for the wrong reason — which is how this was first found, in
# scripts/test-check-secrets.sh case 5.
#
# THE SECOND EDGE, measured 2026-08-07 and the reason this stopped being one battery's
# private helper. It is not only execve that breaks:
#
#     D=$(mktemp -d -p /tmp …); printf '#!/bin/sh\nexit 7\n' >"$D/h"; chmod +x "$D/h"
#     [ -x "$D/h" ]   ->  FALSE          # the bit IS set; the mount hides it
#     "$D/h"          ->  rc=126
#
# `test -x` itself answers FALSE on a noexec mount. So a gate that checks whether a HOOK is
# executable — scripts/check-hooks-path.sh — cannot be exercised from /tmp at all: its
# clean case would go red because the bit reads false, and its not-executable case would go
# green for a reason that has nothing to do with the code under test. Both directions
# broken, one of them silently.
#
# Two consumers now depend on this fact, which is exactly why it lives in one file: a fix
# applied to a copy is a fix the other copy does not get.
#
# Usage:
#   HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)
#   . "$HERE/lib/exec-workdir.sh"
#   WORK="$(olivares_pick_exec_workdir my-battery)" || { …; exit 2; }   # exit 2, never a pass

# Try candidates in order and PROVE each one can run a file before using it. Prints the
# directory on stdout and returns 0; returns 1 if none qualifies — which the caller must
# treat as "could not run the battery", never as a pass.
#
# Order matters: the CI runner's own temp first, this container's exec-capable scratch LAST
# (creating /workspace on a CI runner would be rude, and there it simply fails and falls
# through to nothing).
# ⛔ SEGUNDA PREGUNTA, OPT-IN: ¿lo puede atravesar OTRO uid? Medido el 2026-08-19 en
# ci-runner-7: la sonda de abajo se ejecuta perfectamente —somos el dueño— y aun asi el motor
# no pudo lanzar su plugin, porque `/home/runner` es `-rwx------` y plugjail baja el hijo a un
# uid dedicado no-root que no puede ATRAVESARLO. La sonda respondia por el usuario equivocado.
#
# El nucleo del asunto: «puedo ejecutar aqui» y «puede ejecutar aqui el uid al que voy a bajar»
# son preguntas DISTINTAS, y la segunda depende de ancestros que no creamos ni poseemos.
#
# Va como segundo argumento y no por defecto porque exigirlo siempre descartaria candidatos
# perfectamente buenos para los gates que NO cambian de identidad, que son casi todos.
olivares_dir_traversable_by_others() {
	local d="$1" p modo
	p="$(cd "$d" 2>/dev/null && pwd -P)" || return 1
	while :; do
		modo="$(stat -c '%a' "$p" 2>/dev/null)" || return 1
		case "$modo" in
		*[13579]) : ;; # el ultimo digito impar lleva el bit x para «otros»
		*) return 1 ;;
		esac
		[ "$p" = "/" ] && return 0
		p="$(dirname "$p")"
	done
}

olivares_pick_exec_workdir() {
	local tag="${1:-olivares-battery}"
	local exige_otros="${2:-}"
	local cand d rc
	# El override explicito va PRIMERO y sigue pasando la prueba de ejecucion: quien lo fija
	# esta eligiendo el volumen, no saltandose la comprobacion. Vacio por defecto, asi que los
	# llamantes que no lo usan no notan nada.
	# /var/tmp entra el 2026-08-19 y no es relleno: es un MONTAJE DISTINTO de /tmp. Los dos son
	# 1777 —atravesables por cualquiera, que es la mitad que el jail necesita—, pero un host puede
	# montar /tmp noexec y dejar /var/tmp normal, que es justo el caso que dejaria sin candidato a
	# un runner cuyo RUNNER_TEMP cuelga de un $HOME 0700. Va DESPUES de /tmp porque es persistente
	# entre reinicios y por tanto peor sitio para basura, no porque sirva menos.
	for cand in "${OLIVARES_GATE_BINDIR:-}" "${RUNNER_TEMP:-}" "${TMPDIR:-}" /tmp /var/tmp "${HOME:-}" /workspace/.olivares-tmptest; do
		[ -n "$cand" ] || continue
		mkdir -p "$cand" 2>/dev/null || continue
		d="$(mktemp -d -p "$cand" "${tag}.XXXXXX" 2>/dev/null)" || continue
		# mktemp -d crea 0700, y aqui dentro se lanzan binarios que pueden cambiar de uid: el
		# motor, cuando corre como root, arranca cada plugin bajo un uid dedicado no-root, y
		# ese hijo no puede ATRAVESAR un 0700 ajeno para llegar a su propio ejecutable. 0711
		# concede el paso sin conceder el listado; nada se vuelve legible.
		chmod 711 "$d" 2>/dev/null || true
		printf '#!/bin/sh\nexit 7\n' >"$d/.exectest" 2>/dev/null && chmod +x "$d/.exectest" 2>/dev/null
		# BOTH legs, because the two failure modes are different and only one is loud:
		# `test -x` reading false is what breaks a permission-bit assertion, and a failed
		# execve is what breaks a shim. A directory has to pass both to be usable here.
		if [ -x "$d/.exectest" ]; then
			# ⛔ EL 7 NO PUEDE MATAR AL LLAMANTE. La sonda sale 7 a proposito —un codigo que
			# ningun fallo produce por casualidad— pero bajo `set -e` ese 7 aborta la subshell
			# de `$(...)` y la funcion devuelve vacio: el llamante concluye «no hay directorio
			# ejecutable» teniendo uno perfectamente bueno. Medido 2026-08-18 con
			# check-install-manifest.sh, que corre con `set -e` bajo `sh`: la sonda se ejecutaba
			# CORRECTAMENTE y aun asi el gate contestaba 2.
			rc=0
			"$d/.exectest" 2>/dev/null || rc=$?
			if [ "$rc" -eq 7 ]; then
				if [ "$exige_otros" = "--traversable-by-others" ] &&
					! olivares_dir_traversable_by_others "$d"; then
					# Ejecuta para NOSOTROS y no para el uid enjaulado. Descartar y seguir es
					# lo correcto: elegirlo produciria un EACCES que se lee como noexec.
					rm -rf "$d"
					continue
				fi
				rm -f "$d/.exectest"
				printf '%s' "$d"
				return 0
			fi
		fi
		rm -rf "$d"
	done
	return 1
}
