#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Battery for scripts/check-migration-contiguity.sh — the gate that refuses a GAP in a migration
# directory.
#
# ⛔ POR QUÉ EXISTE ESTA BATERÍA, Y POR QUÉ LLEGA TARDE. El gate se escribió sin ninguna, y con
# `ls` leía el DIRECTORIO EN DISCO en vez del árbol versionado. Los runners auto-alojados
# reutilizan el mismo checkout entre corridas: `git checkout <sha>` deja bien lo RASTREADO, pero
# la basura NO rastreada de la corrida anterior sigue ahí. Eso rompió `mainline-ci` y era
# invisible para cualquier prueba que no fabricara esa condición a propósito — que es
# exactamente lo que hace el caso «basura sin rastrear» de abajo.
#
# Cada caso verde va emparejado con el rojo que prueba que no pasó por casualidad, y los dos
# casos que sostienen el arreglo se apoyan en un repositorio de verdad, no en un simulacro.
set -u

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

# ⛔ AISLAMIENTO DE ENTORNO GIT. Git EXPORTA `GIT_DIR` a los hooks desde todo worktree ENLAZADO
# —o sea, desde cualquier sesion en paralelo— y `GIT_DIR` MANDA SOBRE `-C`: sin sanear, los
# repositorios desechables que construye este banco son el repositorio VIVO de quien lo invoque.
# MEDIDO el 2026-08-30 contra un repositorio de destino desechable, sin esta linea: el destino
# recibio COMMITS. Falla cerrado: no poder aislar es «no he podido».
#
# Este fichero es `#!/bin/sh`, asi que la ruta se resuelve por `$0` y no por `BASH_SOURCE` —
# comprobado que `scripts/lib/git-env.sh` se sourcea limpio bajo dash.
_olivares_git_env="${ROOT}/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
olivares_git_env_isolate
SCRIPT="${ROOT}/scripts/check-migration-contiguity.sh"
pass=0
fail=0

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT HUP INT TERM

check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1)); printf '  ok    %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1)); printf '  FAIL  %-58s %s\n' "$1" "$2"
	fi
}

# ⛔ EL GUION SE COPIA DENTRO DEL REPO DE JUGUETE, y no es comodidad: el gate resuelve su raíz
# con `ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"`, así que mira SIEMPRE el
# repositorio donde vive el fichero. Invocarlo desde fuera con otro `cwd` no prueba nada — lo
# intenté y las ocho casillas dieron «no existe m», que es un NO_HE_PODIDO_MIRAR disfrazado de
# rojo. Copiado dentro, `BASH_SOURCE` cae en el repo desechable y el gate mide lo que se le pide.
repo="$WORK/r"
mkdir -p "$repo/scripts" "$repo/m"
cp "$SCRIPT" "$repo/scripts/"
GATE="scripts/$(basename "$SCRIPT")"
( cd "$repo" && git init -q . && git config user.email t@t && git config user.name t )
for n in 001 002 003; do : > "$repo/m/${n}_x.up.sql"; done
( cd "$repo" && git add -A >/dev/null 2>&1 && git commit -qm base )

corre() { ( cd "$repo" && env OLIVARES_MIGRATION_DIRS=m "$@" bash "$GATE" ) >"$WORK/out" 2>"$WORK/err"; rc=$?; }

echo "check-migration-contiguity — el árbol versionado, no el directorio"

corre
[ "$rc" -eq 0 ] && grep -q 'CONTIGUO' "$WORK/out"
check "tres migraciones contiguas y rastreadas: CONTIGUO" "exit 0" $?

grep -q 'árbol versionado' "$WORK/out"
check "la pata DICE de dónde leyó (sin repliegue silencioso)" "modo declarado" $?

# GitHub pide el árbol que disparó el job, aunque HEAD haya avanzado en el checkout persistente.
# Se prueban ambas respuestas y la tercera: limpio histórico, hueco en HEAD y ref irresoluble.
clean_sha="$(git -C "$repo" rev-parse HEAD)"
clean_short="$(printf '%.12s' "$clean_sha")"
( cd "$repo" && git rm -q m/002_x.up.sql && git commit -qm gap )
gap_sha="$(git -C "$repo" rev-parse HEAD)"
corre GITHUB_SHA="$clean_sha"
[ "$rc" -eq 0 ] && grep -q "árbol solicitado ${clean_short}" "$WORK/out"
check "GITHUB_SHA limpio gana a un HEAD posterior con hueco" "árbol solicitado" $?

corre GITHUB_SHA="$gap_sha"
[ "$rc" -eq 1 ] && grep -q 'HUECOS: 002' "$WORK/err"
check "un hueco en el GITHUB_SHA solicitado sigue saliendo rojo" "002 ausente" $?

