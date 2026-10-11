#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# Bateria del PASO de CI `leg-int-12`: se extrae su `run:` del workflow y se ejerce contra un `task`
# FALSO, bajo el MISMO shell que usa GitHub. No construye nada, no toca la red.
#
# ⛔ POR QUE EXISTE, con la corrida delante. En la 33685919433 (job 100433314803) este paso salio con
# `exit code 2` y **CERO lineas de salida**: ni la del gate, ni ninguno de sus tres `::notice`, ni el
# `::error` del `else`. El log solo mostraba el ECO del guion, que se lee como si el `::error` se
# hubiera emitido — y por eso el hallazgo llego descrito como «termina con "no puedo saber que mitad
# se midio"», cuando esa linea NUNCA se imprimio.
#
# La causa: GitHub corre el `run:` con `bash --noprofile --norc -e -o pipefail`, y bajo `-e` una
# ASIGNACION cuya sustitucion de comandos falla aborta el shell EN ESA LINEA. Las cuatro ramas
# —escritas justamente para decir que mitad se midio— no corrian nunca. El diagnostico se perdia
# exactamente cuando hacia falta.
#
# Un paso de CI es codigo, y hasta hoy era el unico codigo del carril sin banco.
#
# LAS TRES RESPUESTAS DEL PASO, y su severidad, que es lo que el caso (3) ancla: la mitad
# ESTATICA medida con la VIVA sin medir es un `::warning::` (evidencia que falta, dicha), las dos
# mitades o «no aplicable» son `::notice::`, y una salida que no se sabe leer es `::error::` + 2.
# Un desconocido nunca se convierte en PASS (caso 5), y un mismatch de severidad tampoco (3M).
set -uo pipefail
export LC_ALL=C

RAIZ="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
WF="$RAIZ/.github/workflows/mainline-ci.yml"
BASE="$(mktemp -d "${TMPDIR:-/tmp}/int12step.XXXXXX")" || exit 2
trap 'rm -rf "$BASE"' EXIT INT TERM

pasados=0; fallados=0
check() { # etiqueta esperado obtenido
	if [ "$2" = "$3" ]; then
		printf '  ok   %-56s %s\n' "$1" "$3"; pasados=$((pasados + 1))
	else
		printf '  FAIL %-56s expected=%s actual=%s\n' "$1" "$2" "$3"; fallados=$((fallados + 1))
	fi
}

# El `run:` se saca del YAML por su `id`, no por numero de linea: un paso que se mueve no puede
# dejar mudo a su banco.
python3 - "$WF" "$BASE/run.sh" <<'PY'
import sys, yaml
wf = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
# ⛔ EL id TIENE QUE SER UNICO. Tomar la primera coincidencia es fiarse de que no haya dos: si
# alguien duplica el paso —copiando el bloque para otro job—, este banco mediria uno y CI correria
# el otro, y el banco seguiria verde. Lo levanto el contraste sol max (CI-DUP-ID).
hallados = [p["run"] for job in wf.get("jobs", {}).values()
            for p in (job.get("steps", []) or []) if p.get("id") == "leg-int-12"]
if len(hallados) == 0:
    sys.stderr.write("cannot find the workflow step with id leg-int-12\n")
    sys.exit(3)
if len(hallados) > 1:
    sys.stderr.write("found %d steps with id leg-int-12: the test could check a different step than CI runs\n"
                     % len(hallados))
    sys.exit(4)
open(sys.argv[2], "w", encoding="utf-8").write(hallados[0])
sys.exit(0)
PY
[ -s "$BASE/run.sh" ] || { echo "could not extract the step run block"; exit 2; }

# `task` falso: imprime el fixture y sale con el rc que se le pida.
mkdir -p "$BASE/bin"
cat >"$BASE/bin/task" <<'TASK'
#!/usr/bin/env bash
cat "${FAKE_TASK_OUT:?}"
exit "${FAKE_TASK_RC:-0}"
TASK
chmod +x "$BASE/bin/task"

