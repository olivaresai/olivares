#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-gate-parity.sh — a repository gate. Que patas corren en el gancho, cuales en mainline-ci,
# y cuales en UNA SOLA de las dos. Las dos direcciones importan y por motivos distintos:
#
#   SOLO-CI     el push no las ve: un fallo se descubre despues de aterrizar.
#   SOLO-GANCHO mainline-ci no las ve: un rojo deja `main` envenenado y entonces TODA
#               rama de TODAS las cajas muere ahi, cada una descubriendolo a la hora.
#               Medido el 2026-08-29: tres de las cuatro muertes del lote del candidato
#               fueron esto (phone-home-claims, int-12-no-land, list-ceilings).
#
# Las dos listas se DERIVAN de sus fuentes y se comparan contra un registro. No es un
# derivador puro a proposito: el registro obliga a que un cambio de paridad sea una
# DECISION escrita, no una deriva silenciosa.
#
# Tres respuestas: 0 coincide · 1 la paridad cambio sin actualizar el registro · 2 no
# he podido mirar.
set -uo pipefail

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || { echo "check-gate-parity: NO HE PODIDO MIRAR — no puedo entrar en $ROOT" >&2; exit 2; }

HOOK="${OLIVARES_HOOK:-.githooks/pre-push}"
CI="${OLIVARES_CI:-.github/workflows/mainline-ci.yml}"
TASKFILE="${OLIVARES_TASKFILE:-Taskfile.yml}"
REG="${OLIVARES_PARITY_REG:-design/GATE-PARITY-2026-08-29.md}"

blind() { echo "check-gate-parity: NO HE PODIDO MIRAR — $*" >&2; exit 2; }
[ -r "$HOOK" ] || blind "no puedo leer $HOOK"
[ -r "$CI" ]   || blind "no puedo leer $CI"
[ -r "$TASKFILE" ] || blind "no puedo leer $TASKFILE (hace falta para saber que nombre ES una tarea)"
# El registro se comprueba MAS ABAJO, no aqui: `--print` es el modo con el que se CREA
# el registro por primera vez, y exigirlo antes lo haria imposible de arrancar.

# --- derivacion 1a: lo que el gancho DECLARA en su rotulo ---------------------------
# Del rotulo `pre-push: FAST lints (…)`, que es lo que el gancho promete al operador.
# NO de los `task lint:` ejecutados EN UNA CORRIDA: un push en curso ha corrido solo una
# parte, y medir eso da 25 en vez de 168 — con aspecto de resultado. Es el mutante 2.
hook_rotulo() {
	sed -n '/pre-push: FAST lints (/,/)\./p' "$HOOK" |
		sed 's/^[[:space:]]*echo "pre-push: *//; s/"$//; s/FAST lints (//; s/)\.$//' |
		tr '+' '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' |
		command grep -E '^[a-z0-9][a-z0-9:_-]*$' | command grep -E '[a-z]' | sort -u
}