corre GITHUB_SHA=ffffffffffffffffffffffffffffffffffffffff
[ "$rc" -eq 2 ] && grep -q 'NO HE PODIDO MIRAR: no resuelvo' "$WORK/err"
check "un GITHUB_SHA irresoluble da 2, no un verde" "tercera respuesta" $?

# Mutante ejecutable: ignora el input y vuelve a HEAD. El SHA histórico limpio debe matarlo contra
# el HEAD gappy; así la celda no puede pasar porque ambos árboles fueran casualmente iguales.
mutant="$repo/scripts/check-migration-contiguity.head-mutant.sh"
sed 's/^TREE_REF="${GITHUB_SHA:-HEAD}"$/TREE_REF=HEAD/' "$SCRIPT" > "$mutant"
if cmp -s "$SCRIPT" "$mutant" || ! bash -n "$mutant"; then
	echo "check-migration-contiguity: mutación TREE_REF no aplicada o sintaxis rota" >&2
	exit 2
fi
( cd "$repo" && env OLIVARES_MIGRATION_DIRS=m GITHUB_SHA="$clean_sha" \
	bash "scripts/$(basename "$mutant")" ) >"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 1 ] && grep -q 'HUECOS: 002' "$WORK/err"
check "el mutante TREE_REF=HEAD muere contra el SHA histórico" "mutante rojo" $?

git -C "$repo" checkout -q "$clean_sha"

# ⛔ EL CASO QUE SOSTIENE TODO EL ARREGLO: basura NO rastreada del runner anterior.
: > "$repo/m/009_dejado_por_la_corrida_anterior.up.sql"
corre
[ "$rc" -eq 0 ] && grep -q '001..003' "$WORK/out"
check "una .up.sql SIN RASTREAR no cuenta (basura del runner anterior)" "ls-files, no ls" $?

# y el control que prueba que el caso anterior no pasa por no mirar: la MISMA basura, rastreada,
# sí abre un hueco y el gate lo acusa.
( cd "$repo" && git add m/009_dejado_por_la_corrida_anterior.up.sql >/dev/null 2>&1 )
corre
[ "$rc" -ne 0 ] && grep -q '004' "$WORK/err" 2>/dev/null || grep -q '004' "$WORK/out"
check "la MISMA migración, ya rastreada, SÍ acusa el hueco 004..008" "control positivo" $?
( cd "$repo" && git rm -q --cached m/009_dejado_por_la_corrida_anterior.up.sql >/dev/null 2>&1; rm -f m/009_dejado_por_la_corrida_anterior.up.sql )

# una migración recién añadida al índice —el caso de quien la está escribiendo— SÍ cuenta.
: > "$repo/m/004_la_que_estoy_escribiendo.up.sql"
( cd "$repo" && git add m/004_la_que_estoy_escribiendo.up.sql >/dev/null 2>&1 )
corre
[ "$rc" -eq 0 ] && grep -q '001..004' "$WORK/out"
check "una migración en el ÍNDICE cuenta (ls-tree HEAD la habría perdido)" "indexada" $?
( cd "$repo" && git rm -q --cached m/004_la_que_estoy_escribiendo.up.sql >/dev/null 2>&1; rm -f m/004_la_que_estoy_escribiendo.up.sql )

# hueco real, rastreado: rojo
: > "$repo/m/006_x.up.sql"
( cd "$repo" && git add -A >/dev/null 2>&1 )
corre
[ "$rc" -ne 0 ]
check "un hueco REAL sigue saliendo rojo" "004..005 ausentes" $?
( cd "$repo" && git rm -q --cached m/006_x.up.sql >/dev/null 2>&1; rm -f m/006_x.up.sql )

# Fuera de un repositorio: repliegue a disco, DECLARADO. GITHUB_SHA se deja puesto a propósito:
# el export puede heredar el input de Actions y su ausencia de `.git` NO debe convertirse en rc=2.
plano="$WORK/p"; mkdir -p "$plano/m"
for n in 001 002; do : > "$plano/m/${n}_x.up.sql"; done
( mkdir -p "$plano/scripts" && cp "$SCRIPT" "$plano/scripts/" && cd "$plano" \
	&& env OLIVARES_MIGRATION_DIRS=m GITHUB_SHA=ffffffffffffffffffffffffffffffffffffffff \
	bash "$GATE" ) >"$WORK/out" 2>&1; rc=$?
[ "$rc" -eq 0 ] && grep -q 'directorio en disco' "$WORK/out"
check "sin repo + GITHUB_SHA: repliegue a disco y lo DICE" "export sin git" $?