# ⛔ EL MISMO SHELL QUE GITHUB, literal: `-e` es la mitad del defecto que esto vigila, asi que
# correrlo sin `-e` seria medir otro programa.
# ⛔ EL SHELL SE DERIVA DEL WORKFLOW, no se escribe aqui. Escribirlo a mano es leer el YAML y el
# ejecutor por caminos separados: el dia que el job declare otro `shell:`, este banco seguiria
# midiendo el de siempre y su verde no diria nada del paso real. Lo levanto el contraste sol max
# (CI-SHELL-DRIFT). Si el workflow no declara ninguno, se usa el de GitHub POR DEFECTO para `run:`
# en Linux —`bash --noprofile --norc -e -o pipefail {0}`— y se DICE que es el supuesto.
SHELL_PASO="$(python3 - "$WF" <<'PYSH'
import sys, yaml
wf = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
for job in wf.get("jobs", {}).values():
    for p in (job.get("steps", []) or []):
        if p.get("id") == "leg-int-12":
            print(p.get("shell") or (job.get("defaults", {}).get("run", {}) or {}).get("shell")
                  or (wf.get("defaults", {}).get("run", {}) or {}).get("shell") or "")
            sys.exit(0)
print("")
PYSH
)"
# ⛔ `shell: bash` ES UNA PALABRA CLAVE, NO UNA ORDEN. GitHub la traduce a
# `bash --noprofile --norc -eo pipefail {0}`, y esa traduccion es donde vive `-e`, que es LA MITAD
# del defecto que este banco vigila. Tomar el valor literal daba `sh -c "bash"` — un bash sin script
# que se queda leyendo su entrada: el banco colgo y salio 2/11. Las palabras clave se mapean, y
# cualquier otra se REHUSA en vez de suponerse.
case "$SHELL_PASO" in
'')     SHELL_PASO='bash --noprofile --norc -eo pipefail {0}'
        echo "  (the workflow declares no shell; using the GitHub default for Linux run blocks)" ;;
bash)   SHELL_PASO='bash --noprofile --norc -eo pipefail {0}' ;;
sh)     SHELL_PASO='sh -e {0}' ;;
*'{0}'*) : ;;  # una orden completa: se usa tal cual, que es lo que hace el runner
*)      echo "  ⛔ UNVERIFIED: shell '$SHELL_PASO' has no known expansion and no {0} placeholder" >&2
        exit 2 ;;
esac
echo "  step shell (derived from the workflow): $SHELL_PASO"
corre() { # corre <fixture> <rc del gate> -> rc del paso; salida en $BASE/out
	printf '%s\n' "$1" >"$BASE/fixture"
	# `{0}` es donde GitHub pone el fichero del script; se sustituye igual que hace el runner.
	local orden="${SHELL_PASO//\{0\}/$BASE/run.sh}"
	FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC="$2" PATH="$BASE/bin:$PATH" \
		sh -c "$orden" >"$BASE/out" 2>&1
	echo $?
}

# ─────────────── (1) el caso que la corrida real produjo: el gate no pudo mirar ────────────────
rc=$(corre 'check-int-12-no-land: COULD NOT LOOK — no act id in the environment' 2)
check "(1) check exits 2: the step preserves its exit code" 2 "$rc"
grep -q 'COULD NOT LOOK' "$BASE/out" && d=si || d=no
check "(1) check output reaches the log" si "$d"
grep -q '::error::INT-12: the check could not run and reported its cause' "$BASE/out" && d=si || d=no
check "(1) the reported cause is repeated" si "$d"
grep -q 'cannot determine which checks ran' "$BASE/out" && d=si || d=no
check "(1) a known cause is not reclassified as unknown" no "$d"

# ───────────────────────── (2)-(4) las tres mitades, cada una con su NOTICE ─────────────────────
rc=$(corre 'check-int-12-no-land: SCOPED — the record is excluded from export' 0)
grep -q '::notice::INT-12: NOT APPLICABLE' "$BASE/out" && d=si || d=no
check "(2) SCOPED -> not-applicable notice, rc 0" "si 0" "$d $rc"

