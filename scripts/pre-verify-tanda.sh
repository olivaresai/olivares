#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# pre-verify-tanda.sh <worktree> — corre sobre ESE arbol las patas que un push a RAMA no ejecuta, y
# las nombra TODAS.
#
# ⛔ POR QUE EXISTE. El gancho es branch-aware: un push a una rama corre el carril rapido; uno a
# `main` corre ademas el carril PESADO. La consecuencia practica es que un lote puede ir verde toda
# la tarde y morir a los 25-75 minutos en la primera pata pesada, con el mutex cogido y la caja
# ocupada. Esta herramienta paga ese descubrimiento antes, en frio, y sin coger el mutex.
#
# ⛔ LA LISTA SALE DEL GATE, NUNCA DE MEMORIA — y del gate DEL ARBOL QUE SE MIDE, no del de aqui.
# `check-gate-parity.sh --print-heavy` la deriva de las mismas dos fuentes que el resto de ese
# fichero (lo que el gancho ROTULA y lo que INVOCA); el carril pesado no esta en el rotulo por
# diseno, asi que «invocadas y no rotuladas» ES el conjunto que un push a rama se salta. Una lista
# escrita aqui a mano seria una copia, y una copia sin quien la compare deriva en silencio.
#
# Tres respuestas, como toda la casa: 0 limpio · 1 hallazgo (con las patas nombradas) · 2 NO HE
# PODIDO MIRAR (con la causa).
# ⛔ `set +e` EXPLICITO. Si quien llama arranca esto con `bash -e`, el `errexit` se HEREDA y el
# guion se para en la primera pata roja — o sea que la promesa entera de la herramienta («nunca me
# paro, las nombro TODAS») quedaria a merced del llamador. Lo levanto el contraste sol max
# (PV-ERREXIT). Se apaga aqui, una vez, en vez de confiar en como se invoque.
set +e
set -uo pipefail
export LC_ALL=C

# ⛔ AISLAMIENTO DEL ENTORNO GIT. Esta herramienta crea temporales con `mktemp -d` y corre `git`
# sobre el arbol medido (la huella de `git status` que detecta si una pata lo reescribio, y el
# `rev-parse` del fichero de refs sintetico). Con las GIT_* del entorno heredadas, esos `git`
# pueden resolver contra OTRO repositorio sin decirlo — y aqui eso significaria tomar la huella del
# arbol equivocado, que es justo el fallo que la huella viene a evitar. Lo exige `lint:git-env`.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

say()    { printf '%s\n' "$*"; }
cannot() { say "pre-verify-tanda: ⛔ NO HE PODIDO MIRAR — $*" >&2; exit 2; }

WT=""
GATE_EXPLICITO=""
MODO="ambas"
HOOK_ENV=0
SOLO_ENTORNO=0
BISECAR=auto
BISECT_MAX="${OLIVARES_PREVERIFY_BISECT_MAX:-120}"
TIMEOUT="${OLIVARES_PREVERIFY_TIMEOUT:-1800}"
while [ "$#" -gt 0 ]; do
	case "$1" in
	--only-heavy) MODO="heavy" ;;
	--only-ci)    MODO="ci" ;;
	--hook-env)   HOOK_ENV=1 ;;
	# ⛔ Un solo pase, CON el entorno. Nace de a repository gate: con `task test` (5 h por pase) el doble pase
	# son 10 h para una respuesta que muchas veces sólo hace falta en su forma «como muere en el
	# gancho». Quien quiere el diferencial lo pide; quien quiere la verdad del gancho, esto.
	--only-hook-env) HOOK_ENV=1; SOLO_ENTORNO=1 ;;
	--bisect)     BISECAR=si ;;
	--no-bisect)  BISECAR=no ;;
	--bisect-max) shift; [ "$#" -gt 0 ] || cannot "--bisect-max necesita segundos"; BISECT_MAX="$1" ;;
	# ⛔ Un argumento explicito MAL FORMADO no puede cambiar de sujeto. `--gate` sin valor hacia
	# `shift`, dejaba la variable vacia y luego se usaba el gate del arbol EN SILENCIO: quien pidio
	# uno prestado acabaria midiendo con otro sin enterarse.
	--gate)       shift; [ "$#" -gt 0 ] && [ -n "${1:-}" ] || cannot "--gate necesita una ruta"; GATE_EXPLICITO="$1" ;;
	--gate=*)     GATE_EXPLICITO="${1#--gate=}"; [ -n "$GATE_EXPLICITO" ] || cannot "--gate= necesita una ruta" ;;
	--timeout)    shift; TIMEOUT="${1:-}" ;;
	--timeout=*)  TIMEOUT="${1#--timeout=}" ;;
	-h|--help)
		say "uso: pre-verify-tanda.sh <worktree> [--gate <ruta>] [--hook-env|--only-hook-env]"
		say "       [--bisect|--no-bisect] [--bisect-max <segundos>] [--only-heavy|--only-ci] [--timeout <segundos>]"
		exit 0
		;;
	-*) cannot "opcion desconocida: $1" ;;
	*)  [ -z "$WT" ] || cannot "sobra un argumento: $1"; WT="$1" ;;
	esac
	shift
done

[ -n "$WT" ] || cannot "falta el worktree: pre-verify-tanda.sh <worktree>"
case "$TIMEOUT" in
'' | *[!0-9]*) cannot "--timeout debe ser un numero de segundos: '$TIMEOUT'" ;;
esac
[ "$TIMEOUT" -gt 0 ] || cannot "--timeout tiene que ser mayor que cero"

