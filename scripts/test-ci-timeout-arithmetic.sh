#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bateria de `check-ci-timeout-arithmetic.sh`: limpio / hallazgo / NO PUDE MIRAR, y un CONTROL
# NEGATIVO sobre el arbol REAL — bajar el techo de un job por debajo de la suma de sus pasos tiene
# que volver a salir rojo. Sin ese caso, un comprobador que dijera CLEAN siempre pasaria la
# bateria entera.
set -uo pipefail

RAIZ="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GATE="$RAIZ/scripts/check-ci-timeout-arithmetic.sh"
AYUDA="$RAIZ/scripts/ci-timeouts.py"
fallos=0
skips=0
ok() { printf '  ok    %s\n' "$1"; }
mal() { printf '  FAIL  %s — %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

[ -r "$GATE" ] || { echo "test-ci-timeout-arithmetic: 2 NO PUDE MIRAR — sin $GATE" >&2; exit 2; }
[ -r "$AYUDA" ] || { echo "test-ci-timeout-arithmetic: 2 NO PUDE MIRAR — sin $AYUDA" >&2; exit 2; }

# corre_fixture <contenido del workflow> -> imprime el rc
#
# ⛔ CADA CASO CORRE POR LAS DOS VIAS y tiene que dar el MISMO veredicto: la de PyYAML y la de
# repuesto (`OLIVARES_CI_TIMEOUTS_NO_YAML=1`). Si discrepan imprime `DISCREPA:<con>/<sin>`, que
# ningun caso espera, asi que el caso falla CON los dos codigos a la vista en vez de aprobar por
# la via que casualmente corre en esta caja.
#
# Por que importa: en una caja SIN PyYAML las dos vias son la misma y esto no prueba nada extra;
# en una que SI la tenga —la del integrador, y los runners— es el UNICO sitio donde el camino de
# repuesto se contrasta contra el de siempre. Sin esto, el repuesto solo se ejercita justo donde
# no hay con que compararlo, y dos copias de un mismo control acaban discrepando en silencio.
corre_fixture() {
	local caja con sin
	caja="$(mktemp -d)"
	mkdir -p "$caja/scripts" "$caja/.github/workflows"
	cp "$GATE" "$AYUDA" "$caja/scripts/"
	printf '%s\n' "$1" > "$caja/.github/workflows/prueba.yml"
	bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
	con=$?
	OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
	sin=$?
	rm -rf -- "$caja"
	# ⛔ LA INVARIANTE NO ES «LOS DOS IGUALES», Y ESO ERA UN DEFECTO MIO. Exigir igualdad obliga
	# al lector BUENO a cegarse como el de repuesto, y ademas se contradecia con el caso del
	# interruptor de mas abajo, que EXIGE `con=0` y `sin=2` sobre el fixture discriminador. Las dos
	# cosas no pueden ser ciertas a la vez en una caja CON PyYAML, y en una SIN el, `con` y `sin`
	# son el MISMO lector, asi que la contradiccion era invisible aqui: `DISCREPA` resultaba
	# inalcanzable por construccion. Lo encontro el integrador reproduciendo la caja contraria.
	#
	# La propiedad de verdad es de una sola direccion: **la lectura de repuesto puede REHUSAR donde
	# PyYAML lee, pero NUNCA puede ser menos estricta**. Rehusar de mas es recuperable; absolver un
	# hallazgo es el falso verde que este gate existe para no dar.
	if [ "$con" = "$sin" ]; then
		printf '%s' "$con"                       # los dos leyeron igual
	elif [ "$sin" = "2" ]; then
		printf '%s' "$con"                       # la plana REHUSA donde PyYAML lee: mas conservadora, correcto
	else
		printf 'DISCREPA:%s/%s' "$con" "$sin"    # cualquier otra combinacion, incluida sin=0 con con=1
	fi
}

echo "test-ci-timeout-arithmetic: el techo de un job contra la suma de sus pasos"

rc="$(corre_fixture 'on: push
jobs:
  malo:
    timeout-minutes: 30
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20')"
[ "$rc" = "1" ] && ok "techo 30 bajo una suma de 40: HALLAZGO (1)" || mal "techo por debajo" "rc=$rc, esperaba 1"

rc="$(corre_fixture 'on: push
jobs:
  bueno:
    timeout-minutes: 50
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20')"
[ "$rc" = "0" ] && ok "techo 50 sobre una suma de 40: limpio (0)" || mal "techo por encima" "rc=$rc, esperaba 0"

# La FRONTERA exacta. Un techo IGUAL a la suma no deja ni un segundo para checkout ni setup, asi
# que cuenta como hallazgo: `<=`, no `<`. Sin este caso, un `<` se colaria.
rc="$(corre_fixture 'on: push
jobs:
  justo:
    timeout-minutes: 40
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20')"
[ "$rc" = "1" ] && ok "techo IGUAL a la suma: HALLAZGO, la frontera es <= y no <" || mal "frontera" "rc=$rc, esperaba 1"

rc="$(corre_fixture 'on: push
jobs:
  singuardas:
    timeout-minutes: 5
    steps:
      - name: a
      - name: b')"
[ "$rc" = "0" ] && ok "un job sin guardas de paso no es asunto de este gate" || mal "sin guardas" "rc=$rc, esperaba 0"

# ⛔ ESTE CASO APROBABA POR OTRA RAZON, y lo encontro el contraste. Su etiqueta decia «no
# parsea», pero por la via de repuesto NO hay deteccion de error de parseo: el lector plano no
# reconoce nada, no imprime NADA, y el 2 lo pone la guarda de resultado vacio del shell. El rc
# esperado es correcto; el testigo que se afirmaba, no. Se comprueba el MENSAJE por cada via, que
# es lo unico que distingue «no he sabido parsear» de «no habia nada que leer».
caja="$(mktemp -d)"
mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$AYUDA" "$caja/scripts/"
printf '%s\n' 'esto: no es
  - un workflow
    valido: [' > "$caja/.github/workflows/prueba.yml"
salida_con="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc_con=$?
salida_sin="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc_sin=$?
rm -rf -- "$caja"
if [ "$rc_con" = "2" ] && [ "$rc_sin" = "2" ]; then
	case "$salida_sin" in
		*"no devolvio ningun job"*)
			ok "un workflow invalido es 2 por las dos vias — y por la de repuesto lo es porque no reconoce NADA, no porque detecte el error" ;;
		*) mal "no parsea (via de repuesto)" "rc=2 pero por un mensaje inesperado: $(printf '%s' "$salida_sin" | head -1)" ;;
	esac
