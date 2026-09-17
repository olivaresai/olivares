#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Bateria de pre-verify-tanda.sh. Monta un arbol de laboratorio con su PROPIO Taskfile y un
# `check-gate-parity.sh` FALSO que solo sabe contestar `--print-heavy` / `--print-ci-only`. No toca
# el arbol de verdad, no construye nada, no coge el mutex.
#
# ⛔ EL CASO QUE MANDA ES EL DEL MUTANTE, y no es ceremonia: el valor entero de la herramienta es
# que corre TODAS las patas y nombra TODAS las rojas. Una version que se pare en la primera sigue
# saliendo 1 y sigue nombrando una pata — se ve igual de bien en la salida y convierte una tarde en
# una cola de descubrimientos de uno en uno. Por eso el mutante no comprueba el rc: comprueba
# CUANTAS nombra.
set -uo pipefail

# ⛔ MODO DIAGNOSTICO OPT-IN, POR BANDERA Y NUNCA POR ENTORNO. `--solo-primer-caso` corre SOLO el
# caso (1) —el laboratorio benigno de todas verdes— con su diagnostico, y se para. Existe para
# reproducir en la clase de runner que fallo (run 34131797918, 2026-09-07) sin correr el banco
# entero. Es una bandera y no una variable a proposito: una variable heredada podria recortar el
# banco EN SILENCIO; una bandera hay que escribirla. El banco completo sigue siendo el valor por
# defecto y no cambia. Cualquier otra opcion es NO HE PODIDO MIRAR (2), no se ignora.
SOLO_PRIMERO=0
for _a in "$@"; do
	case "$_a" in
	--solo-primer-caso) SOLO_PRIMERO=1 ;;
	*) echo "test-pre-verify-tanda: 2 NO HE PODIDO MIRAR — opcion desconocida: $_a (solo admite --solo-primer-caso)" >&2; exit 2 ;;
	esac
done
unset _a

# ⛔ MI `unset` DE DOS VARIABLES SE RETIRA AQUI: lo sustituye el de KERNEL25 (mas abajo), que
# DERIVA la lista del propio gancho en vez de nombrarlas a mano, asi que la septima variable
# que alguien exporte manana queda cubierta sin editar este fichero. Mio era correcto hoy y
# viejo al siguiente `export`. Lo que SI se queda es mi precondicion del TMPDIR, que la suya
# no cubre: son dos causas distintas y las dos existen (66/8 por la variable, 72/2 por el
# montaje noexec, y las filas que caen no son las mismas).

# ⛔ SEGUNDA SENSIBILIDAD AL ENTORNO, INDEPENDIENTE DE LA DE ARRIBA, Y AQUI LA RESPUESTA CORRECTA NO
# ES UN ROJO: la fila «un git status roto es NO PUDE MIRAR (2)» monta un `git` senuelo bajo
# $BASE/shim y NECESITA EJECUTARLO. Si TMPDIR no esta exportado, $BASE cae en /tmp, que en esta caja
# es NOEXEC: el senuelo no corre, `git status` funciona de verdad y el sujeto contesta 0 en vez de 2.
# Se lee como DOS FALLOS del arbol medido y no lo son — es que el banco no ha podido montar su
# instrumento. Medido el 2026-09-04 sobre el mismo arbol: TMPDIR exportado 74/74, sin exportar 72/2,
# y un guion en /tmp sale rc 126 mientras en $TMPDIR sale 7.
# ⇒ Una precondicion que no se cumple es NO HE PODIDO MIRAR (2), no un hallazgo (1). Se comprueba
#   aqui, antes de la primera fila, porque una precondicion en prosa es una que nadie comprueba.
_bat_t="$(mktemp "${TMPDIR:-/tmp}/batexec.XXXXXX")" || { echo "pre-verify-tanda: NO PUDE MIRAR — no puedo escribir en ${TMPDIR:-/tmp}" >&2; exit 2; }
printf '#!/bin/sh\nexit 7\n' >"$_bat_t"; chmod +x "$_bat_t" 2>/dev/null
"$_bat_t" >/dev/null 2>&1; _bat_rc=$?; rm -f "$_bat_t"
[ "$_bat_rc" = 7 ] || {
	echo "pre-verify-tanda: NO PUDE MIRAR — ${TMPDIR:-/tmp} es NOEXEC (un guion ahi sale rc $_bat_rc)." >&2
	echo "  Este banco monta un 'git' senuelo y necesita ejecutarlo; sin eso, dos filas darian un" >&2
	echo "  FALSO rojo del arbol medido. Exporta TMPDIR a un sitio ejecutable y vuelve a correr." >&2
	exit 2
}
unset _bat_t _bat_rc
export LC_ALL=C

# ⛔ AISLAMIENTO DEL ENTORNO GIT. Este banco monta clones con `mktemp -d` y corre `git` dentro: con
# las GIT_* del entorno heredadas, esos `git` podrian resolver contra OTRO repositorio — y aqui eso
# significaria que la huella del arbol (la que detecta si una pata lo toco) se tomara del clon de
# verdad. Lo exige `lint:git-env` para toda pata que junte `mktemp -d` con git.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"

# ⛔ EL ENTORNO DEL GANCHO SE RETIRA ANTES DE MEDIR, Y ESTO ES UNA CURA CON SU MEDIDA DETRAS.
# Varios casos plantan una variable en el SEGUNDO pase y exigen que la biseccion la nombre. Eso
# supone que la variable NO esta en el entorno ambiental — cierto al correr la bateria a mano, FALSO
# dentro de un pre-push: el gancho exporta `OLIVARES_PUSH_CLASS` (`.githooks/pre-push:320`) y otras
# cinco. Con ella ya puesta, los dos pases la ven igual, no hay diferencia que bisecar, y caen ocho
# filas — las del segundo pase y la biseccion.
#
# Medido el 2026-09-05 sobre un banco acreditado (misma disposicion, 74/0 con el techo original):
#     sin la variable ....................... 74 passed, 0 failed
#     con OLIVARES_PUSH_CLASS=fast .......... 66 passed, 8 failed  ← las MISMAS ocho del push real
# El push murio asi tras 1 h 14 de gancho, y el arbol estaba sano: era la bateria contestando por su
# entorno. Una bateria cuyo veredicto depende de si la invocas dentro o fuera del gancho no es una
# bateria — es la misma clase que «depende de la rama del que la corre».
#
# Se retiran las SEIS que el gancho exporta, no solo la culpable: manana anadira una septima y este
# fichero no se entera. La lista se deriva del gancho, que es su unica fuente de verdad.
_hk="${OLIVARES_HOOK_FOR_ENV:-$RAIZ/.githooks/pre-push}"
# ⛔ RUTA ABSOLUTA Y GUARDA: con la ruta RELATIVA esta cura no hacia NADA desde otro `cwd` —
#    medido, 66/8 otra vez— y sin decirlo. Una cura muda es peor que no tenerla: se cree aplicada.
if [ ! -r "$_hk" ]; then
	echo "test-pre-verify-tanda: 2 NO HE PODIDO MIRAR — no leo $_hk, no puedo derivar el entorno" >&2
	echo "  del gancho para retirarlo, y sin eso este banco contesta por su entorno." >&2
	exit 2
fi
for _v in $(command grep -oE '^export [A-Z_]+' "$_hk" 2>/dev/null | awk '{print $2}'); do
	unset "$_v" || true
done
unset _v
SUT="$RAIZ/scripts/pre-verify-tanda.sh"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/preverify-bat.XXXXXX")" || exit 2
trap 'rm -rf "$BASE"' EXIT INT TERM

# ⛔ Los mutantes viven en `$BASE` y el SUT carga `lib/git-env.sh` relativo a SU ruta: sin esta
# copia mueren en ese FATAL y el rojo seria por la ubicacion, no por lo que se les quito. Es la
# tercera vez hoy que esta trampa muerde, y por eso la copia va aqui arriba, una sola vez.
mkdir -p "$BASE/lib"
cp "$RAIZ/scripts/lib/git-env.sh" "$BASE/lib/" 2>/dev/null

pasados=0; fallados=0
check() { # etiqueta esperado obtenido
	if [ "$2" = "$3" ]; then
		printf '  ok   %-56s %s\n' "$1" "$3"; pasados=$((pasados + 1))
	else
		printf '  FAIL %-56s esperado=%s obtenido=%s\n' "$1" "$2" "$3"; fallados=$((fallados + 1))
	fi
}

# ⛔ LA CAUSA DEL SUJETO SE CONSERVA CUANDO EL PRIMER CASO NO DA LO ESPERADO. Medido el 2026-09-07 en
# CI (run 34131797918, job 101773452031, paso `leg-pre-verify-tanda-selftest`, fuente 70e2c60b81):
# 29 passed, 45 failed, y las 45 nacian de lo mismo — el sujeto contestaba rc 2 HASTA en el
# laboratorio de todas verdes. Su `cannot()` NOMBRA la causa (`pre-verify-tanda.sh:44`), pero este
# banco la capturaba en `out` (ambos flujos), comparaba solo el rc, y la siguiente fila la
# sobreescribia sin imprimirla nunca. Resultado: un rc sin causa, 45 rojos sin accion, y la causa
# raiz en el runner sigue DESCONOCIDA porque no quedo en ningun log ni artefacto.
#
# Lo que se conserva, y lo que NO: la salida capturada del sujeto para el caso (1) —un laboratorio
# benigno con dos `true`, que no corre ninguno de los escenarios posteriores de credenciales—, con su
# rc exacto, ACOTADA (lineas y columnas) y REDACTADA (userinfo de URL, tokens, VAR=valor sensibles),
# mas los TRES prerrequisitos que el arranque comun del sujeto comprueba antes de la primera pata:
# `task` en el PATH (:109), `timeout` en el PATH (:110) y `task --list-all --color=false` en el
# laboratorio (:138 — la misma invocacion que hace el sujeto, bandera incluida; desde R18 el sujeto
# conserva la ultima linea del productor en su refusal cuando este falla, y esta sonda sigue
# guardando los dos flujos enteros). Las dos sondas que lanzan algo llevan plazo propio y tope de
# captura (ver `sonda_acotada`). NO se vuelca el entorno, NO se imprime la salida de los demas
# casos y NADA se salta: las filas FAIL y el veredicto final quedan exactamente como estaban.
# ⛔ PRIMERO SE NORMALIZA, LUEGO SE REDACTA — y el orden es la cura, no un detalle. La revision
# independiente del 2026-09-07 midio la version anterior con dos canarios sinteticos: `TOK<0x01>EN=x`
# salia como `TOKEN=x` porque los bytes de control se quitaban DESPUES de buscar el patron, y
# `API_TOKEN='prefijo x'` dejaba `x'` a la vista porque el valor se cortaba en el primer espacio
# aunque estuviera entrecomillado. Hoy: (1) secuencias CSI y bytes de control fuera, (2) redaccion
# sobre el texto ya normalizado, (3) tope de lineas y columnas.
#
# ⛔ Y LA GRAMATICA DE COMILLAS SE RETIRA ENTERA: en cuanto aparece el nombre de una asignacion
# sensible se redacta EL RESTO DE LA LINEA. La segunda pasada de la misma revision midio que la
# version por comillas SEGUIA filtrando —cortaba en el primer byte de comilla, sin mirar el escape—
# y publicaba lo que venia detras (canarios sinteticos, nunca credenciales):
#     API_TOKEN="prefijo \" CANARIO" plain=ok   ->  API_TOKEN=<redacted> CANARIO" plain=ok
#     API_TOKEN='prefijo'\'' CANARIO' plain=ok  ->  API_TOKEN=<redacted>'' CANARIO' plain=ok
# No es un patron al que le falte un caso: saber donde acaba un valor exige el mismo analisis lexico
# que hace el shell, y aqui equivocarse por poco publica el secreto entero. Un tramo de linea de mas
# es ruido; uno de menos es una fuga. ⇒ Se redacta de mas, y se DICE cuanto.
# Lo que precede al nombre, el nombre mismo y el host de una URL siguen visibles: se redacta el
# secreto, no el dato. Y por aqui pasa TODO campo dinamico del diagnostico, no solo la salida del
# sujeto: las rutas que devuelve `command -v`, la captura conjunta stdout/stderr de cada sonda.
DIAG_MAX_LINEAS=40
DIAG_MAX_COLS=240
_SENSIBLE='[A-Za-z0-9_]*(TOKEN|SECRET|PASSW|CREDENTIAL|_KEY)[A-Za-z0-9_]*='
acotar() { # acotar [max lineas] : stdin -> stdout normalizado, redactado y acotado
	local max="${1:-$DIAG_MAX_LINEAS}"
	LC_ALL=C sed -E -e 's/\x1b\[[0-9;?]*[A-Za-z]//g' \
	| LC_ALL=C tr -d '\000-\010\013-\037\177' \
	| LC_ALL=C sed -E \
		-e 's#://[^/@[:space:]]*@#://<redacted>@#g' \
		-e 's/\b(gh[pousr]|github_pat)_[A-Za-z0-9_]+/<redacted>/g' \
		-e "s/(${_SENSIBLE}).*\$/\1<redacted> (resto de la linea redactado: asignacion sensible)/" \
	| LC_ALL=C awk -v max="$max" -v cols="$DIAG_MAX_COLS" '
		NR <= max { if (length($0) > cols) print substr($0, 1, cols) "...(+" length($0) - cols " bytes)"; else print; next }
		END { if (NR > max) printf "...(%d linea(s) mas, omitidas: tope %d)\n", NR - max, max }'
}