# --- derivacion 1b: lo que el gancho INVOCA, que es otra cosa -----------------------
# ⛔ ESTA DERIVACION FALTABA Y POR ESO LA LISTA MENTIA. La comparacion se hacia contra el
#    rotulo, que es el carril RAPIDO, mientras la lista se rotulaba «SOLO-CI: el push no
#    las ve». Medido el 2026-08-29 sobre 8eee6c362: OCHO de las 34 «solo-CI» las invoca el
#    gancho DIRECTAMENTE en su carril PESADO, ausentes las ocho del rotulo:
#
#      build:go pre-push:1784 · format-ratchet :1759 · guide-docs :1763 · sdk:check :1895
#      test:cloud:norace :1742 · test:license-worker :1697 · tokens:check :1751
#      web:check :1783
#
#    El push SI las ve. La etiqueta prometia ejecucion y el predicado media declaracion —
#    la misma clase de defecto que retracto en su censo, con otra causa: el se quedo
#    corto por el GRAFO de tareas, yo por el CARRIL.
#
#    Y NO se usa el cierre transitivo de `task --dry` aqui, aunque sea la autoridad para
#    «que baterias ejercita el gancho». Medido antes de decidirlo: el cierre añade 11
#    tareas sobre la invocacion directa y NINGUNA de las 4 que no son `:selftest`
#    (build:web, lint:migration-contiguity, openapi:dump, sdk:generate) esta en SOLO-CI.
#    El grafo no mueve este veredicto, y meterlo haria depender el gate de go-task y de
#    182 subprocesos. Se descarta por medida, no por comodidad.
#
#    ⛔ Y EL SUFIJO CUENTA: `task X || true` es una invocacion. Un borrador exigia fin de
#    linea y perdia `lint:test-hook-parallelism` (:1432) — que el rotulo SI promete, asi
#    que el agujero se veia como «el rotulo miente» cuando el mentiroso era mi regex.
# ⛔ `[a-z0-9]` TRAS `task `, Y EL GUION SIGUE FUERA A PROPOSITO. Dos cosas distintas que se
#    arreglan con el mismo caracter:
#      · un nombre de tarea PUEDE empezar por digito (`0022-published-schema`), y si alguien lo
#        invoca sin el prefijo `lint:` el patron viejo `[a-z]` no lo veia. Hoy no ocurre —cero
#        casos en el gancho— pero es la misma clase que ya dejo el registro sin poder guardarlo.
#      · un nombre NUNCA empieza por `-`. Midio que su extractor se tragaba
#        `task --list-all` (`.githooks/pre-push:390`), que es una BANDERA, y le inflaba el censo
#        en uno: 184 en vez de 183. Excluir el guion en la CLASE es el remedio; una lista de
#        excepciones con `--list-all` dentro caduca con la siguiente bandera que alguien escriba.
#
#    Y el rango del rotulo se acota al bloque `FAST lints (…)` POR ESTO: leer todas las lineas
#    `^echo "pre-push:` mete `test` desde `:1555` —«running the FULL gate locally (build + test +
#    web)»—, que es el gate completo y no el rotulo rapido. Medido: acotado 170, sin acotar 171.
hook_invoca() {
	sin_comentarios "$HOOK" |
		# ⛔ LAS DOS FORMAS, Y LA SEGUNDA ES UN PUNTO CIEGO MEDIDO. Hasta hoy este predicado exigia
		#    que la linea EMPEZARA por `task`, asi que una pata invocada como `VAR=1 task lint:x` le
		#    era INVISIBLE — y a las tres sondas de recuento del CLAUDE.md, tambien. El propio arbol
		#    lo anticipaba («el dia que alguien escriba `VAR=1 task lint:algo`, las tres contaran de
		#    menos y ninguna avisara») y el 2026-09-02 ocurrio: al integrar las patas advisory de
		#    observacion de red el recuento cayo de 223 a 222 en un hunk SIN conflicto, sin aviso y
		#    sin que ninguna cifra lo delatara — lo cazo un `comm` entre los dos conjuntos.
		#    La cura NO es prohibir el prefijo: es una forma legitima, la diseno el carril de red y
		#    su propio verificador la EXIGE. La cura es que el contador la vea.
		command grep -oE '^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*task [a-z0-9][a-z0-9:_-]+' |
		sed 's/^[[:space:]]*//; s/^\([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]*\)*//; s/^task //; s/^lint://' | sort -u
}

# ⛔ EL PREFIJO DE ENTORNO TAMBIEN AQUI. `hook_invoca` ya lo aceptaba; esta derivacion no, y era
# la ultima ciega del fichero. Una pata escrita `VAR=1 task lint:x || true` se le escapaba, o sea
# que el informe la habria dado por BLOQUEANTE cuando no lo es — un dato falso sobre la unica
# propiedad que esta funcion existe para medir. Hoy no hay ninguna asi, y por eso se arregla hoy:
# cuando la haya, el fallo sera silencioso y en el lado comodo. El gancho de `origin/main` ya trae
# dos patas con prefijo (`OLIVARES_NETWORK_ADVISORY=1 task lint:session-numbers` y
# `… lint:hub-web-fidelity`), asi que la forma dejo de ser hipotetica el 2026-09-02.
#
# Las patas invocadas con `|| true` CORREN pero no pueden rechazar el push. No es dejadez
# y no se marca como hallazgo: `pre-push:1419-1431` razona la unica que hay hoy
# (lint:test-hook-parallelism) con su contraste, su precedente y su salvedad. Se marca en
# la salida porque «corre en el gancho» y «puede parar el push» no son la misma relacion
# de paridad con un contexto REQUERIDO de CI.
hook_no_bloqueante() {
	sin_comentarios "$HOOK" |
		command grep -oE '^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*task [a-z0-9][a-z0-9:_-]+[[:space:]]*\|\|[[:space:]]*true' |
		sed 's/^[[:space:]]*//; s/^\([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]*\)*//; s/^task //; s/[[:space:]]*||[[:space:]]*true//; s/^lint://' | sort -u
}

