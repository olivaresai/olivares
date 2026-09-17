#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# test-check-gate-parity.sh — a repository gate, los dos sentidos.
#
# El mutante principal es un error REAL, mio, del 2026-08-29: compare `lint:spdx` (nombre
# de CI) con `spdx` (nombre corto del gancho), las listas quedaron disjuntas y el
# resultado — «64 de 64 ausentes» — tenia aspecto de hallazgo. Por eso la asercion del
# gate es un SUELO de coincidencias (`ambas >= 20`) y no un techo de diferencias: un
# umbral sobre la DIFERENCIA no distingue «no hay divergencia» de «la comparacion no
# caso nada».
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SUT="$ROOT/scripts/check-gate-parity.sh"
pass=0; fail=0
ok()  { printf 'ok    %-56s %s\n' "$1" "${2:-}"; pass=$((pass+1)); }
bad() { printf 'FAIL  %-56s %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/parity.XXXXXX")" || { echo "no pude crear el area"; exit 2; }
trap 'rm -rf "$WORK"' EXIT
restore() {
	rm -rf "$WORK/t"; mkdir -p "$WORK/t/.githooks" "$WORK/t/.github/workflows" "$WORK/t/design" "$WORK/t/scripts"
	cp "$ROOT/.githooks/pre-push" "$WORK/t/.githooks/"
	cp "$ROOT/.github/workflows/mainline-ci.yml" "$WORK/t/.github/workflows/"
	cp "$ROOT/design/GATE-PARITY-2026-08-29.md" "$WORK/t/design/"
	cp "$ROOT/Taskfile.yml" "$WORK/t/"
	cp "$SUT" "$WORK/t/scripts/"
}
corre() { ( cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh >"$WORK/out" 2>&1 ); echo $?; }

# --- NO-FIRE: sin mutar debe salir 0. Sin este caso, un gate que rechace todo pasaria
# --- todos los rojos de abajo.
restore
[ "$(corre)" = 0 ] && ok "sin mutar: la paridad coincide con el registro" \
                  || { bad "sin mutar: deberia salir 0"; sed 's/^/       /' "$WORK/out" | head -4; }

# --- MUTANTE 1: la deriva de nombres. Si CI deja de usar el prefijo `lint:`, las dos
# --- listas quedan disjuntas. Debe salir 2 (NO HE PODIDO MIRAR), NUNCA 0 ni un hallazgo.
restore
sed -i 's/task lint:/task LINT_/g' "$WORK/t/.github/workflows/mainline-ci.yml"
rc="$(corre)"
if [ "$rc" = 2 ] && command grep -q 'no esta casando' "$WORK/out"; then
	ok "deriva de nombres: sale 2 y dice que la comparacion no casa" "rc=$rc"
else
	bad "deriva de nombres: esperaba 2 con su explicacion" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -3
fi

# --- MUTANTE 2: medir las patas EJECUTADAS en vez de las DECLARADAS. Se simula dejando
# --- el rotulo con una linea: da un puñado en vez de 168, y las coincidencias caen.
restore
python3 - "$WORK/t/.githooks/pre-push" <<'PY'
import io, re, sys
p = sys.argv[1]
s = io.open(p, encoding="utf-8").read()
# El rotulo son varias lineas `echo "pre-push: ..."` seguidas; las sustituyo todas por
# una sola, que es lo que veria quien midiera un push a medio correr.
pat = re.compile(r'echo "pre-push: FAST lints \(.*?\)\."', re.S)
assert pat.search(s), "no encuentro el rotulo"
s = pat.sub('echo "pre-push: FAST lints (mid-operation + disk-headroom)."', s, count=1)
io.open(p, "w", encoding="utf-8").write(s)
PY
rc="$(corre)"
# Sigue esperando 2, pero por OTRA razon desde que la paridad se deriva de lo que el gancho
# INVOCA: antes lo cazaba el suelo de `ambas` (destrozar el rotulo derrumbaba la comparacion);
# ahora lo caza el suelo del ROTULO (2 nombres de 183 invocadas). Misma respuesta, distinto
# guardian — y por eso el caso se queda: comprueba que la ruina del rotulo no pasa callada.
[ "$rc" = 2 ] && ok "rotulo destrozado: sale 2 (suelo del rotulo)" "rc=$rc" \
             || { bad "rotulo truncado: esperaba 2" "rc=$rc"; sed 's/^/       /' "$WORK/out" | head -3; }

# --- MUTANTE 3: una pata pasa de solo-CI a AMBAS sin tocar el registro. Debe ser rc=1
# --- con su nombre, no un 0 silencioso.
restore
sed -i 's/^echo "pre-push: FAST lints (mid-operation/echo "pre-push: FAST lints (proto:check + mid-operation/' "$WORK/t/.githooks/pre-push"
rc="$(corre)"
if [ "$rc" = 1 ] && command grep -q 'proto:check' "$WORK/out"; then
	ok "pata movida sin actualizar el registro: rc=1 y la nombra" "rc=$rc"
else
	bad "pata movida: esperaba 1 nombrandola" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -4
fi

# --- MUTANTE 4: el registro miente (le quitamos una entrada). Debe cazarlo.
restore
sed -i '0,/^int-12-no-land$/{/^int-12-no-land$/d}' "$WORK/t/design/GATE-PARITY-2026-08-29.md"
rc="$(corre)"
if [ "$rc" = 1 ] && command grep -q 'int-12-no-land' "$WORK/out"; then
	ok "registro incompleto: rc=1 y nombra lo que falta" "rc=$rc"
else
	bad "registro incompleto: esperaba 1" "rc=$rc"; sed 's/^/       /' "$WORK/out" | head -3
fi

# --- NO HE PODIDO MIRAR: sin registro, 2. Nunca 0.
restore
rm -f "$WORK/t/design/GATE-PARITY-2026-08-29.md"
[ "$(corre)" = 2 ] && ok "sin registro: 2, no 0" || bad "sin registro: esperaba 2"

# --- MUTANTE 5 (2026-08-29): una pata que CI corre SIN `task X`. El job a11y no
# --- instala go-task y usa `pnpm --dir web run at:gate`; los COMENTARIOS de mainline-ci
# --- citan `task at:gate`, asi que un censo del YAML entero la veia igualmente y el
# --- veredicto salia bien por accidente. Borrar la invocacion REAL tiene que ponerlo rojo.
restore
# ⛔ Y SE COMPRUEBA QUE EL MUTANTE SE APLICO. Este caso llevaba tiempo ROJO en `origin/main` por
# una razon que no era la que anunciaba: su patron era `/run: pnpm --dir web run at:gate/`, y la
# invocacion paso a vivir dentro de un bloque `run: |`, asi que hoy la linea real es
# `          pnpm --dir web run at:gate 2>&1 | tee …`. El `sed` no borraba NADA, el gate contestaba
# 0 con razon, y el caso leia ese 0 como «el gate no se entera». Un mutante que no se aplica
# reporta al gate como ciego: es el fallo exactamente al reves, y envenena el metodo entero.
sed -i '/pnpm --dir web run at:gate/d' "$WORK/t/.github/workflows/mainline-ci.yml"
if command grep -q 'pnpm --dir web run at:gate' "$WORK/t/.github/workflows/mainline-ci.yml"; then
	bad "MUTANTE NO APLICADO: la invocacion de at:gate sigue en el YAML"
fi
rc="$(corre)"
if [ "$rc" = 1 ] && command grep -q 'at:gate' "$WORK/out"; then
	ok "invocacion real borrada (queda solo el comentario): rc=1" "rc=$rc"
else
	bad "invocacion real borrada: esperaba 1 nombrando at:gate" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -4
fi

# --- MUTANTE 6: el hermano, invocado como `bash scripts/test-console-walk.sh`.
restore
sed -i '/bash scripts\/test-console-walk.sh/d' "$WORK/t/.github/workflows/mainline-ci.yml"
rc="$(corre)"
[ "$rc" = 1 ] && ok "script invocado por bash, borrado: rc=1" "rc=$rc" \
             || { bad "script borrado: esperaba 1" "rc=$rc"; sed 's/^/       /' "$WORK/out" | head -3; }

# --- MUTANTE 9: EL ENSANCHE AL CARRIL PESADO TIENE QUE SER PORTANTE. Si el lado del gancho
# --- vuelve a derivarse solo del rotulo, las patas pesadas reaparecen en SOLO-CI y la lista
# --- vuelve a mentir: 'el push no las ve' sobre algo que el push corre. Se borra del gancho
# --- la invocacion pesada de web:check --que NO esta en el rotulo-- y el gate tiene que
# --- verla mudarse a SOLO-CI y NOMBRARLA. Con el sujeto viejo este mutante NO dispara: es el
# --- que separa el guion corregido del anterior.
restore
sed -i '/^task web:check$/d' "$WORK/t/.githooks/pre-push"
rc="$(corre)"
if [ "$rc" = 1 ] && command grep -q 'web:check' "$WORK/out"; then
	ok "pata PESADA borrada: entra en SOLO-CI y la nombra" "rc=$rc"
else
	bad "pata pesada borrada: esperaba 1 nombrando web:check" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -4
fi

# --- MUTANTE 10: control POSITIVO del suelo del rotulo. Un rotulo sano no debe dispararlo, y
# --- un sufijo en una invocacion tampoco debe romper la derivacion: sin este caso, un suelo
# --- mal calibrado que rechazara SIEMPRE se leeria como proteccion.
restore
sed -i 's/^task lint:spdx$/task lint:spdx || true/' "$WORK/t/.githooks/pre-push"
rc="$(corre)"
if [ "$rc" = 0 ]; then
	ok "no-fire: un sufijo en una invocacion no rompe la derivacion" "rc=$rc"
else
	bad "no-fire del sufijo: esperaba 0" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -3
fi

# --- MUTANTE 11: LA EQUIVALENCIA POR ORDEN, y de paso la CACHE. CI corre `task test:web` sin
# --- nombrarlo: ejecuta su orden literal (pnpm --dir web exec vitest run). Se borra esa linea
# --- del workflow y test:web tiene que VOLVER a SOLO-GANCHO y ser nombrada. Doble deber: si la
# --- cache estuviera indexada por algo que no sean los bytes del workflow, este caso devolveria
# --- la respuesta vieja y pasaria en falso.
restore
sed -i '/pnpm --dir web exec vitest run/d' "$WORK/t/.github/workflows/mainline-ci.yml"
rc="$(corre)"
if [ "$rc" = 1 ] && command grep -q 'test:web' "$WORK/out"; then
	ok "equivalencia por orden borrada de CI: vuelve a SOLO-GANCHO y la nombra" "rc=$rc"
else
	bad "equivalencia borrada: esperaba 1 nombrando test:web" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -4
fi

# --- MUTANTE 12: LA CLASE DEL NOMBRE, en las dos direcciones y en el mismo caso, porque son el
# --- mismo caracter del patron. (a) una tarea que empieza por DIGITO invocada sin prefijo debe
# --- CONTARSE: se añade `task 0099-digito` y el gate tiene que acusarla. (b) una BANDERA no es
# --- una tarea: se añade `task --list-all` —que el arbol usa de verdad en :390— y NO puede
# --- aparecer. Midio que su extractor se la tragaba e inflaba su censo en uno (184 vs 183).
restore
printf 'task 0099-digito\ntask --list-all\n' >> "$WORK/t/.githooks/pre-push"
rc="$(corre)"
if [ "$rc" = 1 ] && command grep -q '0099-digito' "$WORK/out" && ! command grep -q 'list-all' "$WORK/out"; then
	ok "clase del nombre: cuenta el digito y NO cuenta la bandera" "rc=$rc"
else
	bad "clase del nombre: esperaba 1 con 0099-digito y sin list-all" "rc=$rc"
	sed 's/^/       /' "$WORK/out" | head -4
fi

# --- PREFIJO DE ENTORNO (a repository gate, 2026-09-02). El gancho de `origin/main` invoca DOS patas con
# --- una asignacion delante: `OLIVARES_NETWORK_ADVISORY=1 task lint:session-numbers` y la misma
# --- forma para `lint:hub-web-fidelity`. Esa forma es INVISIBLE a la sonda canonica
# --- `^[[:space:]]*task `, que es la que este fichero documenta como su punto ciego — y el dia
# --- que alguien la escriba, el contador cuenta de menos y NADIE avisa. Ya la escribieron.
restore
salida="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print 2>/dev/null)"
falta=""
for pata in session-numbers hub-web-fidelity; do
	command grep -qx "  $pata" <<<"$salida" || falta="$falta $pata"
done
[ -z "$falta" ] && ok "prefijo de entorno: las patas con VAR=1 delante SI se cuentan" \
                || bad "prefijo de entorno: el censo no ve$falta"

# --- NEGATIVO: un COMENTARIO que contenga `task lint:` no es una invocacion. Sin este caso, un
# --- predicado que se limitara a buscar la cadena pasaria el positivo de arriba sin discriminar.
restore
printf '# task lint:pata-de-mentira\n' >>"$WORK/t/.githooks/pre-push"
salida="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print 2>/dev/null)"
command grep -q 'pata-de-mentira' <<<"$salida" \
	&& bad "negativo: un comentario con 'task lint:' se conto como pata" \
	|| ok "negativo: un comentario con 'task lint:' NO cuenta"

# --- MUTANTE 6: se le quita al censo el grupo del prefijo, que es la conducta anterior a la cura.
# ---
# --- ⛔ Y HAY QUE AISLARLO, porque la primera version de este caso PASO CON EL MUTANTE PUESTO y
# --- me hizo creer que el grupo no servia para nada. El sujeto del gate es la UNION de dos
# --- derivaciones —el rotulo que el gancho imprime y las invocaciones que corre— y estas dos
# --- patas estan en LAS DOS (`pre-push:467` las rotula, `:737` y `:1483` las invocan). Con la
# --- union, quitarle el grupo al censo no cambia nada: el rotulo las sostiene.
# ---
# --- Eso NO es una defensa, es un tapon: una pata invocada con prefijo y NO rotulada seria
# --- invisible, y el gancho tiene 186 patas solo-gancho donde eso puede pasar. Asi que el caso
# --- borra primero los nombres del ROTULO y deja que solo la invocacion pueda encontrarlas.
restore
# El nombre se quita de las lineas de ROTULO en cualquier posicion: un `sed` de `nombre + ` solo
# acierta si va seguido de otro, y con eso una de las dos sobrevivia y el caso mentia a medias.
python3 - "$WORK/t/.githooks/pre-push" <<'ROT'
import re, sys
p = sys.argv[1]
out = []
for l in open(p, encoding="utf-8"):
    if l.startswith('echo "pre-push:'):
        for n in ("session-numbers", "hub-web-fidelity"):
            l = re.sub(r"(\s\+\s)?\b" + n + r"\b(\s\+\s)?", " ", l)
    out.append(l)
open(p, "w", encoding="utf-8").write("".join(out))
ROT
salida="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print 2>/dev/null)"
visto=0
for pata in session-numbers hub-web-fidelity; do
	command grep -qx "  $pata" <<<"$salida" && visto=$((visto + 1))
done
[ "$visto" -eq 2 ] && ok "sin rotulo, la INVOCACION con prefijo las encuentra igual" \
                   || bad "sin rotulo deberian verse por la invocacion" "vistas=$visto"

# Y ahora, sobre ese mismo arbol sin rotulo, se le quita el grupo al censo: tienen que perderse.
python3 - "$WORK/t/scripts/check-gate-parity.sh" <<'MUT6'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
grupo = "([A-Za-z_][A-Za-z0-9_]*=[^[:space:]]*[[:space:]]+)*"
if grupo not in s:
    sys.exit(3)
open(p, "w", encoding="utf-8").write(s.replace(grupo, ""))
MUT6
if command cmp -s "$SUT" "$WORK/t/scripts/check-gate-parity.sh"; then
	bad "MUTANTE 6 NO APLICADO: el censo sigue igual"
else
	salida="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print 2>/dev/null)"
	visto=0
	for pata in session-numbers hub-web-fidelity; do
		command grep -qx "  $pata" <<<"$salida" && visto=$((visto + 1))
	done
	[ "$visto" -eq 0 ] && ok "MUTANTE 6: sin el grupo del prefijo, las dos se pierden" \
	                   || bad "MUTANTE 6: el grupo no es lo que las hace visibles" "aun visibles=$visto"
fi

# --- LOS DOS MODOS NUEVOS (`--print-heavy`, `--print-ci-only`) NOMBRAN LO QUE `--print` CUENTA.
# --- `--print` daba el numero —«invocadas y NO en el rotulo: 13», «SOLO-CI (28)»— y sin nombres esa
# --- cifra no sirve para actuar; `scripts/pre-verify-tanda.sh` los consume. Se atan a las MISMAS
# --- cifras del mismo modo: si un dia derivan, aqui se ve, y no en la herramienta que los usa.
restore
salida="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print 2>/dev/null)"
esperado_h="$(printf '%s\n' "$salida" | sed -n 's/.*invocadas y NO en el rotulo: \([0-9]*\).*/\1/p' | head -1)"
esperado_c="$(printf '%s\n' "$salida" | sed -n 's/.*--- SOLO-CI (\([0-9]*\)).*/\1/p' | head -1)"
real_h="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print-heavy 2>/dev/null | grep -c . || true)"
real_c="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print-ci-only 2>/dev/null | grep -c . || true)"
[ -n "$esperado_h" ] && [ -n "$esperado_c" ] \
	&& ok "las cifras de --print se pudieron leer" "heavy=$esperado_h ci=$esperado_c" \
	|| bad "no pude leer las cifras de --print: el caso no mediria nada"
[ "$real_h" = "$esperado_h" ] && ok "--print-heavy nombra tantas como --print cuenta" "$real_h" \
                             || bad "--print-heavy no cuadra con --print" "print=$esperado_h heavy=$real_h"
[ "$real_c" = "$esperado_c" ] && ok "--print-ci-only nombra tantas como --print cuenta" "$real_c" \
                             || bad "--print-ci-only no cuadra con --print" "print=$esperado_c ci=$real_c"
# Y que no imprimen NADA mas que nombres: una cabecera colada convierte la lista en una pata falsa.
primera="$(cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --print-heavy 2>/dev/null | head -1)"
case "$primera" in
*' '*|*:*=*) bad "--print-heavy cuela algo que no es un nombre de pata" "$primera" ;;
*)           ok "--print-heavy imprime SOLO nombres" "$primera" ;;
esac

