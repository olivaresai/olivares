#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Batería de check-run-table.sh — hermética: fixtures JSON, sin red.
set -u -o pipefail
RAIZ=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SUT="${SUT:-$RAIZ/scripts/check-run-table.sh}"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/crt.XXXXXX"); trap 'rm -rf "$TMP"' EXIT
PASS=0; FAIL=0
check(){ if [ "$2" = "$3" ]; then PASS=$((PASS+1)); printf 'ok   %-58s %s\n' "$1" "$3"
         else FAIL=$((FAIL+1)); printf 'FAIL %-58s esperaba [%s], dio [%s]\n' "$1" "$2" "$3"; fi; }
# ⛔ REGLAS DE FIXTURE: la bateria no puede leer el predicado compartido REAL — pasaria o fallaria
# segun lo que anadiera hoy. Trae las suyas, con los MISMOS literales que el fichero real.
cat > "$TMP/reglas.txt" <<'RG'
exacto:"report failure (PR comment, or issue on a main push; PAT-blind)"
exacto:"NOT APPLICABLE notice — the cloud control-plane suite did not run"
RG
corre(){ OLIVARES_TABLA_JSON="$1" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$SUT" 1 > "$TMP/out" 2>&1; echo $?; }

paso(){ printf '{"name":"%s","status":"completed","conclusion":"%s","started_at":"2026-08-30T06:00:%02dZ","completed_at":"2026-08-30T06:00:%02dZ"}' "$1" "$2" "$3" "$4"; }

# 1 · corrida CERRADA y limpia: los saltos ESTRUCTURALES no encienden nada
cat > "$TMP/limpia.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "Run actions/checkout" success 0 5),
  $(paso "unit tests" success 5 30),
  $(paso "report failure (PR comment, or issue on a main push; PAT-blind)" skipped 30 30),
  $(paso "Post Run actions/checkout" skipped 30 30)]}]}
J
check "(1) saltos ESTRUCTURALES -> rc 0" 0 "$(corre "$TMP/limpia.json")"
check "(1b) y los cuenta aparte, sin avisar" 0 "$( grep -q 'estructural(es): informador/Post Run' "$TMP/out"; echo $? )"
check "(1c) y el paso más caro es el de verdad" 0 "$( grep -q 'más caro: unit tests' "$TMP/out"; echo $? )"
check "(1d) el ORIGINAL no los marca como NO estructurales (control positivo del 8c)" 0 \
  "$( grep -q "salto(s) NO estructural(es):" "$TMP/out" && echo "el original ya los marca: el 8c no acreditaria nada" || echo 0 )"

# 2 · un salto que SÍ cuesta cobertura
cat > "$TMP/perdida.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "unit tests" success 0 30),
  $(paso "build the embedded console" skipped 30 30),
  $(paso "report failure (PR comment, or issue on a main push; PAT-blind)" skipped 30 30)]}]}
J
check "(2) un salto NO estructural -> rc 1" 1 "$(corre "$TMP/perdida.json")"
check "(2b) y lo NOMBRA" 0 "$( grep -q 'NO estructural(es): build the embedded console' "$TMP/out"; echo $? )"

# 3 · ⛔ rc 2 MIENTRAS EL RUN ESTÉ EN VUELO — Midió 14 jobs a las 06:12Z y 15 al cerrar
# ⛔ EL JOB EN VUELO TRAE UN PASO, y eso decide si el caso vale. Con `steps: []` lo cazaba primero
# la guarda de CERO PASOS, asi que el mutante de la guarda de vuelo «moria» por la de al lado y
# acreditaba cero — el defecto exacto que me avisó. Un job `in_progress` real SI trae pasos.
cat > "$TMP/vuelo.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[$(paso "x" success 0 10)]},
         {"name":"b","status":"in_progress","conclusion":null,"steps":[$(paso "y" success 0 5)]}]}
J
check "(3) run EN VUELO -> 2, no una tabla a medias" 2 "$(corre "$TMP/vuelo.json")"
check "(3b) y dice cuántos jobs faltan" 0 "$( grep -q '1 de 2 job(s) siguen en vuelo' "$TMP/out"; echo $? )"

