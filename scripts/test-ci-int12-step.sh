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
		printf '  FAIL %-56s esperado=%s obtenido=%s\n' "$1" "$2" "$3"; fallados=$((fallados + 1))
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
    sys.stderr.write("no encuentro el paso con id leg-int-12 en el workflow\n")
    sys.exit(3)
if len(hallados) > 1:
    sys.stderr.write("hay %d pasos con id leg-int-12: este banco mediria uno y CI correria otro\n"
                     % len(hallados))
    sys.exit(4)
open(sys.argv[2], "w", encoding="utf-8").write(hallados[0])
sys.exit(0)
PY
[ -s "$BASE/run.sh" ] || { echo "no pude extraer el run: del paso"; exit 2; }

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
        echo "  (el workflow no declara shell: se usa el de GitHub por defecto para run: en Linux)" ;;
bash)   SHELL_PASO='bash --noprofile --norc -eo pipefail {0}' ;;
sh)     SHELL_PASO='sh -e {0}' ;;
*'{0}'*) : ;;  # una orden completa: se usa tal cual, que es lo que hace el runner
*)      echo "  ⛔ NO HE PODIDO MIRAR: shell '$SHELL_PASO' sin traduccion conocida y sin {0}" >&2
        exit 2 ;;
esac
echo "  shell del paso (derivado del workflow): $SHELL_PASO"
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
check "(1) el gate sale 2: el paso conserva su codigo" 2 "$rc"
grep -q 'COULD NOT LOOK' "$BASE/out" && d=si || d=no
check "(1) y la salida del gate LLEGA al log" si "$d"
grep -q '::error::INT-12: el gate no pudo mirar, y dijo por que' "$BASE/out" && d=si || d=no
check "(1) y se repite su causa en vez de decir 'no se'" si "$d"
grep -q 'no puedo saber que mitad se midio' "$BASE/out" && d=si || d=no
check "(1) y NO se reetiqueta como causa desconocida" no "$d"

# ───────────────────────── (2)-(4) las tres mitades, cada una con su NOTICE ─────────────────────
rc=$(corre 'check-int-12-no-land: SCOPED — el acta no viaja' 0)
grep -q '::notice::INT-12: NO APLICABLE' "$BASE/out" && d=si || d=no
check "(2) SCOPED -> notice de no aplicable, rc 0" "si 0" "$d $rc"

# ⛔ (3) ES UN WARNING, NO UN NOTICE, desde 9e3b3aeb9d (2026-09-05, «scope INT-12 to its static CI
# evidence»): el lado ESTATICO se midio y la mitad VIVA contra el overlay NO SE HA MEDIDO en ese
# runner. Una mitad sin medir es evidencia que falta, y eso se anuncia con la severidad de lo que
# es —visible en la corrida— sin enrojecer el paso, porque esa mitad sigue siendo del gancho. Este
# banco esperaba el `::notice::` anterior y el preflight del 2026-09-05 lo midio 16/1: la
# expectativa envejecio, no el paso. Se exige la severidad Y la frase que dice lo que falta, y el
# mutante de abajo prueba que un paso que la rebajara a notice volveria a ponerse rojo aqui.
rc=$(corre 'check-int-12-no-land: NOTICE — live overlay remasure skipped: no sibling clone' 0)
grep -q '::warning::INT-12: lado ESTATICO medido' "$BASE/out" && d=si || d=no
check "(3) omision de la remedida viva -> su WARNING, rc 0" "si 0" "$d $rc"
grep -q 'NO SE HA MEDIDO' "$BASE/out" && d=si || d=no
check "(3) y dice que la mitad VIVA no se ha medido" si "$d"
grep -q '::notice::INT-12: lado ESTATICO' "$BASE/out" && d=si || d=no
check "(3) y NO la rebaja a notice" no "$d"
# Mutante de severidad: el paso vuelve al `::notice::` de antes. Con la misma entrada, la marca
# de WARNING tiene que desaparecer — que es exactamente lo que el check de arriba dejaria de ver.
MUTSEV="$BASE/run-sev.sh"
python3 - "$BASE/run.sh" "$MUTSEV" <<'MSEV'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = 'echo "::warning::INT-12: lado ESTATICO medido'
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(s.replace(v, 'echo "::notice::INT-12: lado ESTATICO medido', 1))
MSEV
[ -s "$MUTSEV" ] || {
	echo "  ⛔ NO HE PODIDO MIRAR: el mutante de severidad no caso: el paso ya no emite" >&2
	echo '     `::warning::INT-12: lado ESTATICO medido`; actualiza este banco a lo que emita.' >&2
	exit 2
}
cmp -s "$BASE/run.sh" "$MUTSEV" && d=NO-DIFIERE || d=ok
check "(3M) el mutante de severidad REALMENTE difiere" ok "$d"
printf '%s\n' 'check-int-12-no-land: NOTICE — live overlay remasure skipped: no sibling clone' >"$BASE/fixture"
ordensev="${SHELL_PASO//\{0\}/$MUTSEV}"
FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC=0 PATH="$BASE/bin:$PATH" \
	sh -c "$ordensev" >"$BASE/out.sev" 2>&1