# El sujeto de la comparacion: TODO lo que el gancho corre, rotulado o no.
# ⛔ LA SINTAXIS DEL GANCHO SE DECLARA Y SE IMPONE, en vez de ensanchar otra expresion regular.
#
# Los extractores de arriba reconocen TEXTO, no ordenes de shell, y el contraste `sol max` enumero
# lo que eso deja fuera: `A='dos palabras' task lint:x` (el valor con espacios corta el grupo),
# `env A=1 task lint:x` (la linea ya no empieza por la asignacion), y un `A=1 task lint:x` dentro
# de un heredoc (que se contaba). El heredoc lo cierra `sin_heredocs`; los otros dos no los cierra
# ningun regex mas ancho — los cierra **no permitirlos**.
#
# Y se puede: el gancho es NUESTRO fichero y sus invocaciones son nuestras. La forma canonica es
#
#     [VAR=valor-sin-espacios ...] task <nombre> [|| true]
#
# Cualquier linea que INVOQUE `task` en posicion de orden y no case con eso se rechaza NOMBRANDOLA,
# porque una forma que el censo no sabe representar es una pata que el censo no cuenta — y una pata
# no contada no aparece en ninguna lista ni en ningun rojo. Medido el 2026-09-03 sobre el gancho de
# hoy: cero rechazos, asi que esto no pide reescribir nada; existe para que la clase no reviva.
forma_canonica() {
	local malas
	# ⛔ EL DETECTOR DE CANDIDATAS ES MAS ANCHO QUE LA FORMA CANONICA, y tiene que serlo: si usara el
	# mismo predicado, las formas que el censo no ve tampoco serian candidatas y no se rechazaria
	# NINGUNA — el control seria un adorno. Se busca cualquier linea con `task <nombre>` que no sea
	# una linea de impresion (`echo`/`printf`/`say`, donde el nombre viaja como TEXTO, p.ej. el
	# rotulo del gancho) ni un comentario. Lo que no case con la forma canonica, se rechaza.
	malas="$(sin_comentarios "$HOOK" | command grep -nE '(^|[;&|]|[[:space:]])task[[:space:]]+[a-z0-9][a-z0-9:_.-]*' |
		command grep -vE '^[0-9]+:[[:space:]]*(echo|printf|say|#)' |
		command grep -vE '^[0-9]+:[[:space:]]*([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*task[[:space:]]+[a-z0-9][a-z0-9:_.-]*([[:space:]]*\|\|[[:space:]]*true)?[[:space:]]*$' || true)"
	[ -n "$malas" ] || return 0
	say "check-gate-parity: HALLAZGO — invocacion(es) de \`task\` que el censo NO sabe representar:" >&2
	printf '%s\n' "$malas" | sed 's/^/    /' >&2
	say "  La forma canonica es: [VAR=valor-sin-espacios ...] task <nombre> [|| true]" >&2
	say "  Una forma que el censo no representa es una pata que no cuenta — y una pata que no" >&2
	say "  cuenta no sale en ninguna lista ni en ningun rojo. Reescribela, no ensanches el censo." >&2
	return 1
}

hook_list() { { hook_rotulo; hook_invoca; } | sort -u; }

# --- derivacion 2: lo que mainline-ci corre -----------------------------------------
# NORMALIZADO al nombre corto: CI escribe `lint:spdx` y el gancho `spdx`. Comparar sin
# normalizar da «64 de 64 ausentes» — el mutante 1, y mi propio error del 2026-08-29.
# Y VALIDADO contra el Taskfile: `task <palabra>` casa tambien la PROSA del YAML («the
# task that runs…», «no task exists…»), y sin este filtro entraban cinco tokens basura
# — exists, no, points, runs, that — que habrian quedado escritos en el registro como si
# fueran patas. Una lista derivada de un patron laxo no es una medida: es el patron.
# ⛔ UN NOMBRE PUEDE EMPEZAR POR DIGITO, y el registro no podia guardarlo. `lint:0022-published-schema`
#    existe: al normalizar quitando `lint:` queda `0022-…`, que los tres filtros `^[a-z]` tiraban —
#    la derivacion lo producia y `reg_list` no lo podia leer, asi que el gate se quedaba ROJO PARA
#    SIEMPRE en esa clase, pidiendo apuntar en el registro algo que el registro no admite. Lo cazo
#    el propio gate al negarse a ponerse verde, no una relectura mia. Se admite el digito inicial y
#    se exige AL MENOS UNA LETRA, que es lo que separa un nombre de una cifra suelta de la prosa.
es_tarea() { command grep -qE "^  (lint:)?$1:" "$TASKFILE"; }