# ⛔ UNA SONDA OPCIONAL TIENE PLAZO Y TOPE DE CAPTURA, O NO SE LANZA. Lo midio la revision
# independiente del 2026-09-07: con un `task` sintetico cuyo `--version` dormia, el sujeto rehusaba
# en 6 ms y el banco se quedaba esperando en la sonda de diagnostico —sin plazo propio, la mato una
# guarda externa a los 3 s—. Un diagnostico que puede bloquear el banco cuyo sujeto ya contesto es
# peor que no tenerlo: sustituye la respuesta original por un cuelgue.
#
# El auxiliar Linux conserva un lider propio hasta ECHILD o KILL del grupo,
# antes de recogerlo. Cap/EOF tambien cierran la custodia. No alcanza un detach
# intencional. Python/prctl se verifican sin lanzar la orden; si faltan, SIN MEDIR.
# El helper viaja con scripts/lib en el export; la guarda local cubre su ausencia.
DIAG_PLAZO=5
DIAG_MAX_BYTES=8192
DIAG_HELPER="$RAIZ/scripts/lib/preverify-capture.py"
DIAG_PYTHON=""
DIAG_DETAIL=""
sonda_acotada() { # rc auxiliar; DIAG_DETAIL separa productor, captura y custodia
	local dest="$1" dir="$2"; shift 2
	DIAG_DETAIL="producer=unknown capture=unmeasured bytes=0 completeness=unknown custody=unknown"
	: >"$dest"
	if [ -z "$DIAG_TIMEOUT" ] || [ -z "$DIAG_PYTHON" ] || [ ! -r "$DIAG_HELPER" ]; then return 125; fi
	DIAG_DETAIL="$("$DIAG_PYTHON" -I "$DIAG_HELPER" "$dest" "$dir" "$@" 2>/dev/null)"
	return "$?"
}
DIAG_TIMEOUT=""
diagnostico_sujeto() { # diagnostico_sujeto <etiqueta> <rc> <salida capturada> <arbol del laboratorio>
	local et="$1" rc="$2" out="$3" arbol="$4" bytes t cap lrc nota
	bytes="$(printf '%s' "$out" | LC_ALL=C wc -c | tr -d ' ')"
	printf '  ⚠ DIAGNOSTICO %s: el sujeto contesto rc=%s y NO era lo esperado. Se conserva su salida\n' "$et" "$rc"
	printf '    (%s bytes, ambos flujos; acotada a %s lineas x %s columnas; redactada), porque un rc sin\n' "$bytes" "$DIAG_MAX_LINEAS" "$DIAG_MAX_COLS"
	printf '    causa es lo que dejo 45 filas rojas sin accion el 2026-09-07 (run 34131797918):\n'
	if [ -n "$out" ]; then
		printf '%s\n' "$out" | acotar | sed 's/^/    | /'
	else
		printf '    | (salida VACIA: el sujeto no escribio nada en ningun flujo)\n'
	fi
	# Prerrequisitos: `command -v` es un builtin que lee PATH, no lanza nada, asi que no necesita plazo.
	printf '    prerrequisitos del arranque del sujeto, medidos desde este banco con el PATH que el sujeto heredo:\n'
	if t="$(command -v task 2>/dev/null)"; then printf '    | task    : %s\n' "$(printf '%s\n' "$t" | acotar 1)"; else t=""; printf '    | task    : AUSENTE en el PATH\n'; fi
	if DIAG_TIMEOUT="$(command -v timeout 2>/dev/null)"; then printf '    | timeout : %s\n' "$(printf '%s\n' "$DIAG_TIMEOUT" | acotar 1)"; else DIAG_TIMEOUT=""; printf '    | timeout : AUSENTE en el PATH\n'; fi
	[ -n "$t" ] || return 0
	if [ -z "$DIAG_TIMEOUT" ]; then
		printf '    | sondas opcionales (task --version, task --list-all): SIN MEDIR — no hay '"'"'timeout'"'"' con que acotar su plazo,\n'
		printf '    |   y una sonda sin plazo podria colgar este banco tapando la respuesta del sujeto. No se lanzan.\n'
		return 0
	fi
	if ! DIAG_PYTHON="$(command -v python3 2>/dev/null)" || [ ! -r "$DIAG_HELPER" ] ||
		! "$DIAG_PYTHON" -I "$DIAG_HELPER" --check >/dev/null 2>&1; then
		printf '    | sondas opcionales: SIN MEDIR — falta Python3/helper o custodia Linux verificada; no se lanzan.\n'
		return 0
	fi
	cap="$(mktemp "${TMPDIR:-/tmp}/preverify-sonda.XXXXXX")" || { printf '    | sondas opcionales: SIN MEDIR — no puedo crear el fichero de captura\n'; return 0; }
	# Cada sonda: 5s +2s de KILL, 8192 bytes; estado auxiliar separado del productor.
	sonda_acotada "$cap" "$arbol" task --version; lrc=$?
	nota="$(nota_sonda "$DIAG_DETAIL")"
	nota="$nota; $DIAG_DETAIL"
	printf '    | task --version (rc=%s%s): %s\n' "$lrc" "$nota" "$(acotar 1 <"$cap")"
	# La sonda repite la invocacion EXACTA del sujeto (`--color=false` incluido, :138): una sonda
	# que llamara distinto podria ensenar una lista limpia mientras el sujeto vio otra cosa.
	sonda_acotada "$cap" "$arbol" task --list-all --color=false; lrc=$?
	nota="$(nota_sonda "$DIAG_DETAIL")"
	nota="$nota; $DIAG_DETAIL"
	printf '    | task --list-all --color=false en el laboratorio (rc=%s%s, %s linea(s), ambos flujos):\n' "$lrc" "$nota" "$(LC_ALL=C grep -c . <"$cap")"
	acotar 12 <"$cap" | sed 's/^/    |   /'
	rm -f "$cap"
}
nota_sonda() { # nota_sonda <detalle> -> estado de captura; el rc puede ser del productor
	local formato='^producer=(unknown|[0-9]+) capture=(eof|limit|deadline|interrupted|unmeasured) bytes=[0-9]+ completeness=(complete|unknown) custody=(none|term|kill|unknown)$'
	if [[ "${1:-}" =~ $formato ]]; then
		case "${BASH_REMATCH[2]}:${BASH_REMATCH[3]}:${BASH_REMATCH[4]}" in
		eof:complete:none|eof:complete:term|eof:complete:kill)
			if [ "${BASH_REMATCH[1]}" = unknown ]; then
				printf ' — estado del productor SIN MEDIR; captura completa'
			fi
			return 0 ;;
		limit:unknown:none|limit:unknown:term|limit:unknown:kill)
			printf ' — cortada al tope de %s bytes de captura' "$DIAG_MAX_BYTES"; return 0 ;;
		deadline:unknown:term)
			printf ' — AGOTO EL PLAZO de %ss de la sonda (TERM)' "$DIAG_PLAZO"; return 0 ;;
		deadline:unknown:kill)
			printf ' — AGOTO EL PLAZO de %ss e IGNORO TERM: rematada con KILL' "$DIAG_PLAZO"; return 0 ;;
		esac
	fi
	printf ' — SIN MEDIR: custodia o captura no verificada'
}

lab() { # lab <destino> <patas del --print-heavy, una por linea> <cuerpo del Taskfile>
	local d="$1" patas="$2" tareas="$3"
	mkdir -p "$d/scripts" || return 2
	printf '%s\n' "$tareas" >"$d/Taskfile.yml"
	cat >"$d/scripts/check-gate-parity.sh" <<GATE
#!/usr/bin/env bash
case "\${1:-}" in
--print-heavy)  printf '%s' "$patas" ;;
--print-ci-only) : ;;
--print-root)   printf '%s\n' "\${OLIVARES_ROOT:-\$(cd "\$(dirname "\${BASH_SOURCE[0]}")/.." && pwd)}" ;;
*) echo "gate falso: modo no soportado" >&2; exit 2 ;;
esac
GATE
	chmod +x "$d/scripts/check-gate-parity.sh"
	git -C "$d" init -q -b main 2>/dev/null || return 2
}

# ⛔ UN NOMBRE CON DOS PUNTOS, a proposito: los nombres reales del carril pesado son `build:cloud`,
# `test:cloud:norace`, `lint:format-ratchet`… y el primer parseo de `--list-all` retrocedia con
# ellos y no extraia ninguno. El laboratorio solo tenia `verde-uno`/`verde-dos` y por eso el defecto
# era INVISIBLE aqui: lo cazo una prueba de humo contra un arbol de verdad. Un banco cuyo
# vocabulario es mas pobre que el del mundo real no mide el mundo real.
TAREAS_OK='version: "3"
tasks:
  verde-uno:
    cmds: ["true"]
  verde:dos:con:puntos:
    cmds: ["true"]
'
TAREAS_MIXTAS='version: "3"
tasks:
  verde-uno:
    cmds: ["true"]
  roja-uno:
    cmds: ["echo FAIL: la primera se rompe >&2; exit 1"]
  roja-dos:
    cmds: ["echo BROKEN: y la segunda tambien >&2; exit 1"]
'

# ───────────────────────────── (1) negativo: todas verdes -> 0 ─────────────────────────────────
L1="$BASE/verde"; lab "$L1" 'verde-uno
verde:dos:con:puntos' "$TAREAS_OK" || exit 2
out="$(bash "$SUT" "$L1" --only-heavy 2>&1)"; rc=$?
check "(1) todas verdes -> 0" 0 "$rc"
case "$out" in *"LIMPIO"*) d=si ;; *) d=no ;; esac
check "(1) y lo dice como LIMPIO" si "$d"
# Si el laboratorio benigno no da 0/LIMPIO, la causa que el sujeto nombro se imprime AQUI, una vez,
# antes de que la siguiente fila la sobreescriba. Las dos filas de arriba quedan rojas igual.
if ! { [ "$rc" -eq 0 ] && [ "$d" = si ]; }; then diagnostico_sujeto "(1)" "$rc" "$out" "$L1"; fi

if [ "$SOLO_PRIMERO" -eq 1 ]; then
	echo "pre-verify-tanda: ⚠ MODO DIAGNOSTICO (--solo-primer-caso): SOLO se corrio el caso (1)."
	echo "  Esto NO es el veredicto del banco; el banco completo es el que corre sin la bandera."
	echo "pre-verify-tanda: $pasados passed, $fallados failed"
	[ "$fallados" -eq 0 ] || exit 1
	exit 0
fi