WT="$(CDPATH= cd -- "$WT" 2>/dev/null && pwd -P)" || cannot "no puedo entrar en el worktree"
[ -d "$WT/.git" ] || [ -f "$WT/.git" ] || cannot "$WT no parece un arbol de git"
# ⛔ `--gate <ruta>`: USAR UN GATE DE FUERA PARA MEDIR ESTE ARBOL. Existe porque los modos
# `--print-heavy`/`--print-ci-only` son nuevos: un arbol de lote anterior a ellos no puede darse la
# lista a si mismo, y esperar a que se integren cuesta justo los 25-75 minutos que esto ahorra.
#
# ⛔ Y LA SALVEDAD QUE LO HACE CORRECTO, que es toda la dificultad del flag: el gate deriva la lista
# del arbol que lee, y lo elige por `OLIVARES_ROOT` o, si no esta, por DONDE VIVE EL. Invocarlo sin
# fijarlo daria la lista del arbol donde vive el gate prestado — es decir, mediria MI arbol y la
# aplicaria al suyo. Eso es exactamente la clase de defecto que esta herramienta existe para
# evitar, asi que `OLIVARES_ROOT` se fija SIEMPRE al arbol medido, venga el gate de donde venga.
if [ -n "$GATE_EXPLICITO" ]; then
	GATE="$(CDPATH= cd -- "$(dirname -- "$GATE_EXPLICITO")" 2>/dev/null && pwd -P)/$(basename -- "$GATE_EXPLICITO")" \
		|| cannot "no encuentro el gate que me das: $GATE_EXPLICITO"
	[ -r "$GATE" ] || cannot "el gate que me das no se puede leer: $GATE_EXPLICITO"
else
	GATE="$WT/scripts/check-gate-parity.sh"
	[ -r "$GATE" ] || cannot "ese arbol no trae scripts/check-gate-parity.sh: pasa uno con --gate <ruta>"
fi
command -v task    >/dev/null 2>&1 || cannot "no hay 'task' en el PATH: no puedo correr ninguna pata"
command -v timeout >/dev/null 2>&1 || cannot "no hay 'timeout' en el PATH: una pata colgada colgaria esto"

# ⛔ EL NOMBRE CORTO NO ES EL NOMBRE DE LA TAREA. El gate normaliza quitando `lint:` para poder
# comparar con CI, asi que devuelve `format-ratchet` donde la tarea es `lint:format-ratchet` y
# `build:go` donde la tarea es `build:go`. Resolverlo ADIVINANDO —probar `lint:` y si falla el
# pelado— confundiria «la tarea no existe» con «la pata esta roja», que son las dos respuestas que
# esta herramienta tiene que mantener separadas. Se resuelve contra la lista REAL del arbol.
# ⛔ `awk` CON UNA SOLA SUSTITUCION, no un `sed` con grupos. `task --list-all` imprime
# `* <nombre>:<espacios><descripcion>`, y los nombres LLEVAN dos puntos (`build:cloud`,
# `test:cloud:norace`). Un patron con grupos anidados retrocede y no extraia NADA para esos — que
# son casi todos: la herramienta decia «no existe como tarea» de las trece. `:[[:space:]]` es el
# separador real y `sub` sustituye la PRIMERA aparicion, que es exactamente el corte que hace falta.
# ⛔ SIN COLOR POR BANDERA, Y EL rc DEL PRODUCTOR ANTES QUE SU SALIDA. Medido el 2026-09-07 en
# `mainline-ci` (run 34167328609, job 101880875701, paso `leg-pre-verify-tanda-selftest`, fuente
# 91758901e3): Task 3.51.1 con `CI=true` en el entorno escribe secuencias ANSI en `--list-all`
# AUNQUE la salida vaya por un tubo, asi que cada fila empezaba por `ESC[0m ESC[33m* ` y no por
# `* `, el `awk` no casaba ninguna, esta herramienta decia «no devolvio ninguna tarea» y 47 filas
# de su propio banco cayeron con rc 2 — el laboratorio de todas verdes incluido. `--color=false`
# es la forma que Task documenta para apagarlo («Set flag to false or use NO_COLOR=1») y gana a
# `CI=true` y a `FORCE_COLOR=1` juntas, sondeado sobre el binario real. NO se limpia el ANSI a
# posteriori: si la bandera dejara de funcionar, limpiar taparia el sintoma; con el parseo estricto
# la herramienta rehusa (2) y el banco lo ve.
#
# Y el rc se lee ANTES de parsear, con la salida entera guardada: un productor que muere despues
# de imprimir media lista deja filas validas en la salida, y creerselas convertiria «Task fallo» en
# «esas patas no existen» (2 por la causa equivocada) o en un verde sobre la mitad que llego. Con
# rc distinto de 0 no se acepta NADA de lo impreso, y la refusal conserva la ultima linea del
# productor, que es donde suele estar su motivo.
SALIDA_LISTA="$(cd "$WT" && task --list-all --color=false 2>&1)"; RC_LISTA=$?
[ "$RC_LISTA" -eq 0 ] || cannot "'task --list-all' salio $RC_LISTA en $WT y no acepto la parte de lista que imprimio antes de fallar: $(printf '%s\n' "$SALIDA_LISTA" | tail -n 1 | cut -c1-120)"
TAREAS="$(printf '%s\n' "$SALIDA_LISTA" | awk '/^\* /{ sub(/^\* /, ""); sub(/:[[:space:]].*$/, ""); print }')"
[ -n "$TAREAS" ] || cannot "'task --list-all' no devolvio ninguna tarea en $WT"

