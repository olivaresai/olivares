#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-fast-lane-ceiling.sh — refuse to start a fast lane when the box already runs the ceiling.
#
# CENSUS-SUBJECT: external
#   Su sujeto son los PROCESOS de esta caja, no el repositorio: pasar sobre un árbol vacío es
#   CORRECTO, no un verde a ciegas. Se declara aquí y no en una lista del censo, porque una lista
#   que enumera miembros a mano caduca en silencio.
#
# WHY THIS EXISTS, measured 2026-09-01 (a repository gate). La fila #112-sexies fijó un techo de TRES
# carriles rápidos concurrentes por caja. A las 23:5xZ había CUATRO pushes de rama en vuelo
# —s1101-seo 33 min 33 min 54 min, COCKPIT-DESIGN 52 min—, todos arrancados DESPUÉS
# de que la fila se asentara, y **el único carril que hacía cola era el que la leía**: 25 minutos
# parado con el lote listo.
#
#   Una regla que cada carril se aplica a sí mismo leyendo un fichero penaliza EXACTAMENTE a quien
#   la lee y no frena a quien no. Es peor que no tenerla: la caja sigue con cuatro gates y además
#   hay uno parado sin usar su turno.
#
# Por eso el techo se impone donde ya está la lógica —el gancho— y con el fail-closed de la casa.
# Se puede saltar con `git push --no-verify`, y eso está bien: es una decisión DECLARADA, no un
# descuido.
#
# EL CENSO ES DE DOS ETAPAS, Y EL ORDEN IMPORTA (medido el mismo día). `pgrep -f 'task lint:'`
# **cuenta la propia shell que busca**, porque el patrón vive en su línea de comandos: 9 procesos
# con una etapa frente a 5 reales, un 44 % de sobreconteo. Y filtrar por `exe` NO basta: para un
# GUION, `exe` resuelve al INTÉRPRETE (`/bin/dash`, `/usr/bin/zsh`) y todas las patas quedan
# indistinguibles. Sirve `comm`:
#
#   1. `comm` == "task"  → una shell censadora es `zsh`, un gancho es `task`, una pata es `bash`.
#      Ese filtro solo ya saca del censo a quien pregunta.
#   2. sólo ENTONCES `cmdline` → para distinguir un gancho de otra tarea de `task`.
#
# LA UNIDAD ES EL CARRIL, NO EL PROCESO. Un gancho lanza varias tareas a la vez (una corrida real
# tenía dos procesos `task lint:` del mismo árbol), así que contar procesos daría el doble. Se
# agrupa por `cwd`, y el `cwd` del carril PROPIO se excluye: quien pregunta no se cuenta a sí mismo.
#
# ⚠ ADVISORY POR AHORA, Y CON CRITERIO DE PROMOCIÓN ESCRITO (decisión de the planner, a repository gate).
#   Esta pata **NO GATEA**: imprime el censo y sale 0. El «3» de la fila era un número SIN MEDIDA
#   —venía del techo de gates PESADOS— y rechazar a 3 el 2026-09-01 habría parado la flota entera:
#   medido ese día contra el /proc real, SEIS carriles concurrentes, incluida la propia corrida del
#   autor. Un gate que se cablea con un umbral que nadie ha medido no protege: para la caja.
#
#   CRITERIO DE PROMOCIÓN, para que esto no se quede advisory para siempre por inercia: cuando los
#   perfiles de duración —cada corrida publica el suyo: patas, `md5` del gancho, y throttle y nº de
#   ganchos concurrentes minuto a minuto— muestren un sobrecoste NORMALIZADO POR PATAS mayor de
#   1,5× a partir de N concurrentes, el techo se fija en ese N y esta pata pasa a RECHAZAR. Hasta
#   entonces sólo mide, y lo que mide es si la regla es aplicable.
#
#   ⚠ Y se dice lo que esto es: una comprobación que imprime y no decide NO es una guarda. Se
#     acepta aquí, y sólo aquí, porque lo que falta no es la cura sino el UMBRAL, y el umbral se
#     obtiene midiendo. En cuanto haya curva, o rechaza o se retira.
#
# Salida: 0 siempre que se pueda mirar (advisory) · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