# ── (1-CTRL) UN PRERREQUISITO AUSENTE A PROPOSITO sigue siendo 2 Y su causa queda a la vista ─────
# El control del diagnostico, de punta a punta: ESTE MISMO BANCO, invocado en `--solo-primer-caso`
# con un PATH que trae todo lo que el arranque usa (`task` incluido) MENOS `timeout`, uno de los dos
# prerrequisitos que el sujeto comprueba antes de correr nada (:110). Se invoca el banco y no la
# funcion a proposito: probar solo `diagnostico_sujeto` dejaria verde a un mutante que borrase la
# llamada del caso (1), que es justo lo que hay que proteger. Fija, a la vez: que la ausencia no se
# convierte en 0 ni en 1 y el banco sigue saliendo 1 (el fallo se conserva), que la fila del caso (1)
# sigue roja con el rc del sujeto, que el diagnostico conserva la frase exacta del sujeto y su rc a
# traves de la redaccion y el acotado, que la sonda de prerrequisitos ve la ausencia por si misma,
# que las sondas opcionales se declaran SIN MEDIR en vez de lanzarse sin plazo, y que el sujeto no
# declaro LIMPIO. Es sintetico y finito —el banco anidado corre SOLO el caso (1) y sale antes de
# llegar aqui, asi que no hay recursion—: demuestra que el diagnostico FUNCIONA, no cual fue la
# causa en el runner. El `bash` va por ruta absoluta (`$BASH`) porque el PATH temporal manda tambien sobre la
# busqueda del propio comando, y el PATH restringido no lo trae.
granja() { # granja <destino> <utilidades...> : enlaces a las utilidades reales, solo rutas absolutas
	local dest="$1" u p; shift
	mkdir -p "$dest"
	for u in "$@"; do
		p="$(command -v "$u" 2>/dev/null)" || continue
		# Solo rutas absolutas: un `command -v` que devuelva un nombre (funcion, builtin) daria un enlace a si mismo.
		case "$p" in /*) ln -s "$p" "$dest/$u" 2>/dev/null ;; esac
	done
}
UTILES_BASE="bash sh dirname basename git awk grep sed sort head tail cut tr wc mktemp cp rm mkdir chmod env date comm seq cat sleep python3"
SINTO="$BASE/sin-timeout"
# shellcheck disable=SC2086  # la lista se expande a proposito, una utilidad por argumento
granja "$SINTO" $UTILES_BASE task
BANCO="$RAIZ/scripts/$(basename -- "${BASH_SOURCE[0]}")"
out="$(PATH="$SINTO" "$BASH" "$BANCO" --solo-primer-caso 2>&1)"; rc=$?
check "(1-CTRL) el banco sin 'timeout' en el PATH, solo caso (1) -> 1: el fallo se conserva" 1 "$rc"
case "$out" in *"FAIL (1) todas verdes -> 0"*"obtenido=2"*) d=si ;; *) d=no ;; esac
check "(1-CTRL) y su fila sigue ROJA, con el rc 2 del sujeto" si "$d"
case "$out" in *"no hay 'timeout' en el PATH"*) d=si ;; *) d=no ;; esac
check "(1-CTRL) y el diagnostico CONSERVA la causa que el sujeto nombro" si "$d"
case "$out" in *"contesto rc=2 "*) d=si ;; *) d=no ;; esac
check "(1-CTRL) con su rc exacto" si "$d"
case "$out" in *"timeout : AUSENTE"*) d=si ;; *) d=no ;; esac
check "(1-CTRL) y la sonda de prerrequisitos ve la ausencia por si misma" si "$d"
# Sin `timeout` no hay plazo que imponer, y una sonda sin plazo no se lanza: se declara, no se calla.
case "$out" in *"SIN MEDIR"*) d=si ;; *) d=no ;; esac
check "(1-CTRL) y las sondas opcionales se declaran SIN MEDIR, no se lanzan sin plazo" si "$d"
# Anclado al veredicto del SUJETO, no a la palabra: el rotulo de la fila «(1) y lo dice como LIMPIO»
# tambien la lleva y casaria siempre.
case "$out" in *"pre-verify-tanda: LIMPIO"*) d=si ;; *) d=no ;; esac
check "(1-CTRL) y el sujeto no declaro LIMPIO lo que no pudo mirar" no "$d"

# ── (1-COLGADA) EL SUJETO REHUSA RAPIDO Y LA SONDA OPCIONAL SE CUELGA: el banco termina SOLO ─────
# El causal de la revision independiente, ahora dentro del banco: un `task` sintetico cuyo
# `--list-all` rehusa al instante (rc 23, causa en stderr) y cuyo `--version` se queda dormido. El
# sujeto contesta 2 en milisegundos —`task --list-all` salio 23 y no se acepta su salida—; el diagnostico
# lanza la sonda de version, que se cuelga, y tiene que ser SU plazo el que la corte, no la guarda
# de 60 s que este banco se pone por si acaso (si esa disparara, el rc seria 124 y no 1: el techo
# delata en vez de colgar). Se fija: que el banco anidado sale 1 (el fallo original se conserva) y
# no 124; que termina en menos de 20 s por su propio plazo; que la refusal del sujeto sigue en la
# salida; que la sonda colgada queda anotada con su rc 124 y su plazo; que la sonda de la lista
# conserva el rc 23 Y el stderr que el sujeto descarta; y que NO sobrevive ningun hijo de la sonda:
# el senuelo escribe su PID antes de dormirse y despues de la corrida ese proceso tiene que no existir.
LCOL="$BASE/task-colgada"
# shellcheck disable=SC2086  # la lista se expande a proposito, una utilidad por argumento
granja "$LCOL" $UTILES_BASE timeout
PID_COLGADA="$BASE/colgada.pid"
cat >"$LCOL/task" <<SHIM
#!/bin/sh
case "\${1:-}" in
--version)  echo \$\$ >"$PID_COLGADA"; exec sleep 60 ;;
--list-all) echo "SENUELO_LISTA: no puedo enumerar (refusal sintetica en stderr)" >&2; exit 23 ;;
*) exit 23 ;;
esac
SHIM
chmod +x "$LCOL/task"
TIMEOUT_BIN="$(command -v timeout)"
ini=$(date +%s)
out="$(PATH="$LCOL" "$TIMEOUT_BIN" 60 "$BASH" "$BANCO" --solo-primer-caso 2>&1)"; rc=$?
seg=$(( $(date +%s) - ini ))
check "(1-COLGADA) sujeto rehusa rapido y la sonda se cuelga: el banco anidado sale 1, no 124" 1 "$rc"
[ "$seg" -lt 20 ] && d=si || d=no
check "(1-COLGADA) y termina por su propio plazo, sin guarda externa (${seg}s)" si "$d"
case "$out" in *"'task --list-all' salio 23"*) d=si ;; *) d=no ;; esac
check "(1-COLGADA) y la refusal original del sujeto se conserva" si "$d"
case "$out" in *"task --version (rc=124 — AGOTO EL PLAZO de ${DIAG_PLAZO}s"*) d=si ;; *) d=no ;; esac
check "(1-COLGADA) y la sonda colgada queda anotada con su rc 124 y su plazo" si "$d"
case "$out" in *"task --list-all --color=false en el laboratorio (rc=23"*"SENUELO_LISTA"*) d=si ;; *) d=no ;; esac
check "(1-COLGADA) y la sonda de la lista conserva el rc 23 y el stderr del productor" si "$d"
if [ -s "$PID_COLGADA" ] && kill -0 "$(cat "$PID_COLGADA")" 2>/dev/null; then d=vivo; else d=ninguno; fi
[ -s "$PID_COLGADA" ] || d=SIN-PID
check "(1-COLGADA) y no sobrevive ningun hijo de la sonda" ninguno "$d"

# ── (1-HIJO) UN HIJO ORDINARIO RETIENE EL TUBO DESPUES DE QUE SU PADRE SALGA ────────────────────
# El segundo causal de la revision independiente (2026-09-07), ahora permanente. El `task` sintetico
# NO demoniza nada —ni `setsid`, ni manejador, ni sesion nueva—: su `--version` lanza un `sleep`
# ORDINARIO en segundo plano y sale 0 al instante. Ese hijo hereda el tubo de la captura, asi que
# la captura sigue esperando el EOF aunque el productor ya haya contestado. Con el plazo envolviendo
# SOLO al productor, el banco tardaba 9,065 s pese a los 5 s de plazo y anotaba la sonda `rc=0`: no
# es que midiera de mas, es que declaraba MEDIDO un tubo que seguia abierto. Se fija: que el plazo
# corta el tubo COMPLETO y la sonda se anota 124; que el hijo ordinario no sobrevive; que el banco
# anidado sale 1 por el fallo original y no 124 por la guarda; y que la refusal del sujeto y el rc 23
# de la otra sonda siguen ahi. El `sleep` es de 25 s a proposito: el techo de 15 s de la segunda fila
# solo se cumple si algo lo remato antes de que se agotara solo.
LHIJO="$BASE/task-hijo"
# shellcheck disable=SC2086  # la lista se expande a proposito, una utilidad por argumento
granja "$LHIJO" $UTILES_BASE timeout
PID_HIJO="$BASE/hijo.pid"
cat >"$LHIJO/task" <<SHIM
#!/bin/sh
case "\${1:-}" in
--version)  sleep 25 & echo \$! >"$PID_HIJO"; exit 0 ;;
--list-all) echo "SENUELO_LISTA: no puedo enumerar (refusal sintetica en stderr)" >&2; exit 23 ;;
*) exit 23 ;;
esac
SHIM
chmod +x "$LHIJO/task"
: >"$PID_HIJO"
ini=$(date +%s)
out="$(PATH="$LHIJO" "$TIMEOUT_BIN" 60 "$BASH" "$BANCO" --solo-primer-caso 2>&1)"; rc=$?
seg=$(( $(date +%s) - ini ))
check "(1-HIJO) un hijo ordinario retiene el tubo: el banco anidado sale 1, no 124" 1 "$rc"
[ "$seg" -lt 15 ] && d=si || d=no
check "(1-HIJO) y el plazo corta el tubo entero, sin esperar al hijo de 25s (${seg}s)" si "$d"
case "$out" in *"task --version (rc=124 — AGOTO EL PLAZO de ${DIAG_PLAZO}s"*) d=si ;; *) d=no ;; esac
check "(1-HIJO) y la sonda se anota 124, no el rc=0 que la declaraba medida" si "$d"
if [ -s "$PID_HIJO" ] && kill -0 "$(cat "$PID_HIJO")" 2>/dev/null; then d=vivo; else d=ninguno; fi
[ -s "$PID_HIJO" ] || d=SIN-PID
check "(1-HIJO) y el hijo ordinario NO sobrevive a la sonda" ninguno "$d"
case "$out" in *"'task --list-all' salio 23"*) d=si ;; *) d=no ;; esac
check "(1-HIJO) y la refusal original del sujeto se conserva" si "$d"
case "$out" in *"task --list-all --color=false en el laboratorio (rc=23"*"SENUELO_LISTA"*) d=si ;; *) d=no ;; esac
check "(1-HIJO) y la otra sonda conserva su rc 23 y su stderr" si "$d"

# ── (1-TERCA) UNA SONDA QUE IGNORA TERM SE REMATA CON KILL, y tampoco sobrevive ─────────────────
# Protege el escalado: el lider reservado no sale mientras tenga hijos, por lo que
# TERM no basta para dar por terminada la custodia. KILL alcanza al grupo antes de
# recoger al lider. El productor terco debe desaparecer antes del retorno. Ese superviviente es el defecto: un banco que dice haber acotado y deja un
# proceso suelto no ha acotado nada. La fila del rc distingue 137 de 124, que es lo que prueba que
# el escalado llego de verdad.
LTERCA="$BASE/task-terca"
# shellcheck disable=SC2086  # la lista se expande a proposito, una utilidad por argumento
granja "$LTERCA" $UTILES_BASE timeout
PID_TERCA="$BASE/terca.pid"
cat >"$LTERCA/task" <<SHIM
#!/bin/sh
case "\${1:-}" in
--version)  trap '' TERM; echo \$\$ >"$PID_TERCA"; exec sleep 60 ;;
--list-all) echo "SENUELO_LISTA: no puedo enumerar (refusal sintetica en stderr)" >&2; exit 23 ;;
*) exit 23 ;;
esac
SHIM
chmod +x "$LTERCA/task"
: >"$PID_TERCA"
ini=$(date +%s)
out="$(PATH="$LTERCA" "$TIMEOUT_BIN" 60 "$BASH" "$BANCO" --solo-primer-caso 2>&1)"; rc=$?
seg=$(( $(date +%s) - ini ))
check "(1-TERCA) una sonda que ignora TERM: el banco anidado sale 1, no 124" 1 "$rc"
[ "$seg" -lt 20 ] && d=si || d=no
check "(1-TERCA) y termina por su propio remate, no por la guarda (${seg}s)" si "$d"
case "$out" in *"task --version (rc=137 — AGOTO EL PLAZO de ${DIAG_PLAZO}s e IGNORO TERM"*) d=si ;; *) d=no ;; esac
check "(1-TERCA) y queda anotada como rematada con KILL, no como un 124 cualquiera" si "$d"
if [ -s "$PID_TERCA" ] && kill -0 "$(cat "$PID_TERCA")" 2>/dev/null; then d=vivo; else d=ninguno; fi
[ -s "$PID_TERCA" ] || d=SIN-PID
check "(1-TERCA) y el productor terco NO sobrevive a su propia sonda" ninguno "$d"

# ── (1-RUIDOSA) LA SONDA OPCIONAL INUNDA: la captura se corta al tope, el banco no engorda ────────
# La otra mitad del mismo defecto: una sonda que no se cuelga pero escribe sin parar. La version
# anterior la recogia ENTERA en una sustitucion de orden y solo recortaba al mostrarla. Ahora la
# captura se limita a 8192 bytes hacia un fichero: el `task` sintetico vuelca 50 MB por `--version`, el
# banco anidado tiene que salir 1, anotar el rc 141 con su tope, y su salida completa tiene que caber
# de sobra en 64 KB.
LRUI="$BASE/task-ruidosa"
# shellcheck disable=SC2086  # la lista se expande a proposito, una utilidad por argumento
granja "$LRUI" $UTILES_BASE timeout yes
cat >"$LRUI/task" <<'SHIM'
#!/bin/sh
case "${1:-}" in
--version)  yes RUIDO_SINTETICO | head -c 50000000 ;;
--list-all) echo "SENUELO_LISTA: no puedo enumerar" >&2; exit 23 ;;
*) exit 23 ;;
esac
SHIM
chmod +x "$LRUI/task"
out="$(PATH="$LRUI" "$TIMEOUT_BIN" 60 "$BASH" "$BANCO" --solo-primer-caso 2>&1)"; rc=$?
check "(1-RUIDOSA) una sonda que inunda no cambia el veredicto: el banco anidado sale 1" 1 "$rc"
case "$out" in *"task --version (rc=141 — cortada al tope de ${DIAG_MAX_BYTES} bytes"*) d=si ;; *) d=no ;; esac
check "(1-RUIDOSA) y la sonda queda anotada como cortada al tope de captura" si "$d"
[ "${#out}" -lt 65536 ] && d=si || d=no
check "(1-RUIDOSA) y la salida del banco anidado cabe en 64 KB (${#out} bytes)" si "$d"

# ── (1-CUSTODIA) CAP/EOF no liberan el grupo con hijos ordinarios vivos ──────────────────────
# Cada fixture guarda PID/start ticks, y se mide antes de cualquier limpieza de
# rescate. El rescate solo usa pidfd sobre esa identidad, nunca un PGID recordado.
# La guarda externa de 10s es roja (124), no una medicion de exito del auxiliar.
DIAG_TIMEOUT="$TIMEOUT_BIN"
DIAG_PYTHON="$(command -v python3)"
CUST="$BASE/custodia"; mkdir -p "$CUST"
cat >"$CUST/producer.py" <<'CUSTPY'
import os, signal, sys, time
mode, receipt = sys.argv[1:]
if mode in ("cap-child", "cap-ignore", "eof-child"):
    r, w = os.pipe()
    child = os.fork()
    if child == 0:
        os.close(r)
        if mode == "cap-ignore":
            signal.signal(signal.SIGTERM, signal.SIG_IGN)
        if mode == "eof-child":
            null = os.open(os.devnull, os.O_RDWR)
            for fd in (0, 1, 2):
                os.dup2(null, fd)
        with open(receipt, "w") as f:
            f.write(str(os.getpid()) + " " + open("/proc/self/stat").read().rsplit(")", 1)[1].split()[19])
        os.write(w, b"r")
        os.close(w)
        time.sleep(25)
        os._exit(0)
    os.close(w)
    os.read(r, 1)
    os.close(r)
if mode.startswith("cap"):
    os.write(2, b"x" * 8192)  # stderr is inside the SAME byte budget
else:
    os.write(1, b"OUT_SYNTHETIC\n")
    os.write(2, b"ERR_SYNTHETIC\n")
sys.exit(int(mode.rsplit("-", 1)[1]) if mode.startswith("eof-status-") else 23 if mode == "eof-nonzero" else 0)
CUSTPY
for modo in cap-child cap-ignore eof-zero eof-nonzero eof-child eof-status-124 eof-status-125 eof-status-137 eof-status-141; do
	ini=$(date +%s)
	detail="$("$TIMEOUT_BIN" -k 1 10 "$DIAG_PYTHON" -I "$DIAG_HELPER" "$CUST/$modo.cap" "$CUST" "$DIAG_PYTHON" -I "$CUST/producer.py" "$modo" "$CUST/$modo.pid")"; crc=$?
	seg=$(( $(date +%s) - ini ))
	case "$modo" in cap-*) esperado=141 ;; eof-nonzero) esperado=23 ;; eof-status-*) esperado="${modo##*-}" ;; *) esperado=0 ;; esac
	check "(1-CUSTODIA) $modo: resultado auxiliar independiente del productor" "$esperado" "$crc"
	[ "$seg" -lt 9 ] && d=si || d=no
	check "(1-CUSTODIA) $modo: termina dentro de su plazo, sin guarda (${seg}s)" si "$d"
	case "$modo" in
	cap-*)
		check "(1-CUSTODIA) $modo: stderr comparte el tope exacto" 8192 "$(wc -c <"$CUST/$modo.cap" | tr -d ' ')"
		case "$detail" in *"capture=limit bytes=8192 completeness=unknown custody="*) d=si ;; *) d=no ;; esac
		check "(1-CUSTODIA) $modo: tope nunca equivale a captura completa" si "$d" ;;
	*)
		case "$detail" in *"producer=$esperado capture=eof"*"completeness=complete custody="*) d=si ;; *) d=no ;; esac
		check "(1-CUSTODIA) $modo: EOF y estado real del productor separados" si "$d"
		case "$(cat "$CUST/$modo.cap")" in *OUT_SYNTHETIC*ERR_SYNTHETIC*) d=si ;; *) d=no ;; esac
		check "(1-CUSTODIA) $modo: ambos flujos conservados" si "$d" ;;
	esac
	case "$modo" in eof-status-*)
		check "(1-CUSTODIA) $modo: EOF no se etiqueta como plazo, tope o SIN MEDIR" "" "$(nota_sonda "$detail")" ;;
	esac
	if [ "$modo" = cap-ignore ]; then
		case "$detail" in *"custody=kill"*) d=si ;; *) d=no ;; esac
		check "(1-CUSTODIA) cap-ignore: el corte escala a KILL antes de recoger al lider" si "$d"
	fi
	case "$modo" in cap-*|eof-child)
		d="$("$DIAG_PYTHON" -I - "$CUST/$modo.pid" <<'REAPPID'
import os, pathlib, signal, sys
p = pathlib.Path(sys.argv[1])
if not p.exists():
    print("SIN-PID")
else:
    pid, start = map(int, p.read_text().split())
    try:
        fd = os.pidfd_open(pid)
        same = int(pathlib.Path("/proc/%d/stat" % pid).read_text().rsplit(")", 1)[1].split()[19]) == start
        print("vivo" if same else "ninguno")
        if same:
            signal.pidfd_send_signal(fd, signal.SIGKILL)
        os.close(fd)
    except ProcessLookupError:
        print("ninguno")
REAPPID
)"
		check "(1-CUSTODIA) $modo: ningun hijo sobrevive al retorno (antes del rescate propio)" ninguno "$d" ;;
	esac
done
# EOF puede cerrar ambos flujos antes de que el productor termine; una limpieza
# verificada no inventa su estado de salida si el anchor no llego a comunicarlo.
check "(1-CUSTODIA) EOF completo con productor desconocido conserva ambas certezas" \
	' — estado del productor SIN MEDIR; captura completa' \
	"$(nota_sonda 'producer=unknown capture=eof bytes=28 completeness=complete custody=kill')"
# Guardas causales sin ejecutar auxiliares: la presencia de task se observa por
# builtin; cualquier llamada a la orden escribe un marcador. El sujeto ya rehuso.
cat >"$CUST/task" <<SHIM
#!/bin/sh
echo llamado >>"$CUST/aux-called"
exit 23
SHIM
chmod +x "$CUST/task"
old_helper="$DIAG_HELPER"
DIAG_HELPER="$CUST/helper-ausente.py"
diag="$(PATH="$CUST:$PATH" diagnostico_sujeto '(custodia-helper-ausente)' 2 'REFUSAL_SINTETICA_ORIGINAL' "$CUST")"
case "$diag" in *"contesto rc=2"*REFUSAL_SINTETICA_ORIGINAL*"SIN MEDIR"*) d=si ;; *) d=no ;; esac
check "(1-CUSTODIA) helper ausente: refusal original y SIN MEDIR" si "$d"
[ ! -e "$CUST/aux-called" ] && d=si || d=no
check "(1-CUSTODIA) helper ausente: no ejecuta ninguna sonda" si "$d"
DIAG_HELPER="$old_helper"
# La misma funcion con timeout ausente tampoco invoca task.
# shellcheck disable=SC2086
granja "$CUST/sin-timeout" $UTILES_BASE
ln -s "$CUST/task" "$CUST/sin-timeout/task"
diag="$(PATH="$CUST/sin-timeout" diagnostico_sujeto '(custodia-timeout-ausente)' 2 'REFUSAL_SINTETICA_ORIGINAL' "$CUST")"
case "$diag" in *"contesto rc=2"*REFUSAL_SINTETICA_ORIGINAL*"SIN MEDIR"*) d=si ;; *) d=no ;; esac
check "(1-CUSTODIA) timeout ausente: refusal original y SIN MEDIR" si "$d"
[ ! -e "$CUST/aux-called" ] && d=si || d=no
check "(1-CUSTODIA) timeout ausente: no ejecuta ninguna sonda" si "$d"

# Python ausente o un mecanismo no verificable tampoco lanzan el productor.
# shellcheck disable=SC2086
granja "$CUST/sin-python" ${UTILES_BASE% python3} timeout
ln -s "$CUST/task" "$CUST/sin-python/task"
diag="$(PATH="$CUST/sin-python" diagnostico_sujeto '(custodia-python-ausente)' 2 'REFUSAL_SINTETICA_ORIGINAL' "$CUST")"
case "$diag" in *"contesto rc=2"*REFUSAL_SINTETICA_ORIGINAL*"SIN MEDIR"*) d=si ;; *) d=no ;; esac
check "(1-CUSTODIA) Python ausente: refusal original y SIN MEDIR" si "$d"
[ ! -e "$CUST/aux-called" ] && d=si || d=no
check "(1-CUSTODIA) Python ausente: no ejecuta ninguna sonda" si "$d"
printf 'raise SystemExit(125)\n' >"$CUST/helper-no-custodia.py"
DIAG_HELPER="$CUST/helper-no-custodia.py"
diag="$(PATH="$CUST:$PATH" diagnostico_sujeto '(custodia-no-verificable)' 2 'REFUSAL_SINTETICA_ORIGINAL' "$CUST")"
case "$diag" in *"contesto rc=2"*REFUSAL_SINTETICA_ORIGINAL*"SIN MEDIR"*) d=si ;; *) d=no ;; esac
check "(1-CUSTODIA) mecanismo no verificable: refusal original y SIN MEDIR" si "$d"
[ ! -e "$CUST/aux-called" ] && d=si || d=no
check "(1-CUSTODIA) mecanismo no verificable: no ejecuta ninguna sonda" si "$d"
DIAG_HELPER="$old_helper"

# La redaccion y el tope se prueban sobre el formateador con cadenas SINTETICAS: nunca con una
# credencial real, y sin correr ningun escenario posterior. Se exige el marcador ADEMAS de la
# ausencia del token, como en el caso de la fuga: si solo se exigiera la ausencia, un formateador
# que se comiera la linea entera pasaria.
SENUELO_DIAG='ghp_SENUELOdelDIAGNOSTICOquenodebeSALIR0009'
diag="$(printf 'causa https://usuario:%s@example.invalid/x.git y OLIVARES_X_TOKEN=%s\n' "$SENUELO_DIAG" "$SENUELO_DIAG" | acotar)"
case "$diag" in *"$SENUELO_DIAG"*) d=si ;; *) d=no ;; esac
check "(1-RED) el diagnostico no deja pasar una credencial sintetica" no "$d"
case "$diag" in *"://<redacted>@example.invalid/x.git"*"OLIVARES_X_TOKEN=<redacted>"*) d=si ;; *) d=no ;; esac
check "(1-RED) y redacta el secreto, no el dato: host y nombre siguen visibles" si "$d"
# Los dos canarios de la primera pasada de la revision independiente (2026-09-07): un nombre partido
# por un byte de control, y un valor entrecomillado con espacios. Ninguno puede quedar a la vista.
# ⛔ VAN EN LINEAS SEPARADAS, y no es cosmetica: la redaccion es de RESTO DE LINEA, asi que lo que
# siga a una asignacion sensible se pierde A PROPOSITO. La parte inocente se sigue exigiendo donde
# continua siendo observable —la linea de al lado— en vez de dejar de exigirse.
CANARIO_A='CANARIO_CONTROL_00A1'; CANARIO_B='CANARIO_ENTRECOMILLADO_00B2'
diag="$(printf 'TOK\001EN=%s\nAPI_TOKEN='"'"'prefijo %s'"'"' y cola\ndespues=sigue\n' "$CANARIO_A" "$CANARIO_B" | acotar)"
case "$diag" in *"$CANARIO_A"*|*"$CANARIO_B"*) d=si ;; *) d=no ;; esac
check "(1-RED) un byte de control no parte el nombre, ni una comilla salva el valor" no "$d"
case "$diag" in *"TOKEN=<redacted>"*"API_TOKEN=<redacted>"*"despues=sigue"*) d=si ;; *) d=no ;; esac
check "(1-RED) y lo que no es secreto sigue en su sitio" si "$d"
# ⛔ LOS DOS QUE LA SEGUNDA PASADA MIDIO COMO FUGA, y son la razon de que la gramatica de comillas se
# retirara: con ella, `\"` dentro de comillas dobles y la concatenacion POSIX `'\''` cortaban el
# valor antes de tiempo y publicaban la cola. Entran por HEREDOC CON DELIMITADOR ENTRECOMILLADO para
# que lleguen como BYTES EXACTOS —el shell no expande ni un escape— y NUNCA se evaluan como orden.
diag="$(acotar <<'CANARIOS'
API_TOKEN="prefijo \" CANARIO_ESCAPADO_DOBLE_00C3" plain=ok
API_TOKEN='prefijo'\'' CANARIO_ESCAPADO_SIMPLE_00D4' plain=ok
CANARIOS
)"
case "$diag" in *CANARIO_ESCAPADO_DOBLE_00C3*|*CANARIO_ESCAPADO_SIMPLE_00D4*) d=si ;; *) d=no ;; esac
check "(1-RED) ni una comilla ESCAPADA ni la concatenacion POSIX sacan la cola del valor" no "$d"
# Marcador Y recuento, como en el caso de la fuga: exigir solo la ausencia dejaria pasar a un
# formateador que se comiera las dos lineas enteras.
check "(1-RED) y las DOS lineas salen redactadas y anotadas, no comidas" 2 \
	"$(printf '%s\n' "$diag" | LC_ALL=C grep -c '^API_TOKEN=<redacted> (resto de la linea redactado')"
# Comilla sin cerrar: ya no es un caso aparte — el resto de la linea se va igual, y se dice.
diag="$(printf 'API_TOKEN="sin cierre %s y mas cosas\n' "$CANARIO_B" | acotar)"
case "$diag" in *"$CANARIO_B"*) d=si ;; *) d=no ;; esac
check "(1-RED) con la comilla sin cerrar, el resto de la linea no sale" no "$d"
case "$diag" in *"resto de la linea redactado"*) d=si ;; *) d=no ;; esac
check "(1-RED) y se dice que se redacto el resto" si "$d"
# Marcador Y recuento: un acotado que imprimiera las 100 y ademas el marcador pasaria solo con el marcador.
diag="$(seq 1 100 | acotar)"
case "$diag" in *"60 linea(s) mas, omitidas: tope 40"*) d=si ;; *) d=no ;; esac
check "(1-TOPE) y el acotado muerde: 100 lineas -> 40 y cuenta las omitidas" si "$d"
check "(1-TOPE) y de verdad salen 40 lineas mas el marcador" 41 "$(printf '%s\n' "$diag" | LC_ALL=C grep -c .)"

# ── (1-bis) EL LABORATORIO ORDINARIO SIGUE DANDO 0/LIMPIO DESPUES DEL CONTROL ───────────────────
# El control restringe el PATH por orden, no en el banco; esta fila lo demuestra en vez de
# suponerlo, y es el positivo que exige el encargo: un diagnostico que se lleve el verde ordinario
# no ha diagnosticado nada.
out="$(bash "$SUT" "$L1" --only-heavy 2>&1)"; rc=$?
check "(1-bis) tras el control, el laboratorio ordinario sigue -> 0" 0 "$rc"
case "$out" in *"LIMPIO"*) d=si ;; *) d=no ;; esac
check "(1-bis) y sigue diciendo LIMPIO" si "$d"

# ─────────────────────── (2) positivo: una roja se NOMBRA, y el rc es 1 ────────────────────────
L2="$BASE/mixto"; lab "$L2" 'verde-uno
roja-uno
roja-dos' "$TAREAS_MIXTAS" || exit 2
out="$(bash "$SUT" "$L2" --only-heavy 2>&1)"; rc=$?
check "(2) con rojas -> 1" 1 "$rc"
case "$out" in *"roja-uno"*) d=si ;; *) d=no ;; esac
check "(2) nombra la primera roja" si "$d"
case "$out" in *"roja-dos"*) d=si ;; *) d=no ;; esac
check "(2) y TAMBIEN la segunda: no se para en la primera" si "$d"
case "$out" in *"la primera se rompe"*) d=si ;; *) d=no ;; esac
check "(2) y trae la primera linea de causa de cada una" si "$d"

# ───────────────────────────────── (3) lista vacia -> 2, nunca 0 ───────────────────────────────
L3="$BASE/vacio"; lab "$L3" '' "$TAREAS_OK" || exit 2
out="$(bash "$SUT" "$L3" --only-heavy 2>&1)"; rc=$?
check "(3) lista vacia -> 2" 2 "$rc"
case "$out" in *"VACIA"*) d=si ;; *) d=no ;; esac
check "(3) y dice que un 0 ahi seria un verde ciego" si "$d"

# ──────────────── (4) una pata que el gate nombra y el Taskfile no tiene -> 2, no 1 ────────────
# «no existe la tarea» y «la pata esta roja» son respuestas distintas y no pueden confundirse.
L4="$BASE/fantasma"; lab "$L4" 'verde:dos:con:puntos
pata-fantasma' "$TAREAS_OK" || exit 2
out="$(bash "$SUT" "$L4" --only-heavy 2>&1)"; rc=$?
check "(4) pata inexistente -> 2 (no 1)" 2 "$rc"
case "$out" in *"pata-fantasma"*) d=si ;; *) d=no ;; esac
check "(4) y la nombra" si "$d"

# ───────────────────────────────────── (5) el plazo por pata ───────────────────────────────────
L5="$BASE/lenta"; lab "$L5" 'lenta' 'version: "3"
tasks:
  lenta:
    cmds: ["sleep 30"]
' || exit 2
ini=$(date +%s)
out="$(bash "$SUT" "$L5" --only-heavy --timeout 2 2>&1)"; rc=$?
seg=$(( $(date +%s) - ini ))
check "(5) una pata colgada se corta por plazo -> 1" 1 "$rc"
case "$out" in *"AGOTO EL PLAZO"*) d=si ;; *) d=no ;; esac
check "(5) y lo dice por su nombre" si "$d"
[ "$seg" -lt 15 ] && d=si || d=no
check "(5) y no espera los 30s de la tarea" si "$d"

# ──────────────────────────── EL MUTANTE: pararse en la primera roja ──────────────────────────
MUT="$BASE/mut.sh"
python3 - "$SUT" "$MUT" <<'MUTPY'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
viejo = '\t\tn_rojas=$((n_rojas + 1)); ROJAS="$ROJAS $tarea"'
if viejo not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    s.replace(viejo, viejo + '\n\t\tprintf \'  %-34s %5s %8s  %s\\n\' "$tarea" "$rc" "$seg" "$causa"\n\t\trm -f "$sal"\n\t\tbreak', 1))
MUTPY
[ -s "$MUT" ] || { echo "  FAIL MUTANTE NO ESCRITO: el reemplazo no caso"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUT" && d=NO-DIFIERE || d=ok
check "(M) el mutante REALMENTE difiere" ok "$d"
out="$(bash "$MUT" "$L2" --only-heavy 2>&1)"; rcm=$?
check "(M) el mutante SIGUE saliendo 1: el rc no lo distingue" 1 "$rcm"
case "$out" in *"roja-dos"*) d=si ;; *) d=no ;; esac
check "(M) pero se calla la segunda roja, que es lo que compra la herramienta" no "$d"

# ─────── (6) un gate ANTERIOR a estos modos: ignora el flag y contesta con su salida normal ─────
# MEDIDO el 2026-09-03 contra el arbol de otro carril. La lista NO sale vacia —salen dos lineas de
# prosa—, asi que la guarda de «vacia» no dispara y la herramienta se las creia como patas: acababa
# en 2, pero por la causa equivocada («no existe como tarea»), que manda a mirar el Taskfile en vez
# del gate. Un nombre de tarea no lleva espacios ni `=`; con eso se distingue una lista de una frase.
L6="$BASE/gate-viejo"; mkdir -p "$L6/scripts"
printf '%s\n' 'version: "3"' 'tasks:' '  verde-uno:' '    cmds: ["true"]' >"$L6/Taskfile.yml"
cat >"$L6/scripts/check-gate-parity.sh" <<'VIEJO'
#!/usr/bin/env bash
# Un gate anterior: no conoce --print-heavy y cae a su modo normal SIN decirlo.
echo "check-gate-parity: gancho=256 ci=105 ambas=71 solo-ci=34 solo-gancho=185"
echo "check-gate-parity: CLEAN — la paridad coincide con el registro"
VIEJO
chmod +x "$L6/scripts/check-gate-parity.sh"
git -C "$L6" init -q -b main 2>/dev/null
out="$(bash "$SUT" "$L6" --only-heavy 2>&1)"; rc=$?
check "(6) gate que ignora el flag -> 2" 2 "$rc"
# Desde PV-ROOT-PROOF el gate viejo se caza ANTES —en la atestiguacion de la raiz—, asi que la
# causa puede ser cualquiera de las dos. Lo que este caso fija es que nombre AL GATE y no a una
# tarea: cual de los dos controles dispare primero es un detalle de orden, no del hallazgo.
# Hay TRES causas posibles y las tres son correctas —no entiende el modo, no sabe atestiguar la
# raiz, o atestigua una que no es—; cual dispare primero es un detalle de orden. Lo que este caso
# fija es que la causa nombre AL GATE y no a una tarea, que es el error que costaba tiempo.
case "$out" in *"el gate"*) d=si ;; *) d=no ;; esac
check "(6) y la causa es el GATE, no el Taskfile" si "$d"
case "$out" in *"no existe como tarea"*) d=si ;; *) d=no ;; esac
check "(6) y NO se culpa a una tarea que nadie pidio" no "$d"

# ─── (7) `--gate <ruta>`: el gate se presta, pero la lista sale del arbol MEDIDO ────────────────
#
# ⛔ ESTE ES EL CASO QUE HACE CORRECTO AL FLAG, y sin el no valdria la pena tenerlo. El gate deriva
# la lista del arbol que lee, y lo elige por `OLIVARES_ROOT` o —si no esta— por DONDE VIVE EL.
# Invocar un gate prestado sin fijar la raiz daria la lista del arbol del gate aplicada al arbol
# medido: dos arboles distintos con un veredicto cruzado, que es exactamente la clase de defecto
# que esta herramienta existe para evitar. El gate de laboratorio LEE `$OLIVARES_ROOT/patas.txt`,
# asi que los dos arboles piden cosas distintas y se ve cual mando.
gate_que_lee_la_raiz() { # gate_que_lee_la_raiz <destino>
	mkdir -p "$1/scripts"
	cat >"$1/scripts/check-gate-parity.sh" <<'GATE2'
#!/usr/bin/env bash
raiz="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
case "${1:-}" in
--print-heavy)   cat "$raiz/patas.txt" 2>/dev/null ;;
--print-ci-only) : ;;
--print-root)    printf '%s\n' "$raiz" ;;
*) echo "gate falso: modo no soportado" >&2; exit 2 ;;
esac
GATE2
	chmod +x "$1/scripts/check-gate-parity.sh"
}

L7="$BASE/medido"; mkdir -p "$L7"
printf '%s\n' 'version: "3"' 'tasks:' '  la-del-arbol-medido:' '    cmds: ["true"]' >"$L7/Taskfile.yml"
printf 'la-del-arbol-medido\n' >"$L7/patas.txt"
gate_que_lee_la_raiz "$L7"
git -C "$L7" init -q -b main 2>/dev/null

L7B="$BASE/prestado"; mkdir -p "$L7B"
printf 'la-del-arbol-DEL-GATE\n' >"$L7B/patas.txt"
gate_que_lee_la_raiz "$L7B"

out="$(bash "$SUT" "$L7" --gate "$L7B/scripts/check-gate-parity.sh" --only-heavy 2>&1)"; rc=$?
check "(7) gate prestado sobre otro arbol -> 0" 0 "$rc"
case "$out" in *"la-del-arbol-medido"*) d=si ;; *) d=no ;; esac
check "(7) la lista sale del arbol MEDIDO" si "$d"
case "$out" in *"la-del-arbol-DEL-GATE"*) d=si ;; *) d=no ;; esac
check "(7) y NO del arbol donde vive el gate" no "$d"

# Mutante del flag: se deja de fijar `OLIVARES_ROOT` y el gate prestado mide SU arbol.
MUT7="$BASE/mut7.sh"
python3 - "$SUT" "$MUT7" <<'M7'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = '\t\ta="$(OLIVARES_ROOT="$WT" bash "$GATE" --print-heavy 2>/dev/null)"; rca=$?'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    s.replace(v, '\t\ta="$(bash "$GATE" --print-heavy 2>/dev/null)"; rca=$?', 1))
M7
[ -s "$MUT7" ] || { echo "  FAIL MUTANTE 7 NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUT7" && d=NO-DIFIERE || d=ok
check "(M7) el mutante del flag difiere" ok "$d"
# El mutante quita la fijacion en `lista()`; la atestiguacion de la raiz seguiria cazandolo antes,
# asi que para AISLAR lo que este caso mide —que sin fijar se lee el arbol del gate— se le quita
# tambien esa comprobacion. Si no, mediriamos el control nuevo y no el que este caso nombra.
python3 - "$MUT7" <<'M7B'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
v = '[ "$RAIZ_ATESTIGUADA" = "$WT" ] || cannot'
if v in s:
    open(p, "w", encoding="utf-8").write(s.replace(v, '[ -n "$RAIZ_ATESTIGUADA" ] || cannot', 1))
M7B
out="$(bash "$MUT7" "$L7" --gate "$L7B/scripts/check-gate-parity.sh" --only-heavy 2>&1)"
case "$out" in *"la-del-arbol-DEL-GATE"*) d=si ;; *) d=no ;; esac
check "(M7) sin fijar la raiz, mide el arbol del GATE" si "$d"

# ───────────────────────── (8) un `--gate` que no existe -> 2, con su causa ─────────────────────
out="$(bash "$SUT" "$L7" --gate "$BASE/no-hay-tal-gate.sh" --only-heavy 2>&1)"; rc=$?
check "(8) --gate inexistente -> 2" 2 "$rc"

# ────── (9) `--hook-env`: la misma pata, dos veces, y cualquier diferencia es HALLAZGO ─────────
#
# ⛔ VIENE DE a repository gate, la cuarta muerte de un lote: una bateria daba 24/0 suelta y 15/9 BAJO EL
# GANCHO, porque el gancho exporta `OLIVARES_PUSH_REFS_FILE` y cada senuelo acababa midiendo los
# tips del push real en vez de su propio arbol. Correr una pata «a pelo» no predice como se porta
# en el gancho, y el veredicto que cuenta es el de debajo del gancho: ahi es donde muere el push.
L9="$BASE/entorno"; mkdir -p "$L9/scripts" "$L9/.githooks"
printf '%s\n' 'version: "3"' 'tasks:' \
  '  depende-del-entorno:' '    cmds: ["test -z \"${OLIVARES_PUSH_CLASS:-}\""]' \
  '  no-depende:' '    cmds: ["true"]' >"$L9/Taskfile.yml"
printf '%s\n' 'depende-del-entorno' 'no-depende' >"$L9/patas.txt"
# El gancho del laboratorio exporta DOS, para que la biseccion tenga algo que discriminar.
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_CLASS="$gate_class"' \
  'export OLIVARES_PG_LOCAL_DEFAULTS=1' >"$L9/.githooks/pre-push"
gate_que_lee_la_raiz "$L9"
git -C "$L9" init -q -b main 2>/dev/null

out="$(bash "$SUT" "$L9" --hook-env --only-heavy 2>&1)"; rc=$?
check "(9) una pata que cambia con el entorno -> 1" 1 "$rc"
case "$out" in *"CAMBIA CON EL ENTORNO"*) d=si ;; *) d=no ;; esac
check "(9) y lo dice por su nombre" si "$d"
case "$out" in *"por OLIVARES_PUSH_CLASS"*) d=si ;; *) d=no ;; esac
check "(9) y BISECA a la variable culpable" si "$d"
# ⛔ ANCLADO A LA MISMA LINEA. `case "$out" in *"no-depende"*"CAMBIA"*` casaba con las dos cosas en
# LINEAS DISTINTAS —el `CAMBIA` era el de la otra pata— y daba un falso rojo. Un patron sobre una
# salida multilinea no dice «esto en esta fila»: hay que sacar la fila primero.
linea_no="$(printf '%s\n' "$out" | command grep -m1 'no-depende' || true)"
case "$linea_no" in *"CAMBIA"*) d=si ;; *) d=no ;; esac
check "(9) negativo: la que NO depende no se marca" no "$d"
[ -n "$linea_no" ] && d=si || d=no
check "(9) y aun asi aparece en el informe" si "$d"
case "$out" in *"DERIVADO de .githooks/pre-push — 2 variable(s)"*) d=si ;; *) d=no ;; esac
check "(9) y las variables salen DEL GANCHO del arbol medido" si "$d"

# --- MUTANTE 9: se quita el segundo pase. La pata que depende del entorno deja de marcarse, y el
# --- veredicto pasa a 0: exactamente el falso verde que a repository gate costo.
MUT9="$BASE/mut9.sh"
python3 - "$SUT" "$MUT9" <<'M9'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = '\tif [ "$HOOK_ENV" -eq 1 ] && [ "$SOLO_ENTORNO" -eq 0 ]; then\n\t\tsal2='
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, '\tif false; then\n\t\tsal2=', 1))
M9
[ -s "$MUT9" ] || { echo "  FAIL MUTANTE 9 NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUT9" && d=NO-DIFIERE || d=ok
check "(M9) el mutante del segundo pase difiere" ok "$d"
out="$(bash "$MUT9" "$L9" --hook-env --only-heavy 2>&1)"; rcm=$?
check "(M9) sin el segundo pase el veredicto pasa a 0" 0 "$rcm"
case "$out" in *"CAMBIA CON EL ENTORNO"*) d=si ;; *) d=no ;; esac
check "(M9) y la pata deja de marcarse: el falso verde de GAT-173" no "$d"

# ─────── (10) EL CANARIO: un arnes MUDO no puede pasar por «todas iguales» ──────────────────────
#
# ⛔ Es de KERNEL y cierra el ultimo verde ciego del modo: si la exportacion fallara —un `env` mal
# construido, una expansion que se come la lista—, TODAS las patas darian el mismo veredicto en los
# dos pases y el modo saldria LIMPIO **precisamente cuando no esta midiendo nada**. «Siete iguales»
# y «arnes mudo» son indistinguibles sin un control que TENGA que cambiar.
#
# El mutante rompe la exportacion en `corre_pata`, que es el unico camino que usan tanto las patas
# como el canario. Si el canario no lo caza, no vigila el arnes: vigila otra cosa.
MUT10="$BASE/mut10.sh"
python3 - "$SUT" "$MUT10" <<'M10'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = '\t( cd "$WT" && env ${envs[@]+"${envs[@]}"} timeout -k 10 "$TIMEOUT" "$@" ) >"$sal" 2>&1'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    s.replace(v, '\t( cd "$WT" && timeout -k 10 "$TIMEOUT" "$@" ) >"$sal" 2>&1', 1))
M10
[ -s "$MUT10" ] || { echo "  FAIL MUTANTE 10 NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUT10" && d=NO-DIFIERE || d=ok
check "(10) el mutante del arnes mudo difiere" ok "$d"
out="$(bash "$MUT10" "$L9" --hook-env --only-heavy 2>&1)"; rcm=$?
check "(10) con el arnes mudo el modo sale 2, NO limpio" 2 "$rcm"
case "$out" in *"el arnes no esta exportando"*) d=si ;; *) d=no ;; esac
check "(10) y dice que el arnes no exporta" si "$d"
case "$out" in *"LIMPIO"*) d=si ;; *) d=no ;; esac
check "(10) y NO declara limpio lo que no ha medido" no "$d"

# ══ LOS CUATRO MUTANTES QUE EL CONTRASTE `sol max` NOMBRO POR SU NOMBRE (2026-09-03) ═══════════
# Los cuatro estaban como **UNVERIFIED** en su informe: no bastaba con curar, habia que poder
# demostrar que la cura muerde.

# ── PV-PARTIAL: un `--print-*` que falla no puede quedar tapado por el otro ─────────────────────
LP="$BASE/parcial"; mkdir -p "$LP/scripts"
printf '%s\n' 'version: "3"' 'tasks:' '  verde-uno:' '    cmds: ["true"]' >"$LP/Taskfile.yml"
cat >"$LP/scripts/check-gate-parity.sh" <<'GP'
#!/usr/bin/env bash
case "${1:-}" in
--print-heavy)   echo "gate: no he podido derivar las pesadas" >&2; exit 2 ;;
--print-ci-only) echo "verde-uno" ;;
--print-root)    printf '%s\n' "${OLIVARES_ROOT:-x}" ;;
esac
GP
chmod +x "$LP/scripts/check-gate-parity.sh"; git -C "$LP" init -q -b main 2>/dev/null
out="$(bash "$SUT" "$LP" 2>&1)"; rc=$?
check "(PV-PARTIAL) media lista -> 2, NO verde sobre la mitad" 2 "$rc"
case "$out" in *"lista completa"*) d=si ;; *) d=no ;; esac
check "(PV-PARTIAL) y dice que la lista venia a medias" si "$d"

# ── PV-ERREXIT: la promesa no puede depender de como nos invoquen ───────────────────────────────
# `bash -e "$SUT"` con el laboratorio de DOS rojas: tienen que salir las dos y el resumen final.
out="$(bash -e "$SUT" "$L2" --only-heavy 2>&1)"; rc=$?
check "(PV-ERREXIT) invocado con bash -e sigue saliendo 1" 1 "$rc"
case "$out" in *"roja-uno"*) a=si ;; *) a=no ;; esac
case "$out" in *"roja-dos"*) b=si ;; *) b=no ;; esac
check "(PV-ERREXIT) y nombra LAS DOS, no solo la primera" "si si" "$a $b"
case "$out" in *"2 de 3 pata(s) en rojo"*) d=si ;; *) d=no ;; esac
check "(PV-ERREXIT) y llega al resumen final" si "$d"

# ── PV-KILL: una pata que IGNORA TERM tiene que tener techo ─────────────────────────────────────
LK="$BASE/terca"; mkdir -p "$LK/scripts"
printf '%s\n' 'version: "3"' 'tasks:' '  terca:' '    cmds: ["bash -c \"trap {} TERM; sleep 120\""]' >"$LK/Taskfile.yml"
sed -i 's/{}/'"''"'/' "$LK/Taskfile.yml"
printf 'terca\n' >"$LK/patas.txt"
gate_que_lee_la_raiz "$LK"; git -C "$LK" init -q -b main 2>/dev/null
ini=$(date +%s)
# El banco se pone SU PROPIO techo: si el SUT no lo tuviera, este `timeout` lo delata en vez de
# colgar la bateria entera.
out="$(timeout 60 bash "$SUT" "$LK" --only-heavy --timeout 2 2>&1)"; rc=$?
seg=$(( $(date +%s) - ini ))
check "(PV-KILL) una pata que ignora TERM no cuelga la herramienta" 1 "$rc"
[ "$seg" -lt 40 ] && d=si || d=no
check "(PV-KILL) y termina dentro del techo (${seg}s)" si "$d"
case "$out" in *"AGOTO EL PLAZO"*) d=si ;; *) d=no ;; esac
check "(PV-KILL) y nombra el plazo" si "$d"

# ── PV-ROOT-PROOF: un gate que atestigua OTRA raiz se rehusa, aunque su lista sea plausible ─────
LR="$BASE/raiz-mentida"; mkdir -p "$LR/scripts"
printf '%s\n' 'version: "3"' 'tasks:' '  verde-uno:' '    cmds: ["true"]' >"$LR/Taskfile.yml"
cat >"$LR/scripts/check-gate-parity.sh" <<'GP2'
#!/usr/bin/env bash
# Ignora OLIVARES_ROOT a proposito: devuelve una lista PLAUSIBLE y una raiz que no es la medida.
case "${1:-}" in
--print-heavy)   echo "verde-uno" ;;
--print-ci-only) : ;;
--print-root)    echo "/otra/casa" ;;
esac
GP2
chmod +x "$LR/scripts/check-gate-parity.sh"; git -C "$LR" init -q -b main 2>/dev/null
out="$(bash "$SUT" "$LR" --only-heavy 2>&1)"; rc=$?
check "(PV-ROOT-PROOF) raiz atestiguada distinta -> 2" 2 "$rc"
case "$out" in *"mediria otra casa"*) d=si ;; *) d=no ;; esac
check "(PV-ROOT-PROOF) y lo dice: mediria otra casa" si "$d"
case "$out" in *"LIMPIO"*) d=si ;; *) d=no ;; esac
check "(PV-ROOT-PROOF) y NO da verde con una lista plausible" no "$d"

# ── (11) UNA PATA QUE REESCRIBE EL ARBOL SE NOMBRA, y el veredicto no puede ser limpio ─────────
#
# ⛔ Medido el 2026-09-03 con esta misma herramienta sobre mi propio worktree: `lint:format-ratchet`
# dejo `web/src/styles/tokens.css` MODIFICADO. El arbol que esto mide es el de un lote a punto de
# empujarse, asi que esos cambios viajarian en el push sin que nadie los pidiera — y el sintoma
# llega despues, como «tengo cosas sin commitear que yo no toque».
LM="$BASE/muta"; mkdir -p "$LM"
printf '%s\n' 'version: "3"' 'tasks:' \
  '  reescribe:' '    cmds: ["printf tocado > fichero.txt"]' \
  '  no-toca:' '    cmds: ["true"]' >"$LM/Taskfile.yml"
printf '%s\n' 'reescribe' 'no-toca' >"$LM/patas.txt"
printf 'original\n' >"$LM/fichero.txt"
gate_que_lee_la_raiz "$LM"
git -C "$LM" init -q -b main 2>/dev/null
git -C "$LM" -c user.email=b@b -c user.name=b add -A >/dev/null 2>&1
git -C "$LM" -c user.email=b@b -c user.name=b -c commit.gpgsign=false commit -q -m base >/dev/null 2>&1

out="$(bash "$SUT" "$LM" --only-heavy 2>&1)"; rc=$?
check "(11) una pata que toca el arbol -> 1, no limpio" 1 "$rc"
case "$out" in *"TOCO EL ARBOL"*) d=si ;; *) d=no ;; esac
check "(11) y lo dice en su fila" si "$d"
case "$out" in *"fichero.txt"*) d=si ;; *) d=no ;; esac
check "(11) nombrando el FICHERO que toco" si "$d"
linea_nt="$(printf '%s\n' "$out" | command grep -m1 'no-toca' || true)"
case "$linea_nt" in *"TOCO"*) d=si ;; *) d=no ;; esac
check "(11) y la que no toca nada NO se marca" no "$d"
case "$out" in *"LIMPIO"*) d=si ;; *) d=no ;; esac
check "(11) y el veredicto no dice LIMPIO" no "$d"

# Mutante: se quita la huella de DESPUES. La mutacion deja de verse y el veredicto vuelve a limpio.
MUT11="$BASE/mut11.sh"
python3 - "$SUT" "$MUT11" <<'M11'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = '\thuella_despues="$(huella_arbol)"'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, '\thuella_despues="$huella_antes"', 1))
M11
[ -s "$MUT11" ] || { echo "  FAIL MUTANTE 11 NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUT11" && d=NO-DIFIERE || d=ok
check "(M11) el mutante de la huella difiere" ok "$d"
git -C "$LM" checkout -- fichero.txt 2>/dev/null
out="$(bash "$MUT11" "$LM" --only-heavy 2>&1)"; rcm=$?
check "(M11) sin la huella de despues, vuelve a salir limpio" 0 "$rcm"

# ═══ a repository gate: la biseccion no puede costar 25 h para leer lo que la salida ya dice ═════════════
#
# ⛔ MEDIDO POR r26, no inventado: con `task test` (cinco horas por pase) el doble pase son diez
# horas, y bisecar variable a variable —hasta siete corridas— son veinticinco. Para una respuesta
# que el PRIMER pase ya habia escrito en 756 bytes: `pg-test-env: FAILING — … OLIVARES_PG_LOCAL…`.
# Ademas la herramienta BORRABA esa salida al terminar la pata, asi que r26 tuvo que pararla y
# rescatar el fichero a mano.
LG="$BASE/gat176"; mkdir -p "$LG"
# Una pata que (a) difiere con/sin entorno y (b) NOMBRA la variable en su salida.
# ⛔ HEREDOC Y BLOQUE YAML, no `printf` con comillas anidadas: las simples dentro de un argumento
# ya entrecomillado se las come el shell y el `sh -c` llega SIN comillas — la tarea muere de
# sintaxis y el caso mide un laboratorio roto (0 s de reloj es la huella).
cat >"$LG/Taskfile.yml" <<'TF176'
version: "3"
tasks:
  nombra-la-variable:
    cmds:
      - |
        if [ -n "${OLIVARES_PUSH_CLASS:-}" ]; then
          echo "FAILING — culpa de OLIVARES_PUSH_CLASS" >&2
          exit 1
        fi
TF176
printf 'nombra-la-variable\n' >"$LG/patas.txt"
mkdir -p "$LG/.githooks"
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_CLASS="$gate_class"' \
  'export OLIVARES_PG_LOCAL_DEFAULTS=1' >"$LG/.githooks/pre-push"
gate_que_lee_la_raiz "$LG"; git -C "$LG" init -q -b main 2>/dev/null

out="$(bash "$SUT" "$LG" --hook-env --only-heavy 2>&1)"; rc=$?
check "(GAT-176) la pata que difiere sigue saliendo 1" 1 "$rc"
case "$out" in *"nombrada en la salida, sin re-correr"*) d=si ;; *) d=no ;; esac
check "(GAT-176) y la culpable se LEE de la salida, sin re-correr" si "$d"
case "$out" in *"evidencia:"*) d=si ;; *) d=no ;; esac
check "(GAT-176) y se conserva la evidencia de los dos pases" si "$d"
ev="$(printf '%s\n' "$out" | sed -n 's/.*evidencia: \([^ ]*\) .*/\1/p' | head -1)"
[ -n "$ev" ] && [ -s "$ev" ] && d=si || d=no
check "(GAT-176) y el fichero de evidencia EXISTE y no esta vacio" si "$d"

# --- Una pata CARA que difiere y cuya salida NO nombra ninguna variable: no se bisecta sola.
LC2="$BASE/gat176-cara"; mkdir -p "$LC2/.githooks"
# ⛔ LA LOGICA VA EN UN GUION APARTE, y esto lo descubri corriendo el caso: `task` ECOA el texto de
# la orden, asi que una tarea que mencione la variable en su `cmds:` la deja escrita en la salida —
# y la heuristica «leela de la salida» la encuentra, con razon. Para un laboratorio MUDO de verdad,
# la mencion tiene que quedar fuera de lo que se ecoa.
# export-closure: fixture scripts/muda.sh — es contenido de laboratorio que este banco ESCRIBE en su propio arbol desechable; no existe ni en el hub ni en el export, y no debe existir
mkdir -p "$LC2/scripts"
cat >"$LC2/scripts/muda.sh" <<'MUDA'
#!/usr/bin/env bash
sleep 3
[ -z "${OLIVARES_PUSH_CLASS:-}" ] || exit 1
MUDA
chmod +x "$LC2/scripts/muda.sh"
cat >"$LC2/Taskfile.yml" <<'TF176B'
version: "3"
tasks:
  cara-y-muda:
    cmds:
      - bash scripts/muda.sh
TF176B
printf 'cara-y-muda\n' >"$LC2/patas.txt"
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_CLASS="$gate_class"' \
  'export OLIVARES_PG_LOCAL_DEFAULTS=1' >"$LC2/.githooks/pre-push"
gate_que_lee_la_raiz "$LC2"; git -C "$LC2" init -q -b main 2>/dev/null
ini=$(date +%s)
out="$(bash "$SUT" "$LC2" --hook-env --only-heavy --bisect-max 1 2>&1)"; rc=$?
seg=$(( $(date +%s) - ini ))
check "(GAT-176) pata cara y muda: sigue siendo hallazgo" 1 "$rc"
case "$out" in *"NO biseco"*) d=si ;; *) d=no ;; esac
check "(GAT-176) y NO bisecta sola: lo dice y explica el coste" si "$d"
# Dos pases de ~3 s; con biseccion serian dos mas. El techo demuestra que no las hizo.
[ "$seg" -lt 14 ] && d=si || d=no
check "(GAT-176) y se nota en el reloj (${seg}s)" si "$d"

