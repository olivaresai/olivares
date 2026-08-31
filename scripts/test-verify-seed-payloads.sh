#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Bateria de `scripts/verify-seed-payloads.py`. Prueba lo que el guion PROMETE, no que arranque:
# el contrato de `rc` por sus dos mitades y la guarda de secretos VIENDOLA CORTAR y VIENDOLA DEJAR
# PASAR — un control que rechaza todo pasa cualquier prueba de «rechaza».
#
# ⛔ POR QUE HAY MUTANTES AQUI Y NO SOLO CASOS. Un caso verde sobre un control sano no prueba que el
#    control haga algo: prueba que hoy no estorba. Los mutantes de abajo COMPILAN y CORREN, y cada
#    uno tiene que MATAR a su caso. Si un mutante sobrevive, la fila que lo cubre no cubre nada, y
#    esta bateria sale 1 diciendo cual.
#
# ⛔ Y NO SE NECESITA MOTOR. La mitad cara de la verificacion (los payloads) exige uno vivo, pero el
#    CONTRATO —el rc y la guarda— no, y atarlo a un motor lo dejaria sin correr en el gate. Aqui el
#    camino «no he podido mirar» se prueba con un puerto donde no hay nadie, que es literalmente la
#    condicion que ese rc describe.
set -u -o pipefail

# ⛔ EL ENTORNO GIT AMBIENTE MANDA SOBRE `-C` Y SOBRE EL cwd. Con `GIT_DIR` exportada —y git la
# exporta desde CUALQUIER worktree enlazado, o sea desde cualquier sesion en paralelo— los
# repositorios de usar y tirar de esta bateria se conducirian al repositorio VIVO. Medido el
# 2026-08-06: dejo la rama de la PR #526 apuntando a un commit de fixture. Falla cerrado: un
# saneador que falta es «no he podido aislar», nunca «no hacia falta aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
GUION="$RAIZ/scripts/verify-seed-payloads.py"
TRABAJO="$(mktemp -d)"
trap 'rm -rf "$TRABAJO"' EXIT

# ⛔ PREFLIGHT · sin `python3` esta bateria no mide nada, y una bateria que no puede correr tiene
# que decirlo con rc 2, no fallar caso a caso como si el guion estuviera roto (the reviewer/44).
if ! command -v python3 >/dev/null 2>&1; then
	printf 'test-verify-seed-payloads: NO HE PODIDO MIRAR: no hay python3 en el PATH\n' >&2
	exit 2
fi

ok=0
fail=0

paso() { printf 'ok   %s\n' "$1"; ok=$((ok + 1)); }
malo() { printf 'FAIL %s\n' "$1"; fail=$((fail + 1)); }

# rc_de <fichero-guion> <args...> — imprime el codigo de salida, nunca la salida del guion.
# ⛔ EL DIAGNOSTICO NO SE TIRA (the reviewer/44, A-08). La version anterior mandaba toda la salida a
#    /dev/null y devolvia solo el codigo, asi que un caso podia exigir «rc 2» y darse por bueno con
#    un rc 2 de OTRA causa — el motor caido, un typo en la URL, argparse rechazando una bandera.
#    Es mi propia ficha «un mutante acredita la pata que NOMBRA» aplicada a mi banco. Ahora la
#    salida se guarda y `casa_mensaje` exige que el veredicto DIGA lo que el caso afirma.
SALIDA="$TRABAJO/salida.txt"
rc_de() {
	local g="$1"
	shift
	python3 "$g" "$@" >"$SALIDA" 2>&1
	printf '%s' "$?"
}

# casa_mensaje <patron> — cierto si la ULTIMA salida capturada lo contiene.
casa_mensaje() { command grep -qE "$1" "$SALIDA"; }

# ── veredicto_mutante / juicio_valido ─────────────────────────────────────────────────────────
# ⛔ ESTE ARNES INFORMABA «SOBREVIVIO» SOBRE UN rc 2, y lo cazo un lector despues de que la propia
#    bateria ya se hubiera comido el caso una vez: mas abajo hay un comentario contando que un
#    ancla movida dejo el mutante SIN CONSTRUIR y el mensaje dijo que habia sobrevivido. Se
#    arreglo aquel ancla y NO el arnes, asi que la trampa siguio armada para el siguiente.
#
#    `rc_de` corre `python3 <fichero>`: si el mutante no se escribio —porque su `assert` salto al
#    moverse el ancla— python sale 2 con «can't open file», y el `else` de cada caso lo cantaba
#    como supervivencia. **Un «no he podido mirar» leido como veredicto es la clase mas cara de
#    esta casa**, y aqui ademas INVIERTE EL SIGNO: un fallo del arnes se lee como un fallo del
#    codigo, que es donde nadie va a ir a buscar.
#
#    Y hay una cuarta confusion del mismo cuno: un mutante que sale rc 1 **por OTRO mensaje** murio
#    en una pata anterior y NO acredita el caso que lo nombra — el `else` tambien lo llamaba
#    supervivencia. Son CUATRO estados, no dos, y cada uno se dice por su nombre.
veredicto_mutante() { # $1 fichero mutante · $2 patron del mensaje · resto: args del sujeto
	local m="$1" patron="$2"
	shift 2
	if [ ! -s "$m" ]; then
		printf 'NOPUDE:el mutante no se construyo (fichero ausente o vacio): sin artefacto no hay juicio'
		return
	fi
	local r
	r="$(rc_de "$m" "$@")"
	case "$r" in
	1)
		if casa_mensaje "$patron"; then
			printf 'MUERTO'
		else
			printf 'NOPUDE:rc 1 pero por OTRO mensaje que el de su caso: murio en una pata anterior'
		fi
		;;
	2) printf 'NOPUDE:rc 2 es NO HE PODIDO MIRAR, no supervivencia' ;;
	0) printf 'VIVO' ;;
	*) printf 'NOPUDE:rc %s inesperado' "$r" ;;
	esac
}

# juicio_valido <fichero> — rc 0 si el mutante dejo un veredicto LEGIBLE en ese fichero.
# ⛔ Los casos que se juzgan por FICHERO ya tenian constructor fail-closed, pero su comprobacion
#    interna (`grep -q <marca>`) confunde «el mutante no produjo veredicto» con «sobrevivio»: si la
#    corrida revienta, el fichero no trae la marca y el `else` la canta como supervivencia. Misma
#    inversion de signo, por otra puerta.
juicio_valido() {
	if [ ! -s "$1" ]; then
		return 1
	fi
	if command grep -qiE 'Traceback|NO HE PODIDO MIRAR' "$1"; then
		return 1
	fi
	return 0
}

# ── 0-bis · EL ARNES SE MIDE A SI MISMO ───────────────────────────────────────────────────────
# ⛔ ESTA BATERIA INFORMABA «SOBREVIVIO» SOBRE UN rc 2 y ya se lo habia comido UNA VEZ: hay un
#    comentario, mas abajo, contando que un ancla movida dejo un mutante sin construir y el mensaje
#    dijo que habia sobrevivido. **Se arreglo aquel ancla y NO el arnes**, asi que la trampa siguio
#    armada — y un lector la volvio a encontrar. Un defecto que ya conoces y no cierras vuelve.
#
#    Estos casos son de `veredicto_mutante` y `juicio_valido`, no del sujeto: sin ellos, la
#    siguiente regresion del arnes es otra vez muda. Cinco direcciones, una por estado.
falso() { # $1 = nombre · $2 = rc · $3 = mensaje
	# ⛔ `%r` NO EXISTE en el printf de la shell (es de Python). La primera version lo uso y escribio
	#    ficheros rotos: los dos sujetos falsos salian rc 1 por SyntaxError, y el arnes los clasifico
	#    —correctamente— como «murio por OTRO mensaje». Lo canto este mismo caso, en alto.
	{
		printf 'import sys\n'
		printf 'print("""%s""", file=sys.stderr)\n' "$3"
		printf 'sys.exit(%s)\n' "$2"
	} >"$TRABAJO/$1.py"
	python3 -c 'import ast,sys; ast.parse(open(sys.argv[1]).read())' "$TRABAJO/$1.py" ||
		malo "NO HE PODIDO MIRAR: el sujeto falso $1 no es python valido; el auto-testigo del arnes no vale"
}
falso f_muere 1 'corto de mas: localizador legitimo aqui'
falso f_otro 1 'reventé por una razon COMPLETAMENTE distinta'
falso f_nopude 2 'NO HE PODIDO MIRAR: me falta el fichero'
falso f_vivo 0 'todo bien, no corte nada'