else
	mal "no parsea" "rc_con=$rc_con rc_sin=$rc_sin, esperaba 2 y 2"
fi

# --- sobre el ARBOL REAL ---------------------------------------------------------------------
bash "$GATE" >/dev/null 2>&1
rc=$?
[ "$rc" = "0" ] && ok "el arbol real esta limpio" || mal "arbol real" "rc=$rc: hay jobs con la cuenta imposible"

# --- CONTROL NEGATIVO sobre el arbol real ----------------------------------------------------
# Se copia el arbol de workflows y se le baja el techo a UN job por debajo de su suma. Tiene que
# salir 1. Si sale 0, este gate no ve el defecto que existe para ver.
caja="$(mktemp -d)"
mkdir -p "$caja/scripts" "$caja/.github"
cp "$GATE" "$AYUDA" "$caja/scripts/"
cp -r "$RAIZ/.github/workflows" "$caja/.github/workflows"
if python3 - "$caja/.github/workflows/mainline-ci.yml" <<'PY'
import sys, io, re
# EL MUTANTE SE DERIVA DEL ARBOL, NO SE CLAVA A UN NUMERO.
#
# ⛔ La primera version buscaba `timeout-minutes: 180` LITERAL, y eso caduco el 2026-08-23 en
# cuanto un lote movio ese valor (control-plane 180->240, race-modules 180->210): ya no quedaba
# ningun 180 en el fichero, el mutante NO SE PLANTABA y el control negativo rehusaba — bien
# rehusado, porque un mutante que no se aplica no prueba nada, pero bloqueando el push que hacia
# el cambio. Es la misma clase que el pin literal de `test-pg-test-env.sh`: un ancla tecleada
# caduca justo cuando cambia lo que vigila.
#
# Ahora se bajan A UNO TODOS los techos de JOB (sangria de cuatro espacios, que es lo que separa
# un techo de job de uno de paso). Cualquier job con guardas de paso pasa a tener el techo por
# debajo de su suma, asi que el gate TIENE que cazarlo — y el mutante se aplica sea cual sea el
# numero que haya manana.
p = sys.argv[1]
s = io.open(p, encoding="utf-8").read()
n, hecho = [], False
for linea in s.split("\n"):
    m = re.match(r"^(    )timeout-minutes:\s*\d+\s*$", linea)
    if m:
        linea, hecho = "%stimeout-minutes: 1" % m.group(1), True
    n.append(linea)