# --- El modo de un solo pase, que es la otra mitad de la cura.
out="$(bash "$SUT" "$LG" --only-hook-env --only-heavy 2>&1)"; rc=$?
check "(GAT-176) --only-hook-env: un solo pase, con entorno -> 1" 1 "$rc"
case "$out" in *"CAMBIA CON EL ENTORNO"*) d=si ;; *) d=no ;; esac
check "(GAT-176) y NO hace el diferencial (no lo han pedido)" no "$d"

# ── FUGA DE CREDENCIAL POR EL REMOTO ──────────────────────────────────────────────────────────
# ⛔ POR QUE EXISTE, y no es hipotetico: este guion COPIA `remote.origin.url` a
#    `OLIVARES_PUSH_REMOTE_URL` y despues IMPRIME el bloque entero. Un remoto puede llevar la
#    credencial en el userinfo (`https://usuario:TOKEN@host/…`), y de la salida va al log y del log
#    al asiento del bus, que leen todos los carriles. El 2026-09-04 se encontro en ESTA caja un clon
#    cuyo `remote.origin.url` llevaba un PAT de GitHub en claro apuntando a produccion: una sola
#    pre-verificacion en ese clon lo habria publicado.
#
# El caso planta un remoto con credencial y EXIGE la ausencia LITERAL del token en la salida. Se
# comprueba la ausencia del secreto y la presencia del marcador: exigir solo `<redacted>` pasaria
# con una salida que lo imprimiera Y ademas filtrara el token en otra linea.
LFUGA="$BASE/lab-fuga"
mkdir -p "$LFUGA/.githooks"
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_REMOTE_URL="$remote_url"' \
  'export OLIVARES_PUSH_CLASS="$gate_class"' >"$LFUGA/.githooks/pre-push"
