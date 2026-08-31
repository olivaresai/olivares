#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Bateria de scripts/check-orphan-batteries.sh. Fixtures propios: Taskfile y gancho de mentira, sin
# tocar los del arbol y sin red.
set -u

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
SUT="${SUT:-scripts/check-orphan-batteries.sh}"
PASS=0; FAIL=0
TMP=$(mktemp -d "${TMPDIR:-/tmp}/tob.XXXXXX") || exit 2
trap 'rm -rf "$TMP"' EXIT
check(){ local n="$1" e="$2" g="$3"
  if [ "$e" = "$g" ]; then PASS=$((PASS+1)); printf 'ok   %-60s %s\n' "$n" "$g"
  else FAIL=$((FAIL+1)); printf 'FAIL %s esperaba [%s], dio [%s]\n' "$n" "$e" "$g"; fi; }

# El SUT enumera con `git ls-files`, asi que el fixture es un repo de verdad, pequeño.
R="$TMP/r"; mkdir -p "$R/scripts" "$R/docs"
( cd "$R" && git init -q . && git config user.email t@t && git config user.name t )
esc(){ printf '#!/usr/bin/env bash\necho ok\n' > "$R/$1"; chmod +x "$R/$1"; }
esc scripts/test-cableada.sh; esc scripts/test-sin-tarea.sh; esc scripts/test-con-tarea-sin-llamada.sh
cat > "$R/Taskfile.yml" <<'T'
version: '3'
tasks:
# export-closure: fixture scripts/test-cableada.sh — nombre SINTETICO escrito dentro de un Taskfile
# de mentira, en un repo de usar y tirar. No existe ni en el hub ni en el export, y no debe existir:
# la clase es `fixture`, no `absent-by-design`, porque esa segunda exige que la ruta SI este en el hub.
# export-closure: fixture scripts/test-con-tarea-sin-llamada.sh — mismo caso, la otra clase que este
# banco distingue (tarea definida y NO alcanzada). La guarda no puede saber que estos dos nombres
# viven dentro de un heredoc, asi que se le dice aqui y con la clase que les toca.
  lint:cableada:
    cmds:
      - bash scripts/test-cableada.sh
  lint:nadie-la-llama:
    cmds:
      - bash scripts/test-con-tarea-sin-llamada.sh
