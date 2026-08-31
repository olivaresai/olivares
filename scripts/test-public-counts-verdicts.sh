#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-public-counts-verdicts.sh — prueba que check-public-counts.sh distingue «una cifra pública
# es FALSA» (1) de «NO HE PODIDO MIRAR» (2). C15-P6.
#
# ⛔ POR QUÉ HACÍA FALTA, y el detalle que lo hace interesante: el gate YA decía la verdad en
#    prosa. Sus mensajes llevaban escrito «an unmeasurable claim is not a passing one» y
#    «a vanished measurement, not a zero» — el razonamiento estaba bien. Lo que estaba mal era la
#    CODIFICACIÓN: `sys.exit("FAIL …")` con una cadena sale con **1** en Python, y en este
#    repositorio 1 significa «una afirmación pública es falsa».
#
#    Un censo que falta no hace falsa ninguna afirmación: impide comprobarlas. Y la diferencia
#    manda donde se lee el CÓDIGO y no el texto — un job de CI, un `||` en el Taskfile, alguien
#    triando diez gates rojos: «la cifra miente» manda a corregir copy, «no pude mirar» manda a
#    arreglar el checkout.
#
# ⛔ EL CONTROL NEGATIVO ES LA MITAD QUE VALE. Sin él, esta batería la pasa un gate que devuelva
#    2 SIEMPRE — que sería exactamente el defecto contrario y no distinguiría nada. Por eso la
#    última celda rompe una cifra de verdad y exige 1.
#
# Salida: 0 todas pasan · 1 alguna falla · 2 no se ha podido montar el banco.
set -uo pipefail

# ⛔ El entorno git ambiental se sanea aunque este guion no clone nada.
#
# Entró en la clase de `lint:git-env` el 2026-08-21 y **el detector tiene razón**: monta un árbol
# señuelo con `mktemp -d` a partir del repositorio y corre un gate encima. Con un `GIT_DIR`
# heredado —y git lo exporta a todo hook `pre-push`, o sea desde cualquier sesión en paralelo—
# cualquier operación de git que este guion o el sujeto hagan iría al repositorio VIVO en vez de
# al señuelo, y la batería mediría el árbol equivocado creyendo medir el suyo.
#
# No es hipotético en esta casa: el mismo descuido dejó la rama del PR #526 apuntando a un commit
# de fixture. Fail-closed: un saneador que no se puede cargar es «no he podido aislar».
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "test-public-counts-verdicts: FATAL: no puedo cargar $_olivares_git_env (aislamiento git-env)" >&2
	exit 2
}
unset _olivares_git_env
LC_ALL=C
export LC_ALL

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-public-counts.sh"
[ -r "$GATE" ] || {
	echo "test-public-counts-verdicts: ⛔ NO HE PODIDO MIRAR: no existe $GATE" >&2
	exit 2
}
cd "$RAIZ" || exit 2

# ⛔ EL BANCO VA FUERA DEL ÁRBOL DEL REPO, no dentro. Estaba en `$RAIZ/.cpc-verdicts-XXXXXX`,
#    es decir, un directorio SIN TRACKEAR dentro de un árbol que comparten tres contenedores —
#    exactamente el patrón que la REGLA CERO cita con su medida: los 51 ficheros de
#    `console-walk-out/` que cualquier `git add -A` ajeno habría commiteado. Y `.cpc-verdicts-*`
#    NO está en `.gitignore`, así que no había ni esa red. Va al PADRE: mismo sistema de ficheros
#    (lo necesita el `cp -al` de abajo) y fuera del alcance de cualquier `git add`.
BANCO="$(mktemp -d "$(dirname -- "$RAIZ")/.cpc-verdicts-XXXXXX")" || {
	echo "test-public-counts-verdicts: ⛔ NO HE PODIDO MIRAR: no se pudo crear el banco" >&2
	exit 2
}
# ⛔ EL TRAP YA NO RESTAURA NADA, PORQUE YA NO SE MUTA NADA DEL ÁRBOL REAL.
#
#    Escribí esta batería mutando `README.md` EN EL ÁRBOL y devolviéndolo dos líneas después.
#    metió el `cp` de vuelta en el trap (`9361223a2`) para que sobreviviera a que maten el
#    proceso, y eso era cierto y necesario. **Pero cierra una mitad**: el trap responde por «me
#    matan a mí», no por «otro carril hace `git add -A` durante la ventana». Y ésa es justo la
#    distinción que la REGLA CERO de `CLAUDE.md` fija con estas palabras: reducir la ventana
#    **no la cierra**, porque el problema no es cómo commiteas tú, es cuánto tiempo dejas algo a
#    medias donde otro puede recogerlo. Un README público con «157 integrations» commiteado por
#    un tercero es un defecto de producto, no del test.
#
#    ⇒ El control negativo corre contra un SEÑUELO POR HARDLINK del árbol (`cp -al`, 981 ms
#    medidos, `.git` excluido). El `sed -i` crea un fichero nuevo y renombra, así que el inode
#    DIVERGE y el original no se entera — verificado: señuelo 56530459 / real 16692728, mutado 1
#    / real 0. El árbol real no se toca en ningún instante, ni siquiera durante.
trap '[ -n "${BANCO:-}" ] && rm -rf "$BANCO"' EXIT