lab "$LFUGA" 'verde-uno' "$TAREAS_OK"
TOKEN_SENUELO='ghp_TOKENdePRUEBAquenodebeSALIR0001'
git -C "$LFUGA" remote add origin "https://audituser:$TOKEN_SENUELO@github.com/x/y.git" 2>/dev/null
out="$(bash "$SUT" "$LFUGA" --only-hook-env --only-heavy 2>&1)"
case "$out" in *"$TOKEN_SENUELO"*) d=si ;; *) d=no ;; esac
check "el token del remoto NO aparece en la salida" no "$d"
case "$out" in *"<redacted>"*) d=si ;; *) d=no ;; esac
check "y la URL sale redactada, no omitida" si "$d"
case "$out" in *"github.com/x/y.git"*) d=si ;; *) d=no ;; esac
check "y el HOST sigue visible: se redacta el secreto, no el dato" si "$d"

# ── LAS DOS CAPAS SON REALES Y ESTA BATERIA NO LAS DISTINGUE. Se dice, no se disimula ──────────
# ⛔ MEDIDO TRES VECES AL ESCRIBIR ESTO, y el resultado va aqui porque la alternativa era etiquetar
#    como probado algo que no lo esta:
#
#      quitar la redaccion de CAPTURA    -> 70 passed, 0 failed
#      quitar la redaccion de IMPRESION  -> 70 passed, 0 failed
#      quitar LAS DOS                    -> 2 failed
#
#    (mutaciones verificadas por `md5sum` antes de creer el veredicto: la primera vez que las corri,
#    una de ellas fallo en silencio y su bateria verde se leyo como «la capa no hace falta».)
#
#    ⇒ Cada capa sola tapa al mutante de la otra A TRAVES DE ESTA INTERFAZ, porque lo unico que el
#    banco observa es la SALIDA del guion. Eso NO las hace redundantes, y la razon esta en el codigo:
#    `$ENTORNO` no solo se imprime — se pasa a `corre_pata` (`pre-verify-tanda.sh:262,372,396`), o
#    sea que sus valores se EXPORTAN al entorno de cada pata del gate. El `sed` de impresion no toca
#    ese camino: sin la redaccion en la CAPTURA, la credencial entra en el entorno de todas las
#    patas y cualquiera que vuelque el suyo la publica.
#
#    Lo que falta para distinguirlas es observar la salida de la pata, que este banco no captura.
#    Queda escrito como trabajo pendiente Y como aviso: **si alguien borra una de las dos capas por
#    redundante, esta bateria NO se pondra roja.** La proteccion de la capa de captura la sostiene
#    hoy este comentario, no un caso.
#
# El caso de abajo es una asercion de punta a punta valida —el token no sale— y NO prueba la capa.