# ⛔ SE EXIGE LA RAZON, NO EL PREFIJO, y esto lo caza un lector sobre la PRIMERA version de este
#    mismo caso. Comparando con `case "$v" in "$2"*)` los TRES estados de «no pude mirar» pasaban
#    con cualquier razon: un arnes que los colapsara en uno solo habria seguido en verde. Es decir,
#    el caso que existe para acreditar que se distinguen CUATRO estados solo distinguia DOS — el
#    mismo defecto que cura, cometido en su testigo.
#
#    `MUERTO` y `VIVO` se comparan ENTEROS (`=`), no por prefijo. `NOPUDE` se comprueba ADEMAS por
#    un trozo de su razon, distinto en cada caso.
# ⛔ `${v%%:*}` TRUNCABA EN EL PRIMER DOS-PUNTOS, y eso lo caza un lector sobre la version que
#    presumia de comparar «enteros»: era cierto del PREFIJO. `MUERTO:lo-que-sea` daba cabeza
#    `MUERTO` y pasaba, asi que un arnes que le colgara una cola a un veredicto limpio sobrevivia
#    27/0. Tercera vuelta de la misma familia en este fichero, y cada vuelta ha sido mas fina:
#    prefijo -> razon -> COLA. Ahora `MUERTO` y `VIVO` van SOLOS, sin dos puntos ni nada detras.
esperado() { # $1 = fichero · $2 = veredicto exacto · $3 = trozo de razon (vacio si no aplica) · $4 = etiqueta
	local v
	v="$(veredicto_mutante "$1" 'corto de mas: localizador legitimo')"
	if [ -z "$3" ]; then
		# MUERTO / VIVO: el veredicto va SOLO. Comparacion literal, sin trocear.
		if [ "$v" != "$2" ]; then
			malo "el arnes clasifico mal $4: dijo '$v' y esperaba EXACTAMENTE '$2', sin cola"
			return 1
		fi
		return 0
	fi
	# NOPUDE: prefijo con sus dos puntos, Y su razon concreta.
	case "$v" in
	"$2":*) : ;;
	*)
		malo "el arnes clasifico mal $4: dijo '$v' y esperaba '$2:' con su razon"
		return 1
		;;
	esac
	if ! command grep -qF "$3" <<<"$v"; then
		malo "el arnes acerto el veredicto de $4 pero NO su razon: dijo '$v' y esperaba que dijera '$3'"
		return 1
	fi
	return 0
}

fallos_arnes=0
esperado "$TRABAJO/f_muere.py" MUERTO '' 'un mutante que muere por SU mensaje' || fallos_arnes=1
esperado "$TRABAJO/f_otro.py" NOPUDE 'por OTRO mensaje' 'un mutante que sale 1 por OTRO mensaje' || fallos_arnes=1
esperado "$TRABAJO/f_nopude.py" NOPUDE 'rc 2 es NO HE PODIDO MIRAR' 'un mutante que sale rc 2' || fallos_arnes=1
esperado "$TRABAJO/f_vivo.py" VIVO '' 'un mutante que de verdad sobrevive' || fallos_arnes=1
esperado "$TRABAJO/no-existe-jamas.py" NOPUDE 'no se construyo' 'un mutante que NO se construyo' || fallos_arnes=1
if [ "$fallos_arnes" = "0" ]; then
	paso "el arnes separa los CUATRO estados: muerto, vivo, no-pude-mirar (rc 2 y sin construir) y muerto-por-OTRO-mensaje"
fi

: >"$TRABAJO/vacio.txt"
printf 'Traceback (most recent call last):\n  File "x"\n' >"$TRABAJO/revienta.txt"
printf 'FALTA-CORTAR algo\n' >"$TRABAJO/bueno.txt"
if juicio_valido "$TRABAJO/vacio.txt"; then
	malo "juicio_valido da por bueno un fichero VACIO: un mutante sin salida se leeria como supervivencia"
elif juicio_valido "$TRABAJO/revienta.txt"; then
	malo "juicio_valido da por bueno un Traceback: una corrida reventada se leeria como supervivencia"
elif ! juicio_valido "$TRABAJO/bueno.txt"; then
	malo "juicio_valido rechaza un veredicto legible: el arnes no dejaria pasar ningun caso"
else
	paso "juicio_valido distingue «no produjo veredicto» (vacio o reventado) de «no corto», que es lo que confundia"
fi

# ── 1 · la guarda de secretos, sana: corta lo que debe y deja pasar lo que debe ───────────────
r="$(rc_de "$GUION" x x x --autocomprobar)"
if [ "$r" = "0" ]; then
	paso "la autocomprobacion de la guarda de secretos pasa (rc 0)"
else
	malo "la autocomprobacion deberia salir 0 y salio $r"
fi

# ── 2 · MUTANTE: la guarda deja de reconocer formas de credencial ─────────────────────────────
# Tiene que MATAR al caso 1. Si sobrevive, el caso 1 no prueba nada.
m1="$TRABAJO/m1.py"
python3 - "$GUION" "$m1" <<'PY'
import sys
src = open(sys.argv[1]).read()
mut = src.replace("FORMAS_DE_CREDENCIAL = [", "FORMAS_DE_CREDENCIAL = [] and [")
assert mut != src, "el mutante 1 NO se aplico: la bateria estaria midiendo el guion sano"
open(sys.argv[2], "w").write(mut)
PY
v="$(veredicto_mutante "$m1" 'FAIL  no corto: aws-access-key' x x x --autocomprobar)"
case "$v" in
MUERTO) paso "el mutante que ciega la guarda MUERE nombrando el fixture que dejo pasar" ;;
VIVO) malo "el mutante que ciega la guarda SOBREVIVIO (rc 0): el caso 1 no cubre nada" ;;
*) malo "NO HE PODIDO MIRAR, no supervivencia — el mutante que ciega la guarda sin veredicto: ${v#NOPUDE:}" ;;
esac

# ── 3 · MUTANTE: la guarda corta de mas (rechaza los localizadores legitimos) ─────────────────
# La direccion de NO-DISPARO. Sin este caso, una guarda que rechaza TODO pasaria el caso 1 y el 2.
m2="$TRABAJO/m2.py"
python3 - "$GUION" "$m2" <<'PY'
import sys
src = open(sys.argv[1]).read()
# ⛔ EL ANCLA SE MOVIO CON LA CURA DEL CONTEXTO PERSISTENTE, y el caso salio rojo con un mensaje
# que decia «SOBREVIVIO» cuando lo que pasaba es que el mutante NO SE CONSTRUYO (rc 2). Se re-ancla
# a la linea de ahora. El mensaje enganoso queda dicho aqui: un rc 2 y un mutante vivo no son lo
# mismo, y esta rama los confundia.
# ⛔ SEGUNDA VEZ QUE ESTE ANCLA SE MUEVE POR UNA CURA MIA (primero el contexto persistente, ahora
# el paso a raices). Se ancla a la FUNCION que decide, no a la linea que la usa: es lo que menos se
# mueve, y si tambien cambiara, el fail-closed lo dice en vez de contar un verde.
viejo = "    return any(r in norm for r in RAICES_DE_SECRETO)"
nuevo = "    return True  # MUTANTE: corta de mas, tambien los localizadores legitimos"
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante 2 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
v="$(veredicto_mutante "$m2" 'corto de mas: localizador legitimo' x x x --autocomprobar)"
case "$v" in
MUERTO) paso "el mutante que corta de mas MUERE nombrando el localizador legitimo que rechazo" ;;
VIVO) malo "el mutante que corta de mas SOBREVIVIO (rc 0): un rechaza-todo pasaria la bateria" ;;
*) malo "NO HE PODIDO MIRAR, no supervivencia — el mutante que corta de mas sin veredicto: ${v#NOPUDE:}" ;;
esac

# ── 3-bis · MUTANTE: la guarda de ambito deja pasar una parada de `estate` ────────────────────
# Es la unica prohibicion PROPIA del guion —el motor acepta `estate` sin rechistar— y por tanto la
# unica que no tiene una segunda red debajo. Si este mutante sobrevive, el guion puede congelar el
# estate entero y dejar las otras quince capturas en denegado.
m2b="$TRABAJO/m2b.py"
python3 - "$GUION" "$m2b" <<'PY2'
import sys
src = open(sys.argv[1]).read()
viejo = '    if cuerpo.get("scope_kind") != "agent":'
nuevo = '    if False:'
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante 2b NO se aplico"
open(sys.argv[2], "w").write(mut)
PY2
v="$(veredicto_mutante "$m2b" 'guarda de ambito NO corto' x x x --autocomprobar)"
case "$v" in
MUERTO) paso 'el mutante que abre la guarda de ambito MUERE nombrando el ambito que dejo pasar' ;;
VIVO) malo "el mutante de la guarda de ambito SOBREVIVIO (rc 0): una parada de estate saldria" ;;
*) malo "NO HE PODIDO MIRAR, no supervivencia — el mutante de la guarda de ambito sin veredicto: ${v#NOPUDE:}" ;;
esac