# 4-6 · FAIL-CLOSED
printf '{"run":{"status":"completed"},"jobs":[]}' > "$TMP/cero.json"
check "(4) cero jobs -> 2" 2 "$(corre "$TMP/cero.json")"
printf 'no soy json' > "$TMP/roto.json"
check "(5) JSON ilegible -> 2" 2 "$(corre "$TMP/roto.json")"
check "(6) fixture inexistente -> 2" 2 "$( OLIVARES_TABLA_JSON=/no/existe bash "$SUT" 1 >/dev/null 2>&1; echo $? )"
check "(7) sin run-id -> 2" 2 "$( bash "$SUT" >/dev/null 2>&1; echo $? )"

# 9 · ⛔ FALSO NEGATIVO POR PREFIJO: un paso critico que EMPIEZA como el informador
cat > "$TMP/colision.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "Run actions/checkout" success 0 5),
  $(paso "report failure integration coverage" skipped 5 5),
  $(paso "report failure (PR comment, or issue on a main push; PAT-blind)" skipped 5 5)]}]}
J
check "(9) 'report failure integration coverage' NO se exime" 1 "$(corre "$TMP/colision.json")"
check "(9b) y lo nombra" 0 "$( grep -q 'integration coverage' "$TMP/out"; echo $? )"
check "(9c) y el informador EXACTO sí se exime" 0 "$( grep -q '1 salto(s) estructural(es)' "$TMP/out"; echo $? )"

# 10 · un `Post Run X` HUERFANO (sin su `Run X`) no es estructural
cat > "$TMP/huerfano.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "unit tests" success 0 5),
  $(paso "Post Run actions/setup-node@abc" skipped 5 5)]}]}
J
check "(10) un 'Post Run' sin su 'Run' NO se exime" 1 "$(corre "$TMP/huerfano.json")"
cat > "$TMP/pareja.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "Run actions/setup-node@abc" success 0 5),
  $(paso "Post Run actions/setup-node@abc" skipped 5 5)]}]}
J
check "(10b) con su pareja SÍ se exime" 0 "$(corre "$TMP/pareja.json")"

# 11 · un job VALIDO sin `steps` -> 2, no CLEAN
printf '{"run":{"status":"completed"},"jobs":[{"name":"x","status":"completed","conclusion":"success"}]}' > "$TMP/sinsteps.json"
check "(11) job sin 'steps' -> 2, no 0" 2 "$(corre "$TMP/sinsteps.json")"

# 12-14 · LOS TRES ESTADOS DE `steps`, con TRES veredictos distintos
printf '{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success"}]}' > "$TMP/aus.json"
printf '{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":null}]}' > "$TMP/nul.json"
printf '{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[]}]}' > "$TMP/vac.json"
check "(12) steps AUSENTE -> 2" 2 "$(corre "$TMP/aus.json")"
check "(12b) y con MI mensaje, no el de jq" 0 "$( grep -q 'sin lista' "$TMP/out"; echo $? )"
check "(13) steps: null -> 2, mismo sitio que ausente" 2 "$(corre "$TMP/nul.json")"
check "(14) steps: [] -> 2 con mensaje DISTINTO" 2 "$(corre "$TMP/vac.json")"
check "(14b) y su mensaje habla de CERO pasos" 0 "$( grep -q 'CERO pasos' "$TMP/out"; echo $? )"

# 15 · el predicado compartido AUSENTE no se degrada a «sin reglas»
check "(15) sin fichero de reglas -> 2, no 'todo sustantivo'" 2 \
  "$( OLIVARES_TABLA_JSON="$TMP/limpia.json" OLIVARES_SKIPS_FILE=/no/existe bash "$SUT" 1 >/dev/null 2>&1; echo $? )"

# 16 · MUTANTE · la guarda de «run en vuelo». Tenia caso positivo y NINGUN mutante: un caso que
# comprueba «el real da 2» no distingue «la guarda funciona» de «otra cosa da 2 por ella».
cat > "$TMP/mutv.py" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
v = 'if [ "${VUELO:-0}" -gt 0 ]; then'
n = 'if false; then'
assert o.count(v) == 1, "el patron del mutante no casa"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mutv.py" "$SUT" "$TMP/mv.sh" || { echo "MUTANTE NO FABRICADO"; exit 1; }
check "(16a) el mutante de la guarda de vuelo REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mv.sh" && echo 1 || echo 0 )"
OLIVARES_TABLA_JSON="$TMP/vuelo.json" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$TMP/mv.sh" 1 > "$TMP/mvo" 2>&1
MVC=$?
check "(16b) MUTANTE 'sin guarda de vuelo' es CAZADO por su rc" 0 \
  "$( [ "$MVC" -ne 2 ] && echo 0 || echo "el mutante siguio dando 2: la guarda de al lado lo tapa" )"