# un directorio que no existe es NO_HE_PODIDO_MIRAR, no un verde
( cd "$repo" && env OLIVARES_MIGRATION_DIRS=no-existe bash "$GATE" ) >"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 2 ] && grep -q 'NO HE PODIDO MIRAR' "$WORK/err"
check "un directorio ausente da 2, no 0" "tercera respuesta" $?

# ⛔ LA TERCERA RESPUESTA, Y SUS DOS MITADES. En el export publicado el modulo entero se
# cura fuera, asi que la pata no tiene sujeto y NUNCA lo tendra: rc=2 alli es ruido y tumba
# el job del CI publico. Los dos casos van juntos a proposito — el verde solo significa algo
# si el mutante que cambia la clasificacion vuelve a poner el rc=2.
publico="$WORK/exportado"
mkdir -p "$publico/scripts"
cp "$ROOT/scripts/hub-leg.sh" "$publico/scripts/hub-leg.sh"
bash "$ROOT/scripts/hub-leg.sh" --marker-signature > "$publico/PUBLIC-EXPORT.md"
cp "$SCRIPT" "$publico/scripts/check-migration-contiguity.sh"
( cd "$publico" && git init -q . && git config user.email t@t && git config user.name t )

( cd "$publico" && bash scripts/check-migration-contiguity.sh ) >"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 0 ] && grep -q 'SCOPED' "$WORK/out"
check "export publico sin el modulo: SCOPED rc 0, no un 2 que tumba el job" "tercera respuesta" $?

# MUTANTE: el MISMO arbol deja de ser publico en cuanto aparece un camino hub-only. Si el
# rc siguiera siendo 0 aqui, la excepcion seria un comodin y no una respuesta acotada.
mkdir -p "$publico/design"
( cd "$publico" && bash scripts/check-migration-contiguity.sh ) >"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 2 ] && grep -q 'NO HE PODIDO MIRAR' "$WORK/err"
check "mutante clasificador->hub: vuelve el 2" "tercera respuesta" $?
rmdir "$publico/design"

# Y la excepcion es SOLO para el sujeto POR DEFECTO: apuntar la pata a otro sitio en el
# mismo arbol publico sigue siendo un 2, porque ahi el error es de quien la apunto.
( cd "$publico" && env OLIVARES_MIGRATION_DIRS=no-existe bash scripts/check-migration-contiguity.sh ) \
	>"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 2 ]
check "en el export, un sujeto NO por defecto sigue dando 2" "tercera respuesta" $?

# A partial, failed Git read must preserve diagnostics and refuse classification.
falso_git="$WORK/bin"
mkdir -p "$falso_git"
cat > "$falso_git/git" <<'FAKE'
#!/usr/bin/env bash
# Only the tree listing fails; other Git operations retain their actual behavior.
if [ "${1:-}" = "ls-tree" ]; then
	printf '001_x.up.sql
003_x.up.sql
'
	printf 'synthetic tree read failure\n' >&2
	exit 128
fi
exec /usr/bin/env -u PATH_FAKE "$OLIVARES_REAL_GIT" "$@"
FAKE
chmod +x "$falso_git/git"
OLIVARES_REAL_GIT="$(command -v git)"
export OLIVARES_REAL_GIT

( cd "$repo" && env OLIVARES_MIGRATION_DIRS=m GITHUB_SHA="$(cd "$repo" && command git rev-parse HEAD)" 	PATH="$falso_git:$PATH" bash "$GATE" ) >"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 2 ] && grep -q 'NO HE PODIDO MIRAR' "$WORK/err" && grep -q 'ls-tree' "$WORK/err" \
	&& grep -q 'synthetic tree read failure' "$WORK/err"
check "ls-tree parcial y con error: rc 2 NO HE PODIDO MIRAR" "lectura parcial" $?

# A read failure must not recommend renumbering a migration.
! grep -q 'HUECOS' "$WORK/out" && ! grep -q 'HUECOS' "$WORK/err" \
	&& ! grep -qi 'renumera' "$WORK/out" && ! grep -qi 'renumera' "$WORK/err"
check "una lectura parcial NUNCA imprime HUECOS ni manda renumerar" "lectura parcial" $?

# The complete listing of the same tree still passes.
( cd "$repo" && env OLIVARES_MIGRATION_DIRS=m GITHUB_SHA="$(cd "$repo" && command git rev-parse HEAD)" 	bash "$GATE" ) >"$WORK/out" 2>"$WORK/err"; rc=$?
[ "$rc" -eq 0 ] && grep -q 'CONTIGUO' "$WORK/out"
check "lectura completa del mismo arbol: sigue CONTIGUO rc 0" "lectura parcial" $?

echo ""
echo "check-migration-contiguity battery: $pass passed, $fail failed"
[ "$fail" -eq 0 ] || exit 1
