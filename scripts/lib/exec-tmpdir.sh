# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# exec-tmpdir.sh — source this to choose an executable temporary directory for the engine.
# The engine extracts and runs connector plugins in TMPDIR (cmd/olivares/boot.go:1423,
# os.MkdirTemp("", "olivares-connectors-")). In these containers noexec /tmp causes
# fork/exec permission denied despite executable mode, so source reload fails.
# Measured 2026-08-30 while investigating empty /adoption captures.
# Probe execution by writing and running a one-line script, not by mount-name guesses
# or /proc/mounts parsing. Candidate directories must be outside the repository,
# where another git add -A or cleanliness gate could collect temporary files.
#
# Usage:
#     . "$ROOT/scripts/lib/exec-tmpdir.sh"
#     EXEC_TMP="$(olivares_exec_tmpdir)" || {
#       # Tried paths have already been printed to stderr; print the remedy here.
#       echo "No candidate executes. Set OLIVARES_EXEC_TMPDIR to an executable directory." >&2
#       exit 2  # Or explicitly report continuing without connectors if supported.
#     }
#     TMPDIR="$EXEC_TMP" "$BIN" serve …
#
# The old `|| EXEC_TMP=""` example allowed ${EXEC_TMP:-${TMPDIR:-/tmp}} to fall back
# to the noexec directory (the reviewer). All four launchers already refused with rc 2;
# only this example and the capture harness retained the unsafe pattern.
# Return the path on stdout with rc 0, or rc 1 if no candidate executes. The caller
# must report failure. Print attempted paths on stderr, not a variable: calls inside
# $(...) run in a subshell, so assigned variables disappear; stderr reaches the caller.
#
# Scope correction, 2026-08-30: build-bin.sh does not build connectors; the Taskfile's
# build:connectors dependency does. Clean firstparty/bins holds only PLACEHOLDER, but
# docs-captures.sh explicitly builds connectors before the binary, so execution here
# determines whether /adoption has data. Other callers can also inherit embedded plugins
# from a previous build:connectors in that worktree.