resolver() { # resolver <nombre corto> -> nombre de tarea real, o vacio
	local corto="$1"
	# Here-string, no tuberia: `grep -q` cierra el tubo en cuanto casa y el `printf` de la
	# izquierda se lleva un SIGPIPE — 141 bajo `pipefail`, que es la deuda que `lint:sigpipe-booleans`
	# lleva la cuenta de no aumentar.
	command grep -qx "$corto"      <<<"$TAREAS" && { printf '%s\n' "$corto"; return 0; }
	command grep -qx "lint:$corto" <<<"$TAREAS" && { printf '%s\n' "lint:$corto"; return 0; }
	return 1
}

lista() {
	# `OLIVARES_ROOT="$WT"` en todas: ver la salvedad de `--gate` mas arriba.
	#
	# ⛔ Y CADA MODO SE COMPRUEBA POR SEPARADO. La version anterior agrupaba los dos en
	# `{ a; b; } | sort -u`: si el PRIMERO fallaba, su rc se perdia en el grupo, su salida vacia se
	# fundia con la del segundo, y la herramienta corria **media lista creyendola entera** — verde
	# sobre la mitad que si contesto. Lo levanto el contraste sol max (PV-PARTIAL).
	local a b rca rcb
	case "$MODO" in
	heavy)
		a="$(OLIVARES_ROOT="$WT" bash "$GATE" --print-heavy 2>/dev/null)"; rca=$?
		[ "$rca" -eq 0 ] || return 1
		printf '%s\n' "$a"
		;;
	ci)
		b="$(OLIVARES_ROOT="$WT" bash "$GATE" --print-ci-only 2>/dev/null)"; rcb=$?
		[ "$rcb" -eq 0 ] || return 1
		printf '%s\n' "$b"
		;;
	*)
		a="$(OLIVARES_ROOT="$WT" bash "$GATE" --print-heavy 2>/dev/null)"; rca=$?
		b="$(OLIVARES_ROOT="$WT" bash "$GATE" --print-ci-only 2>/dev/null)"; rcb=$?
		[ "$rca" -eq 0 ] && [ "$rcb" -eq 0 ] || return 1
		printf '%s\n%s\n' "$a" "$b" | sort -u
		;;
	esac
}
# ⛔ LA RAIZ SE ATESTIGUA, NO SE SUPONE. Fijar `OLIVARES_ROOT` es un contrato AMBIENTAL: un gate
# que lo ignorase devolveria la lista de su propia casa y aqui se correria contra otro arbol sin
# que nada chirriase — y con `--gate` eso es justo el escenario (PV-ROOT-PROOF del contraste). Se
# le pregunta y se compara; si no sabe contestar, se rehusa en vez de fiarse.
RAIZ_ATESTIGUADA="$(OLIVARES_ROOT="$WT" bash "$GATE" --print-root 2>/dev/null)" \
	|| cannot "ese gate no sabe atestiguar que raiz lee (--print-root): no puedo comprobar que mide ESTE arbol"
[ "$RAIZ_ATESTIGUADA" = "$WT" ] || cannot "el gate dice haber leido '$RAIZ_ATESTIGUADA' y el arbol a medir es '$WT': mediria otra casa"
PATAS="$(lista)" || cannot "el gate de paridad no pudo darme la lista completa (modo=$MODO): media lista corrida como si fuera entera seria un verde sobre lo que si contesto"
PATAS="$(printf '%s\n' "$PATAS" | command grep -v '^[[:space:]]*$' || true)"
[ -n "$PATAS" ] || cannot "la lista salio VACIA (modo=$MODO): sin patas que correr no hay veredicto, y un 0 aqui seria un verde ciego"

# ⛔ Y SE COMPRUEBA QUE LO DEVUELTO SON NOMBRES, no la salida normal del gate. MEDIDO el 2026-09-03
# contra el arbol de otro carril: su `check-gate-parity.sh` es anterior a `--print-heavy`, **ignora
# el flag desconocido en silencio** y corre su modo de siempre, asi que devolvio dos lineas de prosa
# —su resumen y su «CLEAN»— y esta herramienta se las creyo como patas. La lista no salia vacia, asi
# que la guarda de arriba no disparaba; el veredicto acababa siendo 2, pero por la causa equivocada
# («no existe como tarea»), que manda al lector a mirar el Taskfile en vez del gate.
#
# Un nombre de tarea no lleva espacios ni `=`. Con eso basta para distinguir una lista de una frase,
# y la causa que se imprime es la de verdad.
NO_NOMBRE="$(printf '%s\n' "$PATAS" | command grep -vE '^[A-Za-z0-9][A-Za-z0-9:_.-]*$' | head -1)"
[ -z "$NO_NOMBRE" ] || cannot "el gate de ese arbol no entiende '--print-$([ "$MODO" = ci ] && echo ci-only || echo heavy)': ignoro el flag y contesto con su salida normal ('$NO_NOMBRE'). Ese arbol es anterior a esos modos; usa uno que los traiga."