grep -q '::warning::INT-12: lado ESTATICO medido' "$BASE/out.sev" && d=si || d=no
check "(3M) rebajado a notice, la marca de WARNING desaparece: el banco lo veria" no "$d"

rc=$(corre 'check-int-12-no-land: 3 commits behind overlay-main pin' 0)
grep -q '::notice::INT-12: las DOS mitades medidas' "$BASE/out" && d=si || d=no
check "(4) las dos mitades -> su notice, rc 0" "si 0" "$d $rc"

# ───────────────── (5) lo genuinamente desconocido SIGUE rehusando: no se ha aflojado ───────────
rc=$(corre 'algo que este paso no sabe leer' 0)
check "(5) salida irreconocible -> 2, no 0" 2 "$rc"
grep -q 'no puedo saber que mitad se midio' "$BASE/out" && d=si || d=no
check "(5) y ahi SI dice que no lo sabe" si "$d"

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
viejo = 'if salida="$(task -x lint:int-12-no-land 2>&1)"; then rc=0; else rc=$?; fi'
if viejo not in nuevo:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    nuevo.replace(viejo, 'salida="$(task -x lint:int-12-no-land 2>&1)"; rc=$?'))
MUT
# ⛔ NO ENCONTRAR EL OBJETIVO ES «NO HE PODIDO MIRAR», NO UN HALLAZGO, y por eso sale 2 y no 1.
# Esta bateria ANCLA UNA ORTOGRAFIA: su mutante sustituye LITERALMENTE la forma `if salida="$(...)"`.
# Una cura EQUIVALENTE y correcta —`rc=0; salida="$(...)" || rc=$?`, que bajo `-e` se comporta igual—
# deja el reemplazo sin objetivo, y entonces la bateria no ha medido el paso: ha fallado en montarlo.
# Medido el 2026-09-03 sustituyendo la forma a proposito: sin esta linea salia 1 con tres FAIL, que se
# lee como «el paso esta mal» cuando lo que pasa es que el banco no supo aplicarse. Las dos cosas
# bloquean, pero solo una dice la verdad de lo que ocurrio.
[ -s "$BASE/run-mut.sh" ] || {
	echo "  ⛔ NO HE PODIDO MIRAR: el mutante no caso. Esta bateria ancla la forma literal" >&2
	echo '     `if salida="$(task -x lint:int-12-no-land 2>&1)"; then rc=0; else rc=$?; fi`' >&2
	echo "     Si el paso usa otra forma equivalente, actualiza el 'viejo' de arriba a la que use." >&2
	exit 2
}
cmp -s "$BASE/run.sh" "$BASE/run-mut.sh" && d=NO-DIFIERE || d=ok
check "(M) el mutante REALMENTE difiere" ok "$d"
printf '%s\n' 'check-int-12-no-land: COULD NOT LOOK — sin act id' >"$BASE/fixture"
FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC=2 PATH="$BASE/bin:$PATH" \
	bash --noprofile --norc -e -o pipefail "$BASE/run-mut.sh" >"$BASE/out.mut" 2>&1