# ⛔ SOBRE LINEAS EJECUTABLES, NUNCA SOBRE COMENTARIOS. Un borrador de este fichero leia
# el YAML entero, y entonces `at:gate` y `test:console-walk` aparecian como CI porque los
# COMENTARIOS de mainline-ci los citan («`task at:gate` tardo 343 s»). El veredicto salia
# correcto por accidente: borrar la invocacion REAL —`run: pnpm --dir web run at:gate`—
# dejaba el gate en VERDE, porque el comentario seguia ahi. Lo cazo con ese mutante.
#
# Y hay que reconocer las formas que NO son `task X`: el job a11y no instala go-task
# (mainline-ci lo dice al lado, «`task test:console-walk` seria `task: command not found`
# aqui»), asi que invoca `pnpm --dir web run at:gate` y `bash scripts/test-console-walk.sh`.
# Un censo que solo casa `task ` los pierde en las dos direcciones: si ademas se quitan los
# comentarios, desaparecen de las dos listas EN SILENCIO, que es peor que contarlos mal.
# ⛔ FUERA LOS HEREDOCS ANTES DE NADA, y despues los comentarios. Este filtro era TEXTUAL y por
# lineas: una linea `A=1 task lint:x` escrita DENTRO de un heredoc —datos, no una orden— se contaba
# como invocacion, y la cuenta de patas salia inflada sin que nada chirriase. Hoy no hay ninguna
# (medido: cero), asi que esto no cambia ningun numero; entra porque la clase es latente y el dia
# que alguien escriba un heredoc con un ejemplo dentro, el gate mentiria hacia arriba.
#
# No es un parser de shell y no pretende serlo: sigue los `<<TERM` / `<<-'TERM'` y descarta hasta su
# terminador. Es exactamente el contexto que un `grep` por linea no puede ver.
sin_heredocs() {
	awk '
		function abridor(l,   m) {
			if (l ~ /^[[:space:]]*#/) return ""
			if (match(l, /<<-?[[:space:]]*'"'"'?[A-Za-z_][A-Za-z0-9_]*'"'"'?/)) {
				t = substr(l, RSTART, RLENGTH)
				gsub(/^<<-?[[:space:]]*'"'"'?/, "", t); gsub(/'"'"'$/, "", t)
				return t
			}
			return ""
		}
		{
			if (dentro) { if ($0 ~ "^[[:space:]]*" term "[[:space:]]*$") dentro = 0; print ""; next }
			t = abridor($0)
			if (t != "") { dentro = 1; term = t }
			print
		}
	' "$1"
}
sin_comentarios() { sin_heredocs "$1" | sed 's/[[:space:]]#.*$//; s/^[[:space:]]*#.*$//'; }

ci_list() {
	{
		# a) la forma normal: `task X`
		sin_comentarios "$CI" | command grep -oE '(^|[^a-z-])task [a-z0-9][a-z0-9:_-]+' | sed 's/.*task //'
		# b) `pnpm [--dir D] run X` — el job que no tiene go-task
		sin_comentarios "$CI" | command grep -oE 'pnpm( --dir [a-z/]+)? run [a-z][a-z0-9:_-]+' | sed 's/.* run //'
		# c) `bash scripts/test-X.sh` / `scripts/check-X.sh` -> el nombre de tarea equivalente
		sin_comentarios "$CI" | command grep -oE 'scripts/(test|check)-[a-z0-9-]+\.sh' |
			sed 's|scripts/||; s|\.sh$||; s/^test-/test:/; s/^check-//'
	} | sed 's/^lint://' | command grep -E '^[a-z0-9][a-z0-9:_-]*$' | command grep -E '[a-z]' | sort -u |
		while IFS= read -r t; do es_tarea "$t" && printf '%s\n' "$t"; done
}

# --- derivacion 3: EQUIVALENCIAS POR ORDEN, no por nombre --------------------------
# ⛔ CI CORRE COSAS SIN NOMBRARLAS. `task test:web` es exactamente
#    `pnpm --dir web exec vitest run --maxWorkers=2` (Taskfile.yml, cmds de test:web) y CI
#    ejecuta ESA orden literal en mainline-ci.yml — sin escribir `test:web` en ningun sitio.
#    Un censo por NOMBRE la deja en SOLO-GANCHO: dice que CI no la ve cuando CI la corre.
#    Igual `check:web`, cuyas DOS ordenes (install + typecheck) estan las dos en CI.
#
#    encontro esta clase A MANO, una a una, y yo la parcheaba igual. Parchear a mano
#    una clase es como se producen los numeros que los dos hemos tenido que retractar hoy.
#    Aqui se DERIVA: la orden que la tarea resuelve segun `task --dry` —que es la autoridad,
#    no otro parser— contra las lineas `run:` EJECUTABLES del workflow.
#
#    ⛔ Y EL PREDICADO ES «TODAS SUS ORDENES», NO «ALGUNA». Un borrador miraba solo la
#    primera y daba `check:web` por cubierta porque CI corre su `install` — que es el
#    preambulo de media docena de tareas. Con «alguna», cualquier tarea que empiece
#    instalando dependencias entraria. Resulta que check:web SI esta cubierta (CI corre
#    tambien su typecheck), pero eso lo dijo la segunda medida, no la primera.
#
#    Coste medido en esta caja con carga 14: 155 s sobre 151 candidatos. Se acepta porque
#    esta puerta NO esta en el gancho (0 patas; corre por tarea y en CI) y porque la
#    alternativa es una tabla a mano que ya ha dado dos cifras equivocadas esta noche.
#
#    Si falta go-task: exit 2, NUNCA una lista distinta. Un gate cuyo veredicto dependa de
#    lo que hay instalado en la caja mide la caja, no el arbol.
ci_run_lines() {
	sin_comentarios "$CI" | command grep -oE 'run: .*' |
		sed 's/^run: //; s/ 2>&1.*//; s/[[:space:]]*$//' | sort -u
}

equivalencias_por_orden() { # imprime las tareas de la entrada que CI corre sin nombrar
	local cands n
	cands="$(command grep . || true)"
	[ -n "$cands" ] || return 0
	command -v task >/dev/null 2>&1 || {
		echo "check-gate-parity: NO HE PODIDO MIRAR — no hay go-task, y sin el no puedo" >&2
		echo "  resolver que ORDEN ejecuta cada tarea. Con el censo solo por nombre, test:web" >&2
		echo "  y check:web caerian en SOLO-GANCHO afirmando que CI no las ve, y CI las corre." >&2
		echo "  Antes que dar una lista distinta segun la caja, no doy ninguna." >&2
		exit 2
	}
	ci_run_lines > "$TMP_CI_CMDS"

	# ⛔ CACHE POR HASH DEL CONTENIDO, no por fecha ni por ruta. Resolver las tareas cuesta
	#    ~63 s en esta caja (go-task reparsea un Taskfile de 718 tareas en cada invocacion, y
	#    trocear en tandas de 20 no lo baja: el coste es el arranque, no el numero de
	#    objetivos). La bateria corre el gate DIEZ veces: diez minutos. Con la cache, una vez.
	#
	#    La clave son los bytes de los DOS ficheros que deciden el resultado (el Taskfile
	#    resuelve las ordenes, el workflow dice cuales corre CI) mas la lista de candidatos.
	#    Si cambia cualquiera, la clave cambia y se recalcula: una cache asi no puede quedar
	#    rancia, que es el unico defecto que hace que una cache sea peor que no tenerla.
	CACHE_KEY="$( { cat "$TASKFILE" "$CI"; printf '%s' "$cands"; } | sha256sum | cut -c1-32 )"
	CACHE="${TMPDIR:-/tmp}/olivares-parity-eq.$CACHE_KEY"
	if [ -s "$CACHE" ]; then
		command grep . "$CACHE" || true
		return 0
	fi

	# ⛔ EN TANDAS, y las dos cifras estan medidas en esta caja. Una llamada por candidato
	#    cuesta 155-208 s segun la carga y la bateria corre el gate diez veces: media hora.
	#    Las 151 de golpe NO valen tampoco: `task --dry` con 151 objetivos murio con rc=137
	#    —matado, esta caja iba con 3,7 GiB de margen y OOM kills esa misma noche— y devolvio
	#    CERO lineas, que sin la guarda de abajo se habria leido como «ninguna equivalencia».
	#    Tandas de 20: ocho llamadas, segundos, y ningun proceso grande.
	#
	#    Cada tanda que falle se reintenta UNA A UNA: una tarea inexistente aborta su tanda
	#    entera en go-task, y perder 20 por una seria cambiar un fallo ruidoso por uno mudo.
	: > "$TMP_DRY"
	printf '%s\n' "$cands" | command grep . | while IFS= read -r n; do printf '%s\n' "$n"; done |
		xargs -n 20 2>/dev/null | while IFS= read -r tanda; do
			if ! timeout 60 task --dry $tanda >> "$TMP_DRY" 2>&1; then
				for n in $tanda; do timeout 20 task --dry "$n" >> "$TMP_DRY" 2>&1 || true; done
			fi
		done
	# Un TMP_DRY vacio no es «cero equivalencias»: es que no se resolvio nada. Tercera
	# respuesta, no un verde.
	[ -s "$TMP_DRY" ] || {
		echo "check-gate-parity: NO HE PODIDO MIRAR — go-task no resolvio NINGUNA de las" >&2
		echo "  $(printf '%s\n' "$cands" | command grep -c .) tareas candidatas. Sin eso no se cual corre CI sin nombrarla." >&2
		exit 2
	}

	{
	while IFS= read -r n; do
		[ -n "$n" ] || continue
		# Sus ordenes propias, no las de sus dependencias: la etiqueta lleva el nombre.
		sed -n "s/^task: \[$n\] //p" "$TMP_DRY" > "$TMP_CMDS"
		[ -s "$TMP_CMDS" ] || continue
		# ⛔ TODAS sus ordenes, no ALGUNA: un borrador miraba solo la primera y daba check:web
		#    por cubierta porque CI corre su `install`, que es el preambulo de media docena de
		#    tareas. Con «alguna», cualquier tarea que empiece instalando entraria.
		if ! command grep -Fxv -f "$TMP_CI_CMDS" "$TMP_CMDS" >/dev/null 2>&1; then
			printf '%s\n' "$n"
		fi
	done <<-EOF
	$cands
	EOF
	} | tee "$CACHE.tmp" && mv -f "$CACHE.tmp" "$CACHE" 2>/dev/null || true
}

TMP_CI_CMDS="$(mktemp)" && TMP_DRY="$(mktemp)" && TMP_CMDS="$(mktemp)" \
	|| blind "no puedo crear el area de trabajo"
trap 'rm -f "$TMP_CI_CMDS" "$TMP_DRY" "$TMP_CMDS"' EXIT
trap 'rm -f "$TMP_CI_CMDS"' EXIT

# La forma se comprueba ANTES de censar: si hay una invocacion que el censo no sabe representar,
# todo lo que venga despues estaria contado sobre una lista incompleta.
forma_canonica || exit 1

H="$(hook_list)"; C="$(ci_list)"
[ -n "$H" ] || blind "0 patas derivadas del gancho: ni el rotulo ni las invocaciones"

# ⛔ EL ROTULO NECESITA SU PROPIO SUELO DESDE QUE LA PARIDAD NO DEPENDE DE EL. Antes, vaciar
#    el rotulo derrumbaba `ambas` y lo cazaba MIN_AMBAS: el mutante 2 de la bateria destroza
#    el rotulo a dos nombres y esperaba rc=2. Al ensanchar el sujeto a lo que el gancho
#    INVOCA, ese mutante deja de mover `ambas` — correcto, pero el rotulo sigue siendo lo
#    que el operador lee y lo que I-04 comprueba, asi que su ruina no puede pasar callada.
#    El umbral se DERIVA, no se inventa: medido sobre 8eee6c362, el rotulo nombra 168 de las
#    183 invocadas (91 %); el mutante deja 2 de 183 (1 %). La mitad separa los dos casos con
#    margen por los dos lados, y no depende de que nadie recuerde actualizar una constante.
n_rot=$(hook_rotulo | command grep -c . || true)
n_inv=$(hook_invoca | command grep -c . || true)
if [ "$n_inv" -gt 0 ] && [ $((n_rot * 2)) -lt "$n_inv" ]; then
	echo "check-gate-parity: NO HE PODIDO MIRAR — el rotulo FAST nombra $n_rot de $n_inv patas" >&2
	echo "  invocadas (menos de la mitad). Eso no es una paridad distinta: es un rotulo roto," >&2
	echo "  y el operador lee ESE rotulo. Arreglalo antes de creerte ninguna de las listas." >&2
	exit 2
fi
[ -n "$C" ] || blind "0 patas derivadas de $CI"

# Las que el gancho corre y CI ejecuta SIN NOMBRARLAS entran en el lado de CI: el sujeto es
# «¿lo ve CI?», no «¿lo escribe CI?».
EQ="$(comm -23 <(printf '%s\n' "$H") <(printf '%s\n' "$C") | equivalencias_por_orden)"
[ -n "$EQ" ] && C="$(printf '%s\n%s\n' "$C" "$EQ" | command grep . | sort -u)"

AMBAS="$(comm -12 <(printf '%s\n' "$H") <(printf '%s\n' "$C"))"
SOLO_CI="$(comm -13 <(printf '%s\n' "$H") <(printf '%s\n' "$C"))"
SOLO_HOOK="$(comm -23 <(printf '%s\n' "$H") <(printf '%s\n' "$C"))"

n_h=$(printf '%s\n' "$H" | command grep -c .)
n_c=$(printf '%s\n' "$C" | command grep -c .)
n_a=$(printf '%s\n' "$AMBAS" | command grep -c . || true)
n_sc=$(printf '%s\n' "$SOLO_CI" | command grep -c . || true)
n_sh=$(printf '%s\n' "$SOLO_HOOK" | command grep -c . || true)

# --- LA ASERCION QUE IMPORTA --------------------------------------------------------
# `en_ambos >= 20`, y NO `diferencia <= N`. Un umbral sobre la DIFERENCIA no distingue
# «no hay divergencia» de «la comparacion no caso nada»: si alguien cambia la convencion
# de nombres, las dos listas quedan disjuntas, la diferencia se dispara y — segun como se
# escriba el umbral — puede leerse como hallazgo o taparse. Exigir un SUELO de
# coincidencias mata ese mutante, porque una comparacion que no casa nada da 0 aqui.
MIN_AMBAS="${OLIVARES_PARITY_MIN:-20}"
if [ "$n_a" -lt "$MIN_AMBAS" ]; then
	echo "check-gate-parity: NO HE PODIDO MIRAR — solo $n_a patas en AMBAS listas (minimo $MIN_AMBAS)." >&2
	echo "  Eso no es divergencia: es que la comparacion no esta casando. Mira la convencion de" >&2
	echo "  nombres (CI usa 'lint:x', el gancho 'x') antes de creerte los numeros de abajo." >&2
	exit 2
fi

# `gancho` es la UNION de lo rotulado y lo invocado — no el rotulo solo, que es lo que
# producia el «solo-ci=34» con ocho patas del carril pesado dentro.
# ⛔ `--print-heavy`: LOS NOMBRES DE LAS PATAS QUE UN PUSH A RAMA NO CORRE, una por linea y nada
# mas. `--print` ya daba su NUMERO —«invocadas y NO en el rotulo: 13»— pero no sus nombres, y sin
# nombres esa cifra no sirve para actuar: `scripts/pre-verify-tanda.sh` existe para correrlas antes
# de un push a `main` y ahorrar la muerte a los 25-75 minutos.
#
# Sale de las MISMAS dos derivaciones que el resto del fichero (`hook_rotulo` y `hook_invoca`), no
# de una lista aparte: una segunda lista seria la copia que este gate existe para cazar. El carril
# pesado no esta en el rotulo POR DISENO, asi que «invocadas y no rotuladas» es exactamente el
# conjunto que un push a rama se salta.
# ⛔ UN FLAG QUE NO SE CONOCE NO SE IGNORA. Hasta hoy cualquier argumento distinto de `--print`
# caia al modo normal en SILENCIO, y eso convierte «esta version no sabe hacer eso» en «aqui tienes
# otra cosa»: medido el 2026-09-03, `scripts/pre-verify-tanda.sh` pidio `--print-heavy` a un arbol
# anterior a estos modos y se llevo el resumen y el «CLEAN» como si fueran nombres de pata. Quien
# pregunta por una capacidad que no existe tiene que enterarse, no recibir el modo por defecto.
case "${1:-}" in
'' | --print | --print-heavy | --print-ci-only | --print-root) ;;
*) blind "opcion desconocida: '$1' (modos: --print, --print-heavy, --print-ci-only, --print-root)" ;;
esac
# ⛔ `--print-root`: LA RAIZ QUE ESTE GATE HA LEIDO DE VERDAD. Existe para que quien lo PRESTA
# pueda comprobarlo en vez de suponerlo: `pre-verify-tanda.sh --gate <ruta>` fija `OLIVARES_ROOT`
# al arbol medido, pero eso es un contrato AMBIENTAL — un gate que lo ignorara devolveria la lista
# de su propia casa y el prestatario no tendria forma de notarlo. Lo levanto el contraste sol max
# (PV-ROOT-PROOF). Con esto la comprobacion es positiva: se le pregunta y se compara.
if [ "${1:-}" = "--print-root" ]; then
	printf '%s\n' "$ROOT"
	exit 0