pasan=0
fallan=0
comprobar() {
	if [ "$3" -eq "$2" ]; then
		printf '  ok    %-56s rc=%s\n' "$1" "$3"
		pasan=$((pasan + 1))
	else
		printf '  FALLA %-56s rc=%s (quiere %s)\n' "$1" "$3" "$2"
		fallan=$((fallan + 1))
	fi
}

# ── SUELO: el árbol sano tiene que salir 0, o todo lo demás mide otra cosa ────────────────
bash "$GATE" >"$BANCO/0.log" 2>&1
comprobar "el árbol sano sale limpio" 0 "$?"

# ── 1 · Censo de aplicación ausente ⇒ NO HE PODIDO MIRAR ─────────────────────────────────
CPC_ENFORCEMENT_CENSUS="$BANCO/no-existe.tsv" bash "$GATE" >"$BANCO/1.log" 2>&1
comprobar "censo ausente es NO HE PODIDO MIRAR" 2 "$?"

# ── 2 · Censo presente pero VACÍO ⇒ medición desaparecida, no un cero ─────────────────────
: >"$BANCO/vacio.tsv"
CPC_ENFORCEMENT_CENSUS="$BANCO/vacio.tsv" bash "$GATE" >"$BANCO/2.log" 2>&1
comprobar "censo vacío es NO HE PODIDO MIRAR, no un cero" 2 "$?"

# ── 3 · Contrato OpenAPI ilegible ⇒ no se pudo contar ────────────────────────────────────
printf '{no soy json' >"$BANCO/malo.json"
CPC_OPENAPI_CONTRACT="$BANCO/malo.json" bash "$GATE" >"$BANCO/3.log" 2>&1
comprobar "contrato ilegible es NO HE PODIDO MIRAR" 2 "$?"

# ── 4 · Contrato válido y SIN rutas ⇒ tampoco es un cero ─────────────────────────────────
printf '{"paths":{}}' >"$BANCO/sin-rutas.json"
CPC_OPENAPI_CONTRACT="$BANCO/sin-rutas.json" bash "$GATE" >"$BANCO/4.log" 2>&1
comprobar "contrato sin rutas es NO HE PODIDO MIRAR" 2 "$?"

# ── 5 · El mensaje del 2 dice que NO se pudo comprobar ───────────────────────────────────
if grep -q "UNVERIFIED" "$BANCO/1.log" 2>/dev/null; then
	printf '  ok    %-56s\n' "el 2 se explica con UNVERIFIED"
	pasan=$((pasan + 1))
else
	printf '  FALLA %-56s\n' "el 2 salió sin decir que no se pudo comprobar"
	fallan=$((fallan + 1))
fi

