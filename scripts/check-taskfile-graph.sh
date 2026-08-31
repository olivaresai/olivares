#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-taskfile-graph.sh — D-2 del hook: que un carril feature NO pueda comprar una tarea pesada
# a través de una arista del Taskfile que el hook nunca escribe.
#
# ⛔ QUÉ PROPIEDAD CIERRA, y por qué estaba abierta. `.githooks/pre-push` declara en su cabecera
# (LIMITACIÓN N-3) que **gatea lo que LLAMA, no lo que sus herramientas ejecutan**: la garantía del
# split —«una rama feature no corre ninguna tarea pesada»— la prueba `test-prepush-refclass.sh`
# sobre las llamadas que el fichero hace, y **lo que go-task expande detrás de una de esas llamadas
# queda fuera de la observación y fuera de la afirmación**. Es decir: bastaba con que
# `lint:algo` ganara un `deps: [test]` para que el carril rápido arrastrara el gate pesado sin que
# ninguna batería lo viera.
#
# ⛔ Y POR QUÉ ESTE INTENTO ES DISTINTO DEL ANTERIOR. La ronda 11 lo intentó con un resolutor de
# YAML escrito a mano y lo dejó ABIERTO como L-01: era **ciego a sintaxis que go-task 3.51.1 acepta
# y ejecuta** — `deps: ['heavy']` y `deps: [{task: heavy}]` producían CERO aristas mientras
# `task --dry` sí corría la tarea. La cabecera del hook lo dice con todas las letras: *«una clausura
# que se pierde formas válidas en silencio certifica la propiedad que no puede ver, que es peor que
# no afirmarla. REOPEN usando el grafo de go-task como AUTORIDAD, no otro parser parcial de YAML.»*
#
# Esto usa **`task --dry`**: go-task resuelve su propio grafo y enumera cada tarea que ejecutaría,
# sin ejecutar nada. La autoridad es la herramienta, no mi lectura del fichero. Medido: 63 tareas
# resueltas en **2 s** en total.
#
# ⛔ LAS DOS LISTAS SE DERIVAN DEL HOOK, no se escriben aquí. Un gate que lleva su propia copia de
# los miembros caduca en silencio, y es la forma de gate que este repositorio ha encontrado rota más
# veces. El sujeto es `.githooks/pre-push`: lo que llama ANTES de `lint:prepush-refclass` es el
# carril rápido; lo que llama DESPUÉS es el gate pesado. Ese corte es el mismo que usa
# `test-prepush-refclass.sh`, así que las dos baterías no pueden discrepar sobre dónde está la línea.
#
# Salida: 0 ninguna tarea rápida alcanza una pesada · 1 alguna la alcanza (la nombra, con el camino)
#         2 NO HE PODIDO MIRAR (sin `task`, sin hook, listas vacías). Nunca es un verde.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
HOOK="${OLIVARES_HOOK:-$RAIZ/.githooks/pre-push}"
[ -r "$HOOK" ] || {
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: no se lee $HOOK" >&2
	exit 2
}
command -v task >/dev/null 2>&1 || {
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: no encuentro \`task\`; sin él no hay grafo." >&2
	exit 2
}

# --- 0 · FORMA de cada tarea: un mapa con algo que ejecutar ------------------------------------
# Este guion ya recorre el GRAFO del Taskfile; que no mirase la FORMA de sus nodos era recorrer
# las aristas de un grafo cuyos vertices podian no existir. El detalle y la medida del
# 2026-08-20 (catorce tareas rotas en `main`, siete de ellas ejecutables como PROSA), en la
# cabecera de scripts/taskfile-shape.py.
TF_YML="${OLIVARES_TASKFILE:-$RAIZ/Taskfile.yml}"
SHAPE="$RAIZ/scripts/taskfile-shape.py"
# ⛔ AQUI HABIA UN `if [ -r ... ]` SIN RAMA `else` Y UN `2>/dev/null || true`, Y LOS DOS
# CONVIERTEN «no he podido mirar» EN «limpio». Medido el 2026-08-20 plantando el defecto, con el
# control positivo delante para que la medida signifique algo:
#
#   arbol bueno ................................. rc 0   (control negativo)
#   Taskfile con una tarea sin cmds ............. rc 1   (control POSITIVO: el gate sabe ponerse rojo)
#   Taskfile con una tarea de valor CADENA ...... rc 1   (control POSITIVO)
#   taskfile-shape.py revienta .................. rc 0   ⛔ CIEGO
#   taskfile-shape.py ilegible .................. rc 0   ⛔ CIEGO
#
# Los dos ultimos son la clase «un gate ciego no falla: certifica». Y esta guarda existe
# precisamente porque DIECISEIS tareas de main habian perdido sus claves sin que nada se pusiera
# rojo: un gate que se salta a si mismo cuando su herramienta falla repite ese fallo con otro
# nombre. La rama `""` sigue siendo limpio, pero SOLO cuando python salio 0.
if [ ! -r "$TF_YML" ]; then
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: no puedo leer $TF_YML" >&2
	exit 2
fi
if [ ! -r "$SHAPE" ]; then
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: no puedo leer $SHAPE" >&2
	exit 2
fi
if true; then
	err_forma=$(mktemp "${TMPDIR:-/tmp}/taskfile-shape-err.XXXXXX") || exit 2
	forma="$(python3 "$SHAPE" "$TF_YML" 2>"$err_forma")"
	rc_forma=$?
	if [ "$rc_forma" -ne 0 ]; then
		echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: $SHAPE salio $rc_forma" >&2
		sed 's/^/                       /' "$err_forma" >&2
		rm -f -- "$err_forma"
		exit 2
	fi
	rm -f -- "$err_forma"
	case "$forma" in
	NOPUEDO*)
		echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR la forma: $forma" >&2
		exit 2
		;;
	"") : ;;
	*)
		echo "check-taskfile-graph: ⛔ tarea(s) con la FORMA rota en $TF_YML:" >&2
		printf '%s\n' "$forma" |
			sed -e 's/^CADENA\t/  ⛔ su valor es una CADENA — task la ejecutaria como orden: /' \
				-e 's/^SINCMD\t/  ⛔ sin cmds\/cmd\/deps — no hace nada y sale 0: /' \
				-e 's/^PARSE\t/  ⛔ el YAML no parsea: /' >&2
		echo "  Suele ser una clave perdida en un merge: su contenido se pliega en el vecino." >&2
		exit 1
		;;
	esac
fi

# --- las dos listas, derivadas del hook ---------------------------------------------------------
# El marcador de corte es la ÚLTIMA llamada del carril rápido, igual que en test-prepush-refclass.sh.
CORTE='task lint:prepush-refclass'
n_corte="$(grep -n "^${CORTE}\$" "$HOOK" | head -1 | cut -d: -f1)"
if [ -z "${n_corte:-}" ]; then
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: no encuentro '${CORTE}' en el hook." >&2
	echo "                      Sin ese marcador no sé dónde acaba el carril rápido, y adivinarlo" >&2
	echo "                      convertiría una tarea pesada en 'rápida' por descuido." >&2
	exit 2
fi
# El ancla es `^[[:space:]]*task`, IGUAL que la de PESADAS unas lineas mas abajo, y no
# `^task`: el hook llama a `lint:commit-identity` INDENTADO dentro de un condicional
# (linea 395). Con el ancla en columna 0 esa tarea no entraba en el conjunto rapido, asi
# que su grafo nunca se comparaba contra el pesado. Medido por mutacion el 2026-08-18:
# con `deps: [build:cloud]` colgado de lint:commit-identity, este script imprimia
# "OK -- ninguna tarea del carril rapido alcanza el gate pesado". Un instrumento que
# certifica la propiedad sobre 80 de 81 tareas la certifica sobre un conjunto que no es
# el que dice medir.
# EXTRACCION COMPARTIDA POR LAS DOS LISTAS. Pela el sangrado y cualquier prefijo de
# asignaciones de entorno, y solo entonces exige que la linea EMPIECE por `task`. Es lo que
# distingue una INVOCACION de una MENCION: un `echo "... task test ..."` empieza por `echo`
# y un comentario por `#`, asi que ninguno entra.
#
# Las dos anclas eran distintas y la del carril rapido pedia `task` en columna 0. El hook
# llama a `lint:commit-identity` INDENTADO dentro de un condicional (linea 395), asi que esa
# tarea no entraba en el conjunto rapido y su grafo nunca se comparaba con el pesado. Medido
# por mutacion el 2026-08-18: con `deps: [build:cloud]` colgado de lint:commit-identity este
# script imprimia "OK -- ninguna tarea del carril rapido alcanza el gate pesado", y con el
# arreglo nombra el cruce. Contaba 80 tareas de 81.
#
# El lado pesado tenia la misma forma latente: `GOFLAGS="-p=1" GOMAXPROCS=2 task test`
# (linea 1205) tampoco abre linea. Ahi no habia agujero porque `test` aparece ademas suelto,
# pero la siguiente tarea pesada que solo se invoque con prefijo habria desaparecido de la
# lista, y una lista pesada incompleta certifica limpio exactamente igual.
extraer_tareas() {
	sed -e 's/^[[:space:]]*//' \
	    -e ':a' -e 's/^[A-Za-z_][A-Za-z0-9_]*=\("[^"]*"\|'"'"'[^'"'"']*'"'"'\|[^[:space:]]*\)[[:space:]]\+//; ta' \
	  | sed -n 's/^task[[:space:]]\+\([a-z0-9:._-]*\).*/\1/p' | sort -u
}
RAPIDAS="$(head -n "$n_corte" "$HOOK" | extraer_tareas)"
PESADAS="$(tail -n +"$((n_corte + 1))" "$HOOK" | extraer_tareas)"

n_rapidas="$(printf '%s\n' "$RAPIDAS" | grep -c . || true)"
n_pesadas="$(printf '%s\n' "$PESADAS" | grep -c . || true)"

# CONTROL POSITIVO POR PARTIDA DOBLE: con cualquiera de las dos listas vacía, «cero cruces» sería
# cierto y vacío a la vez. Un conjunto vacío no aprueba nada.
if [ "${n_rapidas:-0}" -lt 10 ] || [ "${n_pesadas:-0}" -lt 3 ]; then
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR: listas derivadas inservibles" >&2
	echo "                      (rápidas=${n_rapidas:-0}, pesadas=${n_pesadas:-0}). Con una lista vacía," >&2
	echo "                      «ninguna rápida alcanza una pesada» es cierto por vacuidad." >&2
	exit 2
fi

echo "check-taskfile-graph: ${n_rapidas} tarea(s) del carril rápido contra ${n_pesadas} pesada(s), con \`task --dry\` como autoridad"

cruces=0
sin_resolver=0
while IFS= read -r t; do
	[ -n "$t" ] || continue
	# `task --dry` imprime una línea `task: [<nombre>] <comando>` por cada tarea que EJECUTARÍA,
	# incluidas las que entran por `deps:`, por `cmds: - task:` y por cualquier forma que go-task
	# acepte hoy o mañana. Eso es justo lo que el resolutor a mano no podía ver.
	salida="$(task --dry "$t" 2>&1)" || {
		sin_resolver=$((sin_resolver + 1))
		echo "check-taskfile-graph: ⚠ no pude resolver el grafo de '$t' (se cuenta y no se aprueba)"
		continue
	}
	alcanzadas="$(printf '%s\n' "$salida" | sed -n 's/^task: \[\([a-z0-9:._-]*\)\].*/\1/p' | sort -u)"
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		if printf '%s\n' "$alcanzadas" | grep -qx "$p"; then
			cruces=$((cruces + 1))
			echo "check-taskfile-graph: ⛔ '$t' (carril RÁPIDO) alcanza '$p' (gate PESADO)"
		fi
	done <<EOF_P
$PESADAS
EOF_P
done <<EOF_R
$RAPIDAS
EOF_R

if [ "$sin_resolver" -gt 0 ]; then
	echo "check-taskfile-graph: ⛔ NO HE PODIDO MIRAR ${sin_resolver} tarea(s): sin su grafo no puedo" >&2
	echo "                      afirmar que no cruzan. Un grafo que no se resuelve no es un grafo limpio." >&2
	exit 2
fi

if [ "$cruces" -gt 0 ]; then
	echo "check-taskfile-graph: ⛔ ${cruces} cruce(s). Una rama feature pagaría el gate pesado por una" >&2
	echo "                      arista del Taskfile que el hook NO escribe — que es exactamente la" >&2
	echo "                      mitad de la garantía del split que estaba sin observar (N-3 / D-2)." >&2
	exit 1
fi
echo "check-taskfile-graph: OK — ninguna tarea del carril rápido alcanza el gate pesado por el grafo."
exit 0
