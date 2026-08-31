#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-sigpipe-booleans.sh — `… | grep -q …` bajo `set -o pipefail` devuelve 141 CUANDO ACIERTA.
#
# ⛔ ESTO NO ES TEORÍA: costó una jornada el 2026-08-17. La casilla de calibración de
# `check-egress-claims.sh` puso ROJO el job `control-plane` —contexto REQUERIDO, o sea toda la
# cola— informando `README.md present=NO` sobre una superficie que SÍ lo contiene, y `task` lo
# remató con `exit status 141`.
#
#   141 = 128 + 13 = SIGPIPE.
#
# `productor | grep -q PATRON` hace que `grep -q` salga en cuanto encuentra la coincidencia y cierre
# la tubería. El productor muere con SIGPIPE y, bajo `pipefail`, **el pipeline devuelve 141
# exactamente cuando ha tenido ÉXITO**. Un `if` lee ese 141 como falso y declara AUSENTE lo que está
# presente. Es un falso negativo silencioso en un booleano.
#
# ⛔ Y ES UNA CARRERA, que es lo que lo hace tan caro de diagnosticar: si el productor termina de
# escribir antes de que `grep` se vaya, no pasa nada. Depende del tamaño de la salida, de dónde caiga
# la coincidencia y de la CARGA de la máquina. Por eso el mismo árbol daba verde en el hub y rojo en
# el runner, y por eso «en mi máquina funciona» aquí es literal y no una excusa. Reproducido
# determinista: con la coincidencia al principio de medio millón de líneas, rc=141 y el `if` dice
# AUSENTE.
#
# ⛔ TRINQUETE, NO PROHIBICIÓN, y el motivo es honesto: hay **56 tuberías así en 25 ficheros**
# (primera medida: 68 en 30, que incluía 12 en COMENTARIOS — ver el censo). Arreglarlas todas de golpe es una tanda que choca con toda rama viva. El suelo es
# lo que hay; lo único que se impide es que la cifra SUBA, y cada una se arregla cuando se toca su
# fichero. La forma correcta no cuesta nada:
#
#   MAL:  if lista | grep -qxF "$x"; then …
#   BIEN: l="$(lista)"; case "\n$l\n" in *"\n$x\n"*) … ;; esac
#   BIEN: if grep -qxF "$x" <(lista); then …        # sin tubería que cerrar
#
# ⛔ Y QUÉ SIGNIFICA EL NÚMERO DE LA LÍNEA BASE, porque leerlo mal es peor que no tenerlo: **no son
# 54 bugs latentes**. Barrido el 2026-08-17 sobre las 54: la inmensa mayoría son
# `printf '%s' "$var" | grep -q`, donde el productor es una variable de unos pocos KB que cabe
# entera en el búfer de la tubería (64 KB típicos) y por tanto NO puede recibir SIGPIPE. Las que sí
# podían morder son las de productor grande, y se buscaron por su forma —`git ls-files`, `find`,
# `git log`, `git grep`, `git for-each-ref` alimentando un booleano—: quedaron dos, y las dos son
# inocuas porque `find … -print -quit` emite una línea y termina.
#
# ⇒ Las peligrosas de verdad eran **las tres de `check-egress-claims.sh`** sobre el productor de
# 1245 rutas, y están las tres cerradas. El resto del censo es forma, no riesgo vivo: el trinquete
# existe para que la forma no se propague, no para declarar 54 incendios.
#
# ⛔ CORRECCIÓN DEL 2026-08-19, y es del RAZONAMIENTO de arriba, no de una tubería suelta. El
# párrafo anterior buscó los productores grandes **por su forma** —`git ls-files`, `find`,
# `git log`, `git grep`, `git for-each-ref`— y **no incluyó `grep <FICHERO>`**, que es un
# productor de fichero como cualquiera de ésos. Con ese hueco, esta linea de
# `test-release-workspace-e2e.sh` quedó clasificada como inocua:
#
#     command grep -vE '^[[:space:]]*#' "$WF_REAL" | command grep -q "'?? …') continue ;;"
#
# y puso **rojo `release mechanics`, y con él el job `control-plane` entero, en `main`**, mientras
# en el hub pasaba. Medido cinco corridas seguidas sobre ese mismo workflow: **141 141 0 0 141**.
#
# Y el diagnóstico costó de más por un segundo motivo que conviene recordar: el informe de fallo
# adjuntaba el `$out` de una comprobación ANTERIOR —«the build left the checkout modified»— así
# que el mensaje señalaba un fichero modificado que no tenía nada que ver.
#
# ⇒ La lección no es «quedaban tres»: es que **un censo por forma sólo ve las formas que enumera**.
# Si añades un productor nuevo a este razonamiento, dilo aquí.
#
# ⛔ CORRECCIÓN DEL 2026-08-28, MEDIDA, y es del RAZONAMIENTO que justifica la línea base: **«un
# productor pequeño NO PUEDE recibir SIGPIPE» es FALSO.** Arriba se descarta la mayoría del censo
# con ese argumento —«una variable de unos pocos KB cabe entera en el búfer de 64 KB y por tanto
# NO puede recibir SIGPIPE»—. La forma correcta del argumento no es «no puede»: es **«casi siempre
# gana la carrera»**, que es otra cosa.
#
# Medido en esta caja, `set -uo pipefail`, `printf '%s' "$var" | grep -q PATRON` con acierto:
#
#   productor de     94 bytes → 3000 corridas: rc=0 ×2999, **rc=141 ×1**
#   productor de 948 893 bytes →  200 corridas: rc=0 ×0,    **rc=141 ×200**
#
# El grande es determinista, como ya decía este fichero. **El pequeño es 1/3000, no cero.** Y esa
# tasa no es teórica: `scripts/test-docs-parity.sh` corría ~105 aserciones por pasada, varias por
# esta forma, y su cabecera documenta un rojo intermitente de **«1 corrida de cada 12-20, en un
# caso DISTINTO cada vez»** que nadie había explicado. 1/3000 por tubería, decenas de tuberías por
# pasada, y sube con la carga de la caja: **las dos cifras casan**. Era esto.
#
# ⇒ Lo que cambia para quien lea la línea base: **no es «forma, no riesgo vivo»** en los productores
# pequeños; es riesgo BAJO y ACUMULATIVO, que en una batería con decenas de aserciones se convierte
# en un flaky que bota el push de cualquier carril. Las cuatro de `test-docs-parity.sh` que
# alimentaban un booleano están cerradas (queda una, con `|| true`, que no puede llegar a ninguna
# comprobación). El trinquete sigue siendo trinquete; lo que se corrige es la razón por la que su
# suelo se consideraba inofensivo.
#
# Salida: 0 no sube · 1 hay una tubería NUEVA (la nombra) · 2 NO HE PODIDO MIRAR. Nunca un verde.
# ⛔ ALCANCE, DECLARADO CON SU CENSO PORQUE ES UNA DECISION Y NO UN OLVIDO.
#
# Lo que este trinquete cuenta son DOS formas, y las dos porque en ellas el rc de la tuberia SE LEE:
#   1. `productor | [VAR=v] [command] grep -q|--quiet …`  — el consumidor booleano clasico.
#   2. `VAR="$( … | head|read|grep -q … )"` seguido de `||`/`&&` — la asignacion cuyo rc se prueba.
#
# Lo que NO cuenta, y por que: `head` y `read` A SECAS. El contraste A-01 (the reviewer, 2026-08-30) pidio
# anadirlos, y son consumidores que cierran pronto igual que `grep -q`. Medido sobre este arbol
# antes de decidir:
#
#     `| head` sin contexto ....................... +180  (86 -> 266, TRIPLICA la linea base)
#     en linea con `if`/`while`/`&&`/`||` ..........  10  → los DIEZ son falsos positivos: el
#                                                          `head` va DENTRO del bloque, no en la
#                                                          expresion que se prueba
#     forma clasica `if productor | head …; then` ..   0
#
# El problema no es el consumidor: es que una sonda por LINEA no distingue «tuberia cuyo rc se
# prueba» de «tuberia dentro de un bloque que sigue a un booleano». **Un trinquete de 266 entradas
# con 180 de ruido deja de leerse**, y entonces no protege de nada. La forma 2 recupera los casos
# que SI importan con precision: 1 acierto y 0 ruido en todo el arbol.
#
# ⇒ Si alguien quiere ampliarlo, que lo haga con estas tres cifras delante y no a ciegas. Y si
# aparece una forma nueva en que el rc de un `head` se pruebe de verdad, el sitio de anadirla es la
# forma 2, no un `| head` a secas.