# ── 6 · CONTROL NEGATIVO: una cifra REALMENTE equivocada sigue siendo un hallazgo (1) ─────
# Sin esta celda, un gate que devolviera 2 siempre pasaría las cinco de arriba.
# El señuelo lo lleva TODO por construcción —es el árbol entero enlazado—, que es la única
# forma de que un señuelo no se mida a sí mismo: uno al que le falte lo que el sujeto lee
# devuelve rojo por el fichero ausente y se lee como «detectó la cifra». El gate no usa `git`
# (comprobado: sus únicas menciones son comentarios), así que excluir `.git` no le quita nada.
ARBOL="$BANCO/arbol"
mkdir -p "$ARBOL" || exit 2
if ! cp -al $(ls -A "$RAIZ" | grep -v '^\.git$' | sed "s|^|$RAIZ/|") "$ARBOL/" 2>"$BANCO/6.cp"; then
	# Sin hardlinks (otro sistema de ficheros) se copia de verdad: más lento, mismo resultado.
	rm -rf "${ARBOL:?}"/* 2>/dev/null
	cp -a $(ls -A "$RAIZ" | grep -v '^\.git$' | sed "s|^|$RAIZ/|") "$ARBOL/" 2>>"$BANCO/6.cp" || {
		echo "test-public-counts-verdicts: ⛔ NO HE PODIDO MIRAR: no se pudo montar el señuelo" >&2
		exit 2
	}
fi

# ── 6 · CONTROL POSITIVO del señuelo: sin mutar, el gate tiene que salir 0 ─────────────
# Sin esto, el rojo de la celda 7 podría venir de que al señuelo le falta algo, y estaríamos
# midiendo el señuelo en vez de el gate.
# ⛔ SE INVOCA LA COPIA DEL SEÑUELO, NO `$GATE`. `check-public-counts.sh:47` hace
#    `cd "$(dirname "$0")/.."`: se ancla al árbol DONDE VIVE EL SCRIPT y le da igual el cwd.
#    Escribí esto como `( cd "$ARBOL" && bash "$GATE" )` y salió **verde** — porque medía el
#    árbol real, sin mutar. El señuelo no era el sujeto de nada.
#
#    Y lo que lo destapó fue el CONTROL NEGATIVO, no el positivo: «sin mutar sale 0» se cumple
#    igual si el señuelo está bien que si se ignora entero. Es la familia de siempre — una sonda
#    que contesta lo mismo para cualquier entrada no ha medido nada—, y por eso la celda que
#    rompe una cifra de verdad es la que vale.
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/6.log" 2>&1
base_rc=$?
# ⛔ ESTO ES UNA PRECONDICIÓN, NO UNA CELDA MÁS, Y ABORTA.
#
# Era `comprobar … 0 "$?"` y SEGUÍA. Medido el 2026-08-20 sobre CUATRO árboles del mismo SHA: con
# `web/node_modules` la batería sale **10/10**; sin él, **5/5** en dos worktrees distintos; y en un
# árbol de lote, **2/8**. Tres cifras y un solo hecho: **si el señuelo sin mutar no sale limpio, la
# línea base está rota y todo lo que viene después mide contra ella.** Cuántas celdas caigan depende
# de cuál tropiece primero, no de qué hay en el árbol — por eso 5 aquí y 8 allá **no son dos
# defectos: es una base rota contada de dos maneras**.
#
# Y el daño no es el número, es que **un recuento PARECE un diagnóstico**: mandó a buscar cinco
# defectos que no existen, incluida una búsqueda de «qué rama lo arregla» cuya respuesta era
# NINGUNA, mientras `lint:public-counts` bloqueaba el carril rápido de los cinco carriles (línea 774
# del hook, sin `|| true`, bajo `set -euo pipefail`).
#
# Con el aborto la respuesta sólo puede ser **verde** o **«no puedo correr aquí»**. Nunca «5 fallan»
# en una caja y «8» en otra sobre el mismo commit.
if [ "$base_rc" -ne 0 ]; then
	echo "test-public-counts-verdicts: ⛔ NO HE PODIDO CORRER: el señuelo SIN mutar ya sale ${base_rc}, no 0." >&2
	echo "  La línea base está rota, así que ninguna celda posterior mediría el gate: medirían el señuelo." >&2
	echo "  Causa medida el 2026-08-20: falta la cadena de herramientas WEB de ESTE worktree." >&2
	echo "  Remedio, y es el camino documentado: \`task setup\` (Taskfile.yml:16 — «git hooks + cosign" >&2
	echo "  containment + commit tooling + web deps»). El arranque de sesión instala SOLO la herramienta" >&2
	echo "  de commits y remite a \`task setup\` cuando la sesión toca /web, así que un worktree recién" >&2
	echo "  creado NO tiene la cadena y este gate no puede correr en él." >&2
	head -12 "$BANCO/6.log" 2>/dev/null | sed 's/^/    /' >&2
	exit 2
fi
comprobar "el señuelo SIN mutar sale limpio (si no, mide el señuelo)" 0 "$base_rc"

# ── 7 · y mutado, el gate tiene que decir HALLAZGO ─────────────────────────────────────
sed -i 's/\b158 integrations\b/157 integrations/' "$ARBOL/README.md" || exit 2
grep -q '157 integrations' "$ARBOL/README.md" || {
	echo "test-public-counts-verdicts: ⛔ NO HE PODIDO MIRAR: el señuelo no quedó mutado" >&2
	exit 2
}
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/7.log" 2>&1
comprobar "una cifra pública equivocada sigue siendo un HALLAZGO" 1 "$?"

# ── 8 · y el árbol REAL no se ha tocado en ningún momento ──────────────────────────────
# Es la celda que responde por el arreglo entero, y además cubre un riesgo NUEVO que el
# hardlink introduce: si el gate escribiera EN SITIO sobre un fichero enlazado, corrompería el
# original. Si alguien reintroduce la mutación en el árbol, o el gate escribe, esto se pone rojo.
if grep -q '157 integrations' "$RAIZ/README.md" 2>/dev/null; then
	printf '  FALLA %-56s\n' "el árbol real quedó MUTADO" >&2
	fallan=$((fallan + 1))
else
	printf '  ok    %-56s\n' "el árbol real no se toca en ningún instante"
	pasan=$((pasan + 1))
fi

# Y el árbol queda como estaba: una batería que deja el repo tocado es peor que no tenerla.
bash "$GATE" >"$BANCO/7.log" 2>&1
comprobar "el árbol vuelve a estar limpio tras la mutación" 0 "$?"

echo "test-public-counts-verdicts: $pasan pasan, $fallan fallan"
[ "$fallan" -eq 0 ] || exit 1
exit 0