T
printf '#!/usr/bin/env bash\ntask lint:cableada\n' > "$R/.githooks-pre-push"
( cd "$R" && git add -A >/dev/null && git commit -qm t )
# ⛔ UN 127 NO ES UNA MUERTE, ES QUE EL GUION NO CORRIO — y esta bateria lo aceptaba como valida.
# Lo levanto el lector 48 sobre la v7: `bash <ruta>` con la ruta mal da **127** («command not
# found»), y un caso escrito como «el mutante da algo distinto de 2» cuenta ese 127 como muerte.
# Es decir, un mutante que NI SE EJECUTA acreditaba cobertura. Es la peor forma de banco verde:
# no mide y no se queja.
#
# El arnes devuelve ahora el centinela `NO-EJECUTA` en ese caso, que no casa con ningun numero
# esperado, asi que la fila que lo reciba FALLA en vez de pasar. Y hay una fila que lo comprueba:
# un banco que no verifica su propio centinela vuelve a estar donde estaba.
# Y la ruta se resuelve ANTES del `cd`: `$OLDPWD/` delante de una ruta ya ABSOLUTA (los mutantes
# viven en `$TMP`) daba un disparate que no existe -> 127. Ese era el mecanismo exacto del defecto:
# el arnes no ejecutaba el mutante y la fila lo contaba como muerto.
corre(){ local ec g="${2:-$SUT}"
  case "$g" in /*) : ;; *) g="$PWD/$g";; esac
  ec=$( ( cd "$R" && OLIVARES_TASKFILE=Taskfile.yml OLIVARES_HOOK=.githooks-pre-push \
          OLIVARES_ORPHAN_BASELINE="${1:-docs/base.txt}" bash "$g" > "$TMP/out" 2>&1; echo $? ) )
  if [ "$ec" = "127" ]; then echo "NO-EJECUTA"; else echo "$ec"; fi; }

# 1 · sin linea base es 2, NO 0: sin ella no se distingue una huerfana nueva de las de siempre
check "(1) sin linea base -> 2, no 0" 2 "$(corre docs/no-existe.txt)"
check "(1b) y lo dice" 0 "$( grep -q 'no leo la linea base' "$TMP/out"; echo $? )"

# 2 · escribir la base y quedar verde
( cd "$R" && OLIVARES_TASKFILE=Taskfile.yml OLIVARES_HOOK=.githooks-pre-push \
  OLIVARES_ORPHAN_BASELINE=docs/base.txt OLIVARES_ORPHAN_WRITE_BASELINE=1 bash "$OLDPWD/$SUT" >/dev/null 2>&1 )
check "(2) con la base recien escrita -> 0" 0 "$(corre)"
check "(2b) y la base NOMBRA las dos huerfanas" 0 \
  "$( grep -q 'test-sin-tarea.sh' "$R/docs/base.txt" && grep -q 'test-con-tarea-sin-llamada.sh' "$R/docs/base.txt"; echo $? )"
check "(2c) y NO mete la que si esta cableada" 0 \
  "$( grep -q 'test-cableada.sh' "$R/docs/base.txt" && echo "metio una cableada en la base" || echo 0 )"

# 3 · CONTROL POSITIVO: una huerfana NUEVA (fuera de la base) tiene que cortar
grep -v 'test-sin-tarea.sh' "$R/docs/base.txt" > "$R/docs/menos.txt"
check "(3) una huerfana fuera de la base -> 1" 1 "$(corre docs/menos.txt)"
check "(3b) y la NOMBRA, y dice que es por no tener tarea" 0 \
  "$( grep -q 'test-sin-tarea.sh' "$TMP/out" && grep -q 'SIN TAREA' "$TMP/out"; echo $? )"

# 4 · la segunda clase: tarea que existe y que el gancho NO alcanza
grep -v 'test-con-tarea-sin-llamada.sh' "$R/docs/base.txt" > "$R/docs/menos2.txt"
check "(4) tarea existente pero no alcanzada -> 1" 1 "$(corre docs/menos2.txt)"
check "(4b) y se distingue de la otra clase, nombrando su tarea" 0 \
  "$( grep -q 'SIN LLAMADA' "$TMP/out" && grep -q 'lint:nadie-la-llama' "$TMP/out"; echo $? )"

# 5 · MUTANTE · resolver por TEXTO en vez de por GRAFO: la bateria de una tarea alcanzada por
#     `deps:` se declararia huerfana. Se comprueba que el original NO la marca.
cat >> "$R/Taskfile.yml" <<'T'
  lint:padre:
    deps: [lint:nadie-la-llama]
    cmds:
      - echo padre
T
printf '#!/usr/bin/env bash\ntask lint:cableada\ntask lint:padre\n' > "$R/.githooks-pre-push"
( cd "$R" && git add -A >/dev/null && git commit -qm t2 )
( cd "$R" && OLIVARES_TASKFILE=Taskfile.yml OLIVARES_HOOK=.githooks-pre-push \
  OLIVARES_ORPHAN_BASELINE=docs/base2.txt OLIVARES_ORPHAN_WRITE_BASELINE=1 bash "$OLDPWD/$SUT" >/dev/null 2>&1 )
check "(5) una bateria alcanzada SOLO por deps NO es huerfana" 0 \
  "$( grep -q 'test-con-tarea-sin-llamada.sh' "$R/docs/base2.txt" && echo "la declaro huerfana pese al grafo" || echo 0 )"
# 5b · MUTANTE · resolver por TEXTO en vez de por GRAFO. Se fabrica con python porque el `sed` que
#      lo intentaba FALLABA en silencio y `cmp` decia «difieren» sobre un fichero vacio: un caso que
#      pasaba por no existir el mutante. Ahora se comprueba que difiere Y que mata su fila.
python3 - "$SUT" "$TMP/mg.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
# ⛔ REAPUNTADO el 2026-08-31 con el SUT: el alcance dejo de ser un `task --dry` suelto y pasa a
#    recorrer a punto fijo, siguiendo la indireccion de `hub-only-gate.sh`. El mutante conserva su
#    INTENCION —resolver por TEXTO en vez de por GRAFO— sobre la linea que hoy hace el grafo.
#    El `assert` es lo que obligo a este reapunte: sin el, el mutante no se habria fabricado y
#    la fila habria pasado por no existir, que es el falso verde que este bloque existe para cerrar.
v='    printf \'%s\\n\' "$SALIDA" | sed -n \'s/^task: \\[\\(.*\\)\\].*/\\1/p\' >>"$ALCANZ"'
n='    printf \'%s\\n\' "$t" >>"$ALCANZ"'
assert o.count(v)==1, "el patron del mutante del grafo no casa"
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(5b) el mutante del grafo se FABRICO y difiere" 0 \
  "$( [ -s "$TMP/mg.sh" ] && ! cmp -s "$SUT" "$TMP/mg.sh" && echo 0 || echo "no se fabrico o no difiere" )"
( cd "$R" && OLIVARES_TASKFILE=Taskfile.yml OLIVARES_HOOK=.githooks-pre-push \
  OLIVARES_ORPHAN_BASELINE=docs/base3.txt OLIVARES_ORPHAN_WRITE_BASELINE=1 bash "$TMP/mg.sh" >/dev/null 2>&1 )
check "(5c) MUTANTE 'resuelvo por texto' declara huerfana la que el GRAFO alcanza" 0 \
  "$( grep -q 'test-con-tarea-sin-llamada.sh' "$R/docs/base3.txt"; echo $? )"

# --- v2 · LOS DOS FALSOS VERDES QUE UN DETECTOR NO PUEDE TENER (the reviewer) -----------------------
# 6 · una ruta que aparece SOLO en `desc:` NO hace dueña a la tarea: `task` no ejecuta descripciones.
esc scripts/test-solo-en-desc.sh
cat >> "$R/Taskfile.yml" <<'T'
  lint:solo-la-menciona:
    desc: esta tarea NOMBRA scripts/test-solo-en-desc.sh en su descripcion y no la ejecuta
    cmds:
      - echo nada
T
printf '#!/usr/bin/env bash\ntask lint:cableada\ntask lint:padre\ntask lint:solo-la-menciona\n' > "$R/.githooks-pre-push"
( cd "$R" && git add -A >/dev/null && git commit -qm t3 )
( cd "$R" && OLIVARES_TASKFILE=Taskfile.yml OLIVARES_HOOK=.githooks-pre-push \
  OLIVARES_ORPHAN_BASELINE=docs/base6.txt OLIVARES_ORPHAN_WRITE_BASELINE=1 bash "$OLDPWD/$SUT" >/dev/null 2>&1 )
check "(6) mencionada solo en desc: SIGUE siendo huerfana" 0 \
  "$( grep -q 'test-solo-en-desc.sh' "$R/docs/base6.txt"; echo $? )"

# 7 · una tarea del gancho que NO EXISTE es rc 2, no un verde con el alcance corto.
#
# ⛔ EL FIXTURE SE REANCLA AQUI, y la razon es del lector 47: el censo generico NO DISCRIMINA SU
# CAUSA. m8 moria con rc 1, si, pero lo producia una huerfana AJENA del fixture
# (`test-con-tarea-sin-llamada.sh`), asi que **cualquier huerfana ajena acreditaba a cualquier
# mutante**. Es la regla de siempre un nivel mas arriba: un mutante muere por SU mensaje, y aqui el
# mensaje del censo era mudo sobre la causa.
#
# Se le da entonces una huerfana que SOLO puede producir su fallo: una bateria colgada de una tarea
# REAL (`lint:usa-el-roto`) cuya expansion falla por su hijo inexistente. Con la guarda puesta, el
# gate se NIEGA y esa bateria no llega a nombrarse nunca; sin la guarda, el universo se queda corto,
# la tarea no se alcanza y el censo la nombra bajo SIN LLAMADA. El nombre aparece en la salida del
# mutante y en ninguna otra.
# export-closure: fixture scripts/test-solo-por-el-roto.sh — nombre SINTETICO escrito dentro de un
# Taskfile de mentira, en un repo de usar y tirar; no existe ni en el hub ni en el export, y no debe
# existir. La clase es `fixture`, como las dos de arriba, no `absent-by-design` (esa exige que la
# ruta SI este en el hub) ni el silencio: el gate lee este fichero publicado como texto de shell.
esc scripts/test-solo-por-el-roto.sh
cat >> "$R/Taskfile.yml" <<'T'
  lint:usa-el-roto:
    cmds:
      - task: lint:no-existe-esta
      - bash scripts/test-solo-por-el-roto.sh
T
printf '#!/usr/bin/env bash\ntask lint:cableada\ntask lint:usa-el-roto\n' > "$R/.githooks-pre-push"
( cd "$R" && git add -A >/dev/null && git commit -qm t4 )
check "(7) tarea inexistente en el gancho -> 2, no 0" 2 "$(corre docs/base6.txt)"
check "(7b) y NOMBRA la raiz rota" 0 "$( grep -q 'lint:usa-el-roto' "$TMP/out"; echo $? )"
check "(7c) y dice por que invalida el veredicto" 0 "$( grep -q 'alcance corto' "$TMP/out"; echo $? )"

# 8 · MUTANTE · volver a tragarse la tarea inexistente: el gate contestaria 0 sobre un universo corto
python3 - "$SUT" "$TMP/m8.sh" <<'PYEOF'
import sys
o=open(sys.argv[1]).read()
v='if [ -n "$ROTAS" ]; then'
n='if false; then'
assert o.count(v)==1, "el patron del mutante 8 no casa"
open(sys.argv[2],"w").write(o.replace(v,n,1))
PYEOF
check "(8a) el mutante de la raiz rota se FABRICO y difiere" 0 \
  "$( [ -s "$TMP/m8.sh" ] && ! cmp -s "$SUT" "$TMP/m8.sh" && echo 0 || echo "no se fabrico" )"
M8=$(corre docs/base6.txt "$TMP/m8.sh")
# ⛔ SE CONGELA LA SALIDA DEL MUTANTE. `corre` escribe siempre en el MISMO `$TMP/out`, asi que una
# fila posterior que vuelva a correr —como el control de 8b5— pisa lo que las filas de mensaje van a
# leer. Lo introduje yo al añadir ese control y el banco me lo dijo: 8d fallaba juzgando la salida
# del caso SANO. Un banco cuyas filas comparten fichero es un banco donde el ORDEN decide.
cp "$TMP/out" "$TMP/out.m8"
# ⛔ ESTA FILA SE JUZGA POR EL DIAGNOSTICO, y las dos razones son del lector 48.
# (a) «distinto de 2» aceptaba un 127 —el mutante ni corria— y tambien un 1 producido por una
#     huerfana AJENA: el mutante moria, si, pero NO en su fila. Un mutante acredita la pata que
#     NOMBRA; si muere antes o por otra cosa, la pata sigue sin cubrir y el banco sale verde.
# (b) Lo que la rama produce y solo ella es el DIAGNOSTICO de la raiz rota. Asi que lo que tiene
#     que desaparecer es eso, y ademas hay que exigir que el mutante haya EMITIDO un veredicto.
# ⛔ Y NI SIQUIERA «0 o 1» BASTA, que es lo que levanto el lector 44: un rc 1 lo produce tambien
# una huerfana AJENA del fixture, asi que el mutante moria pero NO en su fila. La firma que solo
# esta rama puede dar es POSITIVA: donde el original se NIEGA a mirar, el mutante sigue y **emite
# censo** sobre un universo corto. Eso se exige, ademas de que desaparezca el diagnostico.
check "(8b) el mutante no se NIEGA (donde el original decia NO HE PODIDO MIRAR)" 0 \
  "$( grep -q 'NO HE PODIDO MIRAR' "$TMP/out.m8" && echo "sigue negandose: el mutante no cambio nada" || echo 0 )"
check "(8b2) y llega a EMITIR CENSO sobre el universo corto" 0 \
  "$( grep -q 'huerfanas hoy:' "$TMP/out.m8"; echo $? )"
# ⛔ LA FIRMA QUE SOLO ESTA RAMA PUEDE DAR. Un censo que dice «hay huerfanas» sin decir CUALES no
# discrimina la causa, y con eso una huerfana ajena acreditaba a este mutante. Se exige el NOMBRE de
# la que unicamente el universo corto puede producir.
check "(8b4) y NOMBRA la huerfana que solo el universo corto produce" 0 \
  "$( grep -q 'test-solo-por-el-roto.sh' "$TMP/out.m8"; echo $? )"
check "(8b5) CONTROL: ese nombre NO aparece cuando la guarda esta puesta" 0 \
  "$( corre docs/base6.txt >/dev/null 2>&1; grep -q 'test-solo-por-el-roto.sh' "$TMP/out" && echo "aparece tambien en el sano: no discrimina" || echo 0 )"
check "(8b3) y no es un rc fantasma" 0 \
  "$( case "$M8" in 0|1) echo 0;; NO-EJECUTA) echo "ni se ejecuto";; *) echo "dio $M8";; esac )"
# ⛔ Y ESTA FILA HAY QUE AFINARLA, no basta con «deja de nombrar la tarea»: el mutante SIGUE
# nombrando `lint:usa-el-roto`, pero como **tarea DUEÑA** de la huerfana en el censo, no como raiz
# rota. Dos apariciones del mismo nombre por razones opuestas — otra vez la sonda casando la palabra
# y no el significado. Lo que solo produce la rama de la guarda es su FRASE de negativa.
check "(8c) y DEJA DE DECIR que el gancho invoca tareas inexistentes" 0 \
  "$( grep -q 'tarea(s) que NO existen' "$TMP/out.m8" && echo "sigue negandose: el mutante no cambio nada" || echo 0 )"
check "(8d) y deja de decir por que invalidaria el veredicto" 0 \
  "$( grep -q 'alcance corto' "$TMP/out.m8" && echo "sigue diciendolo" || echo 0 )"

# 9 · EL CENTINELA DEL PROPIO ARNES. Sin esta fila, el arreglo de arriba no esta comprobado y la
#     bateria vuelve a poder contar como muertos a mutantes que no existen.
check "(9) una ruta de guion que no existe da NO-EJECUTA, no un numero" "NO-EJECUTA" \
  "$(corre docs/base6.txt "$TMP/guion-que-no-existe.sh")"

# 10 · TESTIGO DEL AISLAMIENTO, Y ESTA VEZ CORTA. La v8 perdio el `lib/git-env.sh` de `main` —lo
#      reconstrui desde una version anterior a que main lo ganara— y el banco daba 21/0 mientras
#      podia escribir en el repositorio que apuntase `GIT_DIR`.
#
#      ⛔ Mi PRIMER testigo no valia, y lo digo porque el error es mas instructivo que el arreglo:
#      ponia `GIT_DIR` alrededor de una llamada al SUT **despues** de que el preambulo de esta
#      bateria ya hubiera corrido. El aislamiento actua AL ARRANCAR; ponerle la variable despues no
#      lo pone a prueba, y en efecto: neutralizando el aislamiento, las cuatro filas seguian verdes.
#      Un testigo que no distingue el caso sano del enfermo no es un testigo.
#
#      Lo que se prueba ahora es la libreria en las condiciones reales: un guion que hace lo mismo
#      que hace esta bateria (crear un repo y COMMITEAR en el) se ejecuta con `GIT_DIR` apuntando a
#      un señuelo, una vez sourceando el aislamiento y otra sin el. Con el, el señuelo queda intacto;
#      SIN el, se escribe — y esa segunda mitad es el control positivo que le faltaba al primero.
RZ=$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)   # raiz del repo, sin depender del cwd ni de git
SEN="$TMP/senuelo"; mkdir -p "$SEN"
( cd "$SEN" && git init -q . && git config user.email t@t && git config user.name t \
  && echo intacto > testigo.txt && git add testigo.txt && git commit -qm senuelo )