set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-sigpipe-booleans: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
BASE="${OLIVARES_SIGPIPE_BASELINE:-docs/sigpipe-booleans-baseline.txt}"
# El analizador de la forma 2 vive al lado del guion: es codigo, no configuracion.
AWK_M2="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/sigpipe-m2.awk"
[ -r "$AWK_M2" ] || { echo "check-sigpipe-booleans: ⛔ NO HE PODIDO MIRAR: falta $AWK_M2" >&2; exit 2; }
DIRS="${OLIVARES_SIGPIPE_DIRS:-scripts .githooks}"
for d in $DIRS; do
	[ -d "$d" ] || {
		echo "check-sigpipe-booleans: ⛔ NO HE PODIDO MIRAR: no existe $d" >&2
		exit 2
	}
done

# Sujeto: sólo ficheros que activan `pipefail`. Sin él, un 141 en la tubería no se propaga y el
# defecto no existe — acusar a esos ficheros sería inflar el censo con lo que no puede fallar.
censo() {
	local f m
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		grep -q 'pipefail' "$f" 2>/dev/null || continue
		# ⛔ SE IGNORAN LOS COMENTARIOS, y lo aprendí de la peor manera posible: al cablear este
		# gate escribí en el hook un comentario que EXPLICA el defecto —«`algo | grep -q X` bajo
		# pipefail devuelve 141»— y el censo lo contó como una tubería nueva. El gate se caza a sí
		# mismo por documentarse. Un comentario no abre ninguna tubería y no puede recibir SIGPIPE:
		# contarlo convierte la documentación del defecto en deuda, que es el peor incentivo posible.
		# `(^|[^|])` ANCLA la barra a que no venga precedida de otra. Sin eso, un OR
		# logico `grep -q A || grep -q B` casaba por su SEGUNDA barra y se contaba como
		# tuberia: medido el 2026-08-20 sobre check-eco-14-cur-live.sh:67, que no tiene
		# ninguna. Un falso positivo aqui no es ruido — manda a alguien a "arreglar" un
		# `||` correcto, y a mi me hizo perseguir una tuberia inexistente.
		# ⛔ LA SONDA VE TRES FORMAS, NO UNA, Y LAS DOS QUE FALTABAN ERAN EL 54 % DE LA CLASE.
		# Hasta hoy exigia que `grep` fuese lo PRIMERO tras la barra, asi que un `command` o un
		# prefijo de entorno la volvian invisible: `| command grep -q` y `| LC_ALL=C grep -q` no se
		# contaban. Lo destapo otro carril sobre este repo el 2026-08-30 y lo confirme midiendo el
		# overlay, donde la forma con `command` escondia una tuberia REAL en
		# `fips-verify-enterprise.sh` que habria sobrevivido intacta a una linea base a cero.
		# Descompuesto sobre un arbol LIMPIO el 2026-08-30: patron original **45** · admitiendo
		# `command` **80 (+35)** · admitiendo ademas prefijos `VAR=val` **80 (+0)**. La cifra costo
		# dos correcciones y las dos por la misma causa —medir el arbol de trabajo en vez del
		# commit—: primero publique **99 (+13)** contando 13 tuberias de una bateria mia sin
		# fusionar, y luego **86 (+41)** con un fichero SIN TRACKEAR colandose en el censo. **El 35
		# de esa medida era el correcto.** La rama de `VAR=val` no suma nada hoy y se deja:
		# es barata y la forma aparece en cuanto alguien escribe `| LC_ALL=C grep -q` — yo mismo lo
		# hice. El 141 no distingue la forma:
		# `grep -q` sale al primer acierto lo escriba quien lo escriba.
		# Forma 1: el consumidor booleano. `--quiet` es la forma larga de `-q` y hoy no la usa
		# nadie (+0), pero cuesta cero y cierra el hueco antes de que alguien la escriba.
		m1="$(sed 's/^[[:space:]]*#.*$//' "$f" | grep -cE '(^|[^|])\| *(command +|builtin +|[A-Za-z_][A-Za-z0-9_]*=[^ ]* +)*grep +(-[a-zA-Z]*q|--quiet)' 2>/dev/null || true)"
		# ⛔ Forma 2: LA ASIGNACION CUYO rc SE PRUEBA. `VAR="$( … | consumidor )" || …` — el rc de
		# la asignacion ES el de la tuberia, y el `||` lo prueba. Aqui SI cuentan `head` y `read`,
		# porque en esta forma su rc se lee de verdad.
		#
		# ⛔ Y SE ANALIZA CAMINANDO LA LINEA, NO CON UN PATRON, porque un patron por linea no
		# distingue un comando de la misma palabra DENTRO DE UNAS COMILLAS. the reviewer lo rompio dos
		# veces y las dos las reproduje: `X="$(printf '%s\n' 'cat f | head -1')"` no ejecuta
		# ninguna tuberia y daba positivo (A-05), y `X="$(yes x | builtin read -r y)"` SI la
		# ejecuta —141 real— y daba negativo porque el patron no admitia `builtin` (A-06, que la
		# forma 1 si admitia: dos ramas del mismo guion con reglas distintas).
		#
		# El analizador lleva estado de comillas, respeta la anidacion de parentesis, ignora el `||`
		# (que no es una tuberia) y exige el consumidor en POSICION DE COMANDO tras la barra, con
		# sus prefijos `command`/`builtin`/`VAR=val`. Verificado sobre los cuatro casos: texto entre
		# comillas simples 0, entre dobles 0, `builtin read` 1, tuberia real 1.
		m2="$(awk -f "$AWK_M2" "$f" 2>/dev/null || echo 0)"
		m=$(( ${m1:-0} + ${m2:-0} ))
		[ "${m:-0}" -gt 0 ] && printf '%s\t%s\n' "$m" "$f"
	done < <(find $DIRS -type f \( -name '*.sh' -o -name 'pre-push' -o -name 'commit-msg' \) 2>/dev/null | LC_ALL=C sort)
}