# ⛔ MEDIDO, y corrige lo que yo mismo iba a escribir. Con solo el caso de arriba, quitar CUALQUIERA
#    de las dos redacciones dejaba la bateria verde, y la lectura facil era «son dos capas de lo
#    mismo». No lo son: `$ENTORNO` no solo se IMPRIME — se pasa a `corre_pata` (`:262`, `:372`,
#    `:396`), o sea que sus valores se EXPORTAN al entorno de cada pata que corre. El `sed` de
#    impresion no toca ese camino. Sin la redaccion en la CAPTURA, la credencial entra en el entorno
#    de todas las patas del gate, y cualquiera que vuelque su entorno la publica.
#
#    Este caso corre una pata que imprime la variable y exige que el token no este ahi.
LFUGA3="$BASE/lab-fuga3"
mkdir -p "$LFUGA3/.githooks"
TOKEN3='ghp_ENTORNOdePATAtokenPRUEBA00003'
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_REMOTE_URL="$remote_url"' \
  'export OLIVARES_PUSH_CLASS="$gate_class"' >"$LFUGA3/.githooks/pre-push"
lab "$LFUGA3" 'chiva' 'version: "3"
tasks:
  chiva:
    cmds: ["printf %s\\n \"${OLIVARES_PUSH_REMOTE_URL:-vacia}\""]