# ⛔ EL ENTORNO DEL GANCHO, Y SUS NOMBRES SE DERIVAN DEL GANCHO. Nace de a repository gate, la cuarta muerte
# de un lote: `lint:publisher-contract:selftest` daba **24/0 suelta y 15/9 bajo el gancho**, porque
# el gancho EXPORTA `OLIVARES_PUSH_REFS_FILE` y cada senuelo de esa bateria acababa midiendo los
# tips del PUSH REAL en vez de su propio arbol desechable. Mismo banco, mismo arbol, dos veredictos
# — y el que cuenta es el de debajo del gancho, que es donde muere el push.
#
# ⇒ correr una pata "a pelo" NO predice como se va a comportar en el gancho. Con `--hook-env` cada
# pata se corre DOS VECES —limpia y con el entorno— y cualquier diferencia de veredicto es un
# HALLAZGO, aunque las dos salgan verdes: una pata que cambia de respuesta segun el entorno esta
# midiendo algo que no controla.
#
# ⛔ LOS NOMBRES SE LEEN DEL FICHERO, NO DE UNA LISTA AQUI. Escribirlos a mano seria una copia que
# envejece en silencio — y ya envejecio: el encargo hablaba de seis y el gancho exporta SIETE. La
# septima es `OLIVARES_ACT_ID`, justo la que hoy hacia que `lint:int-12-no-land` contestara «no he
# podido mirar» en CI. Los VALORES si se sintetizan aqui, y se dice cual es cual.
vars_del_gancho() { # -> NOMBRE=valor, uno por linea
	local hk="$WT/.githooks/pre-push" n
	[ -r "$hk" ] || return 1
	local oid; oid="$(git -C "$WT" rev-parse HEAD 2>/dev/null)" || oid="$(printf '0%.0s' $(seq 40))"
	# ⛔ LA URL DEL REMOTO SE REDACTA AL CAPTURARLA, no al imprimirla. Un remoto puede llevar la
	#    credencial en el userinfo (`https://usuario:TOKEN@host/repo.git`), y este guion COPIA ese
	#    valor a `OLIVARES_PUSH_REMOTE_URL` y luego IMPRIME el bloque entero — de la salida al log,
	#    y del log al asiento del bus, que es público para todos los carriles.
	#
	#    No es hipotético: el 2026-09-04 se encontró en esta misma caja un clon cuyo `remote.origin.url`
	#    llevaba un PAT de GitHub en claro apuntando a producción. Con este guion sin redactar, una
	#    sola pre-verificación en ese clon habría publicado el token.
	#
	#    Se redacta AQUÍ y no en el `printf` porque el valor se COPIA a una variable antes de
	#    imprimirse: redactar sólo a la salida deja el secreto vivo en todo lo que lea la variable.
	local url; url="$(git -C "$WT" remote get-url origin 2>/dev/null || echo 'https://example.invalid/repo.git')"
	url="$(printf '%s' "$url" | sed -E 's#://[^/@]*@#://<redacted>@#g')"
	printf 'refs/heads/pre-verify %s refs/heads/pre-verify %s\n' "$oid" "$(printf '0%.0s' $(seq 40))" >"$REFS_SINT"
	while IFS= read -r n; do
		case "$n" in
		OLIVARES_PUSH_REFS_FILE)  printf '%s=%s\n' "$n" "$REFS_SINT" ;;
		OLIVARES_PUSH_CLASS)      printf '%s=full\n' "$n" ;;
		OLIVARES_PUSH_REMOTE_NAME) printf '%s=origin\n' "$n" ;;
		OLIVARES_PUSH_REMOTE_URL) printf '%s=%s\n' "$n" "$url" ;;
		OLIVARES_ACT_ID)          printf '%s=pre-verify-%s\n' "$n" "$oid" ;;
		*)
			# Las que el gancho fija a un literal se copian tal cual: es su valor, no uno inventado.
			local lit
			lit="$(command grep -m1 -oE "^[[:space:]]*export $n=[^[:space:]\"']+" "$hk" | sed "s/.*$n=//")"
			printf '%s=%s\n' "$n" "${lit:-1}"
			;;
		esac
	done < <(command grep -oE '^[[:space:]]*export [A-Z_][A-Z0-9_]*' "$hk" | awk '{print $2}' | sort -u)
}

REFS_SINT=""
ENTORNO=""
# ⛔ UN SOLO CAMINO PARA CORRER CON ENTORNO, y lo comparten las patas y el canario. Si el canario
# usara su propia invocacion podria exportar bien mientras las patas no —o al reves— y entonces no
# estaria vigilando el arnes: estaria vigilando otro arnes.
corre_pata() { # corre_pata <fichero de salida> [VAR=val ...] -- <orden...> -> rc
	local sal="$1"; shift
	local envs=()
	while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do envs+=("$1"); shift; done
	shift || true
	# `-k 10`: sin segunda senal, una pata que ignore TERM no tiene techo y se lleva por delante la
	# promesa de nombrarlas todas (PV-KILL del contraste sol max). KILL diez segundos despues.
	( cd "$WT" && env ${envs[@]+"${envs[@]}"} timeout -k 10 "$TIMEOUT" "$@" ) >"$sal" 2>&1
	echo $?
}