ACTUAL="$(censo)"
N_FICH="$(printf '%s\n' "$ACTUAL" | grep -c . || true)"
N_TUB="$(printf '%s\n' "$ACTUAL" | awk -F'\t' '{s+=$1} END{print s+0}')"

for a in "$@"; do
	# `--list` para sembrar o bajar la línea base sin leer el informe, que recorta.
	[ "$a" = "--list" ] && {
		printf '%s\n' "$ACTUAL"
		exit 0
	}
done

# CONTROL POSITIVO: la sonda tiene que poder encontrar algo. Con cero ficheros y sin línea base, un
# patrón caducado y un árbol limpio son indistinguibles, y el segundo aprobaría cualquier cosa.
if [ "${N_FICH:-0}" -eq 0 ] && [ ! -r "$BASE" ]; then
	echo "check-sigpipe-booleans: ⛔ NO HE PODIDO MIRAR: cero coincidencias y sin línea base." >&2
	exit 2
fi
if [ ! -r "$BASE" ]; then
	echo "check-sigpipe-booleans: ⛔ NO HE PODIDO MIRAR: no leo la línea base $BASE" >&2
	echo "                        Una línea base ausente no es «cero deuda»; es no haber mirado." >&2
	exit 2
fi

# ⛔ LA LÍNEA BASE ADMITE COMENTARIOS, y hace falta: sin ellos, una deuda de 30 entradas es una
# lista de rutas sin un solo «por qué», y quien la herede no sabe cuál entró por la sonda vieja y
# cuál apareció al ensancharla. Se filtran ANTES de comparar y ANTES de contar — un `#` contado
# como entrada inflaría la línea base y taparía una regresión real.
BASE_LIMPIA="$(mktemp "${TMPDIR:-/tmp}/sigbase.XXXXXX")" || exit 2
trap 'rm -f "$BASE_LIMPIA"' EXIT
grep -vE '^[[:space:]]*(#|$)' "$BASE" > "$BASE_LIMPIA" 2>/dev/null