io.open(p, "w", encoding="utf-8").write("\n".join(n))
raise SystemExit(0 if hecho else 1)
PY
then
	bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
	rc=$?
	OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
	rcsin=$?
	# El arbol real lo leen las dos vias, asi que aqui SI se exige el mismo 1 — y ademas se prohibe
	# explicitamente el unico resultado inaceptable: que la plana lo absuelva.
	if [ "$rc" = "1" ] && [ "$rcsin" = "1" ]; then
		ok "CONTROL NEGATIVO: bajando un techo a 1, el gate lo caza por las dos vias"
	elif [ "$rcsin" = "0" ]; then
		mal "CONTROL NEGATIVO" "la lectura de repuesto ABSUELVE un defecto que la de PyYAML caza (con=$rc sin=$rcsin): falso verde"
	else
		mal "CONTROL NEGATIVO" "rc=$rc (con yaml) / $rcsin (sin yaml), esperaba 1 y 1: el gate no ve el defecto"
	fi
else
	mal "CONTROL NEGATIVO" "no he sabido plantar el mutante: no aplica, y no cuenta como aprobado"
fi
rm -rf -- "$caja"


# --- CONTROL POSITIVO sobre el arbol real: que el gate VEA a sus sujetos -----------------------
# ⛔ Este caso existe porque el veredicto limpio y el gate ciego se escriben casi igual. Si la
# lectura se rompiese de forma que devolviera los jobs pero con las sumas a CERO, el gate diria
# «CLEAN — 0 de 22» y saldria 0: verde, silencioso y falso. Sobre el arbol real este repositorio
# tiene NUEVE jobs con guardas de paso (medido 2026-08-23), asi que un cero aqui es una rotura de
# la lectura, no un arbol limpio — y hay que verlo por las DOS vias.
for modo in por-defecto forzada-plana; do
	if [ "$modo" = "forzada-plana" ]; then
		salida="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$GATE" 2>&1)"
	else
		salida="$(bash "$GATE" 2>&1)"
	fi
	vistos="$(printf '%s' "$salida" | sed -n 's/.*CLEAN — \([0-9][0-9]*\) de .*/\1/p')"
	# ⛔ La etiqueta NO afirma que se haya usado PyYAML: en una caja sin la biblioteca, `por_yaml`
	# devuelve None y la via «por defecto» cae al lector plano igual. Decir «con yaml» ahi seria
	# afirmar de mas, asi que se nombra la VIA INVOCADA y se dice cual se resolvio de verdad.
	if [ "$modo" = "por-defecto" ] && ! python3 -c 'import yaml' >/dev/null 2>&1; then
		_real="(sin PyYAML en esta caja: la via por defecto resuelve al lector plano)"
	else
		_real=""
	fi
	if [ -n "$vistos" ] && [ "$vistos" -ge 1 ] 2>/dev/null; then
		ok "CONTROL POSITIVO (via $modo): el gate ve $vistos job(s) con guardas, no cero $_real"
	else
		mal "CONTROL POSITIVO (via $modo)" "no he podido leer un recuento >=1 en: $(printf '%s' "$salida" | head -1)"
	fi