TECHO="${OLIVARES_FAST_LANE_CEILING:-3}"
case "$TECHO" in
	'' | *[!0-9]*)
		echo "check-fast-lane-ceiling: ⛔ NO HE PODIDO MIRAR: OLIVARES_FAST_LANE_CEILING='$TECHO' no es un entero" >&2
		exit 2
		;;
esac

# La raíz PROPIA: el carril que pregunta. Sin ella no se puede excluir a uno mismo, y un censo que
# se cuenta a sí mismo rechaza al primer carril de una caja vacía.
PROPIA="${OLIVARES_FAST_LANE_SELF:-}"
if [ -z "$PROPIA" ]; then
	PROPIA="$(git rev-parse --show-toplevel 2>/dev/null || true)"
fi
[ -n "$PROPIA" ] || {
	echo "check-fast-lane-ceiling: ⛔ NO HE PODIDO MIRAR: no sé cuál es mi propio árbol (ni git ni OLIVARES_FAST_LANE_SELF)" >&2
	exit 2
}

PROC="${OLIVARES_FAST_LANE_PROC:-/proc}"
[ -d "$PROC" ] || {
	echo "check-fast-lane-ceiling: ⛔ NO HE PODIDO MIRAR: no existe $PROC" >&2
	exit 2
}

carriles=""
vistos=0
for entrada in "$PROC"/[0-9]*; do
	[ -d "$entrada" ] || continue
	pid="${entrada##*/}"

	# ETAPA 1 — por `comm`. Un proceso que no es `task` no es un gancho, y esto ya excluye a la
	# shell que corre este mismo guion.
	comm="$(cat "$entrada/comm" 2>/dev/null || true)"
	[ "$comm" = "task" ] || continue

	# ETAPA 2 — sólo ahora la línea de comandos, para separar un gancho de otra tarea de `task`.
	linea="$(tr '\0' ' ' <"$entrada/cmdline" 2>/dev/null || true)"
	case "$linea" in
	*"task lint:"*) ;;
	*) continue ;;
	esac

	vistos=$((vistos + 1))
	cwd="$(readlink "$entrada/cwd" 2>/dev/null || true)"
	[ -n "$cwd" ] || continue

	# LA UNIDAD ES EL CARRIL, y el carril es su WORKTREE, no el directorio en que casualmente esté
	# el proceso. Medido: los ganchos reales corren con `cwd` en la raíz, pero basta un `cd` dentro
	# de una pata para que el mismo carril aparezca dos veces y rechace a quien no toca. Se
	# normaliza a la raíz del worktree; si ahí no hay repositorio, se usa la ruta tal cual, que es
	# lo más estricto que se puede decir sin inventar.
	raiz_carril="$(git -C "$cwd" rev-parse --show-toplevel 2>/dev/null || true)"
	[ -n "$raiz_carril" ] && cwd="$raiz_carril"

	# El carril PROPIO no se cuenta. Se compara por ruta exacta o por prefijo con barra: sin la
	# barra, un prefijo sin barra casa con el de otro carril que empieza igual.
	case "$cwd" in
	"$PROPIA" | "$PROPIA"/*) continue ;;
	esac

	carriles="$carriles$cwd
"
done

ajenos="$(printf '%s' "$carriles" | sort -u | grep -c . || true)"
: "${ajenos:=0}"

if [ "$ajenos" -ge "$TECHO" ]; then
	echo "check-fast-lane-ceiling: AVISO — $ajenos carriles rápidos AJENOS ya corriendo, referencia $TECHO." >&2
	printf '%s' "$carriles" | sort -u | sed 's/^/    /' >&2
	cat >&2 <<'AYUDA'
    ADVISORY: esto NO para tu push. Un carril rápido más sobre una cuota de 6 CPU alarga a
    TODOS, y el que arranca el último paga la cola entera — pero el umbral aún no está medido,
    así que la pata mide en vez de decidir. Publica tu perfil (duración, patas, md5 del gancho
    y ganchos concurrentes por minuto): la curva es lo que fijará el techo.
AYUDA
	exit 0
fi

echo "check-fast-lane-ceiling: OK — $ajenos carriles rápidos ajenos (referencia $TECHO); $vistos proceso(s) de gancho vistos en total."
exit 0