# ── 2-bis · MUTANTE: se retira UNA forma y su fixture tiene que quedarse sin acreditar ────────
# ⛔ ES LA CURA DE A-05, Y EL CASO QUE ANTES NO EXISTIA. Con los fixtures sin declarar su forma, un
#    `sk-…` cubierto por DOS regex sobrevivia al mutante que quitaba una de ellas. Ahora cada
#    fixture nombra la forma que debe cortarlo, asi que retirar `openai-like-key` deja su fixture
#    sin cortar (o cortado por otra) y la autocomprobacion falla. Si este mutante sobrevive, la
#    acreditacion no acredita.
m1b="$TRABAJO/m1b.py"
python3 - "$GUION" "$m1b" <<'PY'
import sys, re
src = open(sys.argv[1]).read()
mut = re.sub(r'\n *\("openai-like-key", re\.compile\([^\n]*\),', '', src, count=1)
assert mut != src, "el mutante 1b NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
v="$(veredicto_mutante "$m1b" 'no corto: openai' x x x --autocomprobar)"
case "$v" in
MUERTO) paso "retirar UNA forma MUERE nombrando el fixture que la acreditaba" ;;
VIVO) malo "el mutante que retira una forma SOBREVIVIO (rc 0): los fixtures no acreditan nada" ;;
*) malo "NO HE PODIDO MIRAR, no supervivencia — el mutante que retira una forma sin veredicto: ${v#NOPUDE:}" ;;
esac

# ── 3-ter · MUTANTE: la forma de 40 hex vuelve a disparar SIN contexto ────────────────────────
# ⛔ Es la direccion de no-disparo de una forma que CASI se queda incondicional. La clave global
#    legada de Cloudflare son 40 hex y un SHA de git tambien: con la forma pura, un asiento que
#    cita `a5433047d14e6ef418a6f438e837e188f030f430` se cortaria como si fuera una credencial. El
#    mutante la devuelve a la lista incondicional y los dos fixtures de SHA tienen que matarlo.
m7="$TRABAJO/m7.py"
python3 - "$GUION" "$m7" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = "        k = _clave_normalizada(clave) if clave else \"\""
nuevo = "        k = \"apikey\"  # MUTANTE: el contexto se da por bueno siempre"
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante 7 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
v="$(veredicto_mutante "$m7" 'corto de mas: sha de git' x x x --autocomprobar)"
case "$v" in
MUERTO) paso "el mutante sin CONTEXTO MUERE nombrando el SHA de git que corto de mas" ;;
VIVO) malo "el mutante sin contexto SOBREVIVIO (rc 0): la guarda cortaria SHAs de git legitimos" ;;
*) malo "NO HE PODIDO MIRAR, no supervivencia — el mutante sin contexto sin veredicto: ${v#NOPUDE:}" ;;
esac

# ── 3-quater · AUSENCIA · dos testigos HERMETICOS, uno por cada inversion ─────────────────────
# ⛔ SIN ESTOS DOS CASOS, LA RAMA FAIL-CLOSED NO ESTABA CUBIERTA POR NADA — y lo peor es que el pase
#    vivo salia 9/0 y parecia cubrirla. Medido: los mutantes que reabren SOLO `k not in suyo` (nivel
#    anidado) o SOLO `k not in fila` (nivel superior) SOBREVIVEN los dos contra el motor, con rc 0.
#    La razon la dio el lector y es la leccion: **las nueve superficies estan COMPLETAS, asi que
#    nunca falta una clave y la rama nunca se ejerce.** La medida viva prueba COMPATIBILIDAD —que
#    poner fail-closed no rompe nada—, no PORTANCIA. Es la familia de «una celda que sobrevive a su
#    mutante mide cero y se lee como cobertura».
#
#    Los testigos son hermeticos a proposito: llaman a `revalidar` con una fila fabricada a la que
#    le falta la clave. Cada uno mata UNA inversion, y por eso son dos y no uno.
if python3 - "$GUION" <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("v", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
mio = {"server_ref": "mcp.github", "transport": "stdio",
       "secret_refs": [{"name": "T", "ref_kind": "env", "ref": "T"}]}
fallos = []
# (a) ausencia de PRIMER NIVEL: falta `transport`
ok, malas = m.revalidar({"server_ref": "mcp.github",
                         "secret_refs": [{"name": "T", "ref_kind": "env", "ref": "T"}]}, mio)
if ok or not any("transport" in x for x in malas):
    fallos.append(f"clave ausente de primer nivel no se señalo: ok={ok} malas={malas}")
# (b) ausencia ANIDADA: falta `ref` dentro de secret_refs[0]
ok, malas = m.revalidar({"server_ref": "mcp.github", "transport": "stdio",
                         "secret_refs": [{"name": "T", "ref_kind": "env"}]}, mio)
if ok or not any("secret_refs[0].ref" in x for x in malas):
    fallos.append(f"clave ausente ANIDADA no se señalo: ok={ok} malas={malas}")
# (c) no-disparo: la fila completa revalida limpia
ok, malas = m.revalidar(dict(mio), mio)
if not ok:
    fallos.append(f"una fila IDENTICA se marco discrepante: {malas}")
if fallos:
    print(fallos)
    sys.exit(1)
sys.exit(0)
PY
then
	paso "ausencia de primer nivel Y anidada se señalan nombrando su ruta; la fila completa pasa"
else
	malo "la rama fail-closed no señala una clave ausente: la revalidacion vuelve a ser fail-open"
fi

# ── 3-quinquies · los DOS MUTANTES DE AUSENCIA, muertos por SU MENSAJE ────────────────────────
# ⛔ TRES COSAS QUE ESTE CASO TUVO MAL, Y LAS TRES LAS ENCONTRO UN LECTOR O SU PROPIA SALIDA.
#
#  1 · EL MUTANTE FABRICABA UN CRASH, NO LA REGRESION. `if False:` hacia caer el flujo en
#      `compara(v, suyo[k], …)` con la clave ausente ⇒ **KeyError: 'ref'**, y mi testigo daba eso
#      por «muerto». Un mutante que revienta no acredita nada: no reproduce lo que se quiere cazar
#      y tapa la diferencia entre «no detecto» y «se rompio». La regresion REAL era `continue`
#      —fail-open— que es literalmente lo que el codigo hacia antes de la cura.
#  2 · SE ACREDITABA POR rc, no por mensaje. Un no-cero cualquiera valia.
#  3 · Y la excepcion no se capturaba, asi que un fallo del BANCO se leia como exito del caso.
#
# Ahora el testigo devuelve TRES estados —senyalado / no-senyalado / REVENTO— y cada uno se juzga
# distinto: reventar es un error DEL BANCO y sale rojo diciendolo.
estado_ausencia() {   # <fichero.py> <superior|anidada> -> imprime senyalado|silencio|EXCEPCION:<tipo>
	python3 - "$1" "$2" <<'PY'
import importlib.util, sys
s = importlib.util.spec_from_file_location("v", sys.argv[1])
m = importlib.util.module_from_spec(s)
s.loader.exec_module(m)
mio = {"server_ref": "x", "transport": "stdio",
       "secret_refs": [{"name": "T", "ref_kind": "env", "ref": "T"}]}
filas = {
    "superior": {"server_ref": "x",
                 "secret_refs": [{"name": "T", "ref_kind": "env", "ref": "T"}]},
    "anidada": {"server_ref": "x", "transport": "stdio",
                "secret_refs": [{"name": "T", "ref_kind": "env"}]},
}
espera = {"superior": "transport: la fila persistida NO trae esta clave",
          "anidada": "secret_refs[0].ref: la fila persistida NO trae esta clave"}
try:
    ok, malas = m.revalidar(filas[sys.argv[2]], mio)
except Exception as e:
    print(f"EXCEPCION:{type(e).__name__}")
    sys.exit(0)
if ok:
    print("silencio")
elif any(espera[sys.argv[2]] in x for x in malas):
    print("senyalado")
else:
    print("otro:" + "; ".join(malas)[:60])
PY
}

# control positivo del propio testigo, en sus dos dimensiones
for dim in superior anidada; do
	e="$(estado_ausencia "$GUION" "$dim")"
	if [ "$e" = "senyalado" ]; then
		paso "el guion sano SEÑALA la ausencia $dim con su mensaje exacto"
	else
		malo "el guion sano deberia señalar la ausencia $dim y dio: $e"
	fi
done

# MUTANTE A · la ausencia ANIDADA vuelve a ser fail-open (`continue`, la regresion REAL)
mA="$TRABAJO/mA.py"
python3 - "$GUION" "$mA" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = ('                    malas.append(f"{ruta}.{k}: la fila persistida NO trae esta clave")\n'
         '                    continue\n')
mut = src.replace(viejo, '                    continue\n', 1)
assert mut != src, "el mutante A NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
eA="$(estado_ausencia "$mA" "anidada")"
eA_sup="$(estado_ausencia "$mA" "superior")"
case "$eA" in
	silencio)
		if [ "$eA_sup" = "senyalado" ]; then
			paso "MUTANTE A muere en la dimension ANIDADA (silencio) y NO toca la superior"
		else
			malo "MUTANTE A cambio tambien la dimension superior ($eA_sup): no separa dimensiones"
		fi ;;
	EXCEPCION:*) malo "MUTANTE A REVENTO ($eA): un mutante que se rompe no acredita nada" ;;
	*) malo "MUTANTE A sobrevivio o dio otra cosa: $eA" ;;