# ⛔ EL CANARIO, y es de KERNEL: sin el, «las siete patas dieron igual» y «el arnes no exporta nada»
# son INDISTINGUIBLES — el modo saldria limpio precisamente cuando no esta midiendo. Se corre una
# orden falsa que LEE una de las variables derivadas y tiene que cambiar de rc: 0 sin entorno
# (variable vacia) y 1 con el (variable puesta). Si no cambia, el arnes esta mudo y eso es 2.
canario() {
	local kv nombre salc rc_a rc_b
	kv="$(printf '%s\n' "$ENTORNO" | command grep -m1 .)"
	nombre="${kv%%=*}"
	salc="$(mktemp "${TMPDIR:-/tmp}/preverify-canario.XXXXXX")" || cannot "no puedo crear un temporal"
	rc_a="$(corre_pata "$salc" -- sh -c "test -z \"\${$nombre:-}\"")"
	# shellcheck disable=SC2046  # la lista VAR=val se expande a proposito, una por argumento
	rc_b="$(corre_pata "$salc" $(printf '%s\n' "$ENTORNO") -- sh -c "test -z \"\${$nombre:-}\"")"
	rm -f "$salc"
	[ "$rc_a" = 0 ] || cannot "el canario no salio 0 con el entorno LIMPIO (rc=$rc_a): no puedo fiarme de la comparacion"
	[ "$rc_b" != 0 ] || cannot "el canario NO cambio de veredicto con el entorno puesto ($nombre): el arnes no esta exportando, asi que «todas iguales» no significaria nada"
	say "pre-verify-tanda: canario OK — con el entorno puesto, una orden que lee $nombre cambia de 0 a $rc_b."
}

if [ "$HOOK_ENV" -eq 1 ]; then
	REFS_SINT="$(mktemp "${TMPDIR:-/tmp}/preverify-refs.XXXXXX")" || cannot "no puedo crear el fichero de refs sintetico"
	ENTORNO="$(vars_del_gancho)" || cannot "ese arbol no trae .githooks/pre-push: no puedo derivar el entorno del gancho"
	[ -n "$ENTORNO" ] || cannot "no he encontrado NINGUNA variable exportada en .githooks/pre-push: derivar de un fichero que no las tiene daria un segundo pase identico al primero, que es un verde ciego"
	# ⛔ Y EL CENSO DE SUJETOS, que es lo que convierte «cambia con el entorno» en «mira aqui». El
	# predicado util NO es «¿el banco menciona una variable del gancho?» —eso encuentra los usos a
	# proposito, que son los sanos— sino **¿el SUJETO del banco LEE alguna de las que el gancho
	# exporta?**. Los rotos son los que la heredan sin saberlo, como paso con a repository gate.
	#
	# Se resuelve sobre `scripts/check-*.sh` del arbol medido y se empareja con su banco por nombre.
	# Un sujeto que lee una de esas variables Y NO TIENE BANCO es el caso mas caro: nada puede cazar
	# la clase ahi, y no aparece en ningun rojo hasta que mata un push.
	censo_sujetos() {
		local f base v vs banco lim
		for f in "$WT"/scripts/check-*.sh; do
			[ -r "$f" ] || continue
			vs=""
			while IFS= read -r v; do
				[ -n "$v" ] || continue
				command grep -q "\$$v\|\${$v" "$f" 2>/dev/null && vs="$vs $v"
			done <<-EOV
			$(printf '%s\n' "$ENTORNO" | sed 's/=.*//')
			EOV
			[ -n "$vs" ] || continue
			base="$(basename "$f" .sh)"; base="${base#check-}"
			if   [ -r "$WT/scripts/test-check-$base.sh" ]; then banco="test-check-$base.sh"
			elif [ -r "$WT/scripts/test-$base.sh" ];       then banco="test-$base.sh"
			else banco="(SIN BANCO)"; fi
			lim="-"
			[ "$banco" != "(SIN BANCO)" ] && {
				command grep -qE 'unset[[:space:]]+[A-Z_ ]*OLIVARES_PUSH|env -u OLIVARES_PUSH' "$WT/scripts/$banco" 2>/dev/null \
					&& lim="limpia" || lim="⚠ NO limpia"
			}
			printf '    %-32s %-30s %s\n' "$base" "$banco" "$lim"
			printf '      lee:%s\n' "$vs"
		done
	}

	say "pre-verify-tanda: entorno del gancho DERIVADO de .githooks/pre-push — $(printf '%s\n' "$ENTORNO" | command grep -c .) variable(s):"
	# Segunda capa, a propósito: la de arriba cubre la variable que HOY lleva una URL; ésta cubre
	# cualquier otra que llegue a llevarla mañana. Una redacción sola es una que alguien puede
	# rodear añadiendo una variable.
	printf '%s\n' "$ENTORNO" | sed -E 's#://[^/@]*@#://<redacted>@#g' | sed 's/^/    /'
	say ""
	canario
	say ""
	say "pre-verify-tanda: sujetos que LEEN alguna de ellas (donde mirar si un veredicto cambia):"
	censo_sujetos
	say ""
fi

# ⛔ LA AGUJA: dos veredictos pueden compartir rc y no ser el mismo. Una bateria que pasa de
# «24 passed, 0 failed» a «15 passed, 9 failed» sale 1 en los dos casos — y esa fue exactamente
# a repository gate. Se compara el recuento cuando lo hay, y si no la primera linea de causa; nunca la salida
# entera, que trae rutas temporales y relojes y cambiaria siempre.
aguja() { # aguja <fichero de salida>
	local a
	a="$(command grep -m1 -aoE '[0-9]+ (passed|pasados),? [0-9]+ (failed|fallados)' "$1")" || true
	[ -n "$a" ] || a="$(command grep -m1 -aE 'FAIL|BROKEN|HALLAZGO|⛔|COULD NOT LOOK' "$1" | cut -c1-60)" || true
	printf '%s' "$a"
}

