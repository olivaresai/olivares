#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-unbound-env.sh — una variable de entorno que en ESTA caja siempre existe no existe en
# los runners, y bajo `set -u` eso no es un aviso: es la muerte del guion.
#
# POR QUE EXISTE, y no es hipotetico: ha mordido CUATRO veces con la misma forma exacta.
#   · check-ci-ports.sh y census-blind-verdict.sh — «los runners sin $HOME, seis de nueve»,
#     segun sus propios comentarios ya corregidos.
#   · check-cosign-pins.sh — murio con «HOME: unbound variable» en ci-runner-9 el 2026-08-18.
#   · test-gates-failclosed.sh — el 2026-08-19 tumbo el job `control-plane` ENTERO en main,
#     y lo hizo del peor modo posible: la comprobacion de frontera de licencia habia dicho
#     «Boundary check OK across 259 package graphs», o sea que el rojo aparecia bajo el
#     rotulo «license boundary» cuando la licencia estaba perfecta. Un lector razonable
#     sale a buscar un conector Apache importando del nucleo AGPL que no existe.
#
# Eso es lo que justifica un gate en vez de arreglar el de turno: el sintoma NO SE PARECE a
# la causa, asi que cada vez cuesta una investigacion entera.
#
# QUE MIDE, exactamente y sin adornos: ficheros de shell que activan `set -u` y que leen una
# de las variables de la lista de abajo SIN forma por defecto (`${VAR:-algo}`). La lista es
# corta a proposito — solo variables que se ha COMPROBADO que faltan en los runners — porque
# una lista larga produce ruido y un gate ruidoso se desactiva.
#
# TRES RESPUESTAS: limpio (0), sucio (1), NO HE PODIDO MIRAR (2).
#
# LIMITES QUE ESTE GATE NO CUBRE, escritos para que nadie lea de mas en su verde:
#   · Solo mira las variables de VARS. Un `$FOO` sin guarda que tambien falte no se ve.
#   · Trabaja por lineas: un `$HOME` dentro de comillas SIMPLES es literal y no se expande,
#     y aqui se descarta por heuristica de linea, no por analisis del shell. Puede escaparse
#     un caso raro con comillas mezcladas, y eso es un falso VERDE — se prefiere a un falso
#     rojo, que es lo que hace que un gate se acabe apagando.
#   · No sigue `source`: un fichero sin `set -u` propio incluido desde otro que si lo tiene
#     hereda la opcion y este gate no lo vera.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VARS="HOME TMPDIR USER LOGNAME XDG_CACHE_HOME XDG_CONFIG_HOME"