esac

# MUTANTE B · la ausencia de PRIMER NIVEL vuelve a ser fail-open
mB="$TRABAJO/mB.py"
python3 - "$GUION" "$mB" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = ('        if k not in fila:\n'
         '            malas.append(f"{k}: la fila persistida NO trae esta clave")\n'
         '            continue\n')
mut = src.replace(viejo, '        if k not in fila:\n            continue\n', 1)
assert mut != src, "el mutante B NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
eB="$(estado_ausencia "$mB" "superior")"
eB_ani="$(estado_ausencia "$mB" "anidada")"
case "$eB" in
	silencio)
		if [ "$eB_ani" = "senyalado" ]; then
			paso "MUTANTE B muere en la dimension SUPERIOR (silencio) y NO toca la anidada"
		else
			malo "MUTANTE B cambio tambien la dimension anidada ($eB_ani): no separa dimensiones"
		fi ;;
	EXCEPCION:*) malo "MUTANTE B REVENTO ($eB): un mutante que se rompe no acredita nada" ;;
	*) malo "MUTANTE B sobrevivio o dio otra cosa: $eB" ;;
esac

# ── 4 · el rc de «no he podido mirar»: un puerto donde no hay nadie ───────────────────────────
# Se elige un puerto libre MIRANDO, no suponiendo: `/proc/net/tcp` con python3, porque el `awk` de
# esta caja es mawk y NO tiene `strtonum` — una sonda con el da la lista VACIA y eso se lee como
# «no hay nada escuchando». Es el fallo que costo dos lecturas falsas el 2026-08-30.
muerto="$(python3 - <<'PY'
ports = set()
for f in ("/proc/net/tcp", "/proc/net/tcp6"):
    try:
        for ln in open(f).read().splitlines()[1:]:
            p = ln.split()
            if len(p) > 3 and p[3] == "0A":
                ports.add(int(p[1].split(":")[1], 16))
    except OSError:
        pass
print(next(p for p in range(29101, 29400) if p not in ports))
PY
)"
r="$(rc_de "$GUION" "http://127.0.0.1:$muerto" tok ten)"
if [ "$r" = "2" ]; then
	paso "motor inalcanzable => rc 2 (no he podido mirar), no 0 ni 1"
else
	malo "motor inalcanzable deberia salir 2 y salio $r"
fi

# ── 5 · MUTANTE: la ceguera se confunde con limpieza ──────────────────────────────────────────
# El defecto mas caro de este repositorio segun el canon (regla 5): tratar «no he podido mirar»
# como «limpio». Este mutante lo comete, y el caso 4 tiene que matarlo.
m3="$TRABAJO/m3.py"
python3 - "$GUION" "$m3" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = "RC_LIMPIO, RC_RECHAZADO, RC_NO_PUDE_MIRAR = 0, 1, 2"
nuevo = "RC_LIMPIO, RC_RECHAZADO, RC_NO_PUDE_MIRAR = 0, 1, 0"
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante 3 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
r="$(rc_de "$m3" "http://127.0.0.1:$muerto" tok ten)"
if [ "$r" = "0" ]; then
	paso "el mutante que aplasta 'no pude mirar' a 'limpio' es DETECTABLE por el caso 4"
else
	malo "el mutante 3 no produjo el 0 que el caso 4 caza (dio $r): el caso 4 no lo distingue"
fi

# ── 6 · el guion declara sus no-viables, y eso no es prosa: es la lista que evita mandar a
#        alguien a intentar lo imposible. Si desaparece, esta fila lo dice.
if python3 - "$GUION" <<'PY'
import sys, re
src = open(sys.argv[1]).read()
m = re.search(r"^NO_VIABLES = \[(.*?)^\]", src, re.S | re.M)
sys.exit(0 if m and "eventing" in m.group(1) else 1)
PY
then
	paso "el guion sigue declarando eventing como NO viable por API"
else
	malo "eventing ya no figura en NO_VIABLES: o se curo el motor, o se perdio el hallazgo"
fi

# ── 7 · OPCIONAL, con motor: la cadena de claude-policy tiene que MORIR sin `revision` ────────
# Es el unico caso que necesita un motor vivo, asi que va detras de una variable. ⛔ Y cuando NO
# corre lo DICE con todas las letras: un caso saltado que se imprime como un `ok` es el defecto que
# esta bateria persigue en otros. El mutante quita `revision` del cuerpo del check-in, que es
# EXACTAMENTE lo que dice el paso 10 del plan; si sobrevive, la cadena no cubre su propio hallazgo.
if [ -n "${OLIVARES_VERIFY_ENGINE:-}" ] && [ -n "${OLIVARES_VERIFY_TOKEN:-}" ] && [ -n "${OLIVARES_VERIFY_TENANT:-}" ]; then
	m4="$TRABAJO/m4.py"
	python3 - "$GUION" "$m4" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '        cuerpo_checkin = {"scope": scope, "revision": revision,'
nuevo = '        cuerpo_checkin = {"scope": scope,'
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante 4 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
	sano="$(rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT")"
	muerto="$(rc_de "$m4" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT")"
	if [ "$sano" = "0" ] && [ "$muerto" = "1" ] && casa_mensaje 'NO llevaba .revision.: la atestacion del sha ni se evaluo'; then
		paso "contra motor vivo: la cadena pasa (0) y su mutante sin revision MUERE nombrando la ausencia de deriva"
	else
		malo "contra motor vivo esperaba sano=0/mutante=1 y salio sano=$sano/mutante=$muerto"
	fi
	# ── 8 · el marcador de `agents` es PORTANTE, y aqui se ve ────────────────────────────
	# ⛔ ESTE CASO EXISTE PORQUE CASI ME COBRO UN CONTROL AJENO. En las otras superficies el
	#    duplicado lo rechaza el SERVIDOR con 409, asi que un mutante que ciegue el marcador no
	#    produce duplicados y el marcador parece portante sin serlo. `/v1/agents` es la excepcion
	#    medida: no deduplica NADA — tres POST con el mismo `external_id` dan tres agentes, y el
	#    cuerpo vacio tambien crea. Aqui, y solo aqui, el marcador ES lo que evita el duplicado, y
	#    la forma de probarlo es CONTANDO FILAS, no leyendo el rc.
	cuenta_agentes() {
		curl -sf -H "Authorization: Bearer $OLIVARES_VERIFY_TOKEN" \
			-H "X-Olivares-Tenant: $OLIVARES_VERIFY_TENANT" \
			"$OLIVARES_VERIFY_ENGINE/v1/agents" 2>/dev/null |
			python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("items") or []))' 2>/dev/null || printf 'NO_PUDE_MIRAR'
	}
	m5="$TRABAJO/m5.py"
	python3 - "$GUION" "$m5" <<'PY'
import sys
src = open(sys.argv[1]).read()
mut = src.replace("        if isinstance(f, dict) and f.get(campo) == valor:",
                  "        if False:  # MUTANTE: el marcador no ve ninguna fila")
assert mut != src, "el mutante 5 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
	antes="$(cuenta_agentes)"
	rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" >/dev/null
	con_marcador="$(cuenta_agentes)"
	rc_de "$m5" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" >/dev/null
	sin_marcador="$(cuenta_agentes)"
	if [ "$antes" = "NO_PUDE_MIRAR" ] || [ "$con_marcador" = "NO_PUDE_MIRAR" ] || [ "$sin_marcador" = "NO_PUDE_MIRAR" ]; then
		malo "no pude contar agentes (antes=$antes con=$con_marcador sin=$sin_marcador): sin conteo no hay veredicto"
	elif [ "$antes" = "$con_marcador" ] && [ "$sin_marcador" -gt "$con_marcador" ]; then
		paso "el marcador de agents es PORTANTE: con el $antes->$con_marcador, sin el ->$sin_marcador"
	else
		malo "el marcador de agents no se comporto como portante (antes=$antes con=$con_marcador sin=$sin_marcador)"
	fi
	# ── 10 · A-06 · la revalidacion tiene que ver DENTRO de los anidados ──────────────────────
	# ⛔ La version anterior solo comparaba escalares de primer nivel, asi que un `secret_refs` con
	#    otro localizador se leia como «revalidado». El mutante cambia SOLO un campo anidado del
	#    payload de `capabilities` —el marcador `server_ref` no se toca—, asi que la fila sigue
	#    casando por marcador y difiere por dentro. Tiene que salir 2.
	m8="$TRABAJO/m8.py"
	python3 - "$GUION" "$m8" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '"ref": "GITHUB_TOKEN",'