fi
if [ "${1:-}" = "--print-heavy" ]; then
	comm -13 <(hook_rotulo) <(hook_invoca)
	exit 0
fi
# Y su hermano: las que SOLO corre CI. Un push no las ve —ni en rama ni en `main`— asi que un
# rojo ahi se descubre DESPUES de aterrizar, que es el caso mas caro de los dos. Misma derivacion
# que la seccion SOLO-CI de `--print`, no una lista aparte.
if [ "${1:-}" = "--print-ci-only" ]; then
	comm -13 <(hook_list) <(ci_list)
	exit 0
fi
printf 'check-gate-parity: gancho=%d ci=%d ambas=%d solo-ci=%d solo-gancho=%d\n' \
	"$n_h" "$n_c" "$n_a" "$n_sc" "$n_sh"

if [ "${1:-}" = "--print" ]; then
	NB="$(hook_no_bloqueante)"
	marca() { # rotula las que corren pero no pueden rechazar el push
		while IFS= read -r g; do
			[ -n "$g" ] || continue
			if printf '%s\n' "$NB" | command grep -qx "$g"; then
				printf '  %s   (informa, no bloquea)\n' "$g"
			else
				printf '  %s\n' "$g"
			fi
		done
	}
	printf '\n--- SOLO-CI (%d): NI el rotulo rapido NI el carril pesado del gancho las invoca ---\n' "$n_sc"
	printf '    (un fallo se descubre DESPUES de aterrizar)\n'
	printf '%s\n' "$SOLO_CI" | command grep . | marca
	printf '\n--- SOLO-GANCHO (%d): mainline-ci no las ve; un rojo aqui envenena main ---\n' "$n_sh"
	printf '%s\n' "$SOLO_HOOK" | command grep . | marca
	printf '\n--- rotulo vs invocacion: lo que el gancho PROMETE y lo que CORRE ---\n'
	printf '    rotulo FAST: %d   ·   invocadas (todo el gancho): %d   ·   union: %d\n' \
		"$(hook_rotulo | command grep -c . || true)" \
		"$(hook_invoca | command grep -c . || true)" "$n_h"
	printf '    en el rotulo y NO invocadas: %d   ·   invocadas y NO en el rotulo: %d\n' \
		"$(comm -23 <(hook_rotulo) <(hook_invoca) | command grep -c . || true)" \
		"$(comm -13 <(hook_rotulo) <(hook_invoca) | command grep -c . || true)"
	printf '    (se INFORMA, no se asevera: el carril pesado no esta en el rotulo por diseno)\n'
	exit 0