# --- UN FLAG DESCONOCIDO NO CAE AL MODO NORMAL. Hasta el 2026-09-03 cualquier argumento distinto
# --- de `--print` se ignoraba en silencio, y eso convierte «esta version no sabe hacer eso» en
# --- «aqui tienes otra cosa»: `pre-verify-tanda.sh` pidio `--print-heavy` a un arbol anterior y se
# --- llevo el resumen como si fueran nombres de pata.
restore
rc="$( (cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh --inventado >"$WORK/out" 2>&1); echo $? )"
[ "$rc" = 2 ] && ok "flag desconocido -> 2, no el modo normal" "rc=$rc" \
              || bad "flag desconocido: esperaba 2" "rc=$rc"
command grep -q 'opcion desconocida' "$WORK/out" \
	&& ok "y lo dice por su nombre" \
	|| { bad "no nombra la opcion desconocida"; sed 's/^/       /' "$WORK/out" | head -2; }

# --- PFX-GRAMMAR (contraste sol max, 2026-09-03). El censo reconoce TEXTO, no ordenes de shell, y
# --- el informe enumero tres formas que se le escapan. Dejaba abierta una DECISION DE FORMA: o el
# --- censo consume una representacion shell consciente de comillas y heredocs, o se declara una
# --- sintaxis canonica estrecha y se RECHAZA lo que no quepa. Elegida la segunda, y se dice por
# --- que: el gancho es nuestro fichero, un parser de shell de verdad es otro proyecto, y «ensanchar
# --- otra expresion regular no cierra la clase» — lo dice el propio informe.
# ---
# --- Asi que estos casos NO exigen que las formas raras se cuenten: exigen que se RECHACEN con su
# --- nombre. Una forma que el censo no sabe representar es una pata que no cuenta, y una pata que
# --- no cuenta no sale en ninguna lista ni en ningun rojo.
restore
printf '%s\n' "A='dos palabras' task lint:quoted-prefix" >>"$WORK/t/.githooks/pre-push"
rc="$( (cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh >"$WORK/out" 2>&1); echo $? )"
[ "$rc" = 1 ] && ok "PFX-GRAMMAR: valor con espacios -> rechazado" "rc=$rc" \
              || bad "PFX-GRAMMAR: valor con espacios deberia rechazarse" "rc=$rc"