nuevo = '"ref": "OTRO_LOCALIZADOR",'
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "el mutante 8 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
	rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" >/dev/null
	r="$(rc_de "$m8" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT")"
	if [ "$r" = "2" ] && casa_mensaje 'secret_refs\[0\]\.ref'; then
		paso "un campo ANIDADO distinto => rc 2 Y LO DICE nombrando secret_refs[0].ref"
	elif [ "$r" = "2" ]; then
		malo "salio 2 pero sin nombrar secret_refs[0].ref: no acredita que mire DENTRO"
	else
		malo "un campo anidado distinto deberia salir 2 y salio $r: revalidacion ciega a anidados"
	fi

	# ── 9 · A-02 · una fila con MI marcador y OTRO cuerpo tiene que salir rc 2 ────────────────
	# ⛔ ES LA MITAD DEL LEDGER QUE NO SE VE SOLA. Que un pase salga 0 con todo «revalidado» no
	#    prueba que la revalidacion COMPARE: lo pasaria igual una que devolviera siempre True. El
	#    mutante cambia el payload de `alerting` (min_severity high -> low) SIN tocar su marcador,
	#    asi que la fila sembrada sigue casando por nombre y difiere en el cuerpo. Tiene que salir
	#    2 —«no puedo afirmar que este payload este verificado»—, nunca 0 ni 1.
	m6="$TRABAJO/m6.py"
	python3 - "$GUION" "$m6" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '"name": "sec-high-to-siem", "destination": "siem", "min_severity": "high"'
nuevo = '"name": "sec-high-to-siem", "destination": "siem", "min_severity": "low"'
mut = src.replace(viejo, nuevo)
assert mut != src, "el mutante 6 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
	# se siembra primero con el guion SANO, para que la fila exista con el cuerpo bueno
	rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" >/dev/null
	r="$(rc_de "$m6" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT")"
	if [ "$r" = "2" ] && casa_mensaje 'MARCADOR IGUAL, CUERPO DISTINTO.*min_severity'; then
		paso "marcador igual y cuerpo distinto => rc 2 Y LO DICE nombrando min_severity"
	elif [ "$r" = "2" ]; then
		malo "salio 2 pero por otra causa: el veredicto no nombra la discrepancia de min_severity"
	else
		malo "marcador igual y cuerpo distinto deberia salir 2 y salio $r"
	fi
	# ── 11 · A-06b · la CADENA de protocol-binding tambien revalida el cuerpo ─────────────────
	# ⛔ Antes devolvia «ya sembrada» comparando SOLO `binding_key`, asi que una spec con mi clave y
	#    otro `peer_authority` salia rc 0 sin que nadie mirase dentro. El mutante cambia el cuerpo
	#    sin tocar el marcador: tiene que salir 2 Y NOMBRAR el campo, no un 2 cualquiera.
	m9="$TRABAJO/m9.py"
	python3 - "$GUION" "$m9" <<'PY'
import sys
src = open(sys.argv[1]).read()
viejo = '"peer_authority": "https://partner.acme.example",'
nuevo = '"peer_authority": "https://otro.acme.example",'
mut = src.replace(viejo, nuevo, 1)
assert mut != src, "el mutante 9 NO se aplico"
open(sys.argv[2], "w").write(mut)
PY
	rc_de "$GUION" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT" >/dev/null
	r="$(rc_de "$m9" "$OLIVARES_VERIFY_ENGINE" "$OLIVARES_VERIFY_TOKEN" "$OLIVARES_VERIFY_TENANT")"
	if [ "$r" = "2" ] && casa_mensaje 'peer_authority'; then
		paso "spec con mi binding_key y otro cuerpo => rc 2 Y LO DICE nombrando peer_authority"
	elif [ "$r" = "2" ]; then
		malo "salio 2 sin nombrar peer_authority: la cadena no acredita que revalide el cuerpo"
	else
		malo "spec con otro cuerpo deberia salir 2 y salio $r"
	fi

else
	printf 'SALTADO  los casos 7 a 11 (contra motor vivo) NO se han corrido:\n'
	printf '         exporta OLIVARES_VERIFY_ENGINE / _TOKEN / _TENANT para ejercerlos.\n'
	printf '         Esto NO es un ok: la cadena de claude-policy, el marcador portante de\n'
	printf '         agents y la revalidacion del ledger quedan SIN CONTROL en este pase.\n'
fi

muta_fichero() { # $1 fuente · $2 destino · $3 viejo · $4 nuevo → rc 0 si quedo construido
	# ⛔ Esta bateria no tenia constructor: cada mutante llevaba su `assert` suelto, y un ancla
	#    movida producia rc 2 que el caso leia como «sobrevivio». Fail-closed y con el fichero
	#    nombrado.
	python3 - "$1" "$2" "$3" "$4" <<'PYMF' || return 1
import sys
fuente, destino, viejo, nuevo = sys.argv[1:5]
src = open(fuente, encoding="utf8").read()
if viejo not in src:
    sys.stderr.write("    ⛔ el ancla no esta en %s: %r\n" % (fuente, viejo[:70]))
    sys.exit(1)
mut = src.replace(viejo, nuevo, 1)
if mut == src:
    sys.stderr.write("    ⛔ el reemplazo no cambio nada\n"); sys.exit(1)
compile(mut, destino, "exec")
open(destino, "w", encoding="utf8").write(mut)
PYMF
	[ -s "$2" ] || return 1
	! cmp -s "$1" "$2" || return 1
	return 0
}

# ── E · LA GUARDA NO SE DESACTIVA ENVOLVIENDO EL VALOR ────────────────────────────────────────
# ⛔ MEDIDO ANTES DE CURAR, sobre la propia funcion, con un valor que no casa ninguna FORMA:
#      {"password": "hunter2"}        -> cortaba
#      {"password": ["hunter2"]}      -> PASABA
#      {"password": {"v": "hunter2"}} -> PASABA
#    La regla de clave exigia `isinstance(v, str)`, asi que una lista o un objeto la saltaban. Y
#    propagar el nombre de la clave NO bastaba: al bajar a un dict anidado, `clave` pasa a ser la
#    INTERNA («v») y el contexto «voy bajo password» se pierde. Hace falta un estado que sobreviva
#    al descenso. Una guarda que una lista desactiva no es una guarda: es una convencion sobre
#    como escribir el payload.
#
#    El caso mide LAS DOS DIRECCIONES: las cuatro formas con secreto tienen que cortarse, y las dos
#    legitimas —incluida la exencion documentada de `ref` con `ref_kind: env`— tienen que pasar. Sin
#    la segunda mitad, «corta todo» seria verde.
juzga_guarda() { # $1 = guion sujeto; imprime «etiqueta<TAB>corta|pasa» por caso
	SUJETO="$1" python3 - <<'PYE'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("v", os.environ["SUJETO"])
v = importlib.util.module_from_spec(spec); sys.modules["v"] = v; spec.loader.exec_module(v)
CASOS = [
    ("directo", {"password": "hunter2-sin-forma"}, True),
    ("lista", {"password": ["hunter2-sin-forma"]}, True),
    ("objeto", {"password": {"v": "hunter2-sin-forma"}}, True),
    ("anidado", {"a": {"password": "hunter2-sin-forma"}}, True),
    ("legitimo-ref", {"secret_refs": [{"ref": "MI_VAR", "ref_kind": "env"}]}, False),
    ("legitimo-nota", {"name": "demo", "note": "hola"}, False),
]
for etq, cuerpo, debe in CASOS:
    try:
        v.guarda_sin_secretos(cuerpo); corto = False
    except v.SecretoEnPayload:
        corto = True
    except Exception as e:
        print("%s\tREVIENTA(%s)" % (etq, type(e).__name__)); continue
    print("%s\t%s" % (etq, "ok" if corto == debe else ("FALTA-CORTAR" if debe else "CORTA-DE-MAS")))
PYE
}
juzga_guarda "$GUION" >"$TRABAJO/guarda.txt" 2>&1 || true
if [ ! -s "$TRABAJO/guarda.txt" ]; then
	malo "NO HE PODIDO MIRAR: el juicio de la guarda salio vacio"
elif command grep -q . <(command awk -F'\t' '$2 != "ok"' "$TRABAJO/guarda.txt"); then
	malo "la guarda de secretos no cubre las seis formas: $(command awk -F'\t' '$2 != "ok" {printf "%s=%s ", $1, $2}' "$TRABAJO/guarda.txt")"
else
	paso "la guarda corta las CUATRO formas con secreto —tambien envuelto en lista y en objeto— y deja pasar las dos legitimas"
fi