fi

# --- comparacion con el registro ----------------------------------------------------
# ⛔ EL REGISTRO ES HUB-ONLY Y ESTE GUION VIAJA — misma clase que INT-12 y medida el mismo dia
# (2026-08-31) desde un export real con `git init`: `lint:gate-parity` contestaba 2 en el arbol
# publico y es la SEGUNDA pata del job hook-only-legs de `mainline-ci` que lo hacia, o sea que
# curar solo INT-12 no ponia el job en verde. La cuenta de patas que este guion imprime justo
# antes (gancho/ci/ambas) SI se puede tomar en el export; lo que no existe alli es el registro
# con el que compararla, y comparar contra nada es exactamente lo que `blind` esta para impedir.
#
# Sin marcador la ausencia sigue siendo checkout roto y sigue siendo 2. El clasificador es el de
# hub-leg.sh —firma del generador MAS ausencia de todo camino hub-only—, no un marcador suelto:
# la revision X-07 dejo escrito que un marcador a pelo es una contraseña que cualquiera teclea.
if [ ! -r "$REG" ] \
   && [ "$(bash "$ROOT/scripts/hub-leg.sh" --classify --root "$ROOT" 2>/dev/null)" = "public" ]; then
	echo "check-gate-parity: SCOPED — public export; $REG is curated out. The leg census above"
	echo "  stands; the registered baseline it would be compared against lives only in the hub."
	exit 0
