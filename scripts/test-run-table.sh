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
         else FAIL=$((FAIL+1)); printf 'FAIL %-58s expected [%s], got [%s]\n' "$1" "$2" "$3"; fi; }
# Fixture rules: do not read the live shared predicate, which changes with work.
# Use local fixture rules containing the same literals as the real data file.
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
check "(1) STRUCTURAL skips -> rc 0" 0 "$(corre "$TMP/limpia.json")"
check "(1b) counts them separately, without warnings" 0 "$( grep -q 'structural skipped step(s): reporter/Post Run' "$TMP/out"; echo $? )"
check "(1c) the most expensive step is the actual one" 0 "$( grep -q 'slowest: unit tests' "$TMP/out"; echo $? )"
check "(1d) ORIGINAL does not flag them as NONstructural (positive control for 8c)" 0 \
  "$( grep -q "NONSTRUCTURAL skipped step(s):" "$TMP/out" && echo "original already flags them: 8c would prove nothing" || echo 0 )"

# 2 · un salto que SÍ cuesta cobertura
cat > "$TMP/perdida.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "unit tests" success 0 30),
  $(paso "build the embedded console" skipped 30 30),
  $(paso "report failure (PR comment, or issue on a main push; PAT-blind)" skipped 30 30)]}]}
J
check "(2) a NONstructural skip -> rc 1" 1 "$(corre "$TMP/perdida.json")"
check "(2b) NAMES it" 0 "$( grep -q 'NONSTRUCTURAL skipped step(s): build the embedded console' "$TMP/out"; echo $? )"

# 3 · Return rc 2 while the run is in progress (14 jobs at 06:12Z, 15 at closure).
# The running job needs a step: with steps: [] the zero-step guard killed the mutant
# before the in-progress guard, proving the wrong property. Real in_progress jobs
# have steps; isolate that condition here, as finding required.
cat > "$TMP/vuelo.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[$(paso "x" success 0 10)]},
         {"name":"b","status":"in_progress","conclusion":null,"steps":[$(paso "y" success 0 5)]}]}
J
check "(3) IN-FLIGHT run -> 2, not a partial table" 2 "$(corre "$TMP/vuelo.json")"
check "(3b) reports how many jobs remain" 0 "$( grep -q '1 of 2 job(s) are still running' "$TMP/out"; echo $? )"

# 4-6 · FAIL-CLOSED
printf '{"run":{"status":"completed"},"jobs":[]}' > "$TMP/cero.json"
check "(4) zero jobs -> 2" 2 "$(corre "$TMP/cero.json")"
printf 'no soy json' > "$TMP/roto.json"
check "(5) unreadable JSON -> 2" 2 "$(corre "$TMP/roto.json")"
check "(6) nonexistent fixture -> 2" 2 "$( OLIVARES_TABLA_JSON=/no/existe bash "$SUT" 1 >/dev/null 2>&1; echo $? )"
check "(7) missing run-id -> 2" 2 "$( bash "$SUT" >/dev/null 2>&1; echo $? )"

# 9 · ⛔ FALSO NEGATIVO POR PREFIJO: un paso critico que EMPIEZA como el informador
cat > "$TMP/colision.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "Run actions/checkout" success 0 5),
  $(paso "report failure integration coverage" skipped 5 5),
  $(paso "report failure (PR comment, or issue on a main push; PAT-blind)" skipped 5 5)]}]}
J
check "(9) 'report failure integration coverage' is NOT exempt" 1 "$(corre "$TMP/colision.json")"
check "(9b) names it" 0 "$( grep -q 'integration coverage' "$TMP/out"; echo $? )"
check "(9c) EXACT reporter IS exempt" 0 "$( grep -q '1 structural skipped step(s)' "$TMP/out"; echo $? )"

# 10 · un `Post Run X` HUERFANO (sin su `Run X`) no es estructural
cat > "$TMP/huerfano.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "unit tests" success 0 5),
  $(paso "Post Run actions/setup-node@abc" skipped 5 5)]}]}
J
check "(10) a 'Post Run' without its 'Run' is NOT exempt" 1 "$(corre "$TMP/huerfano.json")"
cat > "$TMP/pareja.json" <<J
{"run":{"status":"completed"},"jobs":[{"name":"web","status":"completed","conclusion":"success","steps":[
  $(paso "Run actions/setup-node@abc" success 0 5),
  $(paso "Post Run actions/setup-node@abc" skipped 5 5)]}]}
J
check "(10b) WITH its pair it IS exempt" 0 "$(corre "$TMP/pareja.json")"

# 11 · un job VALIDO sin `steps` -> 2, no CLEAN
printf '{"run":{"status":"completed"},"jobs":[{"name":"x","status":"completed","conclusion":"success"}]}' > "$TMP/sinsteps.json"
check "(11) job without 'steps' -> 2, not 0" 2 "$(corre "$TMP/sinsteps.json")"

# 12-14 · LOS TRES ESTADOS DE `steps`, con TRES veredictos distintos
printf '{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success"}]}' > "$TMP/aus.json"
printf '{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":null}]}' > "$TMP/nul.json"
printf '{"run":{"status":"completed"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[]}]}' > "$TMP/vac.json"
check "(12) MISSING steps -> 2" 2 "$(corre "$TMP/aus.json")"
check "(12b) uses OUR message, not jq's" 0 "$( grep -q 'missing or null' "$TMP/out"; echo $? )"
check "(13) steps: null -> 2, same path as missing" 2 "$(corre "$TMP/nul.json")"
check "(14) steps: [] -> 2 with a DIFFERENT message" 2 "$(corre "$TMP/vac.json")"
check "(14b) message reports ZERO steps" 0 "$( grep -q 'job(s) with no steps' "$TMP/out"; echo $? )"