# Su mutante: sin el estado que persiste, envolver el valor vuelve a desactivarla.
if muta_fichero "$GUION" "$TRABAJO/mGuarda.py" \
	'        if bajo_secreto and nodo.strip():' \
	'        if False:  # MUTANTE: el contexto de secreto deja de persistir en el descenso'; then
	juzga_guarda "$TRABAJO/mGuarda.py" >"$TRABAJO/guarda-mut.txt" 2>&1 || true
	if ! juicio_valido "$TRABAJO/guarda-mut.txt"; then
		malo "NO HE PODIDO MIRAR, no supervivencia — el mutante del contexto persistente no dejo veredicto legible en $TRABAJO/guarda-mut.txt"
	elif command grep -q 'FALTA-CORTAR' "$TRABAJO/guarda-mut.txt"; then
		paso "sin el estado que persiste, el valor envuelto vuelve a colarse: el caso E acredita esa mitad"
	else
		malo "el mutante del contexto persistente SOBREVIVIO: el caso E no acredita nada"
	fi
else
	malo "NO se pudo construir el mutante del contexto persistente: sin artefacto no hay juicio"
fi

# ── F · UN 403 NO ES «EL ESTATE ESTA VACIO» ───────────────────────────────────────────────────
# ⛔ `resolver_refs` hacia `s, b = motor.pedir(...)` en sus DOS lecturas y **no miraba `s` nunca**:
#    un 403 acababa en `json.loads(b).get("items") or []`, la lista salia vacia, y el guion
#    publicaba «el estate no tiene ni un agente con nombre». Culpaba al SEMBRADO de un fallo de
#    PERMISOS — la clase que la cabecera de este fichero condena. Y un 2xx con cuerpo no-JSON
#    reventaba con `JSONDecodeError`, que sale como rc 1 cuando es rc 2.
#
#    El caso separa las CUATRO situaciones, porque el valor esta en distinguirlas: no-pude-leer,
#    forma-cambiada, cuerpo-ilegible y vacio-de-verdad se veian todas como la ultima.
juzga_refs() { # $1 = guion sujeto; imprime «caso<TAB>veredicto»
	SUJETO="$1" python3 - <<'PYF'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("v", os.environ["SUJETO"])
v = importlib.util.module_from_spec(spec); sys.modules["v"] = v; spec.loader.exec_module(v)


class Doble:
    def __init__(self, st, body):
        self.st, self.body = st, body

    def pedir(self, metodo, ruta, cuerpo=None):
        return self.st, self.body


CASOS = [
    ("403", Doble(403, '{"error":"forbidden"}'), "no he podido leerlo"),
    ("no-json", Doble(200, "<html>oops</html>"), "no es JSON"),
    ("sin-items", Doble(200, '{"otro":1}'), "sin la clave"),
    ("vacio-real", Doble(200, '{"items":[]}'), "ni un agente"),
]
for etq, motor, esperado in CASOS:
    try:
        v.resolver_refs(motor)
        print("%s\tNO-CORTA" % etq)
    except v.NoPudeMirar as e:
        print("%s\t%s" % (etq, "ok" if esperado in str(e) else "MENSAJE-EQUIVOCADO"))
    except Exception as e:
        print("%s\tREVIENTA(%s)" % (etq, type(e).__name__))
PYF
}
juzga_refs "$GUION" >"$TRABAJO/refs.txt" 2>&1 || true
if [ ! -s "$TRABAJO/refs.txt" ]; then
	malo "NO HE PODIDO MIRAR: el juicio de resolver_refs salio vacio"
elif command grep -q . <(command awk -F'\t' '$2 != "ok"' "$TRABAJO/refs.txt"); then
	malo "resolver_refs no separa las cuatro situaciones: $(command awk -F'\t' '$2 != "ok" {printf "%s=%s ", $1, $2}' "$TRABAJO/refs.txt")"
else
	paso "resolver_refs separa 403, cuerpo no-JSON, forma cambiada y vacio de verdad, cada uno con su mensaje"
fi

# Su mutante: sin mirar el status, el 403 se lee como estate vacio.
if muta_fichero "$GUION" "$TRABAJO/mRefs.py" \
	'        if not (200 <= st < 300):' \
	'        if False:  # MUTANTE: el status deja de mirarse'; then
	juzga_refs "$TRABAJO/mRefs.py" >"$TRABAJO/refs-mut.txt" 2>&1 || true
	if ! juicio_valido "$TRABAJO/refs-mut.txt"; then
		malo "NO HE PODIDO MIRAR, no supervivencia — el mutante del status no dejo veredicto legible en $TRABAJO/refs-mut.txt"
	elif command grep -qE '^403\s+(MENSAJE-EQUIVOCADO|REVIENTA)' "$TRABAJO/refs-mut.txt"; then
		paso "sin mirar el status, el 403 deja de nombrarse como tal: el caso F acredita esa lectura"
	else
		malo "el mutante del status SOBREVIVIO: el caso F no acredita nada ($(cat "$TRABAJO/refs-mut.txt" | tr '\n' ' '))"
	fi
else
	malo "NO se pudo construir el mutante del status: sin artefacto no hay juicio"
fi

# ── G · «NO ESTA EN ESTA PAGINA» NO ES «NO ESTA SEMBRADO» ─────────────────────────────────────
# ⛔ `fila_sembrada` pedia la lista SIN `limit` y recorria solo lo que viniera, sin mirar
#    `has_more` —la señal de paginacion de este API, 144 apariciones en el arbol—. Pasado el tamaño
#    de pagina, una fila YA SEMBRADA se leia como ausente y quien llama re-manda el payload:
#    **sembrado duplicado por creer una ausencia que no se ha comprobado**. Y en un estate lleno
#    —que es el estado en el que se toman las capturas— ese es el caso NORMAL, no el raro.
#
#    Las TRES direcciones importan y por eso el caso las mide: presente, ausente-de-verdad y
#    no-puedo-saberlo. Sin la tercera, las otras dos siguen pasando y el agujero sigue abierto.
juzga_paginacion() { # $1 = guion sujeto
	SUJETO="$1" python3 - <<'PYG'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("v", os.environ["SUJETO"])
v = importlib.util.module_from_spec(spec); sys.modules["v"] = v; spec.loader.exec_module(v)


class Doble:
    def __init__(self, body):
        self.body = body

    def pedir(self, metodo, ruta, cuerpo=None):
        return 200, self.body


CASO = {"marcador": ("name", "mi-fila"), "listar": "/v1/x", "bajo": "items"}
PRUEBAS = [
    ("presente", '{"items":[{"name":"mi-fila"}],"has_more":true}', "encontrada"),
    ("ausente-de-verdad", '{"items":[{"name":"otra"}],"has_more":false}', "ausente"),
    ("no-puedo-saberlo", '{"items":[{"name":"otra"}],"has_more":true}', "rc2"),
]
for etq, body, esperado in PRUEBAS:
    try:
        r = v.fila_sembrada(Doble(body), CASO, {})
        got = "encontrada" if r else "ausente"
    except v.NoPudeMirar:
        got = "rc2"
    except Exception as e:
        got = "REVIENTA(%s)" % type(e).__name__
    print("%s\t%s" % (etq, "ok" if got == esperado else "dio-%s" % got))
PYG
}
juzga_paginacion "$GUION" >"$TRABAJO/pag.txt" 2>&1 || true
if [ ! -s "$TRABAJO/pag.txt" ]; then
	malo "NO HE PODIDO MIRAR: el juicio de paginacion salio vacio"
elif command grep -q . <(command awk -F'\t' '$2 != "ok"' "$TRABAJO/pag.txt"); then
	malo "fila_sembrada no separa las tres direcciones: $(command awk -F'\t' '$2 != "ok" {printf "%s=%s ", $1, $2}' "$TRABAJO/pag.txt")"
else
	paso "fila_sembrada separa presente, ausente-de-verdad y no-puedo-saberlo: has_more deja de leerse como ausencia"
fi

# Su mutante: sin mirar `has_more`, «otra pagina» vuelve a leerse como «no sembrado».
if muta_fichero "$GUION" "$TRABAJO/mPag.py" \
	'    if isinstance(j, dict) and j.get("has_more"):' \
	'    if False:  # MUTANTE: has_more deja de mirarse'; then
	juzga_paginacion "$TRABAJO/mPag.py" >"$TRABAJO/pag-mut.txt" 2>&1 || true
	if ! juicio_valido "$TRABAJO/pag-mut.txt"; then
		malo "NO HE PODIDO MIRAR, no supervivencia — el mutante de has_more no dejo veredicto legible en $TRABAJO/pag-mut.txt"
	elif command grep -q 'no-puedo-saberlo	dio-ausente' "$TRABAJO/pag-mut.txt"; then
		paso "sin mirar has_more, «hay mas paginas» se lee como ausente: el caso G acredita esa lectura"
	else
		malo "el mutante de has_more SOBREVIVIO: el caso G no acredita nada ($(tr '\n' ' ' <"$TRABAJO/pag-mut.txt"))"
	fi
else
	malo "NO se pudo construir el mutante de has_more: sin artefacto no hay juicio"
fi

