# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# exec-tmpdir.sh — SOURCE THIS para elegir un directorio temporal que EJECUTE, y pasarselo al motor.
#
# ⛔ POR QUE EXISTE. El motor extrae sus plugins de conector a `$TMPDIR` y los LANZA:
#    `cmd/olivares/boot.go:1423`, `os.MkdirTemp("", "olivares-connectors-")`. En estos contenedores
#    `/tmp` esta montado **noexec** (`/proc/mounts`: `rw,nosuid,nodev,noexec,…`), asi que el
#    `fork/exec` falla **con el bit de ejecucion puesto** y el motor rechaza la fuente en la recarga:
#    «the connector plugin could not be launched: … permission denied». Medido el 2026-08-30
#    persiguiendo por que `/adoption` salia a cero en las capturas.
#
# ⛔ Y NO SE DEDUCE DE LA RUTA: hay cajas donde `/tmp` ejecuta y cajas donde no. Por eso esto no
#    comprueba el nombre del punto de montaje ni parsea `/proc/mounts` —que diria lo que el kernel
#    cree, no lo que le pasa a un `execve`—: **escribe un guion de una linea y lo corre**. Un control
#    se verifica viendolo actuar.
#
# ⛔ NINGUN CANDIDATO DENTRO DEL REPOSITORIO. Un directorio sin trackear en el arbol lo barre el
#    primer `git add -A` ajeno o lo cuenta un gate de limpieza; en esta casa esa mina ya mato un gate.
#
# Uso:
#     . "$ROOT/scripts/lib/exec-tmpdir.sh"
#     EXEC_TMP="$(olivares_exec_tmpdir)" || {
#       # la lista de candidatos ya salio por stderr; aqui solo el remedio
#       echo "⛔ ninguno EJECUTA. Exporta OLIVARES_EXEC_TMPDIR a un dir que ejecute." >&2
#       exit 2   # …o seguir, si quien llama puede vivir sin conectores — pero DICIENDOLO
#     }
#     TMPDIR="$EXEC_TMP" "$BIN" serve …
#
# ⛔ ESTE EJEMPLO ENSENABA `|| EXEC_TMP=""` Y ESO ERA UN PIE PARA EL FALLO (the reviewer). Con esa
#    forma, el `${EXEC_TMP:-${TMPDIR:-/tmp}}` de la linea siguiente cae de vuelta al `/tmp`
#    **noexec** que este fichero existe para evitar, y el arnes arranca como si nada. Los cuatro
#    lanzadores nunca lo hicieron —rehusan con rc 2—, asi que el ejemplo contradecia a todos sus
#    usuarios: la unica copia del patron malo que quedaba viva estaba AQUI, en la documentacion, y
#    en el arnes de capturas, que ni siquiera usaba esta libreria.
#
# Devuelve la ruta por stdout y rc 0; rc 1 si NINGUN candidato ejecuta —y entonces quien llama lo
# DICE, porque un silencio ahi se lee como «no hacia falta».
#
# ⛔ Y AL FALLAR IMPRIME POR **STDERR** LAS RUTAS QUE PROBO: un diagnostico que dice «ninguno
#    ejecuta» sin decir CUALES no se puede accionar — quien lo lee no sabe si el
#    `OLIVARES_EXEC_TMPDIR` que exporto llego siquiera a mirarse.
#
#    ⛔ POR STDERR Y NO POR UNA VARIABLE, y lo escribo porque mi primera version fue la variable y
#       NO FUNCIONABA: a esta funcion se la llama dentro de `$(...)`, que es un SUBSHELL, asi que
#       cualquier variable que ponga aqui muere con el. El diagnostico habria dicho «<ninguno>»
#       SIEMPRE, en todas las cajas, sin que nada fallara ruidosamente. `stderr` no lo captura la
#       sustitucion de comandos, asi que sale al terminal por su cuenta. Es la misma familia que
#       el `$$` de mas abajo: el estado no cruza la frontera que uno cree.
#
# ⚠ Alcance, corregido el 2026-08-30 porque lo tenia demasiado tranquilo. Es cierto que
#   `build-bin.sh` no construye los conectores (la dependencia `build:connectors` vive en el
#   Taskfile) y que en un arbol limpio `cmd/olivares/firstparty/bins/` solo tiene `PLACEHOLDER`.
#   Pero `scripts/docs-captures.sh` —el arnes que dispara las capturas del acto— llama a
#   `build-connectors.sh` EXPRESAMENTE antes del binario, asi que ahi los plugins SI estan
#   embebidos y esto NO es un seguro: es la diferencia entre `/adoption` con datos y a cero.
#   Para los demas llamadores sigue siendo un seguro, y ademas el dir de embed es un residuo del
#   arbol: si alguien corrio `build:connectors` antes en ese worktree, el `go build` los embebe.

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
		# ⛔ LA SONDA LLEVA EL PID EN EL NOMBRE (the reviewer): con un nombre COMPARTIDO, dos arneses
		#    simultaneos se pisaban — medido, 50 llamadas concurrentes dieron 3 exitos y 47 fallos,
		#    porque cada una borraba la sonda de la otra entre el `chmod` y el `execve`. Un control
		#    que falla por concurrencia no es un control: es una loteria.
		# ⛔ Y SE RETIRA EN TODOS LOS CAMINOS, no solo en los dos que se me ocurrieron
		#    (the reviewer, A-01). Un fallo DURANTE el `printf` —disco lleno, rc 153 por SIGPIPE— dejaba
		#    `.probe-exec.$$` colgado porque el `continue` salia sin borrarlo. Se borra ANTES de cada
		#    salida del cuerpo, incluida la de exito, y ademas al entrar: un residuo de una corrida
		#    anterior no puede hacerse pasar por sonda buena.
		# ⛔ EL NOMBRE LO DA `mktemp`, NO `$$`, Y ESO LO DECIDIO UNA MEDIDA. La cura sugerida era
		#    `.probe-exec.$$`, la puse, y con 30 llamadas simultaneas siguieron fallando 29: **`$$`
		#    es el PID del shell PADRE, no del subshell**, asi que las treinta compartian nombre
		#    exactamente igual que antes. `mktemp` no supone nada sobre quien llama.
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
	printf 'exec-tmpdir: ningun candidato EJECUTA. Probados: %s\n' "${probados:-<ninguno>}" >&2
	return 1
}