# ⛔ LAS PATAS PESADAS REESCRIBEN FICHEROS, y esto lo mide en vez de suponerlo. Medido el
# 2026-09-03 al probar esta herramienta contra mi propio worktree: `lint:format-ratchet` dejo
# `web/src/styles/tokens.css` MODIFICADO. Quien corra esto sobre el arbol de un lote antes de
# empujar se llevaria esos cambios en su push sin haberlos pedido — y el sintoma llega despues, como
# «tengo cosas sin commitear que yo no toque».
#
# Se toma la huella ANTES y DESPUES de CADA pata, no una sola vez al principio: asi la mutacion
# queda atribuida a la pata que la hizo, que es lo unico accionable. Y un arbol ya sucio de antes no
# cuenta: lo que se compara es la diferencia.
# ⛔ EL rc DE `git status` NO SE DESCARTA, Y NO SE LEE DESPUES DE UN TUBO. Esta funcion decia
#    `git status --porcelain 2>/dev/null | LC_ALL=C sort`: el `2>/dev/null` se traga el motivo y el
#    `|` hace que el rc que se ve sea el de `sort`, que casi siempre es 0. Un `git status` que FALLA
#    —repositorio bloqueado, indice corrupto, permiso denegado— devolvia la cadena VACIA, y una
#    huella vacia comparada con otra huella vacia dice «el arbol no cambio». La tercera respuesta
#    desaparecia dentro de la huella: «no he podido mirar» se convertia en «esta limpio».
#
#    Devuelve 2 y NOMBRA el motivo. Quien la llama decide, pero ya no puede confundirse.
huella_arbol() { # -> huella por stdout; rc 0 pude mirar · rc 2 NO pude
	local salida rc
	salida="$(git -C "$WT" status --porcelain 2>&1)"
	rc=$?
	if [ "$rc" -ne 0 ]; then
		printf 'HUELLA-CIEGA rc=%s %s\n' "$rc" "$(printf '%s' "$salida" | tr '\n' ' ' | cut -c1-160)"
		return 2
	fi
	printf '%s\n' "$salida" | LC_ALL=C sort
}

# ⛔ LA EVIDENCIA DE UNA PATA LARGA NO SE BORRA. Nace de a repository gate: al terminar cada pata se hacia
# `rm -f` de su salida, asi que quien encontraba una diferencia en una pata de HORAS tenia que
# pararlo todo y rescatar el fichero a mano antes de que el guion lo tirara. La salida de algo que
# costo cinco horas es evidencia, no un temporal.
EVID="$(mktemp -d "${TMPDIR:-/tmp}/preverify-evidencia.XXXXXX")" || cannot "no puedo crear el directorio de evidencia"
guardar() { # guardar <fichero> <etiqueta> -> ruta conservada
	local d="$EVID/$2"
	cp "$1" "$d" 2>/dev/null && printf '%s\n' "$d"
}

