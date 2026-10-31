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
oks=0
ok() { printf '  ok    %s\n' "$1"; oks=$((oks + 1)); }
mal() { printf '  FAIL  %s — %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

[ -r "$GATE" ] || { echo "test-ci-timeout-arithmetic: 2 CANNOT INSPECT — missing $GATE" >&2; exit 2; }
[ -r "$AYUDA" ] || { echo "test-ci-timeout-arithmetic: 2 CANNOT INSPECT — missing $AYUDA" >&2; exit 2; }

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

echo "test-ci-timeout-arithmetic: a job cap versus the sum of its steps"

rc="$(corre_fixture 'on: push
jobs:
  malo:
    timeout-minutes: 30
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20')"
[ "$rc" = "1" ] && ok "cap 30 below sum 40: FINDING (1)" || mal "cap below sum" "rc=$rc, expected 1"

rc="$(corre_fixture 'on: push
jobs:
  bueno:
    timeout-minutes: 50
    steps:
      - name: a
        timeout-minutes: 20
      - name: b
        timeout-minutes: 20')"
[ "$rc" = "0" ] && ok "cap 50 above sum 40: clean (0)" || mal "cap above sum" "rc=$rc, expected 0"

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
[ "$rc" = "1" ] && ok "cap EQUAL to sum: FINDING, boundary is <= rather than <" || mal "boundary" "rc=$rc, expected 1"

rc="$(corre_fixture 'on: push
jobs:
  singuardas:
    timeout-minutes: 5
    steps:
      - name: a
      - name: b')"
[ "$rc" = "0" ] && ok "a job without step guards is outside this gate scope" || mal "without guards" "rc=$rc, expected 0"

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
    valido: 1' > "$caja/.github/workflows/prueba.yml"
yaml_output="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc_con=$?
plain_output="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc_sin=$?
rm -rf -- "$caja"
if [ "$rc_con" = "2" ] && [ "$rc_sin" = "2" ]; then
	case "$plain_output" in
		*"helper returned no jobs"*)
			ok "an invalid workflow returns 2 through both paths — the fallback recognizes NOTHING rather than detecting the error" ;;
		*) mal "cannot parse (fallback path)" "rc=2 with an unexpected message: $(printf '%s' "$plain_output" | head -1)" ;;
	esac
else
	mal "cannot parse" "rc_con=$rc_con rc_sin=$rc_sin, expected 2 and 2"
fi

# --- sobre el ARBOL REAL ---------------------------------------------------------------------
bash "$GATE" >/dev/null 2>&1
rc=$?
[ "$rc" = "0" ] && ok "the real tree is clean" || mal "real tree" "rc=$rc: jobs have impossible timing totals"

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
		ok "NEGATIVE CONTROL: lowering a cap to 1 is detected through both paths"
	elif [ "$rcsin" = "0" ]; then
		mal "NEGATIVE CONTROL" "the fallback APPROVES a defect detected by PyYAML (with=$rc without=$rcsin): false success"
	else
		mal "NEGATIVE CONTROL" "rc=$rc (with yaml) / $rcsin (without yaml), expected 1 and 1: the gate misses the defect"
	fi
else
	mal "NEGATIVE CONTROL" "could not plant the mutant: not applied and does not count as passed"
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
		output="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$GATE" 2>&1)"
	else
		output="$(bash "$GATE" 2>&1)"
	fi
	vistos="$(printf '%s' "$output" | sed -n 's/.*CLEAN — \([0-9][0-9]*\) of .*/\1/p')"
	# ⛔ La etiqueta NO afirma que se haya usado PyYAML: en una caja sin la biblioteca, `por_yaml`
	# devuelve None y la via «por defecto» cae al lector plano igual. Decir «con yaml» ahi seria
	# afirmar de mas, asi que se nombra la VIA INVOCADA y se dice cual se resolvio de verdad.
	if [ "$modo" = "por-defecto" ] && ! python3 -c 'import yaml' >/dev/null 2>&1; then
		_real="(no PyYAML on this machine: the default path uses the plain reader)"
	else
		_real=""
	fi
	if [ -n "$vistos" ] && [ "$vistos" -ge 1 ] 2>/dev/null; then
		ok "POSITIVE CONTROL (path $modo): the gate sees $vistos guarded job(s), not zero $_real"
	else
		mal "POSITIVE CONTROL (path $modo)" "could not read a count >=1 in: $(printf '%s' "$output" | head -1)"
	fi
done