check "(16c) y por su MENSAJE: deja de nombrar los jobs en vuelo (lo nombra el 3b)" 0 \
  "$( grep -q "siguen en vuelo" "$TMP/mvo" && echo "sigue nombrandolos: el rc cambio por otra via" || echo 0 )"

# 8 · MUTANTE · tratar los saltos estructurales como pérdida de cobertura
# ⛔ HEREDOC ENTRECOMILLADO: sin las comillas del delimitador, bash expande `$todos` y `$skip`
# DENTRO del guion del mutante y lo deja roto («unbound variable»), o sea el mutante no se fabrica
# y el caso falla por el banco, no por el sujeto. Tercera vez esta noche con heredocs anidados.
cat > "$TMP/mut.py" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
v = '      | not))) as $skip |'
n = '      ))) as $skip |'
assert o.count(v) == 1, "el patron del mutante no casa"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mut.py" "$SUT" "$TMP/m.sh" || { echo "MUTANTE NO FABRICADO"; exit 1; }
check "(8a) el mutante REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/m.sh" && echo 1 || echo 0 )"
OLIVARES_TABLA_JSON="$TMP/limpia.json" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$TMP/m.sh" 1 > "$TMP/mo" 2>&1
MSC=$?
check "(8b) MUTANTE 'todo salto cuenta' es CAZADO por su rc" 0 \
  "$( [ "$MSC" -eq 1 ] && echo 0 || echo "el mutante siguio dando $MSC: caso invalido" )"
check "(8c) y por su MENSAJE: marca el 'Post Run' como NO estructural (control positivo, el 1d)" 0 \
  "$( grep -q "salto(s) NO estructural(es):.*Post Run actions/checkout" "$TMP/mo" && echo 0 || echo "no nombro el Post Run: el rc 1 llego por otra via" )"

# 17 · A-01 · el RUN en vuelo con todos sus jobs actuales terminados
printf '{"run":{"status":"in_progress"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[{"name":"x","conclusion":"success","started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:00:10Z"}]}]}' > "$TMP/ventana.json"
check "(17) run in_progress con TODOS sus jobs cerrados -> 2" 2 "$(corre "$TMP/ventana.json")"
check "(17b) y su mensaje habla del RUN, no de los jobs" 0 "$( grep -q "el run .* esta en 'in_progress'" "$TMP/out"; echo $? )"
printf '{"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[]}]}' > "$TMP/sinrun.json"
check "(18) un fixture SIN run.status -> 2, no se supone cerrado" 2 "$(corre "$TMP/sinrun.json")"

# ⛔ A-03 · LOS MUTANTES SE JUZGAN POR SU MENSAJE, no solo por el rc. Un rc puede coincidir por otra
# via —lo vivi con el mutante del kill-after— y entonces el caso acredita cero.
cat > "$TMP/mutr.py" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
v = 'if [ "$EST" != "completed" ]; then'
n = 'if false; then'
assert o.count(v) == 1, "el patron del mutante no casa"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mutr.py" "$SUT" "$TMP/mr.sh" || { echo "MUTANTE NO FABRICADO"; exit 1; }
check "(19a) el mutante de la guarda del RUN REALMENTE difiere" 0 "$( cmp -s "$SUT" "$TMP/mr.sh" && echo 1 || echo 0 )"
OLIVARES_TABLA_JSON="$TMP/ventana.json" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$TMP/mr.sh" 1 > "$TMP/mro" 2>&1
MRC=$?
check "(19b) MUTANTE 'sin guarda de RUN' es CAZADO por su rc" 0 "$( [ "$MRC" -ne 2 ] && echo 0 || echo "siguio dando 2" )"
check "(19c) y por su MENSAJE: deja de nombrar el estado del run" 0 \
  "$( grep -q "esta en 'in_progress'" "$TMP/mro" && echo "sigue nombrandolo" || echo 0 )"

echo
echo "check-run-table selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