# ── H · UN DIAGNOSTICO NO ELIGE LA CAUSA QUE NO HA MEDIDO ─────────────────────────────────────
# ⛔ `_diagnostico` devolvia UNA causa para un sintoma con varias, y la presentaba como «la causa
#    REAL». MEDIDO sobre el arbol: **86 sitios** emiten `invalid JSON body` y solo **22** usan
#    `DisallowUnknownFields`, asi que «campos desconocidos» acierta como mucho en una cuarta parte
#    — y ni ahi es la unica: el mismo mensaje sale de un JSON malformado, de un tipo que no casa y
#    de un cuerpo que no se pudo leer. Un diagnostico que acierta 1 de cada 4 y suena seguro manda
#    a quien lo lee al sitio equivocado CON CONFIANZA, que es peor que no tenerlo.
juzga_diagnostico() { # $1 = guion sujeto; imprime «prueba<TAB>veredicto»
	SUJETO="$1" python3 - <<'PYI'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("v", os.environ["SUJETO"])
v = importlib.util.module_from_spec(spec); sys.modules["v"] = v; spec.loader.exec_module(v)
d = v._diagnostico("400: invalid JSON body")
print("lista-alternativas\t%s" % ("ok" if ("(2)" in d and "(4)" in d) else "NO"))
print("no-elige-una\t%s" % ("ok" if "no se distingue" in d or "NO se distingue" in d else "NO"))
print("cifra-medida\t%s" % ("ok" if "22 de los 86" in d else "NO"))
print("otro-mensaje-vacio\t%s" % ("ok" if v._diagnostico("otra cosa") == "" else "NO"))
PYI
}
juzga_diagnostico "$GUION" >"$TRABAJO/diag.txt" 2>&1 || true
if [ ! -s "$TRABAJO/diag.txt" ]; then
	malo "NO HE PODIDO MIRAR: el juicio del diagnostico salio vacio"
elif command grep -q . <(command awk -F'\t' '$2 != "ok"' "$TRABAJO/diag.txt"); then
	malo "el diagnostico no cumple: $(command awk -F'\t' '$2 != "ok" {printf "%s ", $1}' "$TRABAJO/diag.txt")"
else
	paso "el diagnostico lista las candidatas, dice que no se distinguen desde aqui y trae su cifra medida"
fi

# Su mutante: si vuelve a devolver UNA causa a secas, el caso muere.
if muta_fichero "$GUION" "$TRABAJO/mDiag.py" \
	'        return ("400 generico. El motor no dice cual de estas es y desde aqui NO se distingue; van "' \
	'        return ("el handler rechaza campos DESCONOCIDOS y no dice cual"  # MUTANTE  ("'; then
	juzga_diagnostico "$TRABAJO/mDiag.py" >"$TRABAJO/diag-mut.txt" 2>&1 || true
	if ! juicio_valido "$TRABAJO/diag-mut.txt"; then
		malo "NO HE PODIDO MIRAR, no supervivencia — el mutante de la causa unica no dejo veredicto legible en $TRABAJO/diag-mut.txt"
	elif command grep -q . <(command awk -F'\t' '$2 != "ok"' "$TRABAJO/diag-mut.txt"); then
		paso "volviendo a una sola causa, el caso H lo caza"
	else
		malo "el mutante de la causa unica SOBREVIVIO: el caso H no acredita nada"
	fi
else
	malo "NO se pudo construir el mutante del diagnostico: sin artefacto no hay juicio"
fi

# ── I · LO QUE UNA GUARDA LOCAL CORTA NO SE ANOTA COMO «EJERCIDO» ─────────────────────────────
# ⛔ El ledger define «ejercido» como «se mando el payload y el motor lo juzgo», y DOS ramas lo
#    anotaban sobre payloads que las guardas LOCALES cortaron — que nunca salieron de esta maquina.
#    Certificar una llamada que no se hizo, la misma clase que la cadena de consentimiento.
#    `cortado` es terminal —el corte fue deliberado y correcto— pero se cuenta APARTE.
sin_ejercido=""
for pat in 'guarda de secretos' 'guarda de ambito'; do
	if command grep -q "anota(ident, \"ejercido\", f\"cortado por la $pat" "$GUION"; then
		sin_ejercido="$sin_ejercido $pat"
	fi
done
if [ -n "$sin_ejercido" ]; then
	malo "sigue anotandose «ejercido» lo que corta una guarda local:$sin_ejercido"
elif ! command grep -q 'cortados = sum(1 for e in estado.values() if e == "cortado")' "$GUION"; then
	malo "las guardas ya no anotan «ejercido» pero NADIE cuenta los «cortado»: desaparecen del resumen"
elif ! command grep -q 'cortados} cortados' "$GUION"; then
	malo "los «cortado» se cuentan y NO se imprimen: un estado que no sale en el LEDGER no informa"
else
	paso "lo que corta una guarda local se anota «cortado», se cuenta aparte y sale en el LEDGER"
fi

# ── J · LA CLAVE DE SECRETO SE RECONOCE POR RAIZ, NO POR IGUALDAD EXACTA ──────────────────────
# ⛔ MEDIDO ANTES DE CURAR: la lista tenia 17 nombres exactos y de DOCE nombres realistas pasaban
#    SIETE limpios — `secret_key`, `secretKey`, `api_secret`, `signing_secret`, `auth_key`,
#    `webhook_token`, `db_password`. Justo las formas que un operador escribe: cubria `password`
#    pero no `db_password`, y `clientsecret` pero no `api_secret`. Una lista cerrada de nombres
#    exactos para un espacio ABIERTO de nombres no es una guarda: es una apuesta sobre como va a
#    llamar alguien a su campo.
#
#    El caso mide LAS DOS direcciones, y la segunda es la que impide «cortar todo»: la exencion
#    `secret_refs` es el contenedor de LOCALIZADORES, el patron que este guion existe para enseñar.
juzga_claves() { # $1 = guion sujeto
	SUJETO="$1" python3 - <<'PYJ'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("v", os.environ["SUJETO"])
v = importlib.util.module_from_spec(spec); sys.modules["v"] = v; spec.loader.exec_module(v)
DEBEN_CORTAR = ["secret_key", "secretKey", "api_secret", "signing_secret", "auth_key",
                "webhook_token", "db_password", "client_secret", "password", "token"]
DEBEN_PASAR = ["secret_refs", "name", "note", "scope", "external_id"]
for k in DEBEN_CORTAR:
    print("%s\t%s" % (k, "ok" if v.es_clave_de_secreto(k) else "PASA"))
for k in DEBEN_PASAR:
    print("%s\t%s" % (k, "CORTA-DE-MAS" if v.es_clave_de_secreto(k) else "ok"))
PYJ
}
juzga_claves "$GUION" >"$TRABAJO/claves.txt" 2>&1 || true
if [ ! -s "$TRABAJO/claves.txt" ]; then
	malo "NO HE PODIDO MIRAR: el juicio de claves salio vacio"
elif command grep -q . <(command awk -F'\t' '$2 != "ok"' "$TRABAJO/claves.txt"); then
	malo "la guarda no reconoce por raiz: $(command awk -F'\t' '$2 != "ok" {printf "%s=%s ", $1, $2}' "$TRABAJO/claves.txt")"
else
	paso "la clave de secreto se reconoce por RAIZ en las diez formas realistas, y secret_refs sigue pasando"
fi

# Su mutante: volviendo a la igualdad exacta, las siete formas de operador se cuelan.
if muta_fichero "$GUION" "$TRABAJO/mClaves.py" \
	'    return any(r in norm for r in RAICES_DE_SECRETO)' \
	'    return norm in RAICES_DE_SECRETO  # MUTANTE: vuelve a la igualdad exacta'; then
	juzga_claves "$TRABAJO/mClaves.py" >"$TRABAJO/claves-mut.txt" 2>&1 || true
	n=$(command awk -F'\t' '$2 == "PASA"' "$TRABAJO/claves-mut.txt" | wc -l)
	if [ "$n" -ge 5 ]; then
		paso "con igualdad exacta se cuelan $n de las diez formas: el caso J acredita la raiz"
	else
		malo "el mutante de la igualdad exacta solo dejo pasar $n: el caso J no acredita la raiz"
	fi
else
	malo "NO se pudo construir el mutante de la igualdad exacta: sin artefacto no hay juicio"
fi

# ── C · EL CONSENTIMIENTO SE EJERCE, NO SE HEREDA DE LA PRIMERA FILA ──────────────────────────
# ⛔ `cadena_redteam_consent` juzgaba sobre `filas[0]` de una lista que es DEL TENANT y viene
#    PAGINADA: esa primera fila no tiene por que ser la que este guion siembra, y puede ser de otro
#    carril. Si estaba ya autorizada, la cadena devolvia LIMPIO **sin mandar un solo POST**, y el
#    ledger lo anotaba como «ejercido» — que el propio ledger define como «se mando el payload y el
#    motor lo juzgo». Certificar una llamada que no se hizo.
#
#    El doble sirve una lista donde la PRIMERA ya esta autorizada y la SEGUNDA no, y APUNTA a que
#    ruta recibe el POST: el caso no mira solo el rc, mira que el POST haya salido y contra CUAL.
cat > "$TRABAJO/doble-consent.py" <<'PYC'
import http.server, importlib.util, json, os, subprocess, sys, threading