escanea() {
	# Un lexico minimo, y hace falta: la primera version comparaba por LINEA y daba tres
	# falsos positivos, dos de ellos de la misma causa —un `\$HOME` escapado dentro de
	# comillas dobles es TEXTO, no expansion— y el tercero un `$HOME` dentro de una cadena
	# en comillas simples abierta lineas antes. Los dos casos se leen igual en una linea
	# suelta y significan lo contrario. Asi que se recorre caracter a caracter con tres
	# estados (normal / comilla simple / comilla doble), la comilla simple SOBREVIVE al
	# salto de linea, y una barra invertida se come el caracter siguiente.
	#
	# LIMITE CONOCIDO: no interpreta heredocs. Un `$HOME` dentro de un <<'EOF' se declara
	# literal y aqui contaria como lectura. Si aparece, se marca con `# unbound-ok` al final
	# de la linea y este gate lo respeta — con la exencion VISIBLE en el codigo, que es lo
	# contrario de una lista de exclusiones en otro fichero que nadie vuelve a leer.
	local raiz="$1"
	find "$raiz" -type f \( -name '*.sh' -o -name 'pre-push' -o -name 'commit-msg' \) 2>/dev/null |
		sort | while IFS= read -r f; do
		grep -qE '^[[:space:]]*set -[a-zA-Z]*u|set -o nounset' "$f" 2>/dev/null || continue
		printf '%s\n' "$f"
	done | while IFS= read -r f; do
		awk -v archivo="${f#"$ROOT"/}" -v vars="$VARS" '
			BEGIN { split(vars, V, " "); estado = "n" }
			{
				linea = $0
				if (linea ~ /# *unbound-ok *$/) next
				# Una guarda EN LA MISMA LINEA y ANTES del uso lo cubre: el patron
				#   [ -n "${VAR:-}" ] && hacer_algo "$VAR"
				# es correcto y muy usado aqui, y marcarlo empujaria a reescribir codigo
				# sano para contentar al gate — que es como un gate se gana que lo apaguen.
				# LIMITE: comprueba que la guarda este ANTES en el texto, no que domine el
				# flujo. Un `[ -n "${V:-}" ] || true; usa "$V"` pasaria. Se acepta: falso
				# verde estrecho frente a falso rojo que empuja a empeorar el codigo.
				delete guardada
				for (k in V) {
					pos = index(linea, "${" V[k] ":")
					if (pos > 0) guardada[V[k]] = pos
				}
				# ⛔ UNA ASIGNACION DEL PROPIO FICHERO CUBRE LAS LECTURAS POSTERIORES, y se detecta
				# AQUI y no en el bucle de abajo por una razon concreta: ese bucle solo examina
				# caracteres `$`, y `export TMPDIR=...` no lleva ninguno. Mi primer intento colgo el
				# registro de las ramas de asignacion de ese bucle y NO CAMBIO NADA — veredicto
				# identico al del gate original sobre los casos nuevos. Se vio comparando las dos
				# salidas, no leyendo el codigo.
				#
				# El caso que lo motiva: the short rehearsal script salio con VEINTE hallazgos y los
				# veinte eran `$TMPDIR` posteriores a su propio `export TMPDIR=...` de la linea 43.
				# La variable no viene del entorno: la pone el guion, asi que el motivo por el que
				# este gate existe —que en el runner no exista— no aplica.
				#
				# Y la cura mecanica habria sido PEOR: `${TMPDIR:-/tmp}` manda a /tmp, que en esta
				# caja es **noexec**, justo lo que la sonda de su linea 47 aborta. Es exactamente lo
				# que el comentario de la guarda de arriba llama «reescribir codigo sano para
				# contentar al gate».
				#
				# LIMITE, con el mismo criterio que la guarda de linea: mira el ORDEN en el texto,
				# no el flujo. Una asignacion dentro de un `if` que no se toma pasaria. Se acepta,
				# y por eso la bateria trae el caso `lee-y-asigna`, que DEBE seguir saliendo rojo.
				for (k in V) {
					if (!(V[k] in asignada) && linea ~ ("(^|[[:space:]])(export[[:space:]]+)?" V[k] "="))
						asignada[V[k]] = FNR
				}
				n = length(linea)
				for (i = 1; i <= n; i++) {
					c = substr(linea, i, 1)
					if (estado == "s") { if (c == "'"'"'") estado = "n"; continue }
					if (c == "\\") { i++; continue }
					if (estado == "d") {
						# Dentro de comillas DOBLES la variable SI se expande: aqui solo
						# se cierra la cadena, no se salta la comprobacion de $ — saltarla
						# fue el fallo de la primera version del lexico y dejo el gate en
						# cero hallazgos, que es el peor resultado posible para un gate.
						if (c == "\"") { estado = "n"; continue }
					} else {
						if (c == "'"'"'") { estado = "s"; continue }
						if (c == "\"") { estado = "d"; continue }
						if (c == "#" && (i == 1 || substr(linea, i-1, 1) ~ /[[:space:]]/)) break
					}
					if (c != "$") continue
					resto = substr(linea, i + 1)
					llave = (substr(resto, 1, 1) == "{")
					if (llave) resto = substr(resto, 2)
					for (k in V) {
						v = V[k]
						if (substr(resto, 1, length(v)) != v) continue
						sig = substr(resto, length(v) + 1, 1)
						if (sig ~ /[A-Za-z0-9_]/) continue          # otra variable mas larga
						if (llave && sig == ":") continue           # ${VAR:-...} ya tiene guarda
						if (v in guardada && guardada[v] < i) continue  # guarda previa en la misma linea
						if (!llave && sig == "=") continue          # asignacion
						if (i > 1 && substr(linea, 1, i-1) ~ /(^|[[:space:]])(export[[:space:]]+)?$/ && sig == "=") continue
						# ⛔ UNA LECTURA DOMINADA POR UNA ASIGNACION DEL PROPIO FICHERO NO PUEDE ESTALLAR.
						# Anadido el 2026-08-30: the short rehearsal script salio con VEINTE hallazgos,
						# y los veinte eran `$TMPDIR` **posteriores** a su propio
						# `export TMPDIR="$SCRATCH/.tmp-ensayo-$STAMP"` de la linea 43. La variable no
						# viene del entorno: la pone el guion, asi que el motivo por el que este gate
						# existe —que en el runner no exista— no aplica.
						#
						# Y la cura mecanica habria sido PEOR que el falso positivo: `${TMPDIR:-/tmp}`
						# manda a /tmp, que en esta caja es **noexec**, justo lo que la sonda de su
						# linea 47 existe para abortar. Un gate que empuja a romper el guion que revisa
						# no es un gate estricto: es un gate equivocado.
						#
						# El gate YA tenia esta idea en `guardada[]`, pero solo dentro de la MISMA
						# linea. Esto la lleva al fichero, que es donde `set -u` la evalua.
						if (v in asignada && asignada[v] <= FNR) continue
						printf "  %s:%d  $%s sin ${%s:-...}\n", archivo, FNR, v, v
						hallazgos++
					}
				}
			}
			END { printf "%d\n", hallazgos + 0 > "/dev/stderr" }
		' "$f"
	done
}

if [ "${1:-}" = "--selftest" ]; then
	fail=0
	caso="$(mktemp -d "${TMPDIR:-/tmp}/unbound-selftest.XXXXXX")"
	trap 'rm -rf "$caso"' EXIT
	printf '#!/bin/bash\nset -euo pipefail\necho "$HOME/x"\n' > "$caso/malo.sh"
	printf '#!/bin/bash\nset -euo pipefail\necho "${HOME:-/nope}/x"\n' > "$caso/bueno.sh"
	printf '#!/bin/bash\necho "$HOME/x"\n' > "$caso/sin-set-u.sh"
	printf '#!/bin/bash\nset -euo pipefail\necho '"'"'literal $HOME aqui'"'"'\n' > "$caso/comillas.sh"

	n="$(escanea "$caso" 2>/dev/null | grep -c . || true)"
	if [ "$n" = "1" ]; then
		echo "  ok    encuentra la lectura sin guarda y SOLO esa (1 de 4 ficheros)"
	else
		echo "  FAIL  esperaba 1 hallazgo, dio $n"; fail=1
		escanea "$caso" 2>/dev/null | sed 's/^/        /'
	fi

	out="$(bash "$0" "$caso" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "1" ] && grep -q 'malo.sh' <<<"$out"; then
		echo "  ok    un arbol sucio sale ROJO y nombra el fichero"
	else
		echo "  FAIL  un arbol sucio no salio rojo (rc=$rc)"; fail=1
	fi

	rm -f "$caso/malo.sh"
	out="$(bash "$0" "$caso" 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "0" ]; then
		echo "  ok    sin la lectura sin guarda queda verde (la direccion que no dispara)"
	else
		echo "  FAIL  un arbol limpio no quedo verde (rc=$rc)"; fail=1
	fi

	out="$(bash "$0" /ruta-que-no-existe-para-el-selftest 2>&1)" && rc=0 || rc=$?
	if [ "$rc" = "2" ] && grep -q 'NO HE PODIDO MIRAR' <<<"$out"; then
		echo "  ok    una raiz ilegible es NO HE PODIDO MIRAR, no verde"
	else
		echo "  FAIL  una raiz ilegible no dio la tercera respuesta (rc=$rc)"; fail=1
	fi

	# ⛔ AMBITO DE FICHERO: en SU PROPIO directorio, porque los casos de arriba comparten uno y
	#    el tercero borra `malo.sh` esperando VERDE — meter aqui un fichero sucio lo rompe. Lo
	#    aprendi rompiendolo: la bateria paso de 4/0 a FAILED en el caso del arbol limpio.
	amb="$(mktemp -d "${TMPDIR:-/tmp}/unbound-ambito.XXXXXX")"
	trap 'rm -rf "$caso" "$amb"' EXIT
	printf '#!/bin/bash\nset -euo pipefail\nexport HOME=/w/x\necho "$HOME/y"\n' > "$amb/asigna-y-lee.sh"
	na="$(escanea "$amb" 2>/dev/null | grep -c . || true)"
	if [ "$na" = "0" ]; then
		echo "  ok    una lectura DOMINADA por la asignacion del propio fichero no es hallazgo"
	else
		echo "  FAIL  esperaba 0 en asigna-y-lee, dio $na"; fail=1
		escanea "$amb" 2>/dev/null | sed 's/^/        /'
	fi

	# Y el que prueba que el ORDEN manda: sin este, un gate que ignorase toda variable asignada
	# en cualquier parte del fichero pasaria igual de verde y seria INCORRECTO.
	rm -f "$amb/asigna-y-lee.sh"
	printf '#!/bin/bash\nset -euo pipefail\necho "$HOME/y"\nexport HOME=/w/x\n' > "$amb/lee-y-asigna.sh"
	nb="$(escanea "$amb" 2>/dev/null | grep -c . || true)"
	if [ "$nb" = "1" ]; then
		echo "  ok    leer ANTES de asignar sigue siendo hallazgo (el orden manda)"
	else
		echo "  FAIL  esperaba 1 en lee-y-asigna, dio $nb"; fail=1
	fi

	[ "$fail" = "0" ] && { echo "check-unbound-env selftest: 6 passed, 0 failed"; exit 0; }
	echo "check-unbound-env selftest: FAILED"; exit 1
fi

RAIZ="${1:-$ROOT/scripts}"
EXTRA="${2:-$ROOT/.githooks}"
[ -d "$RAIZ" ] || {
	echo "check-unbound-env: NO HE PODIDO MIRAR — '$RAIZ' no es un directorio; no se ha" >&2
	echo "  examinado nada, y eso no es lo mismo que estar limpio." >&2
	exit 2
}

# Una sola pasada por raiz, y el total se cuenta de la SALIDA. La version anterior escaneaba
# dos veces y pegaba los dos textos sin salto de linea entre ellos, asi que el ultimo hallazgo
# de una raiz y el primero de la otra salian en el mismo renglon.
salida="$(escanea "$RAIZ" 2>/dev/null)"
if [ -d "$EXTRA" ] && [ "$EXTRA" != "$RAIZ" ]; then
	extra_txt="$(escanea "$EXTRA" 2>/dev/null)"
	[ -n "$extra_txt" ] && salida="$(printf '%s\n%s' "$salida" "$extra_txt")"
fi
salida="$(printf '%s' "$salida" | sed '/^$/d')"
total="$(printf '%s' "$salida" | grep -c . || true)"

if [ "$total" -gt 0 ]; then
	echo "check-unbound-env: SUCIO — $total lectura(s) sin forma por defecto bajo 'set -u':"
	printf '%s\n' "$salida"
	echo
	echo "  En los runners de CI estas variables NO siempre existen (HOME falta en seis de"
	echo "  nueve, medido). Bajo 'set -u' el guion muere en esa linea, y el rojo aparece con"
	echo "  el nombre del PASO, no con el de la variable: el 2026-08-19 un \$HOME sin guarda"
	echo "  tumbo 'control-plane' bajo el rotulo 'license boundary' con la licencia impecable."
	echo "  Arreglo: \${VAR:-valor}. Si no hay valor razonable, comprueba y di que falta."
	exit 1
fi
echo "check-unbound-env: LIMPIO — ninguna lectura sin guarda de [$VARS] bajo 'set -u'."