# --- el ayudante ausente es NO PUDE MIRAR, no CLEAN -------------------------------------------
# La lectura de los workflows vive en un fichero aparte. Si falta, el gate no tiene con que mirar,
# y eso es 2. Sin este caso, borrar el ayudante convertiria el gate en verde permanente.
caja="$(mktemp -d)"
mkdir -p "$caja/scripts" "$caja/.github/workflows"
cp "$GATE" "$caja/scripts/"
cp "$RAIZ/.github/workflows/mainline-ci.yml" "$caja/.github/workflows/"
output="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"
rc=$?
rm -rf -- "$caja"
# ⛔ Se comprueba el MENSAJE y no solo el rc. Hay DOS guardas capaces de devolver 2 aqui — la
# explicita (`[ -f "$AYUDANTE" ]`) y el `||` que recoge la muerte de python3 — y un caso que solo
# mire el rc mide la que dispare SEGUNDA: retirar la primera lo dejaba VERDE. Medido con un
# mutante el 2026-08-23, que sobrevivio por esto exactamente.
case "$rc/$output" in
	2/*"COULD NOT CHECK — missing "*)
		ok "missing helper => CANNOT INSPECT (2) through the explicit guard, not CLEAN" ;;
	2/*)
		mal "missing helper" "rc=2 through another guard: $(printf '%s' "$output" | head -1)" ;;
	*)
		mal "missing helper" "rc=$rc, expected 2" ;;
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
[ "$rc" = "2" ] && ok "the fallback REFUSES a form it cannot read (2)" \
	|| mal "unknown form" "rc=$rc, expected 2: the plain reader guessed"

# --- the fallback READS the compact list style (dash at the key's column) ----------------------
# merge-gate-check.yml writes `steps:` with its `- ` at column 4 and the step keys at column 6; the
# fallback used to read only the indented style (dash 6, keys 8) and refused the real tree (#2635).
# These cases force the plain reader and demand its exact verdict: `corre_fixture` accepts a refusal
# where PyYAML reads, so it cannot catch a fallback that stays blind to this style.
plana() {
	local caja out txt
	caja="$(mktemp -d)"
	mkdir -p "$caja/scripts" "$caja/.github/workflows"
	cp "$GATE" "$AYUDA" "$caja/scripts/"
	printf '%s\n' "$1" > "$caja/.github/workflows/prueba.yml"
	txt="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"
	out=$?
	rm -rf -- "$caja"
	if [ "${2:-}" = texto ]; then printf '%s' "$txt"; else printf '%s' "$out"; fi
}

rc="$(plana 'jobs:
  compact:
    runs-on:
    - self-hosted
    timeout-minutes: 50
    steps:
    - uses: actions/checkout@v4
      timeout-minutes: 20
    - name: b
      timeout-minutes: 20
      run: echo hi')"
[ "$rc" = "0" ] && ok "the fallback READS the compact list style: cap 50 above sum 40 is clean (0)" \
	|| mal "compact style clean" "rc=$rc, expected 0"

rc="$(plana 'jobs:
  compact:
    runs-on:
    - self-hosted
    timeout-minutes: 30
    steps:
    - uses: actions/checkout@v4
      timeout-minutes: 20
    - name: b
      timeout-minutes: 20
      run: echo hi')"
[ "$rc" = "1" ] && ok "the fallback READS the compact list style: cap 30 below sum 40 is a FINDING (1)" \
	|| mal "compact style finding" "rc=$rc, expected 1"

rc="$(plana 'jobs:
  compact:
    timeout-minutes: 10
    steps:
    - timeout-minutes: 20
      run: echo hi')"
[ "$rc" = "2" ] && ok "the fallback REFUSES a compact step key written on the dash line (2)" \
	|| mal "compact inline key" "rc=$rc, expected 2: the dash-line key was not attributed and the reader did not refuse"

rc="$(plana 'jobs:
  compact:
    timeout-minutes: 30
    steps:
    - name: a
      timeout-minutes: 20
    strategy:
      timeout-minutes: 40')"
[ "$rc" = "2" ] && ok "the fallback REFUSES a column-6 cap outside the steps of a compact job (2)" \
	|| mal "compact cap outside steps" "rc=$rc, expected 2: the 40 was read as a step guard or as nothing"

rc="$(plana 'jobs:
  deep:
    timeout-minutes: 50
    steps:
        - name: a
          timeout-minutes: 20')"
[ "$rc" = "2" ] && ok "the fallback REFUSES a steps list whose dash is at neither known column (2)" \
	|| mal "unknown list column" "rc=$rc, expected 2: the reader accepted a list style it was never measured on"

# A quoted key after the steps is a job key the reader does not name, so it must still END the
# steps: the 5 below is not a step guard (PyYAML sums 40, a reader that kept going sums 45).
rc="$(plana 'jobs:
  quoted:
    timeout-minutes: 30
    steps:
    - name: x
      timeout-minutes: 20
    - name: y
      timeout-minutes: 20
    "strategy":
      timeout-minutes: 5')"
[ "$rc" = "2" ] && ok "the fallback REFUSES a column-6 cap under a quoted job key after compact steps (2)" \
	|| mal "compact quoted key" "rc=$rc, expected 2: the 5 was added to the steps (30 against 45 is a finding PyYAML does not see)"

rc="$(plana 'jobs:
  quoted:
    timeout-minutes: 30
    steps:
      - name: x
        timeout-minutes: 20
      - name: y
        timeout-minutes: 20
    "strategy":
      matrix:
        timeout-minutes: 5')"
[ "$rc" = "2" ] && ok "the fallback REFUSES a column-8 cap under a quoted job key after indented steps (2)" \
	|| mal "indented quoted key" "rc=$rc, expected 2: the 5 was added to the steps"

# The guard sits at the dash column + 2 and nowhere else: a cap nested under a step key (column 8)
# in a compact job is not a step guard, and a reader that took 6 or 8 would sum 104 against 5.
rc="$(plana 'jobs:
  nested:
    timeout-minutes: 50
    steps:
    - name: a
      timeout-minutes: 5
      with:
        timeout-minutes: 99')"
[ "$rc" = "2" ] && ok "the fallback REFUSES a column-8 cap nested under a compact step (2)" \
	|| mal "compact nested cap" "rc=$rc, expected 2: the 99 was read as a step guard"

# Mixed styles in one file, both orders: the dash column is read again for every steps list.
txt="$(plana 'jobs:
  compacto:
    timeout-minutes: 30
    steps:
    - name: a
      timeout-minutes: 20
    - name: b
      timeout-minutes: 20
  indentado:
    timeout-minutes: 25
    steps:
      - name: c
        timeout-minutes: 15
      - name: d
        timeout-minutes: 20' texto)"
case "$txt" in
*compacto*"job limit 30"*"sum of steps 40"*indentado*"job limit 25"*"sum of steps 35"*)
	ok "the fallback reads a compact job then an indented job in one file, with their own sums" ;;
*) mal "mixed styles, compact first" "expected 30/40 then 25/35, got: ${txt//$'\n'/ }" ;;
esac

txt="$(plana 'jobs:
  indentado:
    timeout-minutes: 25
    steps:
      - name: c
        timeout-minutes: 15
      - name: d
        timeout-minutes: 20
  compacto:
    timeout-minutes: 30
    steps:
    - name: a
      timeout-minutes: 20
    - name: b
      timeout-minutes: 20' texto)"
case "$txt" in
*indentado*"job limit 25"*"sum of steps 35"*compacto*"job limit 30"*"sum of steps 40"*)
	ok "the fallback reads an indented job then a compact job in one file, with their own sums" ;;
*) mal "mixed styles, indented first" "expected 25/35 then 30/40, got: ${txt//$'\n'/ }" ;;
esac

# The diagnostic counts steps too: four steps, two guarded, and lists nested inside a step (one at
# the step-key column, one deeper) are not steps. A reader counting every "- " says 4 unguarded.
txt="$(plana 'jobs:
  contados:
    timeout-minutes: 30
    steps:
    - name: a
      timeout-minutes: 20
      with:
        files:
        - one
    - name: b
      timeout-minutes: 20
      args:
      - one
      - two
    - name: sin-guarda-uno
    - name: sin-guarda-dos' texto)"
case "$txt" in
*"(+2 step(s) without timeouts)"*) ok "the fallback counts the steps of a compact job, not the lists nested in them (2 of 4 unguarded)" ;;
*) mal "compact step count" "the diagnostic does not report '+2 step(s) without timeouts': ${txt//$'\n'/ }" ;;
esac


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
		ok "the switch WORKS: PyYAML returns 0, forced plain reading returns 2"
	else
		mal "the switch does not work" "with=$con without=$sin, expected 0 and 2"
	fi
else
	skips=$((skips + 1))
	printf '  skip  the switch WORKS: UNMEASURABLE on this machine, no PyYAML for comparison\n'
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
1) ok "REGRESSION: the real reader READS an inline step key — 1, the actual finding" ;;
2) ok "REGRESSION: an inline step key means CANNOT INSPECT (2), not false CLEAN" ;;
*) mal "REGRESSION inline key" "rc=$rc, expected 1 (real reader) or 2 (fallback) — never 0, the measured false success (10 versus 20 returned 0)" ;;
esac

rc="$(corre_fixture 'jobs: # un comentario perfectamente valido
  bad:
    timeout-minutes: 10
    steps:
      - name: a
        timeout-minutes: 20')"
[ "$rc" = "1" ] && ok "REGRESSION: jobs with an inline comment are READ, and their defect is reported as FINDING (1)" \
	|| mal "REGRESSION commented jobs" "rc=$rc, expected 1 — previously the ENTIRE file silently disappeared"

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
0) ok "M7: the real reader disables the transition — 30 versus sum 20, CLEAN, and 40 is not counted" ;;
2) ok "M7: a cap under another key AFTER steps makes the fallback refuse — the disabled transition now has a witness" ;;
*) mal "M7 transition off" "rc=$rc, expected 0 (real reader) or 2 (fallback) — 1 would mean 40 was attributed to steps" ;;
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
	&& ok "MARGIN: default 1 · negative 2 (previously CERTIFIED the defect) · noninteger 2 (previously 1 with traceback) · valid 1" \
	|| mal "MARGIN" "default=$rc_def negative=$rc_neg noninteger=$rc_abc valid=$rc_100, expected 1/2/2/1"

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
output="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc=$?
rm -rf -- "$caja"
case "$rc/$output" in
	2/*"unreadable helper row"*) ok "M8: an unreadable helper row blocks with 2 and does NOT continue to print CLEAN" ;;
	2/*) mal "M8 unreadable row" "rc=2 through another guard: $(printf '%s' "$output" | head -1)" ;;
	*) mal "M8 unreadable row" "rc=$rc, expected 2 — the gate continues reading after a row it cannot understand" ;;
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
output="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"; rc=$?
rm -rf -- "$caja"
# Cuatro pasos, dos con guarda -> el diagnostico tiene que decir exactamente 2 sin guarda.
if [ "$rc" = "1" ] && printf '%s' "$output" | command grep -q '(+2 step(s) without timeouts)'; then
	ok "M9: the diagnostic correctly counts unguarded steps (2 of 4), beyond producing the correct verdict"
else
	mal "M9 step count" "rc=$rc and the diagnostic does not report '+2 unguarded step(s)': $(printf '%s' "$output" | command grep 'job limit' | tail -1)"
fi

# --- the plain reader REFUSES the shapes it would otherwise guess ---------------------
# Forced plain path, one fixture per shape, exact verdict AND reason: rc 2 through the guard that
# names the shape, never a row (a row is a guess: shapes 1-4 used to print one, 5 a traceback).
# plana_v <workflow> -> "<rc>/<first line of the gate output>", built on the plana helper above
plana_v() {
	printf '%s/%s' "$(plana "$1")" "$(plana "$1" texto | head -1)"
}
# rehusa <label> <reason substring> <workflow>
rehusa() {
	local got
	got="$(plana_v "$3")"
	case "$got" in
		2/*"$2"*) ok "plain reader refuses $1 (2): $2" ;;
		2/*) mal "$1" "rc=2 through another guard: $got" ;;
		*) mal "$1" "got ${got%%/*}, expected 2 — the plain reader guessed: $got" ;;
	esac
}
GUARDAS='    steps:
      - name: x
        timeout-minutes: 20
      - name: y
        timeout-minutes: 20'
rehusa "a quoted job id" "unsupported job line" 'jobs:
  a:
    timeout-minutes: 30
'"$GUARDAS"'
  "b":
    timeout-minutes: 100
    steps:
      - name: z'
# Any other column-2 spelling of a job id is refused the same way, not only the double-quoted one.
rehusa "a single-quoted job id" "unsupported job line" "jobs:
  a:
    timeout-minutes: 30
$GUARDAS
  'b':
    timeout-minutes: 100"
rehusa "a job id with a space before the colon" "unsupported job line" "jobs:
  a:
    timeout-minutes: 30
$GUARDAS
  b :
    timeout-minutes: 100"
# An explicit `? env` key is not the plain `key:` this reader knows: it used to stay inside job
# `a`, and the cap of `env` replaced a's own (100 against 40 instead of 30). A column-0 line that
# is not a plain mapping key is refused now (the regression review).
rehusa "a column-0 explicit key after jobs" "unsupported top-level line" "jobs:
  a:
    timeout-minutes: 30
$GUARDAS
? env
:
    timeout-minutes: 100"
# A top-level key that does not start with a letter ends `jobs:` too: `1:`, `0x:` and `.x:`
# are plain keys for YAML, and the cap under each one is not job a's (PyYAML reads 30 against 40).
for clave in '1' '0x' '.x'; do
	rehusa "a column-0 key '$clave:' after jobs" "4 mention(s) of timeout-minutes and only 3 attributed" "jobs:
  a:
    timeout-minutes: 30
$GUARDAS
$clave:
    timeout-minutes: 99"
done
rehusa "an anchored job line" "anchor or alias" 'jobs:
  j:
    timeout-minutes: 30
'"$GUARDAS"'
  k: &k
    timeout-minutes: 99
    steps:
      - name: z'
rehusa "an alias reusing a step" "anchor or alias" 'jobs:
  j:
    timeout-minutes: 30
    steps:
      - &s
        name: x
        timeout-minutes: 20
      - *s'
# The anchored node's own timeout is attributed once; every alias to it adds timeouts nobody
# counts, so the anchor is refused wherever it hides (own line, behind a tag) and so is each alias
# position. The alias fixtures anchor OUTSIDE `jobs:` so only the alias can trip them.
rehusa "an anchor on its own line" "anchor or alias" 'jobs:
  a:
    timeout-minutes: 100
    steps:
      -
        &s
        name: x
        timeout-minutes: 20
  b:
    timeout-minutes: 30
    steps: [x, y]'
rehusa "a tagged anchor" "anchor or alias" 'jobs:
  a:
    timeout-minutes: 100
    steps:
      - !!map &s
        name: x
        timeout-minutes: 20'
FUERA='x-step: &s
  name: x
  timeout-minutes: 20
jobs:
  b:
    timeout-minutes: 30
    steps'
rehusa "an alias after a list dash" "anchor or alias" "$FUERA:
      - *s
      - *s"
rehusa "an alias after a key" "anchor or alias" "$FUERA: *s"
rehusa "an alias first in a flow list" "anchor or alias" "$FUERA: [*s]"
rehusa "an alias after a flow comma" "anchor or alias" "$FUERA: [x, *s]"
rehusa "a Unicode line break hiding a job line" "Unicode line breaks" 'jobs:
  a:
    timeout-minutes: 30
'"$GUARDAS"'
  # c'"$(printf '\xe2\x80\xa8')"'  b:
    timeout-minutes: 100'
rehusa "a leading-zero value (YAML 1.1 octal)" "is not a literal" 'jobs:
  j:
    timeout-minutes: 010
    steps:
      - name: x'
rehusa "a non-ASCII digit" "is not a literal" 'jobs:
  j:
    timeout-minutes: ²
    steps:
      - name: x'
# `²` is no Unicode decimal, so even `\d` rejects it; an Arabic-Indic digit parses with int() and
# is what separates `[0-9]` from `\d`, in the first position and in the rest.
rehusa "a leading non-ASCII decimal digit" "is not a literal" 'jobs:
  j:
    timeout-minutes: ٣
    steps:
      - name: x'
rehusa "a trailing non-ASCII decimal digit" "is not a literal" 'jobs:
  j:
    timeout-minutes: 1٣
    steps:
      - name: x'
# A block-scalar indicator counts only at the value position. The same characters inside a trailing
# comment (`: |`, `- >`) are comment text, and treating them as a block switched the anchor check off
# for every deeper line: a false CLEAN where a real YAML reader sums 30 against 40.
rehusa "a block indicator in a job-line comment" "anchor or alias" 'jobs:
  j: # see: |
    timeout-minutes: 30
    steps:
      - &s
        timeout-minutes: 20
        run: echo
      - *s'
rehusa "a block indicator in a steps-line comment" "anchor or alias" 'jobs:
  j:
    timeout-minutes: 30
    steps: # - >
      - &s
        timeout-minutes: 20
        run: echo
      - *s'
# For `- run: |` the block belongs to the key (column 8), not to the dash (column 6): a sibling key
# of the same step mapping is YAML, not script text.
rehusa "a sibling key after a dash-line block scalar" "anchor or alias" 'jobs:
  j:
    timeout-minutes: 30
    steps:
      - run: |
        name: &n x
        timeout-minutes: 20'
# A header-looking line inside a multi-line quoted scalar is string text, not a block header: it
# used to open a fake block, and the alias on the closing line escaped the refusal. A quoted scalar
# that does not close on its line is refused outright, in both quote styles and after a flow `[`.
rehusa "a header-looking line in a multi-line double-quoted scalar" "multi-line quoted scalar" 'env: &d {runs-on: ubuntu-latest}
jobs:
  j:
    timeout-minutes: 5
    x: ["a
      b: |
        ", *d ]
    steps:
      - run: echo hi'
rehusa "a header-looking line in a multi-line single-quoted scalar" "multi-line quoted scalar" "env: &d {runs-on: ubuntu-latest}
jobs:
  j:
    timeout-minutes: 5
    x: ['a
      b: >
        ', *d ]
    steps:
      - run: echo hi"
# A quoted scalar has many spellings of its opener (behind a `!tag`, as a JSON-like key's value with
# or without a space before the colon, after `? `, after `{`, `[` or a comma, with a key that is
# itself a flow collection) and an escaped line break ends a double-quoted line still open. The
# continuation line `b: |` sits left of the key (a PyYAML or js-yaml reader still takes it), opens a
# fake block that hides the alias below, and a real YAML reader sums 30 against 40 where the plain
# one printed a row (a false CLEAN). Each opener is a fixture: <opener line> <text that closes the
# string on the line after `b: |`, its quote included>; every one is valid YAML.
abierta() {
	rehusa "a header-looking line after the quoted opener '${3:-$1}'" "multi-line quoted scalar" "jobs:
  j:
    timeout-minutes: 30
    $1
   b: |
     $2
    steps:
      - &s
        name: x
        timeout-minutes: 20
      - *s"
}
abierta 'x: !!str "a' '"'
abierta "x: !!str 'a" "'"
abierta 'x: {"k":"a' '"}'
abierta "x: {'k':'a" "'}"
abierta '? "a' '"'
abierta 'x: [!!str "a' '"]'
abierta 'x: [1, "a' '"]'
abierta 'x: "a\' '"'
abierta 'x: "a \' '"'
abierta 'x: "a\"' '"'
abierta $'x:\n      "a' '"' 'x:, then "a on its own line'
abierta $'x:\n      - "a' '"' 'x:, then - "a as a list item'
abierta 'x: {? "a' '" : 1}'
abierta 'x: [? "a' '" : 1]'
abierta 'x: {"k" :"a' '"}'
abierta 'x: {[a]:"b' '"}'
abierta 'x: {{a: 1}:"b' '"}'
rehusa "an escaped steps key" "quoted key with a backslash escape" 'jobs:
  a:
    timeout-minutes: 30
    "st\x65ps":
      - name: x
        timeout-minutes: 20'
# A quoted KEY with a backslash escape hides its name from the plain reader: `"timeout\x2dminutes"`
# is `timeout-minutes` to YAML, while the reader finds no literal to count (a CLEAN that summed 0
# where PyYAML sums 40). Every spelling of the escape is refused, at any column and in a flow map.
ESCAPE='quoted key with a backslash escape'
rehusa "an escaped step key, \\x2d" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        "timeout\x2dminutes": 20
      - name: y
        "timeout\x2dminutes": 20'
BS='\'
rehusa "an escaped step key, \\u002d" "$ESCAPE" "jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        \"timeout${BS}u002dminutes\": 20"
rehusa "an escaped step key, \\U0000002d" "$ESCAPE" "jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        \"timeout${BS}U0000002dminutes\": 20"
rehusa "an escaped step key, uppercase hex digits" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        "timeout\x2Dminutes": 20'
rehusa "an escaped step key, a letter spelled with \\U" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        "\U00000074imeout-minutes": 20'
rehusa "an escaped steps key, \\u" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    "\u0073teps":
      - name: x
        timeout-minutes: 20'
rehusa "an escaped key name inside a flow sequence value" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        env: ["timeout\x2dminutes"]
        timeout-minutes: 20'
rehusa "an escaped job ceiling key" "$ESCAPE" 'jobs:
  a:
    "timeout\x2dminutes": 30
    steps:
      - name: x
        timeout-minutes: 20'
rehusa "an escaped step key in a flow mapping" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - {"timeout\x2dminutes": 20, name: x}'
rehusa "an escaped step key after an explicit ?" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        ? "timeout\x2dminutes"
        : 20'
rehusa "an escaped step key after a ? alone on its line" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        ?
          "timeout\x2dminutes"
        : 20'
# A top-level key is never block text, so an escaped `jobs` key is refused even when no line of the
# file spells the literal (the reader would otherwise drop the file and print no row).
rehusa "an escaped top-level jobs key" "$ESCAPE" '"jo\x62s":
  a:
    steps:
      - name: x
        run: true'
# The key can be hidden behind a `?`, a tag, an anchor or a root flow map; each one is refused. A
# `?` line, a root flow map and a key split over lines stop at the top-level line guard first.
TOPLINEA='unsupported top-level line'
rehusa "an escaped top-level jobs key after ?" "$TOPLINEA" '? "jo\x62s"
:
  a:
    steps:
      - name: x
        run: true'
rehusa "an escaped top-level jobs key behind a tag" "$ESCAPE" '!!map "jo\x62s":
  a:
    steps:
      - name: x
        run: true'
rehusa "an escaped top-level jobs key in a root flow map" "$TOPLINEA" '{"jo\x62s": {a: {steps: [{name: x}]}}}'
rehusa "an escaped key after ? alone, tag on the next line" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        ?
          !!str "timeout\x2dminutes"
        : 20'
rehusa "an escaped key after a ? with a comment" "$ESCAPE" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        ? # c
          "timeout\x2dminutes"
        : 20'
rehusa "a top-level jobs key split over two lines" "$TOPLINEA" '"jo\
bs":
  a:
    steps:
      - name: x
        run: true'
# Not refused: a single-quoted key is a literal backslash in YAML (no escape, no timeout key), and a
# column-0 value with a backslash is not a key. Both must be read, with the step guard summed: a
# step of 40 under a ceiling of 30 is rc 1 only when the reader reads the step line.
ctl2="$(plana 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        '"'"'timeout\x2dminutes'"'"': 20
        timeout-minutes: 40')"
[ "$ctl2" = 1 ] && ok "a single-quoted key with a backslash is a literal, read and summed (1)" \
	|| mal "single-quoted key control" "rc=$ctl2, expected 1"
# An escape that does not spell a name exactly is not the key: the trailing \n makes it another string.
ctl5="$(plana 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        env: "timeout\x2dminutes\n"
        timeout-minutes: 40')"
[ "$ctl5" = 1 ] && ok "a quoted token that only starts like a key name is read and summed (1)" \
	|| mal "near-name control" "rc=$ctl5, expected 1"
# A block scalar body is script text to YAML, and the reader skips it, so an escape spelling inside a
# run: body is read, not refused (ordinary escapes in a body stay readable too).
ctl4="$(plana 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        run: |
          echo "timeout\x2dminutes": 99
        timeout-minutes: 40')"
[ "$ctl4" = 1 ] && ok "an escape spelling inside a run: body is script text, read and summed (1)" \
	|| mal "run body control" "rc=$ctl4, expected 1"
ctl3="$(plana 'name: "a\tb"
jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        timeout-minutes: 40')"
[ "$ctl3" = 1 ] && ok "a column-0 double-quoted value with a backslash is read and summed (1)" \
	|| mal "column-0 value control" "rc=$ctl3, expected 1"
# Control: a double-quoted VALUE with a backslash is read, not refused (the run: scripts of the tree
# are full of them), and the step guard next to it is summed.
ctl="$(plana 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        run: printf "%s\n" x
        timeout-minutes: 40')"
if [ "$ctl" = 1 ]; then
	ok "plain reader reads a double-quoted value with a backslash (a run: line) and sums the step (1)"
else
	mal "escape control" "rc=$ctl, expected 1: the key rule refused a value or skipped the step"
fi
# A quote inside a plain key is a literal and must not pair with the real opener: `'s: '` is not a
# closed string, whether the stray one follows a space or a letter, and whatever follows the pair.
abierta "it 's: 'a" "'"
abierta "don't: 'a" "'"
abierta "k ': ' abc" "z'"
abierta 'k ": " abc' 'z"'
# Quotes glued to `-`, `?`, `:`, `,` or `[` are an opener in a flow collection and text in a plain
# scalar; the line cannot say which, so the reading that leaves the string open is enough to refuse.
abierta 'a-"b: "c' 'x"'
abierta 'a? "b: "c' 'x"'
abierta 'a:"b: "c' 'x"'
abierta "a,'b: 'c" "x'"
abierta "a['b: 'c" "x'"
abierta 'k: [a-"b, "c' 'x"]'
# `??` is a plain scalar, not two indicators: its quote is text, and the `'` after `: ` opens.
abierta '?? "a: '"'"'b"c' "x'"
# A flow collection that spans lines is refused at its own first line, before its continuation
# lines are read at all: the quote reading of a continuation that starts with a quote is never
# reached. This is a stronger guard than the quoted-scalar one, and the reason names it.
rehusa "a flow mapping left open at 'k: {ab, then a continuation line" "multi-line flow collection" 'jobs:
  j:
    timeout-minutes: 30
    k: {ab
      '"'"'x,'"'"'
   b: |
     x'"'"'}
    steps:
      - &s
        name: x
        timeout-minutes: 20
      - *s'
rehusa "a flow sequence left open at 'k: [ab, then a continuation line" "multi-line flow collection" 'jobs:
  j:
    timeout-minutes: 30
    k: [ab
      "c, '"'"'d" e
   b: |
     x'"'"']
    steps:
      - &s
        name: x
        timeout-minutes: 20
      - *s'
# A quote after an anchor is the quote after a node property.
abierta '? &a "b' 'x"'
abierta 'k: {&a "b' 'x"}'
abierta 'k: [? &a "b' 'x"]'
abierta 'k: [{&a "b' 'x"}]'
abierta '? ? &a "b' 'x"'
# A `''` inside a single-quoted string is an escaped quote, so it can also reopen one.
abierta "k: 'a''" "x'"
# A flow mapping may hold a multi-line quoted key, and a tag may be glued to the token before it.
abierta 'k: {"a' 'x"}'
abierta 'k: [a,!!str "a' 'x"]'
abierta 'k: {"b":!!str "a' 'x"}'
# Control: text that only LOOKS like the shapes above is still read, so the refusals do not eat
# real workflows (`&&`, `*` globs and a quoted `&` are everyday shell and expression syntax, and
# a `run: |` block is script text: a `*pattern)` case arm or a `, *args` splat there is not an
# alias; measured on release.yml).
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    if: ${{ github.ref == '"'"'x'"'"' && github.actor != '"'"'y'"'"' }}
    steps:
      - name: "build & test"
        timeout-minutes: 20
        run: |
          cp *foo bar \
          && ls -l *
          case "$v" in
            *GitVersion:*) echo skip ;;
          esac
          python3 -c "check_output([x, *args])"
      - name: tail')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader still READS jobs with &&, * globs and a quoted & (0)" ;;
	*) mal "over-refusal control" "got $got, expected 0/..." ;;
esac
# Quoted scalars that close on their own line, an escaped quote, a doubled single quote and quotes
# inside an expression are everyday workflow text and are still read.
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    if: ${{ contains(github.event.head_commit.message, '"'"'[skip ci]'"'"') }}
    steps:
      - name: "say \"hi\", then go"
        timeout-minutes: 20
      - name: '"'"'it'"''"'s fine'"'"'
      - run: echo ok # it'"'"'s a note')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader still READS quoted scalars that close on their line (0)" ;;
	*) mal "over-refusal control (quoted scalars)" "got $got, expected 0/..." ;;
esac
# A quote inside a plain scalar is literal text: an apostrophe after a letter (`run's`), quotes in
# the middle of a shell word (`x="a"b`, `echo "x"y`), the shell's `'\''` idiom, and a quote that only
# appears in a trailing comment. A first version of the scan that refused them refused 13 of the 37
# real workflows.
Q="'"
got="$(plana_v "jobs:
  ok:
    timeout-minutes: 50
    steps:
      - name: remove this run${Q}s image tag
        timeout-minutes: 20
      - run: x=\"a\"b
      - run: echo \"x\"y
      - run: echo ${Q}it${Q}\\${Q}${Q}s${Q}
      - name: tail # todo: \"the docs")"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader still READS a plain scalar with literal quotes and a quote in a comment (0)" ;;
	*) mal "over-refusal control (literal quotes)" "got $got, expected 0/..." ;;
esac
# A string that closes on its line is read whatever its last characters are: a quote that follows
# `, `, `: ` or `- ` inside it is its end, not a second opener.
got="$(plana_v "jobs:
  ok:
    timeout-minutes: 50
    steps:
      - name: \"Build, test, \"
        timeout-minutes: 20
      - name: ${Q}Usage: ${Q}
      - name: \"a - \"
      - name: {\"k\": \"v\"}
      - run: echo \"a\" \"b\"
      - if: env.A != ${Q}${Q}
        run: echo ${Q}${Q} \"\"")"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader still READS closed strings that end in punctuation and flow mappings (0)" ;;
	*) mal "over-refusal control (closed strings)" "got $got, expected 0/..." ;;
esac
# The block-scalar form with a dash and a comment after the indicator is still a block: its script
# text (a `*pattern)` case arm) is not an alias.
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    steps:
      - run: | # note
          case "$v" in
            *x*) echo skip ;;
          esac
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader still READS a dash-line block scalar with a trailing comment (0)" ;;
	*) mal "over-refusal control (dash-line block)" "got $got, expected 0/..." ;;
esac

# --- a multi-line flow collection, a block scalar body and a very long line ------------
# A flow collection may span lines, and a `#` after a space hides its closing bracket. Read line by
# line, `env: { # }` left the reader in job a with its `}` three lines below, so the cap inside the
# collection replaced the job's own (1000 against 10), and a header-looking line inside one (`>`
# alone) opened a fake block that hid an escaped key (the reader summed 0 where PyYAML sums 40).
# A collection that opens at a node start and is still open at the end of its line is refused.
rehusa "a flow mapping whose closing brace hides behind a comment" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    runs-on: x
    env: { # }
      A: 1,
    timeout-minutes: 1000
    }
    steps:
      - run: x
        timeout-minutes: 50'
rehusa "flow mappings that hold a header-looking line and an escaped key" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - {name: foo
         >
           , "timeout\x2dminutes": 20}
      - {name: foo
         |
           , "timeout\x2dminutes": 20}'
# A closer only closes its own opener: `{]` is invalid YAML, and popping the `{` with the `]` would
# approve a file PyYAML rejects.
rehusa "a flow collection whose closer does not match its opener" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 30
    env: {]
    timeout-minutes: 1000'
rehusa "a flow sequence closed by a mapping brace" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 30
    env: [}
    timeout-minutes: 1000'
# A mismatched closer stays mismatched even when a matching one follows: `{a] }` is invalid YAML,
# and letting the later `}` close the `{` printed a row for a file PyYAML rejects.
rehusa "a mismatched closer followed by a matching one" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 30
    env: {a] }
    timeout-minutes: 1000'
# Outside jobs the open collection and the open quote change what this reader sees just the same:
# either can hide the `jobs:` line below it, and PyYAML rejects the file in both cases.
rehusa "a flow collection left open outside jobs" "multi-line flow collection" 'on: {push,
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a quoted scalar left open outside jobs" "multi-line quoted scalar" 'name: "unterminated
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
# The same check runs after `jobs:` ends: the old reader ended the block at the top-level line and
# never looked at it, so a row was printed for a file PyYAML rejects.
rehusa "a flow collection left open after jobs" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20
on: {push,'
# Every opener spelling: after `key: `, behind a tag, nested, and with a `#` that hides the closer.
# Each hides a `timeout-minutes: 1000` that a line-by-line reader takes for the job cap (PyYAML
# reads 30 against 20); every one is valid YAML.
abierto() {
	rehusa "a flow collection left open after '$1'" "multi-line flow collection" "jobs:
  j:
    timeout-minutes: 30
    $1
    timeout-minutes: 1000
    $2
    steps:
      - name: x
        timeout-minutes: 20"
}
abierto 'x: [a,' ']'
abierto 'x: {a: 1,' '}'
abierto 'x: [' ']'
abierto 'x: !!seq [a,' ']'
abierto 'x: [[a,' ']]'
abierto 'x: {a: [1,' ']}'
abierto 'x: [a, # ]' ']'
abierto 'x:   {   # }' '}'
# Inside a flow collection the colon may touch the opener (`{x:[a]}`, JSON style); at block level
# `x:[a]` is a plain scalar, so the space is only required there. Missing the adjacent form closed
# the collection early and printed a row for a file PyYAML rejects (the regression review).
rehusa "a flow sequence opened after a colon with no space" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: {x:[a}
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A closer or a comma where a node starts is invalid YAML: a plain scalar cannot begin with a flow
# indicator, and a second closer after a collection just closed has nothing to close (`[x]]`).
# Text inside a plain scalar (`echo a, b ]`) stays text (control below).
cierra() {
	rehusa "a stray '$1' where a node starts" "multi-line flow collection" "jobs:
  a:
    timeout-minutes: 10
    $1
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50"
}
cierra 'env: ]'
cierra 'env: }'
cierra 'env: ,'
cierra 'env: [x]]'
# Text after a closed collection is not YAML: the value ended at the closer. Reading the rest as a
# plain scalar printed a row PyYAML rejects (the regression review).
rehusa "text after a closed flow sequence" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: [x] y
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
rehusa "a quoted scalar after a closed flow mapping" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: {x: 1} "y"
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
rehusa "a flow opener after a closed collection" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: [x] [y]
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
rehusa "a comma after a closed collection" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: [x],
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A closer or comma where a node starts is refused at the line start too: only a `}` there is
# expression text from a `${{ }}` that continues on the next line (control below).
rehusa "a stray ']' at the line start" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env:
      ]
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
rehusa "a stray ',' at the line start" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env:
      ,
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A lone `}` at the line start has nothing to close. Only the doubled `}}` of a `${{ ... }}`
# expression that continues on the next line is excused; excusing any `}` let a lone one close
# nothing and the cap after it became the job's own (PyYAML rejects the file) (the regression review).
rehusa "a lone '}' at the line start" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env:
      }
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A quote where a separator would be, after a collection that already closed, cannot start a
# scalar: `env: [x] ""` ended the value at the closer (the regression review).
rehusa "a quoted scalar with nothing after it" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: [x] ""
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# An explicit-key indicator stands alone: the opener after it is a node start, and the collection
# is still open at the end of the line (PyYAML rejects the explicit key here) (the regression review).
rehusa "a flow after an explicit key indicator" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    k: ? [a,
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A flow collection may also start at the line start (a value on the line below its key). The
# opener must be read there; a mutation that only kept the after-dash case stayed green
# (the regression review).
rehusa "a flow collection opened at the line start" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env:
      {A: 1,
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# The quote scanner's escape rules are load-bearing: without them a `\"` or `''` closes the
# string early, the collection looks closed, and the cap after it is read as the job's own
# (PyYAML reads the collection over the next line) (the regression review).
rehusa "a flow mapping whose quoted value escapes the closing quote" "multi-line flow collection" 'jobs:
  a:
    timeout-minutes: 10
    env: {a: "b\"}c",
      d: 1}
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
rehusa "a flow mapping whose quoted value doubles a single quote" "multi-line flow collection" "jobs:
  a:
    timeout-minutes: 10
    env: {a: 'it''}s',
      d: 1}
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50"
# Control: collections that close on their line, and brackets that are text (inside a plain scalar,
# a quoted string or a comment), are still read. `${{ }}` is the everyday one.
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    strategy: { matrix: { os: [a, b] } }
    env: { A: 1 } # a { comment
    needs: [x, y] # ]
    if: ${{ contains(fromJSON('"${Q}"'["a","b"]'"${Q}"'), matrix.os) }}
    steps:
      - name: "[ x"
        run: echo ${{ matrix.os }} [x] {y} [ {
        timeout-minutes: 20
      - name: '"${Q}"'{'"${Q}"'')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader still READS closed flow collections and bracket text (0)" ;;
	*) mal "over-refusal control (flow collections)" "got $got, expected 0/..." ;;
esac
# Controls for the review shapes: an adjacent colon inside a collection, escapes that close on
# their line, and flow indicators as plain-scalar text are still read, not refused.
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    env: {x:[a]}
    steps:
      - run: echo a, b ] } ,
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader READS an adjacent colon in a closed collection and indicators as text (0)" ;;
	*) mal "over-refusal control (adjacent colon, flow text)" "got $got, expected 0/..." ;;
esac
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    env: {a: "b\"c"}
    steps:
      - run: x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader READS a closed flow collection with an escaped double quote (0)" ;;
	*) mal "over-refusal control (escaped double quote)" "got $got, expected 0/..." ;;
esac
got="$(plana_v "jobs:
  ok:
    timeout-minutes: 50
    env: {a: 'it''s'}
    steps:
      - run: x
        timeout-minutes: 20")"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader READS a closed flow collection with a doubled single quote (0)" ;;
	*) mal "over-refusal control (doubled single quote)" "got $got, expected 0/..." ;;
esac
# A collection that closes and is followed by a comma and another key is valid YAML; the comma
# resets the just-closed state, so the following text is not a stray token.
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    env: {x: [1, 2], y: 3}
    steps:
      - run: x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader READS a closed collection followed by a comma and a key (0)" ;;
	*) mal "over-refusal control (comma inside a collection)" "got $got, expected 0/..." ;;
esac
# A `?` glued to the word before it is plain-scalar text, not an explicit-key indicator: the `[`
# after it does not open a collection. PyYAML reads the multi-line scalar.
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    steps:
      - run: echo really? [x,
          y]
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader READS a question mark inside a plain scalar (0)" ;;
	*) mal "over-refusal control (question mark in text)" "got $got, expected 0/..." ;;
esac
# A multi-line `${{ }}` expression continues on a line that starts with `}}`: that is text, not a
# stray closer (the real tree has `}}/postgres?sslmode=disable`).
got="$(plana_v 'jobs:
  ok:
    timeout-minutes: 50
    steps:
      - name: x
        run: echo ${{ github.event.client_payload
          }}done
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "plain reader READS a line starting with }} as expression text (0)" ;;
	*) mal "over-refusal control (line-start }})" "got $got, expected 0/..." ;;
esac

# The reader knows job ids at column 2 only. With `jobs:` indented by one space they sit at column 1,
# which it did not see, so what it read at column 2 was nested in them: the body of `k: |` made
# `fake` a job and the `timeout-minutes` in the body of `z: |` replaced a ceiling (PyYAML reads job
# a, 10 against 50), and a nested key was a job where PyYAML has none. Refused at the first column-1 line.
rehusa "block scalar bodies read as a job id and a ceiling" "jobs indented by one space" 'jobs:
 k: |
  fake:
 a:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 50
 z: |
    timeout-minutes: 1000'
rehusa "a key nested under a column-1 job read as a job" "jobs indented by one space" 'jobs:
 a:
  b:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 5'
# The text of a block scalar is script text: a `timeout-minutes` written there is neither a cap nor
# a guard. It is still counted as a mention, so the reader refuses the file instead of printing a
# row PyYAML would not (the regression, shape 3).
rehusa "a timeout-minutes written in a block scalar body" "mention(s) of timeout-minutes" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: |
          timeout-minutes: 1000
        timeout-minutes: 20'
# A keyless `- |` scalar is the dash's node: its body may start at the dash column + 2, which is
# also the column a step guard would use, so the header's parent is the dash and not the indicator.
rehusa "a timeout-minutes written in a keyless block scalar body" "mention(s) of timeout-minutes" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - |
        timeout-minutes: 1000
      - name: x
        timeout-minutes: 20'
# Node properties do not stop the indicator from being a header: without the tag the body was
# read as YAML and `timeout-minutes: 1000` replaced the step guard (PyYAML reads a string step).
rehusa "a timeout-minutes written in a tagged keyless block body" "mention(s) of timeout-minutes" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - !!str |
        timeout-minutes: 1020'
# An indicator alone on its line has no parent on that line: its body's end cannot be derived, so
# the reader refuses instead of swallowing the rest of the job as body.
rehusa "a block scalar indicator on its own line" "indicator on its own line" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20
    x:
      |
      text'
# A block header's key cannot hold an unquoted `: `: PyYAML rejects `if: echo a: |`, but RE_BLOQUE
# took the whole text as the key and skipped the next line, which could hide a cap (the regression
# review).
rehusa "a block scalar key holding an unquoted colon" "unquoted colon" 'jobs:
  a:
    timeout-minutes: 10
    if: echo a: |
      whatever
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A quoted key is refused the same way: the guard cannot tell a quoted `: ` from a plain one
# (PyYAML reads this key; refusing more is the allowed direction) (the regression review).
rehusa "a quoted block scalar key holding a colon" "unquoted colon" 'jobs:
  a:
    timeout-minutes: 10
    "a: b": |
      whatever
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50'
# A block indicator whose suffix is not a YAML header is not a header: PyYAML rejects all four,
# but the reader took the next cap as the job's own (the regression review). `|0` is an invalid indent
# indicator, `|#c` needs a space before the comment, and text after the indicator is not a
# chomping/indent suffix. The keyless form is the same shape at the dash's node.
bloque_malo() {
	rehusa "$1" "block scalar indicator" "jobs:
  a:
    timeout-minutes: 10
    $2
    timeout-minutes: 1000
    steps:
      - run: x
        timeout-minutes: 50"
}
bloque_malo "a block header with text after the indicator" 'run: | junk'
bloque_malo "a block header with a zero indent indicator" 'run: |0'
bloque_malo "a block header whose comment has no space" 'run: |#c'
bloque_malo "a block header whose suffix is text" 'run: >good'
# An explicit indentation indicator (`|2`) is valid YAML, but it says where the body starts, and
# the reader does not place it: ignoring it read the body as structure. The strict header accepts
# only the chomping signs, so the loose detector refuses every other suffix (the regression review).
bloque_malo "a block header with an explicit indent indicator" 'run: |2'
bloque_malo "a block header with an indent indicator and chomping" 'run: |2+'
bloque_malo "a block header with chomping and an indent indicator" 'run: |-2'
# A `#` inside a plain key is the key's text when no space precedes it, so `run: a#b: |` is a
# block header spelling the strict key pattern cannot hold. PyYAML rejects the line; missing it
# read the next cap as the job's own (the regression review).
bloque_malo "a block header whose key holds a hash" 'run: a#b: |'
rehusa "a keyless block header with text after the indicator" "block scalar indicator" 'jobs:
  a:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 20
      - | junk
        timeout-minutes: 30'
# The body skip is not jobs-only: a top-level `env: X: |` body is text too, and a `{` there must
# not be read as an open flow collection. The real tree has one in commit-outcome-oracle.yml.
got="$(plana_v 'env:
  X: |
    timeout-minutes: 1
    { unclosed
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a top-level block scalar body is not read as YAML (0)" ;;
	*) mal "top-level block scalar body" "got ${got:0:200}, expected 0/..." ;;
esac
# Guard order: the body skip runs before the quote/flow checks, so script text containing an
# unclosed `{` is still just text.
got="$(plana_v 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: |
          { echo start;
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "an unclosed brace inside a run body is not read as YAML (0)" ;;
	*) mal "flow text inside a block body" "got ${got:0:200}, expected 0/..." ;;
esac
# The body starts one column past the key: the threshold is exactly `sangria > bloque`.
got="$(plana_v 'jobs:
  a:
    timeout-minutes: 30
    x: |
     { body
    steps:
      - run: x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a block body at key column + 1 is skipped (0)" ;;
	*) mal "block body boundary" "got ${got:0:200}, expected 0/..." ;;
esac
# RE_COL1 is a guard INSIDE jobs: a one-space indented mapping outside them is read past.
got="$(plana_v 'env:
 FOO: bar
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a one-space indented mapping outside jobs does not trip RE_COL1 (0)" ;;
	*) mal "column-1 control outside jobs" "got ${got:0:200}, expected 0/..." ;;
esac
# A `jobs:` with a value is not a mapping. The global body tracker used to take `jobs: |` for a
# block header, skip its body and print no row; a readable sibling file then turned the missing
# rows into CLEAN. The header itself is refused now (the regression review).
rehusa "a jobs: scalar instead of a mapping" "jobs with a value" 'jobs: |
  timeout-minutes: 1000'
# YAML needs whitespace between a key's colon and its value: `#` without a space before it is not
# a comment (`jobs:#c` is a plain scalar), and NBSP is not a separation character. The structural
# patterns accepted zero spaces via `\s*`, so these read as the bare forms and a file PyYAML
# rejects printed rows (the regression review).
rehusa "a jobs: header whose comment has no space" "jobs with a value" 'jobs:#comment
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a jobs: header with a non-breaking space" "jobs with a value" "$(printf 'jobs:\302\240\n  a:\n    timeout-minutes: 30\n    steps:\n      - run: x\n        timeout-minutes: 20')"
rehusa "a job id whose colon has no space before a comment" "unsupported job line" 'jobs:
  a:#comment
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a steps: key whose comment has no space" "steps with a value" 'jobs:
  a:
    timeout-minutes: 30
    steps:#comment
      - run: x
        timeout-minutes: 20'
rehusa "a timeout-minutes value with no space" "mention(s) of timeout-minutes" 'jobs:
  a:
    timeout-minutes:30
    steps:
      - run: x
        timeout-minutes: 20'
# The same no-space rule on the other three structural patterns: ` +` that drifts back to `\s+`
# would read these as the bare forms and approve a file PyYAML rejects (the regression review).
rehusa "a job id whose colon has a non-breaking space" "unsupported job line" "$(printf 'jobs:\n  a:\302\240#c\n    timeout-minutes: 30\n    steps:\n      - run: x\n        timeout-minutes: 20')"
rehusa "a steps: key with a non-breaking space" "steps with a value" "$(printf 'jobs:\n  a:\n    timeout-minutes: 30\n    steps:\302\240#c\n      - run: x\n        timeout-minutes: 20')"
rehusa "a timeout-minutes key whose space is a non-breaking space" "mention(s) of timeout-minutes" "$(printf 'jobs:\n  a:\n    timeout-minutes:\302\24030\n    steps:\n      - run: x\n        timeout-minutes: 20')"
# CRLF files are legal YAML and must stay readable. The reader opens files in text mode, whose
# universal newlines turn `\r\n` into `\n` before matching, so the patterns carry no `\r?`; this
# test pins the outcome (a Windows checkout still reads), not a dead regex branch.
got="$(plana_v "$(printf 'jobs:\r\n  a:\r\n    timeout-minutes: 30\r\n    steps:\r\n      - run: x\r\n        timeout-minutes: 20\r\n')")"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a CRLF workflow is read (0), 1 of 1 job(s)" ;;
	*) mal "CRLF workflow" "got ${got:0:200}, expected 0/1 of 1 job(s)" ;;
esac
# A second YAML document is not something `safe_load` reads: it raises ("expected a single
# document"), so the gate refuses, while reading only the first and printing a row approved a
# file PyYAML rejects (the regression review). A directive is the same class: `%TAG` can change what a
# tag means, which the plain reader cannot follow, so it refuses too — over-refusal is the
# allowed direction of the one-way rule.
rehusa "a second YAML document" "document marker" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20
---
foo: bar'
rehusa "a YAML directive" "document marker" '%YAML 1.2
---
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
# A marker or directive is refused wherever it is structural, not only at column 0 and not only in
# its exact spelling. PyYAML raises on every one of these (`mapping values are not allowed here`,
# `while scanning a directive`), and skipping the line printed the rows below it (the regression review).
rehusa "an indented document marker" "document marker" ' ---
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a marker glued to a comment" "document marker" '---#c
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "an unknown directive" "document marker" '%FOO bar
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a YAML directive without its separator" "document marker" '%YAML1.2
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a BOM before a directive" "document marker" "$(printf '\357\273\277')%TAG ! !foo
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20"
# A UTF-8 BOM at the start of a stream is legal and PyYAML ignores it, so the plain reader strips
# it too instead of losing the first line and printing no row at all.
got="$(plana_v "$(printf '\357\273\277')jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        timeout-minutes: 20")"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a BOM before jobs: is read (0), 1 of 1 job(s)" ;;
	*) mal "BOM workflow" "got ${got:0:200}, expected 0/1 of 1 job(s)" ;;
esac
# A marker needs whitespace, a comment or the end of the line after it, so these two are mapping
# keys for YAML and stay readable. Refusing them would turn a valid workflow into a NOPUEDO.
for clave in '---foo' '...foo'; do
	got="$(plana_v "$clave: bar
jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: x
        timeout-minutes: 20")"
	case "$got" in
		0/*"1 of 1 job(s)"*) ok "a top-level key '$clave:' is read (0)" ;;
		*) mal "top-level key $clave" "got ${got:0:200}, expected 0/1 of 1 job(s)" ;;
	esac
done
# A column-0 line that is not a mapping key is block structure this reader cannot attribute.
# PyYAML rejects a sequence root, the malformed marker and plain junk; it reads a flow root as a
# mapping with `jobs`, which makes the plain reader's refusal the allowed direction (the regression
# review).
rehusa "a malformed document marker" "unsupported top-level line" '----
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a sequence document" "unsupported top-level line" '- foo
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a flow-style document" "unsupported top-level line" '{jobs: {a: {timeout-minutes: 30, steps: [{name: x, timeout-minutes: 20}]}}}'
rehusa "a top-level line that is not a key" "unsupported top-level line" 'not a key
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
# GitHub reads a positive number of minutes; zero and negative values were literals to the plain
# path. Both are refused now, on both paths (the regression review).
rehusa "a zero timeout" "timeout-minutes is not a literal" 'jobs:
  a:
    timeout-minutes: 0
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a negative timeout" "timeout-minutes is not a literal" 'jobs:
  a:
    timeout-minutes: -20
    steps:
      - run: x
        timeout-minutes: 20'
# --- fourth review round: shapes the plain reader approved where PyYAML refuses ---------------
# plana_dos <suspect file contents> -> "<rc>/<first line>" via the forced plain path,
# with a valid workflow next to it. Alone, the shell's "no jobs" guard gives 2 through both paths
# and hides the divergence; beside a readable file, the missing rows used to become CLEAN.
plana_dos() {
	local caja out txt
	caja="$(mktemp -d)"
	mkdir -p "$caja/scripts" "$caja/.github/workflows"
	cp "$GATE" "$AYUDA" "$caja/scripts/"
	printf '%s\n' 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20' > "$caja/.github/workflows/valido.yml"
	printf '%s\n' "$1" > "$caja/.github/workflows/sospechoso.yml"
	txt="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"
	out=$?
	rm -rf -- "$caja"
	printf '%s/%s' "$out" "$(printf '%s' "$txt" | head -1)"
}
# A job with no lines under it is a null value, not a job without a cap: the PyYAML path refuses
# it as a non-mapping, while the plain reader printed nothing for it and CLEANed the tree when a
# readable sibling had rows (the regression review).
rehusa "a null job" "job 'a' is not a mapping" 'jobs:
  a:
  b:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
# An empty or comment-only workflow is not a document with jobs: `safe_load` returns None and the
# PyYAML path refuses it. The plain reader printed no rows and a readable sibling turned that
# silence into CLEAN (the regression review).
for vacio in '' '# only a comment'; do
	etiqueta="an empty document"
	[ -n "$vacio" ] && etiqueta="a comment-only document"
	got="$(plana_dos "$vacio")"
	case "$got" in
		2/*"document is not a mapping"*) ok "plain reader refuses $etiqueta beside a valid file (2)" ;;
		2/*) mal "$etiqueta" "rc=2 through another guard: $got" ;;
		*) mal "$etiqueta" "got $got, expected 2 — the plain reader approved the tree" ;;
	esac
done
# A plain scalar cannot hold an unquoted `: `: PyYAML rejects `env: FOO: bar` with "mapping
# values are not allowed here", while the reader ignored the line and printed a row. A quote in
# the middle of a plain scalar is text for YAML, so `run: echo "a: b"` is refused too; only a
# quote that certainly opens a string (`name: "a: b"`) hides the colon (the regression review).
rehusa "an unquoted colon in a step value" "unquoted colon in a value" 'jobs:
  a:
    runs-on: ubuntu: latest
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "an unquoted colon in a top-level value" "unquoted colon in a value" 'on: push
env: FOO: bar
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a value ending in a colon" "unquoted colon in a value" 'jobs:
  a:
    name: b:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a quoted colon inside a plain scalar" "unquoted colon in a value" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: echo "a: b"
        timeout-minutes: 20'
# Control: a flow mapping is read (the colon inside it is not the plain marker), as are a URL and
# a quoted string opened right after its key. Without this, a scanner that refuses every second
# colon would pass the four cases above and break the real tree.
got="$(plana_v 'on: push
env: {FOO: bar}
jobs:
  a:
    timeout-minutes: 30
    steps:
      - name: "a: b"
        run: see https://example.com/x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a flow mapping, a URL and a quoted value stay readable (0)" ;;
	*) mal "colon controls" "got ${got:0:200}, expected 0/1 of 1 job(s)" ;;
esac
# A `jobs` key written as a quoted string (or with a space before the colon) is the same mapping
# for YAML, but the plain reader only knows `jobs:` at column 0: it ignored the key, and a valid
# sibling turned the missing rows into CLEAN. PyYAML reads the key and refuses a non-mapping
# value, so approving the tree was the wrong direction (the regression review).
got="$(plana_dos '"jobs": [a, b]')"
case "$got" in
	2/*"quoted key or a space before the colon"*) ok "plain reader refuses a quoted jobs key beside a valid file (2)" ;;
	2/*) mal "quoted jobs key" "rc=2 through another guard: $got" ;;
	*) mal "quoted jobs key" "got $got, expected 2 — the plain reader approved the tree" ;;
esac
got="$(plana_dos 'jobs :
  a:
    timeout-minutes: 30')"
case "$got" in
	2/*"quoted key or a space before the colon"*) ok "plain reader refuses a space before the jobs colon (2)" ;;
	2/*) mal "jobs key with a space" "rc=2 through another guard: $got" ;;
	*) mal "jobs key with a space" "got $got, expected 2 — the plain reader approved the tree" ;;
esac
# The two top-level structure branches that carry a colon are pinned on their own: a sequence
# entry and an explicit key that hold a mapping. PyYAML refuses both, and deleting either
# alternative of RE_TOP_ESTRUCTURA made the reader print a JOB row (the regression review).
rehusa "a top-level sequence entry holding a mapping" "unsupported top-level line" '- a: b
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
rehusa "a top-level explicit key holding a mapping" "unsupported top-level line" '? a: b
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
# A bare `...` is an end marker wherever it is structural: indented inside a job, PyYAML fails
# scanning a simple key, and without the `\.\.\.` branch the reader printed the cap that follows
# it (the regression review).
rehusa "an end marker inside a job" "document marker" 'jobs:
  a:
    timeout-minutes: 10
    ...
    timeout-minutes: 1000
    steps:
      - run: x'
# Control: `+` chomping is valid block-scalar header syntax and the real tree uses `>-`; narrowing
# RE_BLOQUE to `-` alone would break a valid workflow without any case saying so.
got="$(plana_v 'x: |+
  body
jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20')"
case "$got" in
	0/*"1 of 1 job(s)"*) ok "a block scalar with + chomping stays readable (0)" ;;
	*) mal "valid + chomping" "got ${got:0:200}, expected 0/1 of 1 job(s)" ;;
esac
# MAX_LINEA applies to every line, not only to lines inside jobs: a 100 KB top-level value is the
# same unbounded work as one inside a step (the regression review, shape 4).
largo="$(printf 'a%.0s' $(seq 1 100000))"
rehusa "a 100 KB top-level line" "characters in a line" "name: $largo
jobs:
  a:
    timeout-minutes: 30"
# On a box WITH PyYAML these are valid YAML and the reader reads them: the one-way rule lets the
# plain reader refuse them, but it must agree with PyYAML's verdict where PyYAML reads. This is
# the only block that catches a fixture that refuses for the wrong reason (invalid YAML).
if python3 -c 'import yaml' >/dev/null 2>&1; then
	# yaml_v <workflow> -> "<rc>/<first line>", default (PyYAML) path only. rehusa_yaml demands the
	# exact NOPUEDO reason: a traceback also reaches the gate as 2 ("helper failed"), and without
	# the message the two are indistinguishable — the crash would pass as a refusal.
	yaml_v() {
		local caja out txt
		caja="$(mktemp -d)"
		mkdir -p "$caja/scripts" "$caja/.github/workflows"
		cp "$GATE" "$AYUDA" "$caja/scripts/"
		printf '%s\n' "$1" > "$caja/.github/workflows/prueba.yml"
		txt="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"
		out=$?
		rm -rf -- "$caja"
		printf '%s/%s' "$out" "$(printf '%s' "$txt" | head -1)"
	}
	rehusa_yaml() {
		local got
		got="$(yaml_v "$3")"
		case "$got" in
			2/*"$2"*) ok "PyYAML path refuses $1 (2): $2" ;;
			2/*) mal "$1" "rc=2 through another guard: $got" ;;
			*) mal "$1" "got ${got%%/*}, expected 2 — the PyYAML path crashed or guessed: $got" ;;
		esac
	}
	# A value PyYAML reads as a string, a float or a bool is not the integer GitHub accepts.
	# `int(techo)` raised ValueError, `sum(...)` raised TypeError, the helper exited 1 and the gate
	# said "helper failed": a crash where the contract promises NOPUEDO (the regression review).
	rehusa_yaml "a non-numeric cap" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: abc
    steps:
      - run: x
        timeout-minutes: 20'
	rehusa_yaml "a cap with a zero-width space" "timeout-minutes is not an integer" "$(printf 'jobs:\n  a:\n    timeout-minutes: 30\342\200\213\n    steps:\n      - run: x\n        timeout-minutes: 20')"
	rehusa_yaml "a fractional cap" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: 1.5
    steps:
      - run: x
        timeout-minutes: 20'
	rehusa_yaml "a boolean cap" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: true
    steps:
      - run: x
        timeout-minutes: 20'
	rehusa_yaml "a quoted cap" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: "20"
    steps:
      - run: x
        timeout-minutes: 20'
	rehusa_yaml "a quoted step guard" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: "20"'
	rehusa_yaml "a fractional step guard" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 1.5'
	# `for s in 5` raised TypeError; a string was walked character by character and the job printed
	# with zero steps. Neither is the list the reader knows.
	rehusa_yaml "a steps value that is not a list" "steps is not a list" 'jobs:
  a:
    timeout-minutes: 30
    steps: 5'
	rehusa_yaml "a steps value that is a mapping" "steps is not a list" 'jobs:
  a:
    timeout-minutes: 30
    steps: {run: x}'
	# `jobs: |` is a scalar, already refused as non-mapping; pin it on this path too.
	rehusa_yaml "a jobs value that is a block scalar" "jobs is not a mapping" 'jobs: |
  a:
    timeout-minutes: 1
    steps:
      - run: x
        timeout-minutes: 2'
	# A document or job the walk skipped is not a file it read: `- a` has no jobs mapping, and a
	# scalar job has no key whose cap could be attributed. Both were dropped silently while the
	# plain reader refuses them (the regression review).
	rehusa_yaml "a document that is not a mapping" "document is not a mapping" '- a
- b'
	rehusa_yaml "a job that is not a mapping" "job 'b' is not a mapping" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20
  b: literal'
	# A job with no lines under it parses as None; the walk refuses it as a non-mapping, and the
	# plain reader must not print CLEAN for the same file (the regression review).
	rehusa_yaml "a null job" "job 'a' is not a mapping" 'jobs:
  a:
  b:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20'
	# `safe_load` of an empty or comment-only stream is None, not a mapping.
	rehusa_yaml "an empty document" "document is not a mapping" ''
	# A quoted `jobs` key is still the jobs mapping for YAML; a sequence value is refused by the
	# walk. The plain reader must refuse the key it cannot read (the regression review).
	rehusa_yaml "a quoted jobs key with a sequence" "jobs is not a mapping" '"jobs": [a, b]'
	# A declared-null step guard is present with value None: filtering by truthiness dropped it
	# from the count. Zero is present too, and GitHub reads a positive number of minutes; a
	# `guarda < 0` mutant would stay green on both (the regression review).
	rehusa_yaml "a declared-null step guard" "a step timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes:'
	rehusa_yaml "a zero step guard" "a step timeout-minutes is not a positive integer" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 0'
	# A long value is cut before `repr`: the refusal reports its length without rendering it.
	largo="$(printf 'a%.0s' $(seq 1 200))"
	rehusa_yaml "a long string cap" "(200 chars)" "jobs:
  a:
    timeout-minutes: $largo
    steps:
      - run: x
        timeout-minutes: 20"
	# A declared null cap is present, and a present value this reader cannot attribute is not a
	# job to skip: `.get()` returned None and the whole job disappeared (the regression review).
	rehusa_yaml "a declared-null cap" "timeout-minutes is not an integer" 'jobs:
  a:
    timeout-minutes:
    steps:
      - run: x
        timeout-minutes: 20'
	# GitHub reads a positive number of minutes. A negative guard made the sum smaller and could
	# approve a job whose real guards exceed its cap (the regression review).
	rehusa_yaml "a zero cap" "timeout-minutes is not a positive integer" 'jobs:
  a:
    timeout-minutes: 0
    steps:
      - run: x
        timeout-minutes: 20'
	rehusa_yaml "a negative cap" "timeout-minutes is not a positive integer" 'jobs:
  a:
    timeout-minutes: -20
    steps:
      - run: x
        timeout-minutes: 20'
	rehusa_yaml "a negative step guard" "a step timeout-minutes is not a positive integer" 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: -20'
	# A scalar step carries no guard, so filtering it out of the count does not change the
	# verdict, but it printed a different row than the plain reader. Both count list items now
	# (the regression review).
	caja="$(mktemp -d)"
	mkdir -p "$caja/scripts" "$caja/.github/workflows"
	cp "$GATE" "$AYUDA" "$caja/scripts/"
	printf '%s\n' 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - x
      - name: x
        timeout-minutes: 20' > "$caja/.github/workflows/prueba.yml"
	yaml_rows="$(python3 "$AYUDA" "$caja/.github/workflows")"
	plain_rows="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 python3 "$AYUDA" "$caja/.github/workflows")"
	rm -rf -- "$caja"
	case "$yaml_rows" in
		"$plain_rows") case "$yaml_rows" in
			*$'\t2\t1') ok "PyYAML counts a scalar step like the plain reader: row parity (2 steps, 1 guarded)" ;;
			*) mal "scalar step row parity" "unexpected rows: $yaml_rows" ;;
		esac ;;
		*) mal "scalar step row parity" "PyYAML rows differ: $yaml_rows / $plain_rows" ;;
	esac
	# A bare `-` is a null step. PyYAML counts it as an item; the plain reader did not, so the
	# same file printed `2 1` and `1 1`. Both list styles are read, so both are pinned
	# (the regression review).
	for pasos_nulos in '      -
      - name: x
        timeout-minutes: 20' '    -
    - name: x
      timeout-minutes: 20'; do
		caja="$(mktemp -d)"
		mkdir -p "$caja/scripts" "$caja/.github/workflows"
		cp "$GATE" "$AYUDA" "$caja/scripts/"
		printf 'jobs:
  a:
    timeout-minutes: 30
    steps:
%s\n' "$pasos_nulos" > "$caja/.github/workflows/prueba.yml"
		yaml_rows="$(python3 "$AYUDA" "$caja/.github/workflows")"
		plain_rows="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 python3 "$AYUDA" "$caja/.github/workflows")"
		rm -rf -- "$caja"
		case "$yaml_rows" in
			"$plain_rows") case "$yaml_rows" in
				*$'\t2\t1') ok "PyYAML counts a bare-dash step like the plain reader: row parity (2 steps, 1 guarded)" ;;
				*) mal "bare-dash step row parity" "unexpected rows: $yaml_rows" ;;
			esac ;;
			*) mal "bare-dash step row parity" "PyYAML rows differ: $yaml_rows / $plain_rows" ;;
		esac
	done
	rc="$(corre_fixture 'on: push
jobs:
  a:
    timeout-minutes: 10
    runs-on: x
    env: { # }
      A: 1,
    timeout-minutes: 1000
    }
    steps:
      - run: x
        timeout-minutes: 50')"
	[ "$rc" = "1" ] && ok "PyYAML reads the hidden-brace mapping: cap 10 below sum 50 (1)" \
		|| mal "PyYAML parity: hidden brace" "rc=$rc, expected 1"
	rc="$(corre_fixture 'jobs:
  j:
    timeout-minutes: 30
    x: [a,
      b]
    steps:
      - name: x
        timeout-minutes: 20')"
	[ "$rc" = "0" ] && ok "PyYAML reads a multi-line flow collection: cap 30 above sum 20 (0)" \
		|| mal "PyYAML parity: flow collection" "rc=$rc, expected 0"
	rc="$(corre_fixture ' jobs:
  a:
    timeout-minutes: 10
    steps:
      - run: x
        timeout-minutes: 50')"
	[ "$rc" = "1" ] && ok "PyYAML reads jobs: indented by one space: cap 10 below sum 50 (1)" \
		|| mal "PyYAML parity: column-1 jobs" "rc=$rc, expected 1"
	rc="$(corre_fixture 'jobs:
  a:
    timeout-minutes: 30
    steps:
      - |
        timeout-minutes: 1000
      - name: x
        timeout-minutes: 20')"
	[ "$rc" = "0" ] && ok "PyYAML reads the keyless body as text: cap 30 above sum 20 (0)" \
		|| mal "PyYAML parity: keyless body" "rc=$rc, expected 0"
	rc="$(corre_fixture "jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: a$(printf '%0100000d' 0)
        timeout-minutes: 20")"
	[ "$rc" = "0" ] && ok "PyYAML reads a 100000-character scalar: cap 30 above sum 20 (0)" \
		|| mal "PyYAML parity: long line" "rc=$rc, expected 0"
	# The real tree both ways: the same rows, byte for byte. The gate case above only checks rc 0,
	# so a row silently lost would stay green; this is the case that notices (the regression review).
	# A `jobs:` that PyYAML reads as a sequence or a scalar is not the mapping the helper walks:
	# `.items()` raised AttributeError, the helper exited 1, and the gate reported a helper crash
	# instead of the documented NOPUEDO (the regression review).
	caja="$(mktemp -d)"
	mkdir -p "$caja/scripts" "$caja/.github/workflows"
	cp "$GATE" "$AYUDA" "$caja/scripts/"
	printf '%s\n' 'jobs: [a]' > "$caja/.github/workflows/prueba.yml"
	output="$(bash "$caja/scripts/check-ci-timeout-arithmetic.sh" 2>&1)"
	rc=$?
	rm -rf -- "$caja"
	case "$rc/$output" in
		2/*"jobs is not a mapping"*) ok "PyYAML refuses a jobs: sequence with a NOPUEDO, not a crash (2)" ;;
		2/*) mal "PyYAML non-mapping jobs" "rc=2 through another guard: ${output:0:200}" ;;
		*) mal "PyYAML non-mapping jobs" "got rc=$rc, expected 2: ${output:0:200}" ;;
	esac
	yaml_tree="$(python3 "$AYUDA" "$RAIZ/.github/workflows")"
	plain_tree="$(OLIVARES_CI_TIMEOUTS_NO_YAML=1 python3 "$AYUDA" "$RAIZ/.github/workflows")"
	# Two empty outputs compare equal too: require the comparison to hold real rows (the regression
	# review).
	case "$yaml_tree" in
		JOB*) [ "$yaml_tree" = "$plain_tree" ] \
			&& ok "PyYAML and the plain reader print the same rows on the real tree" \
			|| mal "real-tree parity" "the two readers disagree on the real workflows" ;;
		*) mal "real-tree parity" "the PyYAML path printed no JOB rows; the comparison would be vacuous" ;;
	esac
else
	skips=$((skips + 1))
	printf '  skip  the PyYAML parity fixtures: UNMEASURABLE on this machine, no PyYAML for comparison\n'
fi

# A line longer than the reader has ever needed is refused before any pattern runs: the anchor
# pattern backtracks, and `,!` fifty thousand times held a pre-push hook for 48 s. Lines inside a
# block scalar body are script text and are not read, so a long one there is fine.
MAXL="$(sed -n 's/^MAX_LINEA = \([0-9]*\).*/\1/p' "$AYUDA")"
case "$MAXL" in
	''|*[!0-9]*) mal "line length cap" "no MAX_LINEA = <number> in ci-timeouts.py" ;;
	*)
		# larga <largo> [bloque] -> a job whose step has one line of exactly <largo> characters: the
		# `run:` line itself, made of the `,!` that backtracks, or with `bloque` the body of `run: |`
		larga() {
			python3 -c 'import sys
n = int(sys.argv[1])
cab = "jobs:\n  a:\n    timeout-minutes: 30\n    steps:\n"
if sys.argv[2:] == ["bloque"]:
    print(cab + "      - run: |\n          " + "a" * (n - 10) + "\n        timeout-minutes: 20")
else:
    cl = "      - run: a"
    print(cab + cl + (",!" * n)[: n - len(cl)] + "\n        timeout-minutes: 20")' "$@"
		}
		rehusa "a line of $((MAXL + 1)) characters" "characters in a line" "$(larga $((MAXL + 1)))"
		# The cap must run BEFORE the patterns: with the cap moved after RE_ANCLA, the same
		# fixture still refuses with the same reason but takes about a minute.
		SECONDS=0
		rehusa "a line of 100000 characters" "characters in a line" "$(larga 100000)"
		[ "$SECONDS" -lt 10 ] && ok "the 100000-character line is refused in $SECONDS s, under 10" \
			|| mal "long-line refusal time" "took $SECONDS s"
		# The cap runs before the quote and flow checks: a long line that also opens one still
		# reports the cap, the reason that costs no scan of the line.
		relleno="$(printf '%05000d' 0)"
		rehusa "a line over the cap that also opens a quote" "characters in a line" "jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20
    env: x: \"$relleno"
		rehusa "a line over the cap that also opens a flow collection" "characters in a line" "jobs:
  a:
    timeout-minutes: 30
    steps:
      - run: x
        timeout-minutes: 20
    env: x: [$relleno"
		SECONDS=0
		got="$(plana_v "$(larga "$MAXL")")"
		case "$got" in
			0/*"1 of 1 job(s)"*) [ "$SECONDS" -lt 10 ] && ok "a line of exactly $MAXL characters is read (0), in $SECONDS s" \
				|| mal "line at the cap" "took $SECONDS s" ;;
			*) mal "line at the cap" "got ${got:0:200}, expected 0/..." ;;
		esac
		got="$(plana_v "$(larga $((MAXL * 10)) bloque)")"
		case "$got" in
			0/*"1 of 1 job(s)"*) ok "a line of $((MAXL * 10)) characters inside a block scalar body is still read (0)" ;;
			*) mal "long line in a block scalar body" "got ${got:0:200}, expected 0/..." ;;
		esac ;;
esac

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
	[ "$got" = "$3" ] || mal "verdict logic" "with=$1 without=$2 -> $got, expected $3"
}
_antes=$fallos
_par 0 0 0             ; _par 1 1 1             ; _par 2 2 2
_par 0 2 0             ; _par 1 2 1
_par 1 0 DISCREPA:1/0  ; _par 0 1 DISCREPA:0/1
_par 2 0 DISCREPA:2/0  ; _par 2 1 DISCREPA:2/1
if [ "$fallos" -eq "$_antes" ]; then
	ok "verdict logic is ONE-WAY: the plain reader may refuse, never approve (9 pairs)"
fi

if [ "$fallos" -eq 0 ]; then
	echo "test-ci-timeout-arithmetic: 0 CLEAN — $oks cases, $skips unmeasurable on this machine"
	exit 0
fi
echo "test-ci-timeout-arithmetic: 1 — $fallos failed case(s)"
exit 1