n_total=0; n_rojas=0; n_mudas=0; n_dif=0; n_muta=0
MUTADAS=""
DIFS=""
ROJAS=""; MUDAS=""
say "pre-verify-tanda: arbol=$WT · gate=$GATE · modo=$MODO · timeout=${TIMEOUT}s"
say ""
printf '  %-34s %5s %8s  %s\n' "PATA" "rc" "seg" "primera linea de causa"
while IFS= read -r corto; do
	[ -n "$corto" ] || continue
	n_total=$((n_total + 1))
	tarea="$(resolver "$corto")" || {
		n_mudas=$((n_mudas + 1)); MUDAS="$MUDAS $corto"
		printf '  %-34s %5s %8s  %s\n' "$corto" "-" "-" "no existe como tarea en este arbol"
		continue
	}
	sal="$(mktemp "${TMPDIR:-/tmp}/preverify.XXXXXX")" || cannot "no puedo crear un temporal"
	huella_antes="$(huella_arbol)"; hrc_a=$?
	[ "$hrc_a" -eq 0 ] || cannot "no he podido leer el estado del arbol antes de la pata $tarea: $huella_antes"
	ini="$(date +%s)"
	if [ "$SOLO_ENTORNO" -eq 1 ]; then
		# shellcheck disable=SC2046  # la lista VAR=val se expande a proposito, una por argumento
		rc="$(corre_pata "$sal" $(printf '%s\n' "$ENTORNO") -- task "$tarea")"
	else
		rc="$(corre_pata "$sal" -- task "$tarea")"
	fi
	seg=$(( $(date +%s) - ini ))
	# ⛔ LA CAUSA SE SACA DEL FINAL, no del principio: `task` imprime su cabecera antes que nada, asi
	# que la primera linea del fichero es casi siempre `task: [x] bash scripts/…` y no dice nada.
	# `grep -m1` en vez de `| head -1`: `head` tambien cierra el tubo antes de tiempo.
	causa="$(command grep -m1 -aE 'FAIL|BROKEN|HALLAZGO|⛔|error|Error|no puedo|COULD NOT LOOK' "$sal" | cut -c1-88)"
	[ -n "$causa" ] || causa="$(tail -1 "$sal" | cut -c1-88)"
	aguja_a="$(aguja "$sal")"
	# ── ¿esta pata ha tocado el arbol? ────────────────────────────────────────────────────────
	huella_despues="$(huella_arbol)"; hrc_d=$?
	[ "$hrc_d" -eq 0 ] || cannot "no he podido leer el estado del arbol tras la pata $tarea: $huella_despues"
	tocados=""
	if [ "$huella_antes" != "$huella_despues" ]; then
		tocados="$(comm -13 <(printf '%s\n' "$huella_antes") <(printf '%s\n' "$huella_despues") | sed 's/^...//' | tr '\n' ' ')"
		n_muta=$((n_muta + 1)); MUTADAS="$MUTADAS $tarea"
	fi

	# ─── el segundo pase: la MISMA pata con el entorno que el gancho exporta ───────────────────
	dif=""
	if [ "$HOOK_ENV" -eq 1 ] && [ "$SOLO_ENTORNO" -eq 0 ]; then
		sal2="$(mktemp "${TMPDIR:-/tmp}/preverify2.XXXXXX")" || cannot "no puedo crear un temporal"
		# shellcheck disable=SC2046  # la lista de VAR=val se expande a proposito, una por argumento
		rc2="$(corre_pata "$sal2" $(printf '%s\n' "$ENTORNO") -- task "$tarea")"
		aguja_b="$(aguja "$sal2")"
		# ⛔ Y LA HUELLA SE VUELVE A MIRAR AQUI, DESPUES DEL SEGUNDO PASE. Antes se tomaba una sola vez
		#    tras el PRIMER pase, asi que **todo lo que ensuciara el arbol en el segundo era invisible**
		#    — y el segundo pase es justo el que corre con el entorno del gancho, o sea el que mas se
		#    parece al push real. Una pata que solo escribe cuando `OLIVARES_PUSH_CLASS` esta puesta
		#    pasaba la pre-verificacion entera sin que nadie la contara como mutadora.
		#
		#    Se compara contra la huella POSTERIOR al primer pase, no contra la inicial: asi el
		#    segundo pase se atribuye lo suyo y no hereda lo que ya conto el primero.
		huella_p2="$(huella_arbol)"; hrc_2=$?
		[ "$hrc_2" -eq 0 ] || cannot "no he podido leer el estado del arbol tras el segundo pase de $tarea: $huella_p2"
		if [ "$huella_despues" != "$huella_p2" ]; then
			t2="$(comm -13 <(printf '%s\n' "$huella_despues") <(printf '%s\n' "$huella_p2") | sed 's/^...//' | tr '\n' ' ')"
			tocados="$(printf '%s%s' "$tocados" "${tocados:+ }")$t2(2.o pase)"
			case " $MUTADAS " in
			*" $tarea "*) : ;;
			*) n_muta=$((n_muta + 1)); MUTADAS="$MUTADAS $tarea" ;;
			esac
		fi
		if [ "$rc" != "$rc2" ] || [ "$aguja_a" != "$aguja_b" ]; then
			# ⛔ SE BISECA A UNA SOLA VARIABLE si se puede: «cambia con el entorno del gancho» manda a
			# leer siete; «cambia con OLIVARES_PUSH_REFS_FILE» manda a leer una linea. Se prueba cada
			# una SOLA, y si ninguna lo reproduce se dice que no se pudo — nunca se nombra la primera
			# por si acaso.
			# ⛔ ANTES DE RE-CORRER NADA, SE LEE LO QUE LA SALIDA YA DICE. Nace de a repository gate: la
			# biseccion vuelve a correr la pata una vez por variable —hasta siete—, y con `task test`
			# a cinco horas el pase eso son 25 h para averiguar algo que el PRIMER pase ya habia
			# escrito en 756 bytes (`pg-test-env: FAILING — … OLIVARES_PG_LOCAL_DEFAULTS…`).
			# Re-ejecutar para descubrir lo que el texto ya nombra es el desperdicio mas caro que
			# puede cometer una herramienta de pre-verificacion.
			#
			# La salida sirve por DOS vias, y la segunda no la habia previsto: el diagnostico del
			# guion (`pg-test-env: FAILING — …`) y el ECO que `task` hace del texto de la orden, que
			# nombra la variable si la orden la menciona. Las dos son evidencia legitima de que esa
			# variable esta en juego, y las dos ahorran la re-corrida.
			culpable=""
			while IFS= read -r kv; do
				[ -n "$kv" ] || continue
				n_kv="${kv%%=*}"
				if command grep -q -- "$n_kv" "$sal2" 2>/dev/null || command grep -q -- "$n_kv" "$sal" 2>/dev/null; then
					culpable="$n_kv (nombrada en la salida, sin re-correr)"; break
				fi
			done <<-EOF
			$ENTORNO
			EOF

			# Y si la salida no la nombra, la biseccion es CARA y deja de ser automatica: una pata
			# que tardo mas de $BISECT_MAX segundos costaria eso por CADA variable. Se pide con
			# `--bisect` a sabiendas, o se dice que no se hizo y por que.
			if [ -z "$culpable" ]; then
				if [ "$BISECAR" = no ]; then
					culpable=""
					dif_nota="biseccion desactivada (--no-bisect)"
				elif [ "$BISECAR" = si ] || [ "$seg" -le "$BISECT_MAX" ]; then
					while IFS= read -r kv; do
						[ -n "$kv" ] || continue
						sal3="$(mktemp "${TMPDIR:-/tmp}/preverify3.XXXXXX")" || break
						rc3="$(corre_pata "$sal3" "$kv" -- task "$tarea")"
						if [ "$rc3" = "$rc2" ] && [ "$(aguja "$sal3")" = "$aguja_b" ]; then
							culpable="${kv%%=*}"; rm -f "$sal3"; break
						fi
						rm -f "$sal3"
					done <<-EOF
					$ENTORNO
					EOF
				else
					dif_nota="NO biseco: la pata tardo ${seg}s (> ${BISECT_MAX}s) y bisecar costaria eso por variable — pidelo con --bisect si lo quieres"
				fi
			fi
			dif="rc ${rc}->${rc2}"
			[ "$aguja_a" = "$aguja_b" ] || dif="$dif · aguja '${aguja_a:-–}'->'${aguja_b:-–}'"
			if [ -n "$culpable" ]; then dif="$dif · por $culpable"
			elif [ -n "${dif_nota:-}" ]; then dif="$dif · ${dif_nota}"
			else dif="$dif · no la he podido bisecar a una sola"; fi
			ev1="$(guardar "$sal" "${tarea//[^A-Za-z0-9._-]/_}.limpia.log")"
			ev2="$(guardar "$sal2" "${tarea//[^A-Za-z0-9._-]/_}.entorno.log")"
			dif="$dif · evidencia: ${ev1:-?} y ${ev2:-?}"
			n_dif=$((n_dif + 1)); DIFS="$DIFS $tarea"
		fi
		# La evidencia ya esta copiada arriba si hubo diferencia; el temporal se puede tirar.
		rm -f "$sal2"
	fi

	if [ -n "$tocados" ]; then
		printf '  %-34s %5s %8s  ⚠ TOCO EL ARBOL: %s\n' "$tarea" "$rc" "$seg" "$(printf '%s' "$tocados" | cut -c1-60)"
		[ "$rc" -eq 0 ] || { n_rojas=$((n_rojas + 1)); ROJAS="$ROJAS $tarea"; }
	elif [ "$rc" -eq 0 ] && [ -z "$dif" ]; then
		printf '  %-34s %5s %8s\n' "$tarea" "0" "$seg"
	elif [ -n "$dif" ]; then
		printf '  %-34s %5s %8s  ⚠ CAMBIA CON EL ENTORNO: %s\n' "$tarea" "$rc" "$seg" "$dif"
		[ "$rc" -eq 0 ] || { n_rojas=$((n_rojas + 1)); ROJAS="$ROJAS $tarea"; }
	else
		n_rojas=$((n_rojas + 1)); ROJAS="$ROJAS $tarea"
		ev="$(guardar "$sal" "${tarea//[^A-Za-z0-9._-]/_}.log")"
		[ -n "$ev" ] && causa="$causa · evidencia: $ev"
		# ⛔ DOS CODIGOS, DOS COSAS DISTINTAS. `timeout` devuelve 124 cuando la pata muere con TERM y
		# 137 cuando hubo que rematarla con KILL diez segundos despues — o sea, cuando **ignoro TERM**.
		# Rotular solo el 124 dejaba a la segunda sin nombre, que es justo la que hay que ir a mirar.
		case "$rc" in
		124) causa="AGOTO EL PLAZO de ${TIMEOUT}s (rc 124, murio con TERM) — $causa" ;;
		137) causa="AGOTO EL PLAZO de ${TIMEOUT}s e IGNORO TERM: rematada con KILL (rc 137) — $causa" ;;
		esac
		printf '  %-34s %5s %8s  %s\n' "$tarea" "$rc" "$seg" "$causa"
	fi
	rm -f "$sal"
	# ⛔ NUNCA SE PARA EN LA PRIMERA ROJA, y es el punto entero de la herramienta: quien la corre
	# quiere saber CUANTAS curas le esperan, no cual es la primera. Parar aqui convertiria una
	# tarde en una cola de descubrimientos de uno en uno.
done <<EOF
$PATAS
EOF

say ""
if [ "$n_mudas" -gt 0 ]; then
	say "pre-verify-tanda: ⛔ NO HE PODIDO MIRAR — $n_mudas de $n_total pata(s) no existen como tarea en ese arbol:$MUDAS" >&2
	say "  El gate las deriva del gancho; si el Taskfile no las tiene, lo que discrepa es el arbol," >&2
	say "  no esta herramienta. No se puede dar un veredicto sobre lo que no se ha corrido." >&2
	exit 2
fi
if [ "$n_muta" -gt 0 ]; then
	say "pre-verify-tanda: HALLAZGO — $n_muta pata(s) MODIFICARON el arbol medido:$MUTADAS" >&2
	say "  Ese arbol es el de un lote a punto de empujarse: esos cambios viajarian en el push sin" >&2
	say "  que nadie los pidiera, y el sintoma llega despues como «tengo cosas sin commitear que yo" >&2
	say "  no toque». Revisa con 'git -C <arbol> status' y decide TU si se quedan." >&2
	exit 1
fi
if [ "$n_dif" -gt 0 ]; then
	say "pre-verify-tanda: HALLAZGO — $n_dif pata(s) CAMBIAN de veredicto con el entorno del gancho:$DIFS" >&2
	say "  Una pata que responde distinto segun el entorno esta midiendo algo que no controla, y el" >&2
	say "  veredicto que cuenta es el de DEBAJO del gancho, que es donde muere el push (GAT-173)." >&2
	exit 1
fi
if [ "$n_rojas" -gt 0 ]; then
	say "pre-verify-tanda: HALLAZGO — $n_rojas de $n_total pata(s) en rojo:$ROJAS" >&2
	say "  Se corrieron TODAS: eso es lo que esta herramienta compra. Cura las $n_rojas antes del push." >&2
	exit 1
fi
say "pre-verify-tanda: LIMPIO — las $n_total pata(s) que un push a rama no ejecuta pasan en ese arbol."
exit 0
