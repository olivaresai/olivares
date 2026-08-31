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
sed -i '/run: pnpm --dir web run at:gate/d' "$WORK/t/.github/workflows/mainline-ci.yml"
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

printf '\ntest-check-gate-parity: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