# ⛔ LA LINEA BASE SE VALIDA POR FORMA ANTES DE USARSE, y el momento en que esto importa es el de la
# INTEGRACION. Medido el 2026-08-30: con el fichero en CONFLICTO —`<<<<<<<`, `=======`, `>>>>>>>`—
# el parser trataba los marcadores como filas de datos y el gate salia **rc 0 diciendo que la deuda
# BAJABA**, con una entrada cuyo nombre era la cadena vacia. Y ese estado no es raro: es
# exactamente el que tiene el integrador mientras resuelve el conflicto de este mismo fichero.
# **Un control es mas inutil justo cuando mas se necesita si acepta como dato lo que no entiende.**
# El mismo contrato que este guion ya exigia al predicado de saltos estructurales — la disciplina no
# se hereda sola entre ficheros del mismo guion.
MALA="$(awk -F'\t' '
	$0 !~ /^[0-9]+\t/ || NF != 2 || $2 == "" { printf "%d: %s\n", FNR, $0; salidas++ }
	END { exit (salidas > 0 ? 1 : 0) }' "$BASE_LIMPIA" 2>/dev/null)"
if [ -n "$MALA" ]; then
	echo "check-sigpipe-booleans: ⛔ NO HE PODIDO MIRAR: $BASE tiene lineas que no son" >&2
	echo "                        \`<entero><TAB><ruta>\` — marcadores de conflicto sin resolver," >&2
	echo "                        una cuenta que no es entera o una ruta vacia:" >&2
	printf '%s\n' "$MALA" | sed 's/^/                          /' >&2
	echo "                        Una linea base que no se entiende NO es «cero deuda» ni «la deuda" >&2
	echo "                        baja»: es no haber mirado." >&2
	exit 2