POSTS = []
YA = {"id": "ajeno-de-otro-carril", "name": "ajeno", "authorized": True}
MIO = {"id": "mio-sin-autorizar", "name": "mio", "authorized": False}


class H(http.server.BaseHTTPRequestHandler):
    def _json(self, obj, cod=200):
        c = json.dumps(obj).encode()
        self.send_response(cod)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(c)))
        self.end_headers(); self.wfile.write(c)

    def do_GET(self):
        if self.path.startswith("/v1/m/redteam/targets"):
            # La YA AUTORIZADA va la PRIMERA a proposito: es la trampa.
            self._json({"items": [YA, MIO]})
        else:
            self._json({"items": []})

    def do_POST(self):
        POSTS.append(self.path)
        n = int(self.headers.get("Content-Length") or 0)
        self.rfile.read(n)
        self._json({"authorized": True, "authorized_by": "banco@olivares.ai"})

    def log_message(self, *a):
        pass


srv = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=srv.serve_forever, daemon=True).start()
base = "http://127.0.0.1:%d" % srv.server_port
spec = importlib.util.spec_from_file_location("v", sys.argv[1])
v = importlib.util.module_from_spec(spec); sys.modules["v"] = v; spec.loader.exec_module(v)
motor = v.Motor(base, "tok", "ten") if hasattr(v, "Motor") else None
if motor is None:
    print("SINMOTOR"); raise SystemExit(0)
rc, msg = v.cadena_redteam_consent(motor, {})
srv.shutdown()
print("rc=%s posts=%s msg=%s" % (rc, POSTS, msg[:90]))
PYC
if ! command grep -q 'class Motor' "$GUION"; then
	malo "NO HE PODIDO MIRAR: no encuentro la clase Motor en el guion; el doble no puede construirse"
else
	salida="$(python3 "$TRABAJO/doble-consent.py" "$GUION" 2>&1)"
	if command grep -q 'SINMOTOR' <<<"$salida"; then
		malo "NO HE PODIDO MIRAR: el doble no pudo instanciar el Motor del guion"
	elif command grep -q "posts=\['/v1/m/redteam/targets/mio-sin-autorizar/authorize'\]" <<<"$salida"; then
		paso "el consentimiento se ejerce contra la fila SIN autorizar, no se hereda de la primera: sale un POST y va a la correcta"
	elif command grep -q 'posts=\[\]' <<<"$salida"; then
		malo "la cadena devolvio sin mandar NINGUN POST y la primera fila era ajena: certifica lo que no hizo ($salida)"
	else
		malo "el POST fue a una ruta inesperada: $salida"
	fi
fi

# Su mutante: si vuelve a juzgar sobre `filas[0]` y a devolver limpio cuando ya esta autorizada,
# el caso C tiene que morir — sin POST y con la primera fila ajena.
python3 - "$GUION" "$TRABAJO/mConsent.py" <<'PYM'
import ast, sys
src = open(sys.argv[1], encoding="utf8").read()
viejo = "    sin_autorizar = [f for f in filas if not f.get(\"authorized\")]"
if viejo not in src:
    sys.stderr.write("    el guion ya no elige por `sin_autorizar`: el mutante no se puede construir\n")
    sys.exit(1)
i = src.index(viejo)
j = src.index("    objetivo = sin_autorizar[0]", i) + len("    objetivo = sin_autorizar[0]")
mut = src[:i] + ("    objetivo = filas[0]  # MUTANTE: vuelve a la primera fila\n"
                 "    if objetivo.get(\"authorized\"):\n"
                 "        return RC_LIMPIO, \"ya autorizado; no lo repito\"") + src[j:]
ast.parse(mut)
open(sys.argv[2], "w", encoding="utf8").write(mut)
PYM
if [ -s "$TRABAJO/mConsent.py" ] && ! cmp -s "$GUION" "$TRABAJO/mConsent.py"; then
	salm="$(python3 "$TRABAJO/doble-consent.py" "$TRABAJO/mConsent.py" 2>&1)"
	if command grep -q 'posts=\[\]' <<<"$salm"; then
		paso "volviendo a filas[0] la cadena certifica SIN mandar POST: el caso C acredita esa eleccion"
	else
		malo "el mutante de filas[0] SOBREVIVIO: el caso C no acredita como se elige la fila ($salm)"
	fi
else
	malo "NO se pudo construir el mutante de filas[0]: sin artefacto no hay juicio"
fi

# ── D · LA URL BASE NO SALE LITERAL POR NINGUNA DE LAS DOS SALIDAS ────────────────────────────
# ⛔ ESTE GUION NO TENIA NINGUNA REDACCION, y era el unico de los cuatro que faltaba. La PRIMERA
#    linea del informe imprimia `args.base_url` verbatim —y es justo la linea que se pega en un
#    buzon o en un PR— y varias excepciones se concatenaban con `{e}`. Se prueba con una credencial
#    ARBITRARIA que el guion no puede conocer: si solo taparan los valores recordados, esto fugaria.
SEC_D='sk-CREDENCIAL-ARBITRARIA-DEL-BANCO-99'
fuga_url() { # $1 = guion sujeto; rc 0 = TAPADO
	local out
	# ⛔ LA URL VA MALFORMADA A PROPOSITO, y me costo dos intentos verlo. Con un host inalcanzable el
	#    mensaje es «URLError: <urlopen error Name or service not known>» y NO CONTIENE la URL: el
	#    caso pasaba por una razon trivial, midiendo nada. Con `://` el `Request` lanza
	#    `ValueError: unknown url type: '<la URL ENTERA>'`, que es el camino por el que la
	#    credencial sale de verdad. Medido: asi el mutante fuga en la linea 18 y el sujeto tapa.
	out="$(python3 "$1" "://usuario:$SEC_D@host/" tok ten 2>&1)"
	command grep -qF "$SEC_D" <<<"$out" && return 1
	return 0
}
if fuga_url "$GUION"; then
	paso "una credencial arbitraria en la URL base no sale por stdout ni por stderr"
else
	malo "la URL base con credencial sale literal: la frontera de salida no cubre este guion"
fi

# Su mutante: si la primera linea del informe vuelve a `print` crudo, fuga.
# ⛔ EL MUTANTE SE ANCLA AL CAMINO QUE ESTE CASO RECORRE DE VERDAD, y lo corrijo aqui porque me
#    salio mal a la primera: mutaba la linea del informe, que con un motor inalcanzable NUNCA se
#    ejecuta —la corrida muere antes, en `/healthz`—, asi que el mutante «no fugaba» y el caso lo
#    contaba en rojo sin que la cura tuviera nada que ver. Un mutante solo acredita la pata que
#    NOMBRA si el caso pasa por esa linea. La del informe queda curada y sin mutante propio, y lo
#    digo en vez de fingir que la cubro: ejercitarla exige un motor vivo.
python3 - "$GUION" "$TRABAJO/mRed.py" <<'PYD'
import ast, sys
src = open(sys.argv[1], encoding="utf8").read()
# La linea PORTANTE de este camino es el `raise`, no el `print`: el mensaje ya llega redactado, y
# mutar la impresion no fuga nada. Se muta donde la credencial entra en el texto.
viejo = '            raise NoPudeMirar(redacta(f"{metodo} {ruta}: {type(e).__name__}: {e}")) from e'
if viejo not in src:
    sys.stderr.write("    no encuentro el raise redactado de NoPudeMirar\n"); sys.exit(1)
mut = src.replace(viejo,
                  '            raise NoPudeMirar(f"{metodo} {ruta}: {type(e).__name__}: {e}") from e',
                  1)
mut = mut.replace('        print(redacta(f"verify-seed-payloads: NO HE PODIDO MIRAR: {e}"), file=sys.stderr)',
                  '        print(f"verify-seed-payloads: NO HE PODIDO MIRAR: {e}", file=sys.stderr)', 1)
mut = mut.replace('    instala_excepthook(redacta, "verify-seed-payloads")',
                  '    pass  # MUTANTE: sin frontera para excepciones', 1)
ast.parse(mut)
open(sys.argv[2], "w", encoding="utf8").write(mut)
PYD
if [ -s "$TRABAJO/mRed.py" ] && ! cmp -s "$GUION" "$TRABAJO/mRed.py"; then
	if ! fuga_url "$TRABAJO/mRed.py"; then
		paso "sin la frontera en la linea del informe, la credencial FUGA: el caso D acredita la redaccion"
	else
		malo "el mutante que quita la frontera no fuga: el caso D no acredita nada"
	fi
else
	malo "NO se pudo construir el mutante de la frontera: sin artefacto no hay juicio"
fi

printf '\ntest-verify-seed-payloads: %d pasan, %d fallan\n' "$ok" "$fail"
[ "$fail" -eq 0 ] || exit 1
exit 0