command grep -q 'quoted-prefix' "$WORK/out" && ok "y la nombra" || bad "no nombra la linea"

restore
printf '%s\n' "env C=3 task lint:env-prefix" >>"$WORK/t/.githooks/pre-push"
rc="$( (cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh >"$WORK/out" 2>&1); echo $? )"
[ "$rc" = 1 ] && ok "PFX-GRAMMAR: prefijo con env -> rechazado" "rc=$rc" \
              || bad "PFX-GRAMMAR: prefijo con env deberia rechazarse" "rc=$rc"

# --- Y el heredoc: TEXTO, no una orden. Ni se cuenta ni se rechaza — se ignora, que es lo correcto.
restore
{ printf '%s\n' "cat <<'EJEMPLO'"; printf '%s\n' "D=4 task lint:heredoc-falso"; printf '%s\n' "EJEMPLO"; } \
	>>"$WORK/t/.githooks/pre-push"
rc="$( (cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh >"$WORK/out" 2>&1); echo $? )"
[ "$rc" = 0 ] && ok "PFX-GRAMMAR: dentro de un heredoc no es una invocacion" "rc=$rc" \
              || { bad "PFX-GRAMMAR: el heredoc no deberia alterar el veredicto" "rc=$rc"; sed 's/^/       /' "$WORK/out" | head -3; }