# ⛔ (3) ES UN WARNING, NO UN NOTICE, desde 9e3b3aeb9d (2026-09-05, «scope INT-12 to its static CI
# evidence»): el lado ESTATICO se midio y la mitad VIVA contra el overlay NO SE HA MEDIDO en ese
# runner. Una mitad sin medir es evidencia que falta, y eso se anuncia con la severidad de lo que
# es —visible en la corrida— sin enrojecer el paso, porque esa mitad sigue siendo del gancho. Este
# banco esperaba el `::notice::` anterior y el preflight del 2026-09-05 lo midio 16/1: la
# expectativa envejecio, no el paso. Se exige la severidad Y la frase que dice lo que falta, y el
# mutante de abajo prueba que un paso que la rebajara a notice volveria a ponerse rojo aqui.
rc=$(corre 'check-int-12-no-land: NOTICE — live overlay remasure skipped: no sibling clone' 0)
grep -q '::warning::INT-12: STATIC checks completed' "$BASE/out" && d=si || d=no
check "(3) skipped live checks -> WARNING, rc 0" "si 0" "$d $rc"
grep -q 'NOT MEASURED' "$BASE/out" && d=si || d=no
check "(3) the warning discloses unmeasured LIVE checks" si "$d"
grep -q '::notice::INT-12: STATIC' "$BASE/out" && d=si || d=no
check "(3) missing evidence is not downgraded to a notice" no "$d"
# Mutante de severidad: el paso vuelve al `::notice::` de antes. Con la misma entrada, la marca
# de WARNING tiene que desaparecer — que es exactamente lo que el check de arriba dejaria de ver.
MUTSEV="$BASE/run-sev.sh"
python3 - "$BASE/run.sh" "$MUTSEV" <<'MSEV'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = 'echo "::warning::INT-12: STATIC checks completed'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, 'echo "::notice::INT-12: STATIC checks completed', 1))
MSEV
[ -s "$MUTSEV" ] || {
	echo "  ⛔ UNVERIFIED: the severity mutation did not match the step output" >&2
	echo '     `::warning::INT-12: STATIC checks completed`; update this test to match the emitted message.' >&2
	exit 2
}
cmp -s "$BASE/run.sh" "$MUTSEV" && d=NO-DIFIERE || d=ok
check "(3M) the severity mutation changes the step" ok "$d"
printf '%s\n' 'check-int-12-no-land: NOTICE — live overlay remasure skipped: no sibling clone' >"$BASE/fixture"
ordensev="${SHELL_PASO//\{0\}/$MUTSEV}"
FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC=0 PATH="$BASE/bin:$PATH" \
	sh -c "$ordensev" >"$BASE/out.sev" 2>&1
grep -q '::warning::INT-12: STATIC checks completed' "$BASE/out.sev" && d=si || d=no
check "(3M) downgrading to notice removes the WARNING marker" no "$d"

rc=$(corre 'check-int-12-no-land: 3 commits behind overlay-main pin' 0)
grep -q '::notice::INT-12: BOTH static and live checks completed' "$BASE/out" && d=si || d=no
check "(4) both sets of checks -> notice, rc 0" "si 0" "$d $rc"

# ───────────────── (5) lo genuinamente desconocido SIGUE rehusando: no se ha aflojado ───────────
rc=$(corre 'unrecognized step output' 0)
check "(5) unrecognized output -> 2, not 0" 2 "$rc"
grep -q 'cannot determine which checks ran' "$BASE/out" && d=si || d=no
check "(5) an unknown result is reported as unknown" si "$d"

# ────────────────────────────────── el mutante: la forma anterior ──────────────────────────────
# Se vuelve a la asignacion suelta, que es lo que habia. Bajo `-e` tiene que quedarse MUDA.
# ⛔ SUSTITUCION LITERAL, no `sed`: la linea lleva `$(`, `"` y `?`, y escaparla para BRE es justo
# donde un mutante deja de aplicarse sin decirlo. El banco lo comprueba abajo con `cmp`.
python3 - "$BASE/run.sh" "$BASE/run-mut.sh" <<'MUT'
import sys
nuevo = open(sys.argv[1], encoding="utf-8").read()
# ⛔ SIN SANGRIA: YAML DESANGRA el bloque `run: |`, asi que la linea que llega aqui NO lleva los
# diez espacios que tiene en el workflow. Con ellos el reemplazo no casaba, python salia 3 sin
# escribir nada, y el `cmp` de abajo leia «el fichero no existe» como «el mutante difiere»: un
# mutante inexistente dandose por aplicado, que es justo la clase que este banco vino a cerrar.
viejo = 'if output="$(task -x lint:int-12-no-land 2>&1)"; then rc=0; else rc=$?; fi'
if viejo not in nuevo:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    nuevo.replace(viejo, 'output="$(task -x lint:int-12-no-land 2>&1)"; rc=$?'))
MUT
# ⛔ NO ENCONTRAR EL OBJETIVO ES «NO HE PODIDO MIRAR», NO UN HALLAZGO, y por eso sale 2 y no 1.
# This test mutates the literal `if output="$(...)"` form.
# An equivalent `rc=0; output="$(...)" || rc=$?` form has the same behavior under `-e` but
# deja el reemplazo sin objetivo, y entonces la bateria no ha medido el paso: ha fallado en montarlo.
# Medido el 2026-09-03 sustituyendo la forma a proposito: sin esta linea salia 1 con tres FAIL, que se
# lee como «el paso esta mal» cuando lo que pasa es que el banco no supo aplicarse. Las dos cosas
# bloquean, pero solo una dice la verdad de lo que ocurrio.
[ -s "$BASE/run-mut.sh" ] || {
	echo "  ⛔ UNVERIFIED: the mutation did not match. This test requires the literal form" >&2
	echo '     `if output="$(task -x lint:int-12-no-land 2>&1)"; then rc=0; else rc=$?; fi`' >&2
	echo "     If the step uses an equivalent form, update the 'viejo' input above." >&2
	exit 2
}
cmp -s "$BASE/run.sh" "$BASE/run-mut.sh" && d=NO-DIFIERE || d=ok
check "(M) the mutation changes the step" ok "$d"
printf '%s\n' 'check-int-12-no-land: COULD NOT LOOK — no act id' >"$BASE/fixture"
FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC=2 PATH="$BASE/bin:$PATH" \
	bash --noprofile --norc -e -o pipefail "$BASE/run-mut.sh" >"$BASE/out.mut" 2>&1