antes_head=$(git -C "$SEN" rev-parse HEAD)
antes_sha=$(sha256sum "$SEN/testigo.txt" | cut -d" " -f1)

_sonda_iso(){ # 1=con|sin  -> imprime el HEAD que git le contesta desde SU PROPIO repo
	{ echo 'set -u'
	  [ "$1" = con ] && printf '. "%s/scripts/lib/git-env.sh" || exit 2\n' "$RZ"
	  echo 'D=$(mktemp -d); cd "$D" || exit 2'
	  echo 'git init -q . 2>/dev/null; git config user.email t@t; git config user.name t'
	  echo 'echo x > f.txt; git add f.txt 2>/dev/null; git commit -qm probe 2>/dev/null'
	  echo 'git rev-parse HEAD 2>/dev/null || echo SIN-HEAD'
	} > "$TMP/iso-$1.sh"
	( GIT_DIR="$SEN/.git" GIT_WORK_TREE="$SEN" bash "$TMP/iso-$1.sh" 2>/dev/null | tail -1 ); }

# ⛔ Lo que un `GIT_DIR` heredado hace de verdad es una FUGA DE LECTURA: git contesta por el
# repositorio ajeno aunque estes parado en el tuyo. Mi primer control positivo buscaba una
# ESCRITURA en el señuelo y no la encontraba —con la variable puesta, el `git add` de un fichero
# que no esta en su arbol simplemente falla—, asi que declaraba «no distingue nada» sobre un
# peligro que existe y que yo estaba midiendo por el sitio equivocado. El testigo pregunta ahora lo
# que se puede contestar: ¿de QUE repositorio habla git?
CON=$(_sonda_iso con); SIN=$(_sonda_iso sin)
check "(10) CON aislamiento, git contesta por el repo PROPIO, no por el señuelo" 0 \
  "$( [ "$CON" != "$antes_head" ] && [ "$CON" != "SIN-HEAD" ] && echo 0 || echo "contesto $CON, y el señuelo es $antes_head" )"
check "(10b) CONTROL POSITIVO: SIN aislamiento contesta por el SEÑUELO" 0 \
  "$( [ "$SIN" = "$antes_head" ] && echo 0 || echo "sin aislamiento dio $SIN: el testigo no distingue los dos casos" )"
check "(10c) y el señuelo queda intacto en los dos casos" "$antes_sha" "$(sha256sum "$SEN/testigo.txt" | cut -d" " -f1)"
check "(10d) y sin nada sin commitear" 0 \
  "$( [ -z "$(git -C "$SEN" status --porcelain 2>/dev/null)" ] && echo 0 || echo "escribio en el señuelo" )"

echo
echo "check-orphan-batteries selftest: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