fi
[ -r "$REG" ] || blind "no puedo leer el registro $REG (crealo con --print)"
reg_list() { sed -n "/^<!-- $1 BEGIN -->\$/,/^<!-- $1 END -->\$/p" "$REG" | sed '1d;$d' |
	sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | command grep -E '^[a-z0-9][a-z0-9:_-]*$' | command grep -E '[a-z]' | sort -u; }
R_SC="$(reg_list SOLO-CI)"; R_SH="$(reg_list SOLO-GANCHO)"
[ -n "$R_SC$R_SH" ] || blind "el registro $REG no tiene los bloques SOLO-CI / SOLO-GANCHO"

status=0

# Comparacion explicita, sin empaquetar las listas en una cadena: los nombres LLEVAN `:`
# (alc-02-f1-hold:selftest), asi que cualquier delimitador `:` los parte por la mitad. Un
# borrador mio lo hizo y produjo listas concatenadas que parecian un hallazgo enorme.
# Y `comm` exige entrada ORDENADA: sin sort explicito avisa y devuelve basura.
comparar() {
	local nom="$1" viva="$2" reg="$3" add del mas menos
	add="$(comm -23 <(printf '%s\n' "$viva" | sort) <(printf '%s\n' "$reg" | sort) | command grep . || true)"
	del="$(comm -13 <(printf '%s\n' "$viva" | sort) <(printf '%s\n' "$reg" | sort) | command grep . || true)"
	[ -z "$add" ] && [ -z "$del" ] && return 0
	# ⛔ EL VERBO IMPORTA, Y ESTE MENSAJE YA CAUSO UNA ALARMA FALSA. Decia «SOLO-GANCHO — salen:
	#    export-closure, …», donde «salen» significaba SALEN DE LA LISTA. A las 3 de la manana otro
	#    carril lo leyo como «salieron del gancho» y publico que cinco gates no los corria NADIE.
	#    Medido entonces: las cinco seguian invocadas en el gancho Y ademas en CI — habian pasado de
	#    UN sitio a DOS, o sea lo contrario de una perdida.
	#
	#    Un mensaje de gate se redacta nombrando el MOVIMIENTO y su consecuencia, y se prueba
	#    leyendolo SIN conocer el codigo: si asi suena a incidente, es defecto de redaccion.
	echo "check-gate-parity: HALLAZGO — la lista $nom cambio y el registro no lo dice." >&2
	case "$nom" in
		SOLO-CI)     mas="ahora SOLO las ve CI (el push dejo de verlas)"
		             menos="ya NO son solo-CI: el gancho tambien las corre — mas cobertura, no menos" ;;
		SOLO-GANCHO) mas="ahora SOLO las corre el gancho (CI dejo de verlas)"
		             menos="ya NO son solo-gancho: CI tambien las corre — mas cobertura, no menos" ;;
		*)           mas="entran en la lista"; menos="dejan de estar en la lista" ;;
	esac
	[ -n "$add" ] && { echo "  ENTRAN en $nom — $mas:" >&2; printf '%s\n' "$add" | sed 's/^/    + /' >&2; }
	[ -n "$del" ] && { echo "  DEJAN DE ESTAR en $nom — $menos:" >&2; printf '%s\n' "$del" | sed 's/^/    - /' >&2; }
	return 1
}
comparar "SOLO-CI"     "$SOLO_CI"   "$R_SC" || status=1
comparar "SOLO-GANCHO" "$SOLO_HOOK" "$R_SH" || status=1

if [ "$status" -ne 0 ]; then
	echo "  Un cambio de paridad es una DECISION: actualiza $REG con su razon." >&2
	echo "  Para ver las listas enteras: bash scripts/check-gate-parity.sh --print" >&2
	exit 1
fi
echo "check-gate-parity: CLEAN — la paridad coincide con $REG"