command grep -q 'heredoc-falso' "$WORK/out" \
	&& bad "el texto del heredoc se colo en el censo" \
	|| ok "y su texto no aparece en ninguna lista"

# --- Mutante: se le quita al filtro la conciencia de heredoc. El texto vuelve a contarse.
restore
python3 - "$WORK/t/scripts/check-gate-parity.sh" <<'PFXM'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
v = 'sin_comentarios() { sin_heredocs "$1" | sed'
if v not in s:
    sys.exit(3)
open(p, "w", encoding="utf-8").write(s.replace(v, 'sin_comentarios() { cat "$1" | sed', 1))
PFXM
{ printf '%s\n' "cat <<'EJEMPLO'"; printf '%s\n' "D=4 task lint:heredoc-falso"; printf '%s\n' "EJEMPLO"; } \
	>>"$WORK/t/.githooks/pre-push"
rc="$( (cd "$WORK/t" && OLIVARES_ROOT="$WORK/t" bash scripts/check-gate-parity.sh >"$WORK/out" 2>&1); echo $? )"
command grep -q 'heredoc-falso' "$WORK/out" \
	&& ok "(M) sin conciencia de heredoc, el texto SI se cuela" \
	|| bad "(M) el mutante no cambia nada: el caso de arriba no mide el filtro" "rc=$rc"

printf '\ntest-check-gate-parity: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