done

# --- el ayudante ausente es NO PUDE MIRAR, no CLEAN -------------------------------------------
# La lectura de los workflows vive en un fichero aparte. Si falta, el gate no tiene con que mirar,
# y eso es 2. Sin este caso, borrar el ayudante convertiria el gate en verde permanente.
caja="$(mktemp -d)"
mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$caja/scripts/"
cp "$RAIZ/.github/workflows/mainline-ci.yml" "$caja/.github/workflows/"
salida="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"
rc=$?
rm -rf -- "$caja"
# ⛔ Se comprueba el MENSAJE y no solo el rc. Hay DOS guardas capaces de devolver 2 aqui — la
# explicita (`[ -f "$AYUDANTE" ]`) y el `||` que recoge la muerte de python3 — y un caso que solo
# mire el rc mide la que dispare SEGUNDA: retirar la primera lo dejaba VERDE. Medido con un
# mutante el 2026-08-23, que sobrevivio por esto exactamente.
case "$rc/$salida" in
	2/*"NO PUDE MIRAR — sin "*)
		ok "sin el ayudante, NO PUDE MIRAR (2) por la guarda explicita, y no CLEAN" ;;
	2/*)
		mal "ayudante ausente" "rc=2 pero por otra guarda: $(printf '%s' "$salida" | head -1)" ;;
	*)
		mal "ayudante ausente" "rc=$rc, esperaba 2" ;;
esac

# --- la lectura de repuesto REHUSA una forma que no sabe leer ---------------------------------
# El camino sin PyYAML es una lectura conservadora, no un parser. Ante una forma desconocida su
# contrato es NOPUEDO -> 2, nunca adivinar. Aqui el `timeout-minutes` cuelga de una clave anidada
# que la lectura plana no sabe atribuir, asi que tiene que rehusar. Este caso solo dice algo del
# camino de repuesto, asi que se corre SOLO por esa via.
caja="$(mktemp -d)"
mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$AYUDA" "$caja/scripts/"
printf '%s\n' 'on: push
jobs:
  raro:
    timeout-minutes: 30
    strategy:
      matrix:
        timeout-minutes: 7
    steps:
      - name: a
        timeout-minutes: 20' > "$caja/.github/workflows/prueba.yml"
OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
rc=$?
rm -rf -- "$caja"
[ "$rc" = "2" ] && ok "la lectura de repuesto REHUSA una forma que no sabe leer (2)" \
	|| mal "forma desconocida" "rc=$rc, esperaba 2: la lectura plana ha adivinado"


# --- que el interruptor de camino CONMUTE de verdad -------------------------------------------
# ⛔ Sin este caso, `OLIVARES_CI_TIMEOUTS_NO_YAML` podia no hacer NADA y toda la bateria seguia
# verde: en una caja sin PyYAML las dos vias son la misma, asi que un interruptor muerto es
# indistinguible de uno vivo. Medido con un mutante el 2026-08-23, que sobrevivio por esto.
#
# El discriminador es un fixture que las dos lecturas juzgan DISTINTO a proposito: un
# `timeout-minutes` colgando de `strategy.matrix` — PyYAML lo lee y lo ignora (CLEAN, 0), y la
# lectura plana no sabe atribuirlo y REHUSA (2). Si las dos vias coinciden, el interruptor no
# conmuta.
#
# Y donde no hay PyYAML esto NO SE PUEDE MEDIR. Se dice con su propia palabra en vez de contarlo
# como aprobado: un skip que se escribe igual que un ok es un pase silencioso.
disc='on: push
jobs:
  raro:
    timeout-minutes: 30
    strategy:
      matrix:
        timeout-minutes: 7
    steps:
      - name: a
        timeout-minutes: 20'
if python3 -c 'import yaml' >/dev/null 2>&1; then
	caja="$(mktemp -d)"
	mkdir -p "$caja/scripts" "$caja/.github/workflows"
	cp "$GATE" "$AYUDA" "$caja/scripts/"
	printf '%s\n' "$disc" > "$caja/.github/workflows/prueba.yml"
	bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
	con=$?
	OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1
	sin=$?
	rm -rf -- "$caja"
	if [ "$con" = "0" ] && [ "$sin" = "2" ]; then
		ok "el interruptor CONMUTA: con PyYAML 0, forzando la lectura plana 2"
	else
		mal "el interruptor no conmuta" "con=$con sin=$sin, esperaba 0 y 2"
	fi
else
	skips=$((skips + 1))
	printf '  skip  el interruptor CONMUTA: NO MEDIBLE en esta caja, no hay PyYAML con que contrastar\n'
fi


# --- REGRESIONES DEL CONTRASTE (Codex sol max, 2026-08-23) -----------------------------------
# Los dos primeros eran FALSOS VERDES VERIFICADOS antes de la red de atribucion: el arbol llevaba
# el defecto y el gate salia 0. Ningun mutante sobre lineas que el escaner YA reconocia podia
# cazarlos, porque el defecto no estaba en lo que reconocia sino en lo que IGNORABA en silencio.

rc="$(corre_fixture 'jobs:
  bad:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - timeout-minutes: 20
        run: echo hi')"
# La propiedad es «no salga 0», y la cumplen las DOS respuestas legitimas: el lector real LEE la
# clave inline y dice 1 (techo 10 contra suma 20 ES un hallazgo, y es el veredicto correcto); el
# de repuesto no sabe leer esa forma y rehusa con 2. Anclar a `2` exigia que el lector bueno se
# equivocase igual que el otro.
case "$rc" in
1) ok "REGRESION: una clave de paso en la linea del guion la LEE el lector real — 1, el hallazgo real" ;;
2) ok "REGRESION: una clave de paso en la linea del guion es NO PUDE MIRAR (2), no un CLEAN falso" ;;
*) mal "REGRESION clave inline" "rc=$rc, esperaba 1 (lector real) o 2 (repuesto) — nunca 0, que fue el falso verde medido (10 contra 20 y salia 0)" ;;
esac

rc="$(corre_fixture 'jobs: # un comentario perfectamente valido
  bad:
    timeout-minutes: 10
    steps:
      - name: a
        timeout-minutes: 20')"
[ "$rc" = "1" ] && ok "REGRESION: jobs con comentario inline se LEE, y su defecto sale como HALLAZGO (1)" \
	|| mal "REGRESION jobs comentado" "rc=$rc, esperaba 1 — antes el fichero ENTERO desaparecia en silencio"

# M7 · LA TRANSICION QUE APAGA `en_steps`, que ningun caso ejercitaba: el fixture de forma
# desconocida pone `strategy` ANTES de `steps`, asi que nunca necesita la transicion que dice
# cubrir. Aqui va DESPUES. Si `en_steps` no se apagara, el 40 se atribuiria como guarda de paso y
# el job saldria CLEAN con techo 100.
rc="$(corre_fixture 'jobs:
  odd:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - name: guarded
        timeout-minutes: 20
        run: echo hi
    strategy:
      matrix:
        timeout-minutes: 40')"
# ⛔ EL TECHO BAJA DE 100 A 30, Y ES INSEPARABLE DE RELAJAR LA ASERCION. Con 100 la lectura correcta
# (suma 20) y la que tiene el defecto (20+40=60) salen LAS DOS CLEAN, asi que el unico testigo era
# que el repuesto rehusara: el caso medía la CEGUERA del repuesto, no la transicion. Mientras la
# asercion exigia `2` eso bastaba; en cuanto acepta `0` —y tiene que aceptarlo, o el lector real no
# pasa nunca— el caso queda ciego. Con 30 discrimina: 30 > 20 limpio · 30 <= 60 hallazgo. Medido
# con el mutante que apaga la transicion: con 30 el caso falla (rc=1); con 100 pasa.
# La propiedad es «el 40 NUNCA se atribuye a los pasos», o sea «no salga 1».
case "$rc" in
0) ok "M7: el lector real apaga la transicion — 30 contra una suma de 20, CLEAN, y el 40 no cuenta" ;;
2) ok "M7: un techo bajo otra clave DESPUES de steps hace rehusar al repuesto — la transicion off ya tiene testigo" ;;
*) mal "M7 transicion off" "rc=$rc, esperaba 0 (lector real) o 2 (repuesto) — un 1 seria el 40 atribuido a los pasos" ;;
esac

# MARGEN. Entra en `techo <= suma + margen`, asi que uno NEGATIVO relaja el predicado hasta
# certificar un arbol defectuoso — medido rc=0 sobre un 30-contra-40 real. Y uno no entero
# reventaba con traceback devolviendo 1, que dice «hay un defecto» cuando no se puede ni mirar.
DEFECTUOSO='jobs:
  malo:
    timeout-minutes: 30
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20'
caja="$(mktemp -d)"; mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$AYUDA" "$caja/scripts/"
printf '%s\n' "$DEFECTUOSO" > "$caja/.github/workflows/prueba.yml"
bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1; rc_def=$?
OLIVARES_CI_TIMEOUT_MARGIN=-20 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1; rc_neg=$?
OLIVARES_CI_TIMEOUT_MARGIN=abc bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1; rc_abc=$?
OLIVARES_CI_TIMEOUT_MARGIN=100 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" >/dev/null 2>&1; rc_100=$?
rm -rf -- "$caja"
[ "$rc_def" = "1" ] && [ "$rc_neg" = "2" ] && [ "$rc_abc" = "2" ] && [ "$rc_100" = "1" ] \
	&& ok "MARGEN: defecto 1 · negativo 2 (antes CERTIFICABA el defecto) · no entero 2 (antes 1 con traceback) · valido 1" \
	|| mal "MARGEN" "defecto=$rc_def negativo=$rc_neg noentero=$rc_abc valido=$rc_100, esperaba 1/2/2/1"

# M8 · UNA FILA ILEGIBLE DEL AYUDANTE. Ningun caso inyectaba una, asi que el mutante que cambia
# ese `exit 2` por un `continue` sobrevivia entero: imprimia «NO PUDE MIRAR», seguia, imprimia
# CLEAN y salia 0. Es la direccion exacta del falso verde en el protocolo de filas.
caja="$(mktemp -d)"; mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$caja/scripts/"
# El stub va en PYTHON, no en bash: el gate lo invoca con `python3`, asi que un stub de shell
# muere al parsearse y el caso mediria la guarda de «el ayudante murio» en vez de la del protocolo
# de filas. Lo cazo el propio case por MENSAJE, que es para lo que esta.
printf '%s\n' 'print("JOB\tx.yml\tjob1\t50\t10\t2\t1")' 'print("ROW-ROTA")' > "$caja/scripts/ci-timeouts.py"
printf '%s\n' 'jobs:
  x:
    timeout-minutes: 5
    steps:
      - name: a' > "$caja/.github/workflows/prueba.yml"
salida="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc=$?
rm -rf -- "$caja"
case "$rc/$salida" in
	2/*"fila ilegible"*) ok "M8: una fila ilegible del ayudante corta con 2 y NO sigue hasta imprimir CLEAN" ;;
	2/*) mal "M8 fila ilegible" "rc=2 pero por otra guarda: $(printf '%s' "$salida" | head -1)" ;;
	*) mal "M8 fila ilegible" "rc=$rc, esperaba 2 — el gate sigue leyendo tras una fila que no entiende" ;;
esac

# M9 · EL RECUENTO DE PASOS DEL DIAGNOSTICO. Quitar `pasos += 1` deja los veredictos intactos
# —siguen saliendo 1— y corrompe el numero de pasos SIN GUARDA que el mensaje reporta, que se
# calcula como `pasos - conguarda` y pasaria a ser NEGATIVO. Ningun caso miraba ese numero, asi
# que el mutante sobrevivia entero. Un veredicto correcto con una evidencia falsa es peor que un
# rojo: manda a arreglar lo que no es.
caja="$(mktemp -d)"; mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$AYUDA" "$caja/scripts/"
printf '%s\n' 'jobs:
  malo:
    timeout-minutes: 30
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20
      - name: sin-guarda-uno
      - name: sin-guarda-dos' > "$caja/.github/workflows/prueba.yml"
salida="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc=$?
rm -rf -- "$caja"
# Cuatro pasos, dos con guarda -> el diagnostico tiene que decir exactamente 2 sin guarda.
if [ "$rc" = "1" ] && printf '%s' "$salida" | command grep -q '(+2 paso(s) sin guarda)'; then
	ok "M9: el diagnostico cuenta bien los pasos sin guarda (2 de 4), no solo acierta el veredicto"
else
	mal "M9 recuento de pasos" "rc=$rc y el diagnostico no dice '+2 paso(s) sin guarda': $(printf '%s' "$salida" | command grep 'techo' | tail -1)"
fi

# --- LA LOGICA DEL VEREDICTO, probada por PARES y no por comportamiento --------------------------
# ⛔ ESTE CASO EXISTE PORQUE ESTA BATERIA NO PUEDE PROBARSE A SI MISMA EN UNA CAJA SIN PyYAML.
# Ahi `con` y `sin` son EL MISMO lector, asi que `con != sin` es inalcanzable y toda la comparacion
# de `corre_fixture` queda sin ejercitar — un verificador construido desde la misma suposicion que
# el codigo verifica lo que el codigo CREE. Lo encontro el integrador reproduciendo la caja
# contraria, y por eso la propiedad se prueba aqui con pares SIMULADOS, que no dependen de que la
# biblioteca este o no.
#
# La propiedad es de UNA DIRECCION: la lectura de repuesto puede REHUSAR donde PyYAML lee, pero
# NUNCA puede ser menos estricta. Rehusar de mas es recuperable; absolver un hallazgo no.
_veredicto() {
	if [ "$1" = "$2" ]; then printf '%s' "$1"
	elif [ "$2" = "2" ]; then printf '%s' "$1"
	else printf 'DISCREPA:%s/%s' "$1" "$2"; fi
}
_par() {
	local got; got="$(_veredicto "$1" "$2")"
	[ "$got" = "$3" ] || mal "logica del veredicto" "con=$1 sin=$2 -> $got, esperaba $3"
}
_antes=$fallos
_par 0 0 0             ; _par 1 1 1             ; _par 2 2 2
_par 0 2 0             ; _par 1 2 1
_par 1 0 DISCREPA:1/0  ; _par 0 1 DISCREPA:0/1
_par 2 0 DISCREPA:2/0  ; _par 2 1 DISCREPA:2/1
if [ "$fallos" -eq "$_antes" ]; then
	ok "la logica del veredicto es de UNA direccion: la plana puede rehusar, nunca absolver (9 pares)"
fi

if [ "$fallos" -eq 0 ]; then
	echo "test-ci-timeout-arithmetic: 0 CLEAN — $((19 - skips)) casos, $skips no medible(s) en esta caja"
	exit 0
fi
echo "test-ci-timeout-arithmetic: 1 — $fallos caso(s) mal"
exit 1