# 15 · el predicado compartido AUSENTE no se degrada a «sin reglas»
check "(15) missing rules file -> 2, not 'all substantive'" 2 \
  "$( OLIVARES_TABLA_JSON="$TMP/limpia.json" OLIVARES_SKIPS_FILE=/no/existe bash "$SUT" 1 >/dev/null 2>&1; echo $? )"

# 16 · MUTANTE · la guarda de «run en vuelo». Tenia caso positivo y NINGUN mutante: un caso que
# comprueba «el real da 2» no distingue «la guarda funciona» de «otra cosa da 2 por ella».
cat > "$TMP/mutv.py" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
v = 'if [ "${VUELO:-0}" -gt 0 ]; then'
n = 'if false; then'
assert o.count(v) == 1, "mutant pattern does not match"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mutv.py" "$SUT" "$TMP/mv.sh" || { echo "MUTANT NOT CONSTRUCTED"; exit 1; }
check "(16a) in-flight guard mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mv.sh" && echo 1 || echo 0 )"
OLIVARES_TABLA_JSON="$TMP/vuelo.json" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$TMP/mv.sh" 1 > "$TMP/mvo" 2>&1
MVC=$?
check "(16b) MUTANT 'no in-flight guard' is CAUGHT by its rc" 0 \
  "$( [ "$MVC" -ne 2 ] && echo 0 || echo "mutant still returned 2: adjacent guard masks it" )"
check "(16c) and MESSAGE: no longer names in-flight jobs (named by 3b)" 0 \
  "$( grep -q "are still running" "$TMP/mvo" && echo "still names them: rc changed through another path" || echo 0 )"

# 8 · MUTANTE · tratar los saltos estructurales como pérdida de cobertura
# ⛔ HEREDOC ENTRECOMILLADO: sin las comillas del delimitador, bash expande `$todos` y `$skip`
# DENTRO del guion del mutante y lo deja roto («unbound variable»), o sea el mutante no se fabrica
# y el caso falla por el banco, no por el sujeto. Tercera vez esta noche con heredocs anidados.
cat > "$TMP/mut.py" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
v = '      | not))) as $skip |'
n = '      ))) as $skip |'
assert o.count(v) == 1, "mutant pattern does not match"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mut.py" "$SUT" "$TMP/m.sh" || { echo "MUTANT NOT CONSTRUCTED"; exit 1; }
check "(8a) mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/m.sh" && echo 1 || echo 0 )"
OLIVARES_TABLA_JSON="$TMP/limpia.json" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$TMP/m.sh" 1 > "$TMP/mo" 2>&1
MSC=$?
check "(8b) MUTANT 'all skips count' is CAUGHT by its rc" 0 \
  "$( [ "$MSC" -eq 1 ] && echo 0 || echo "mutant still returned $MSC: invalid case" )"
check "(8c) and MESSAGE: flags 'Post Run' as NONstructural (positive control, 1d)" 0 \
  "$( grep -q "NONSTRUCTURAL skipped step(s):.*Post Run actions/checkout" "$TMP/mo" && echo 0 || echo "did not name Post Run: rc 1 came through another path" )"

# 17 · A-01 · el RUN en vuelo con todos sus jobs actuales terminados
printf '{"run":{"status":"in_progress"},"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[{"name":"x","conclusion":"success","started_at":"2026-08-30T06:00:00Z","completed_at":"2026-08-30T06:00:10Z"}]}]}' > "$TMP/ventana.json"
check "(17) run in_progress with ALL jobs completed -> 2" 2 "$(corre "$TMP/ventana.json")"
check "(17b) message names the RUN, not the jobs" 0 "$( grep -q "run .* is 'in_progress'" "$TMP/out"; echo $? )"
printf '{"jobs":[{"name":"a","status":"completed","conclusion":"success","steps":[]}]}' > "$TMP/sinrun.json"
check "(18) fixture WITHOUT run.status -> 2, not assumed completed" 2 "$(corre "$TMP/sinrun.json")"

# ⛔ A-03 · LOS MUTANTES SE JUZGAN POR SU MENSAJE, no solo por el rc. Un rc puede coincidir por otra
# via —lo vivi con el mutante del kill-after— y entonces el caso acredita cero.
cat > "$TMP/mutr.py" <<'PYEOF'
import sys
o = open(sys.argv[1]).read()
v = 'if [ "$EST" != "completed" ]; then'
n = 'if false; then'
assert o.count(v) == 1, "mutant pattern does not match"
open(sys.argv[2], "w").write(o.replace(v, n, 1))
PYEOF
python3 "$TMP/mutr.py" "$SUT" "$TMP/mr.sh" || { echo "MUTANT NOT CONSTRUCTED"; exit 1; }
check "(19a) RUN guard mutant ACTUALLY differs" 0 "$( cmp -s "$SUT" "$TMP/mr.sh" && echo 1 || echo 0 )"
OLIVARES_TABLA_JSON="$TMP/ventana.json" OLIVARES_SKIPS_FILE="$TMP/reglas.txt" bash "$TMP/mr.sh" 1 > "$TMP/mro" 2>&1
MRC=$?
check "(19b) MUTANT 'no RUN guard' is CAUGHT by its rc" 0 "$( [ "$MRC" -ne 2 ] && echo 0 || echo "still returned 2" )"
check "(19c) and MESSAGE: no longer names the run status" 0 \
  "$( grep -q "is 'in_progress'" "$TMP/mro" && echo "still names it" || echo 0 )"

echo
echo "check-run-table selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