fi

# ⛔ SE COMPARA EL NÚMERO POR RUTA, NO LA FILA ENTERA — y la diferencia no es cosmética: un
# trinquete que pone ROJO a quien REDUCE deuda enseña a no reducirla. La comparación anterior era
# `grep -vxF` sobre `<n>\t<ruta>`, así que bajar de 2 a 1 en un fichero producía una fila «nueva»
# y un `exit 1`. Lo destapó the reviewer al contrastar el ensanche, y me había mordido a mí el mismo día:
# una cura mía bajó `check-git-env-isolation.sh` de 4 a 3 y el gate me paró el commit — lo
# racionalicé como «el trinquete pide bajar la línea base» en vez de verlo como el defecto que es.
# Ahora: SUBIR (o aparecer) es rojo; BAJAR (o desaparecer) es una mejora que se anuncia.
# ⛔ SE SEPARAN LOS DOS FICHEROS POR FILENAME, NO POR `NR == FNR`. Con la linea base VACIA —el
# caso al que este repo quiere llegar— `NR == FNR` es cierto para el PRIMER registro de la entrada
# siguiente, asi que la primera linea del censo se traga como si fuera linea base y el veredicto
# sale invertido: lo destapo mi propio banco al exigir el mensaje literal, que reportaba
# «BAJA sujeto.sh: 1 -> 0» donde debia decir «SUBE 0 -> 1». Un cero en el suelo no puede volver
# ciega la comparacion que existe para vigilarlo.
VEREDICTO="$(printf '%s\n' "$ACTUAL" | awk -F'\t' -v BASEF="$BASE_LIMPIA" '
	# ⛔ UNA LINEA VACIA NO ES UNA ENTRADA. Con el censo VACIO —el caso «desaparece la ultima
	# tuberia», que es adonde queremos llegar— `printf '%s\n' ""` emite UN registro vacio, y sin
	# esta guarda se contaba como una baja mas y se imprimia una fila fantasma `: 0 -> 0`, con la
	# ruta en blanco. Lo destapo the reviewer (A-02) y lo reproduje: «bajan 2» donde solo habia una.
	NF == 0 || $0 == "" { next }
	FILENAME == BASEF { base[$2] = $1; next }
	{
		vistos[$2] = 1
		if ($1 > base[$2] + 0) { printf "SUBE\t%s\t%d\t%d\n", $2, base[$2] + 0, $1; sube++ }
		else if ($1 < base[$2] + 0) { printf "BAJA\t%s\t%d\t%d\n", $2, base[$2] + 0, $1; baja++ }
	}
	END {
		for (r in base) if (!(r in vistos)) { printf "BAJA\t%s\t%d\t0\n", r, base[r]; baja++ }
		printf "TOTALES\t%d\t%d\n", sube + 0, baja + 0
	}' "$BASE_LIMPIA" -)"
SUBEN="$(printf '%s\n' "$VEREDICTO" | awk -F'\t' '$1 == "TOTALES" { print $2 }')"
BAJAN="$(printf '%s\n' "$VEREDICTO" | awk -F'\t' '$1 == "TOTALES" { print $3 }')"

echo "check-sigpipe-booleans: $N_TUB tubería(s) en $N_FICH fichero(s) con pipefail · línea base $(grep -c . <"$BASE_LIMPIA") · suben ${SUBEN:-0} · bajan ${BAJAN:-0}"

if [ "${SUBEN:-0}" -gt 0 ]; then
	echo "check-sigpipe-booleans: ⛔ la deuda SUBE — tubería(s) que pueden devolver 141 EN ÉXITO:" >&2
	printf '%s\n' "$VEREDICTO" | awk -F'\t' '$1 == "SUBE" { printf "                          %s: %d -> %d\n", $2, $3, $4 }' >&2
	echo "                        Sin tubería:  l=\"\$(lista)\"; case \"\$l\" in …  ·  o  grep -q X <(lista)" >&2
	exit 1
fi
if [ "${BAJAN:-0}" -gt 0 ]; then
	echo "check-sigpipe-booleans: ✔ la deuda BAJA en ${BAJAN} entrada(s) — baja la línea base en el mismo commit:"
	printf '%s\n' "$VEREDICTO" | awk -F'\t' '$1 == "BAJA" { printf "                          %s: %d -> %d\n", $2, $3, $4 }'
fi
echo "check-sigpipe-booleans: OK — la deuda no sube."
exit 0