rcm=$?
check "(M) the unguarded assignment makes the step exit 2" 2 "$rcm"
check "(M) the step produces no output, matching the original failure" 0 "$(wc -l <"$BASE/out.mut" | tr -d ' ')"

# ───── (6) SALIDA GRANDE: la clase 141, que es por lo que estos clasificadores son here-strings ──
#
# ⛔ `printf … | grep -q X` bajo `pipefail` devuelve **141 justo cuando ACIERTA**: `grep -q` cierra
# el tubo al primer casamiento y el `printf` de la izquierda se lleva un SIGPIPE. Con salida corta
# el `printf` cabe en el bufer del tubo y termina antes, asi que el defecto **no se ve**: hace falta
# una salida que no quepa. Por eso este caso existe y por eso es grande.
#
# El sintoma no es un error ruidoso: es que el clasificador **no reconoce una marca que SI esta**,
# reaches the `else` branch and exits 2 with "cannot determine which checks ran" — matching
# delante. Lo levanto el contraste sol max (CI-LARGE-141).
# La carga se fabrica SIN tuberia: `head … | tr … | fold` seria justo la clase que este caso mide,
# y `lint:sigpipe-booleans` la contaria — con razon.
GRANDE="check-int-12-no-land: SCOPED — the record is excluded from export
$(python3 -c 'print("\n".join("x" * 120 for _ in range(2500)))')"
rc=$(corre "$GRANDE" 0)
check "(6) marker at the start of LARGE output -> 0" 0 "$rc"
grep -q '::notice::INT-12: NOT APPLICABLE' "$BASE/out" && d=si || d=no
check "(6) a here-string recognizes the marker" si "$d"

# Mutante: se vuelve a la tuberia en el PRIMER clasificador. Con la salida grande tiene que dejar
# de reconocerla y caer al else.
MUT141="$BASE/run-141.sh"
python3 - "$BASE/run.sh" "$MUT141" <<'M141'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = """if grep -q 'check-int-12-no-land: SCOPED' <<<"$output"; then"""
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    # La tuberia del mutante se COMPONE, no se escribe: con el literal dentro,
    # `lint:sigpipe-booleans` la cuenta como una tuberia de ESTE fichero — y tiene razon en su
    # premisa, porque no puede saber que son datos de prueba. Un banco que prueba un detector tiene
    # que poder escribir lo que el detector busca sin declararlo el mismo.
    s.replace(v, """if printf '%s' "$output" """ + "|" + """ grep -q 'check-int-12-no-land: SCOPED'; then""", 1))
M141
[ -s "$MUT141" ] || { echo "  FAIL MUTATION 141 NOT WRITTEN"; fallados=$((fallados + 1)); }
cmp -s "$BASE/run.sh" "$MUT141" && d=NO-DIFIERE || d=ok
check "(6) the pipeline mutation changes the step" ok "$d"
printf '%s\n' "$GRANDE" >"$BASE/fixture"
orden141="${SHELL_PASO//\{0\}/$MUT141}"
FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC=0 PATH="$BASE/bin:$PATH" \
	sh -c "$orden141" >"$BASE/out.141" 2>&1
rc141=$?
check "(6) the pipeline fails to recognize the marker -> 2" 2 "$rc141"
grep -q 'cannot determine which checks ran' "$BASE/out.141" && d=si || d=no
check "(6) the step reports an unknown result despite the marker" si "$d"

echo "test-ci-int12-step: $pasados passed, $fallados failed"
[ "$fallados" -eq 0 ] || exit 1
exit 0