'
git -C "$LFUGA3" remote add origin "https://audituser:$TOKEN3@github.com/x/y.git" 2>/dev/null
out="$(bash "$SUT" "$LFUGA3" --only-hook-env --only-heavy 2>&1)"
case "$out" in *"$TOKEN3"*) d=si ;; *) d=no ;; esac
check "con un remoto con credencial, el token no sale (punta a punta)" no "$d"

# ── Una credencial en OTRA variable: cubre lo que la redaccion de la URL del remoto no alcanza ───────────────────────
# ⛔ MEDIDO AL ESCRIBIR ESTO, y por eso existe: con el caso de arriba solo, quitar CUALQUIERA de las
#    dos redacciones deja la bateria en VERDE — cada capa sola tapa al mutante de la otra. Una
#    defensa en profundidad que ninguna prueba distingue no es defensa en profundidad: es una capa
#    y una copia sin vigilar, y la copia se borra el dia que alguien la vea redundante.
#
#    La segunda capa NO es una copia: la primera cubre la variable que HOY lleva una URL
#    (`OLIVARES_PUSH_REMOTE_URL`) y la segunda cubre cualquier OTRA que llegue a llevarla. Asi que
#    su caso planta la credencial en una variable DISTINTA, que la primera capa no toca.
LFUGA2="$BASE/lab-fuga2"
mkdir -p "$LFUGA2/.githooks"
TOKEN2='ghp_SEGUNDAcapaTOKENdePRUEBA00002'
printf '%s\n' '#!/usr/bin/env bash' \
  "export OLIVARES_MIRROR_URL=\"https://espejo:$TOKEN2@example.invalid/m.git\"" \
  'export OLIVARES_PUSH_CLASS="$gate_class"' >"$LFUGA2/.githooks/pre-push"
lab "$LFUGA2" 'verde-uno' "$TAREAS_OK"
git -C "$LFUGA2" remote add origin 'https://example.invalid/limpio.git' 2>/dev/null
out="$(bash "$SUT" "$LFUGA2" --only-hook-env --only-heavy 2>&1)"
case "$out" in *"$TOKEN2"*) d=si ;; *) d=no ;; esac
check "una credencial en OTRA variable tampoco sale" no "$d"

# ── LA HUELLA SE MIRA TAMBIEN DESPUES DEL SEGUNDO PASE ────────────────────────────────────────
# ⛔ Se tomaba UNA sola vez, tras el primer pase, asi que lo que ensuciara el arbol en el SEGUNDO era
#    invisible — y el segundo pase es el que corre con el entorno del gancho, o sea el que mas se
#    parece al push real. La pata de abajo escribe `hook.txt` SOLO cuando `OLIVARES_PUSH_CLASS` esta
#    puesta, que es exactamente el caso que se colaba.
# ⛔ EL GUION DEL LABORATORIO VIVE EN `bin/`, NO EN `scripts/`, y no es capricho: corregido el
#    2026-09-04 (r27). `lint:export-closure` clasifica SIEMPRE «una ruta desnuda que se
#    ejecuta» (`bash scripts/x.sh`), y no puede saber que ésta es interna a un laboratorio
#    que el propio banco fabrica dos lineas mas abajo. Con la ruta bajo `scripts/` acusaba a
#    `scripts/ensucia.sh` de referencia colgante y dejaba la pata ROJA en el lote — verde en
#    `main` y roja aqui, o sea del lote. El fixture es autocontenido y la ruta es arbitraria:
#    sacarla de la forma que el gate reconoce cuesta tres lineas y no cambia lo que prueba.
LH2="$BASE/lab-huella2"
mkdir -p "$LH2/.githooks"
mkdir -p "$LH2/bin"
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_CLASS="$gate_class"' >"$LH2/.githooks/pre-push"
lab "$LH2" 'ensucia-solo-con-entorno' 'version: "3"
tasks:
  ensucia-solo-con-entorno:
    cmds:
      - bash bin/ensucia.sh