olivares_exec_tmpdir() {
	local raiz cand
	# `$1` deja a quien llama proponer su propia raiz; si no, se usa la hermana del repositorio.
	raiz="${1:-$(dirname "${ROOT:-$PWD}")}"
	# Se rearma en CADA llamada: si no, una segunda llamada acumularia las rutas de la primera y
	# el diagnostico nombraria candidatos que esta corrida no miro.
	local probados=""
	for cand in "${OLIVARES_EXEC_TMPDIR:-}" "$raiz/.olivares-exec-tmp"; do
		[ -n "$cand" ] || continue
		probados="${probados:+$probados }$cand"
		mkdir -p "$cand" 2>/dev/null || continue
		# Use unique probe names (the reviewer): shared names collided between chmod and execve,
		# with only 3 successes out of 50 concurrent calls. Remove the probe before every
		# exit from the loop body, including success and failed printf (full disk or SIGPIPE
		# rc 153), and remove stale residue before starting (A-01).
		# mktemp supplies uniqueness. A .probe-exec.$$ fix still failed 29 of 30 concurrent
		# calls because $$ names the parent shell, not the command-substitution subshell.
		local probe
		probe="$(mktemp "$cand/.probe-exec.XXXXXX" 2>/dev/null)" || continue
		if ! printf '#!/bin/sh\nexit 0\n' >"$probe" 2>/dev/null; then
			rm -f "$probe" 2>/dev/null
			continue
		fi
		if ! chmod +x "$probe" 2>/dev/null; then
			rm -f "$probe" 2>/dev/null
			continue
		fi
		if "$probe" 2>/dev/null; then
			rm -f "$probe" 2>/dev/null
			# ⛔ SUBDIRECTORIO PROPIO DE ESTA CORRIDA, no la raiz compartida. Dos arneses a la vez
			#    no se pisan los plugins, y —lo que de verdad importa— quien llama PUEDE borrar lo
			#    suyo sin llevarse lo de otro. Sin esto, la raiz acumula una extraccion por corrida
			#    y nadie la limpia nunca: en una caja al 93 % de disco eso es una fuga lenta.
			# ⛔ LA PURGA VA AQUI, EN LA CREACION, PORQUE UNA LIMPIEZA QUE HAY QUE ACORDARSE DE HACER
			#    NO ES UN MECANISMO. El comentario de arriba llevaba desde su primer dia diciendo
			#    «nadie la limpia nunca … es una fuga lenta» y describiendo un defecto en vez de
			#    cerrarlo: DOCE `run-*` huerfanos medidos el 2026-08-30. Es el modelo que esta casa ya
			#    usa con `go-build`: quien pasa por aqui adelanta la purga.
			#
			#    DOS CRIBAS, Y LA SEGUNDA NO ES OPCIONAL. La edad primero: 12 h, que no es cifra
			#    heredada sino margen sobre la corrida mas larga MEDIDA (el gate pesado, 3 h 37). Y
			#    despues un TESTIGO DE QUE NADIE LO USA, porque la edad es una SUPOSICION sobre la
			#    duracion y no una garantia: un lector midio que un `run-*` de mas de 12 h CON un
			#    proceso dentro se borraba igual y el proceso quedaba apuntando a `(deleted)`. Matar
			#    trabajo vivo es el unico fallo que una purga no se puede permitir — y cuando limpie
			#    este almacen A MANO si mire procesos y descriptores: le aplique al mecanismo un
			#    estandar mas flojo que a mi.
			#
			#    El /proc se lee UNA vez y con `ls -l`, que resuelve los enlaces en C: un bucle de
			#    shell costaba 11,6 s por directorio —inaceptable aqui—; asi son 104 ms para 2.432
			#    enlaces. Se compara la ruta ENTERA tras `-> `, no un prefijo: `run-AA` no casa con
			#    `run-AABB`.
			#
			#    ⛔ Y EL TESTIGO DE /proc TIENE UN PUNTO CIEGO DE CLASE, no de permisos: un SOCKET vivo
			#    aparece en `/proc/<pid>/fd` como `socket:[<inodo>]` y NUNCA con su ruta, asi que
			#    comparar rutas no lo ve — y su dueño tampoco tiene por que estar ahi con el `cwd`.
			#    Medido sobre el socket del bus de esta sesion: 0 enlaces con la ruta, 535 con la forma
			#    `socket:[…]`. Borrar el NOMBRE no rompe ningun descriptor abierto: rompe que a uno lo
			#    puedan ENCONTRAR, y ese fallo es mudo en los dos extremos. Por eso hay una segunda
			#    guarda que no depende de /proc: si el directorio contiene un socket, no se toca.
			#    (Un carril de esta caja se quedo incomunicado del bus exactamente asi.)
			#
			#    LIMITES que QUEDAN, dichos: solo se ven los procesos cuyos enlaces podemos leer, y la
			#    guarda de socket mira el arbol del candidato, no otras formas de uso sin ruta.
			_olv_viejos="$(find "$cand" -maxdepth 1 -type d -name 'run-*' -mmin +720 2>/dev/null)"
			if [ -n "$_olv_viejos" ]; then
				_olv_uso="$(ls -l /proc/[0-9]*/cwd /proc/[0-9]*/fd/* 2>/dev/null)"$'\n'
				while IFS= read -r _olv_d; do
					[ -n "$_olv_d" ] || continue
					case "$_olv_uso" in
					*"-> $_olv_d"$'\n'* | *"-> $_olv_d/"*) continue ;;
					esac
					# La guarda que /proc no puede dar: un socket vivo no expone su ruta en ningun fd.
					[ -z "$(find "$_olv_d" -type s -print -quit 2>/dev/null)" ] || continue
					rm -rf "$_olv_d" 2>/dev/null || true
				done <<EOF
$_olv_viejos
EOF
				unset _olv_viejos _olv_uso _olv_d
			fi
			local propio
			propio="$(mktemp -d "$cand/run-XXXXXX" 2>/dev/null)" || continue
			printf '%s' "$propio"
			return 0
		fi
		rm -f "$probe" 2>/dev/null
	done
	printf 'exec-tmpdir: no candidate supports execution. Tried: %s\n' "${probados:-<none>}" >&2
	return 1
}