rcm=$?
check "(M) con la asignacion suelta el paso sale 2" 2 "$rcm"
check "(M) y se queda MUDO: cero lineas, como en la corrida real" 0 "$(wc -l <"$BASE/out.mut" | tr -d ' ')"

# ───── (6) SALIDA GRANDE: la clase 141, que es por lo que estos clasificadores son here-strings ──
#
# ⛔ `printf … | grep -q X` bajo `pipefail` devuelve **141 justo cuando ACIERTA**: `grep -q` cierra
# el tubo al primer casamiento y el `printf` de la izquierda se lleva un SIGPIPE. Con salida corta
# el `printf` cabe en el bufer del tubo y termina antes, asi que el defecto **no se ve**: hace falta
# una salida que no quepa. Por eso este caso existe y por eso es grande.
#
# El sintoma no es un error ruidoso: es que el clasificador **no reconoce una marca que SI esta**,
# cae al `else` y el paso sale 2 diciendo «no puedo saber que mitad se midio» — con la marca
# delante. Lo levanto el contraste sol max (CI-LARGE-141).
# La carga se fabrica SIN tuberia: `head … | tr … | fold` seria justo la clase que este caso mide,
# y `lint:sigpipe-booleans` la contaria — con razon.
GRANDE="check-int-12-no-land: SCOPED — el acta no viaja
$(python3 -c 'print("\n".join("x" * 120 for _ in range(2500)))')"
rc=$(corre "$GRANDE" 0)
check "(6) marca al principio de una salida GRANDE -> 0" 0 "$rc"
grep -q '::notice::INT-12: NO APLICABLE' "$BASE/out" && d=si || d=no
check "(6) y la reconoce: here-string, no tuberia" si "$d"

# Mutante: se vuelve a la tuberia en el PRIMER clasificador. Con la salida grande tiene que dejar
# de reconocerla y caer al else.
MUT141="$BASE/run-141.sh"
python3 - "$BASE/run.sh" "$MUT141" <<'M141'
import sys
s = open(sys.argv[1], encoding="utf-8").read()
v = """if grep -q 'check-int-12-no-land: SCOPED' <<<"$salida"; then"""
if v not in s:
    sys.exit(3)
open(sys.argv[2], "w", encoding="utf-8").write(
    # La tuberia del mutante se COMPONE, no se escribe: con el literal dentro,
    # `lint:sigpipe-booleans` la cuenta como una tuberia de ESTE fichero — y tiene razon en su
    # premisa, porque no puede saber que son datos de prueba. Un banco que prueba un detector tiene
    # que poder escribir lo que el detector busca sin declararlo el mismo.
    s.replace(v, """if printf '%s' "$salida" """ + "|" + """ grep -q 'check-int-12-no-land: SCOPED'; then""", 1))
M141
[ -s "$MUT141" ] || { echo "  FAIL MUTANTE 141 NO ESCRITO"; fallados=$((fallados + 1)); }
cmp -s "$BASE/run.sh" "$MUT141" && d=NO-DIFIERE || d=ok
check "(6) el mutante de la tuberia difiere" ok "$d"
printf '%s\n' "$GRANDE" >"$BASE/fixture"
orden141="${SHELL_PASO//\{0\}/$MUT141}"
FAKE_TASK_OUT="$BASE/fixture" FAKE_TASK_RC=0 PATH="$BASE/bin:$PATH" \
	sh -c "$orden141" >"$BASE/out.141" 2>&1
rc141=$?
check "(6) con la tuberia, la marca deja de reconocerse -> 2" 2 "$rc141"
grep -q 'no puedo saber que mitad se midio' "$BASE/out.141" && d=si || d=no
check "(6) y el paso dice 'no lo se' con la marca DELANTE" si "$d"

echo "test-ci-int12-step: $pasados passed, $fallados failed"
[ "$fallados" -eq 0 ] || exit 1
exit 0