'
cat >"$LH2/bin/ensucia.sh" <<'ENS'
#!/usr/bin/env bash
[ -n "${OLIVARES_PUSH_CLASS:-}" ] && printf 'sucio
' >hook.txt
exit 0
ENS
chmod +x "$LH2/bin/ensucia.sh"
git -C "$LH2" add -A >/dev/null 2>&1
git -C "$LH2" -c user.email=b@b -c user.name=b commit -qm base >/dev/null 2>&1
out="$(bash "$SUT" "$LH2" --hook-env --only-heavy 2>&1)"
case "$out" in *"hook.txt"*) d=si ;; *) d=no ;; esac
check "una pata que solo ensucia en el 2.o pase SE CUENTA" si "$d"
case "$out" in *"2.o pase"*) d=si ;; *) d=no ;; esac
check "y se dice EN QUE pase lo hizo" si "$d"

# ── UN `git status` QUE FALLA ES «NO PUDE MIRAR», NO «ESTA LIMPIO» ─────────────────────────────
# ⚠ ESTE CASO PASA HOY Y **NO DISCRIMINA**: se dice porque medirlo y callarlo seria peor que no
#   medirlo. Con la cura quitada —`huella_arbol` volviendo a `2>/dev/null | sort`— la bateria sigue
#   dando 0 fallos, o sea que este caso NO mata a su mutante y por tanto no prueba la propiedad.
#   El mutante de ALTO-1, al lado, SI muere (2 fallos), asi que el banco discrimina cuando puede.
#
#   Una causa QUEDA ESTABLECIDA y otra NO. Establecida: al reproducirlo a mano el senuelo estaba en
#   `/tmp`, que en esta caja es **noexec**, y se invocaba con rc 126 sin llegar a correr nunca — la
#   sonda de diagnostico media el vacio. NO establecida: por que el senuelo de ESTE banco, que nace
#   bajo `$TMPDIR` y si es ejecutable, tampoco hace morir al mutante.
#
#   ⇒ La cura se queda porque es correcta por construccion —leer el rc de un veredicto DESPUES de un
#   tubo es leer el del ultimo tramo, y `sort` casi siempre sale 0— y no porque este caso la
#   defienda. Queda como trabajo pendiente con nombre: **hacer que este caso mate a su mutante**.
#   Mientras no lo haga, borrar la cura NO pondra roja la bateria.
# ⛔ `huella_arbol` hacia `git status --porcelain 2>/dev/null | sort`: el `2>/dev/null` se traga el
#    motivo y el `|` deja ver el rc de `sort`, no el de `git`. Un status que falla devolvia cadena
#    VACIA, y dos huellas vacias dicen «el arbol no cambio»: la tercera respuesta desaparecia dentro
#    de la huella. El senuelo hace fallar SOLO `git … status` y deja pasar todo lo demas.
LSH="$BASE/lab-status-roto"
mkdir -p "$LSH/.githooks" "$BASE/shim"
printf '%s\n' '#!/usr/bin/env bash' 'export OLIVARES_PUSH_CLASS="$gate_class"' >"$LSH/.githooks/pre-push"
lab "$LSH" 'verde-uno' "$TAREAS_OK"
GITREAL="$(command -v git)"
cat >"$BASE/shim/git" <<SHIM
#!/usr/bin/env bash
for a in "\$@"; do [ "\$a" = status ] && exit 73; done
exec "$GITREAL" "\$@"
SHIM
chmod +x "$BASE/shim/git"
out="$(PATH="$BASE/shim:$PATH" bash "$SUT" "$LSH" --only-heavy 2>&1)"; rc=$?
check "un git status roto es NO PUDE MIRAR (2), no limpio" 2 "$rc"
case "$out" in *"estado del arbol"*) d=si ;; *) d=no ;; esac
check "y lo NOMBRA en vez de seguir" si "$d"

# ══ R18 · EL `task` REAL BAJO `CI=true`, Y UN PRODUCTOR QUE MUERE A MEDIA LISTA ═════════════════
# ⛔ MEDIDO el 2026-09-07 en `mainline-ci` (run 34167328609, job 101880875701, paso
#    `leg-pre-verify-tanda-selftest`, fuente 91758901e3): 118 passed, 47 failed, y las 47 nacian de
#    lo mismo — Task 3.51.1 con `CI=true` en el entorno escribe secuencias ANSI en `--list-all`
#    AUNQUE la salida vaya por un tubo (162 bytes con 14 ESC donde a mano salen 102 limpios), el
#    `awk` del sujeto no casaba ni una fila y el laboratorio benigno contestaba «no devolvio ninguna
#    tarea». Ningun senuelo lo reproduce: hace falta el `task` REAL del PATH con la variable puesta,
#    y por eso estos casos lo usan. El sujeto pide ahora `--color=false`, que gana a `CI=true` y a
#    `FORCE_COLOR=1` juntas (sondeado sobre el binario 3.51.1).
#
# ⛔ `NO_COLOR` SE RETIRA A PROPOSITO en cada invocacion de este bloque, y no es cosmetica: un
#    `NO_COLOR=1` heredado —lo lleva la shell de mas de una caja de desarrollo— hace que Task calle
#    el color aunque `CI=true` este puesta, y entonces el mutante sin bandera SOBREVIVIRIA y este
#    bloque pasaria por la razon equivocada. Medido al escribirlo: con `NO_COLOR=1` en el entorno el
#    sujeto BASE (sin la cura) salia 0 bajo `CI=true`; sin `NO_COLOR`, 2. El runner no lleva
#    `NO_COLOR`, asi que retirarla es reproducirlo, no maquillarlo.
out="$(env -u NO_COLOR CI=true FORCE_COLOR=1 bash "$SUT" "$L1" --only-heavy 2>&1)"; rc=$?
check "(R18-COLOR) todas verdes bajo CI=true FORCE_COLOR=1 con el task real -> 0" 0 "$rc"
case "$out" in *"pre-verify-tanda: LIMPIO"*) d=si ;; *) d=no ;; esac
check "(R18-COLOR) y lo dice como LIMPIO" si "$d"
linea_colon="$(printf '%s\n' "$out" | command grep -m1 'verde:dos:con:puntos' || true)"
case "$linea_colon" in *"verde:dos:con:puntos"*" 0 "*) d=si ;; *) d=no ;; esac
check "(R18-COLOR) y el nombre con dos puntos sale ENTERO en su fila, con rc 0" si "$d"
out="$(env -u NO_COLOR CI=true FORCE_COLOR=1 bash "$SUT" "$L2" --only-heavy 2>&1)"; rc=$?
check "(R18-COLOR) con rojas bajo CI=true sigue -> 1" 1 "$rc"
case "$out" in *"roja-uno"*) a=si ;; *) a=no ;; esac
case "$out" in *"roja-dos"*) b=si ;; *) b=no ;; esac
check "(R18-COLOR) y nombra LAS DOS rojas" "si si" "$a $b"
out="$(env -u NO_COLOR CI=true FORCE_COLOR=1 bash "$SUT" "$L4" --only-heavy 2>&1)"; rc=$?
check "(R18-COLOR) pata inexistente bajo CI=true sigue -> 2, no 1" 2 "$rc"
case "$out" in *"pata-fantasma"*"no existe como tarea"*) d=si ;; *) d=no ;; esac
check "(R18-COLOR) y la sigue distinguiendo de una roja: «no existe como tarea»" si "$d"

# --- MUTANTE R18-COLOR: se quita `--color=false`. Bajo CI=true el laboratorio benigno cae a 2 con
# --- la refusal exacta del runner; sin CI, el mismo mutante sigue en 0 — la dimension es el entorno.
MUTC="$BASE/mut-color.sh"
python3 - "$SUT" "$MUTC" <<'MC'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = 'task --list-all --color=false 2>&1)"; RC_LISTA=$?'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, 'task --list-all 2>&1)"; RC_LISTA=$?', 1))
MC
[ -s "$MUTC" ] || { echo "  FAIL MUTANTE R18-COLOR NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUTC" && d=NO-DIFIERE || d=ok
check "(M-R18-COLOR) el mutante sin --color=false difiere" ok "$d"
out="$(env -u NO_COLOR CI=true FORCE_COLOR=1 bash "$MUTC" "$L1" --only-heavy 2>&1)"; rcm=$?
check "(M-R18-COLOR) sin la bandera, bajo CI=true el laboratorio benigno cae a 2" 2 "$rcm"
case "$out" in *"'task --list-all' no devolvio ninguna tarea"*) d=si ;; *) d=no ;; esac
check "(M-R18-COLOR) con la refusal exacta del runner: el ANSI se comio las filas" si "$d"
out="$(env -u NO_COLOR -u CI -u FORCE_COLOR bash "$MUTC" "$L1" --only-heavy 2>&1)"; rcm=$?
check "(M-R18-COLOR) control: el mismo mutante SIN CI ni FORCE_COLOR sigue -> 0" 0 "$rcm"

# --- (R18-PRODUCTOR) UN `task --list-all` QUE IMPRIME MEDIA LISTA Y MUERE: 2, y NADA de lo impreso
# --- vale. La media lista trae justo la unica pata que el gate pide, asi que una version que se
# --- creyera la salida sin mirar el rc daria LIMPIO sobre una lista que su productor no termino.
LPF="$BASE/productor-roto"; lab "$LPF" 'verde-uno' "$TAREAS_OK" || exit 2
SHIMPF="$BASE/task-parcial"
# shellcheck disable=SC2086  # la lista se expande a proposito, una utilidad por argumento
granja "$SHIMPF" $UTILES_BASE timeout
MARCA_PF="$BASE/parcial.llamadas"
cat >"$SHIMPF/task" <<SHIM
#!/bin/sh
case "\${1:-}" in
--list-all) printf 'task: Available tasks for this project:\n* verde-uno:            \n'; echo "SENUELO_PARCIAL: me muero a media lista" >&2; exit 5 ;;
--version)  echo 3.51.1 ;;
*)          echo "\$*" >>"$MARCA_PF"; exit 0 ;;
esac
SHIM
chmod +x "$SHIMPF/task"
: >"$MARCA_PF"
out="$(PATH="$SHIMPF" "$BASH" "$SUT" "$LPF" --only-heavy 2>&1)"; rc=$?
check "(R18-PRODUCTOR) media lista y rc 5 -> 2, no un verde sobre la mitad que llego" 2 "$rc"
case "$out" in *"'task --list-all' salio 5"*) d=si ;; *) d=no ;; esac
check "(R18-PRODUCTOR) y nombra el rc del productor" si "$d"
case "$out" in *"SENUELO_PARCIAL"*) d=si ;; *) d=no ;; esac
check "(R18-PRODUCTOR) y conserva lo que el productor dijo al morir" si "$d"
case "$out" in *"no existe como tarea"*) d=si ;; *) d=no ;; esac
check "(R18-PRODUCTOR) y NO culpa a una tarea: la causa es el productor" no "$d"
[ -s "$MARCA_PF" ] && d=corrio || d=ninguna
check "(R18-PRODUCTOR) y NO corrio ninguna pata con la media lista" ninguna "$d"

# --- MUTANTE R18-PRODUCTOR: se deja de leer el rc. La media lista pasa por entera, la pata corre y
# --- el veredicto es LIMPIO: el verde ciego que la guarda existe para cortar.
MUTPF="$BASE/mut-productor.sh"
python3 - "$SUT" "$MUTPF" <<'MPF'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = '[ "$RC_LISTA" -eq 0 ] || cannot'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, '[ "$RC_LISTA" -ge 0 ] || cannot', 1))
MPF
[ -s "$MUTPF" ] || { echo "  FAIL MUTANTE R18-PRODUCTOR NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$SUT" "$MUTPF" && d=NO-DIFIERE || d=ok
check "(M-R18-PRODUCTOR) el mutante que no lee el rc difiere" ok "$d"
: >"$MARCA_PF"
out="$(PATH="$SHIMPF" "$BASH" "$MUTPF" "$LPF" --only-heavy 2>&1)"; rcm=$?
check "(M-R18-PRODUCTOR) sin leer el rc, la media lista pasa por entera y sale 0" 0 "$rcm"
case "$out" in *"pre-verify-tanda: LIMPIO"*) d=si ;; *) d=no ;; esac
check "(M-R18-PRODUCTOR) y declara LIMPIO lo que su productor no termino" si "$d"
[ -s "$MARCA_PF" ] && d=corrio || d=ninguna
check "(M-R18-PRODUCTOR) y corre patas sobre esa lista a medias" corrio "$d"

echo "pre-verify-tanda: $pasados passed, $fallados failed"
[ "$fallados" -eq 0 ] || exit 1
exit 0
