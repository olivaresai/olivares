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
	echo "test-public-counts-verdicts: FATAL: cannot load $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
LC_ALL=C
export LC_ALL

RAIZ="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)"
GATE="$RAIZ/scripts/check-public-counts.sh"
[ -r "$GATE" ] || {
	echo "test-public-counts-verdicts: ⛔ CANNOT INSPECT: $GATE does not exist" >&2
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
	echo "test-public-counts-verdicts: ⛔ CANNOT INSPECT: could not create the test workspace" >&2
	exit 2
}
# The trap no longer restores files because the suite no longer mutates the real tree.
# The first version edited README.md then restored it added trap restoration
# (9361223a2) for process death. That did not prevent another worker's git add -A
# from collecting the intermediate “157 integrations” copy. Shortening the exposure
# window did not close it, as CLAUDE.md's rule zero explains.
# Run the negative control in a hardlink fixture tree (cp -al, .git excluded;
# 981 ms measured). sed -i replaces the inode, leaving the original untouched.
# Verified fixture inode 56530459 / real 16692728, mutated count 1 / real 0:
# the actual tree is never temporarily modified.
trap '[ -n "${BANCO:-}" ] && rm -rf "$BANCO"' EXIT

pass_count=0
fail_count=0
comprobar() {
	if [ "$3" -eq "$2" ]; then
		printf '  ok    %-56s rc=%s\n' "$1" "$3"
		pass_count=$((pass_count + 1))
	else
		printf '  FAIL %-56s rc=%s (expected %s)\n' "$1" "$3" "$2"
		fail_count=$((fail_count + 1))
	fi
}

# ── SUELO: el árbol sano tiene que salir 0, o todo lo demás mide otra cosa ────────────────
bash "$GATE" >"$BANCO/0.log" 2>&1
comprobar "the healthy tree passes" 0 "$?"

# ── 1 · Censo de aplicación ausente ⇒ NO HE PODIDO MIRAR ─────────────────────────────────
CPC_ENFORCEMENT_CENSUS="$BANCO/no-existe.tsv" bash "$GATE" >"$BANCO/1.log" 2>&1
comprobar "missing census means CANNOT INSPECT" 2 "$?"

# ── 2 · Censo presente pero VACÍO ⇒ medición desaparecida, no un cero ─────────────────────
: >"$BANCO/vacio.tsv"
CPC_ENFORCEMENT_CENSUS="$BANCO/vacio.tsv" bash "$GATE" >"$BANCO/2.log" 2>&1
comprobar "empty census means CANNOT INSPECT, not zero" 2 "$?"

# ── 3 · Contrato OpenAPI ilegible ⇒ no se pudo contar ────────────────────────────────────
printf '{no soy json' >"$BANCO/malo.json"
CPC_OPENAPI_CONTRACT="$BANCO/malo.json" bash "$GATE" >"$BANCO/3.log" 2>&1
comprobar "unreadable contract means CANNOT INSPECT" 2 "$?"

# ── 4 · Contrato válido y SIN rutas ⇒ tampoco es un cero ─────────────────────────────────
printf '{"paths":{}}' >"$BANCO/sin-rutas.json"
CPC_OPENAPI_CONTRACT="$BANCO/sin-rutas.json" bash "$GATE" >"$BANCO/4.log" 2>&1
comprobar "contract without routes means CANNOT INSPECT" 2 "$?"

# ── 5 · El mensaje del 2 dice que NO se pudo comprobar ───────────────────────────────────
if grep -q "UNVERIFIED" "$BANCO/1.log" 2>/dev/null; then
	printf '  ok    %-56s\n' "exit 2 is explained with UNVERIFIED"
	pass_count=$((pass_count + 1))
else
	printf '  FAIL %-56s\n' "exit 2 did not explain inability to verify"
	fail_count=$((fail_count + 1))
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
		echo "test-public-counts-verdicts: ⛔ CANNOT INSPECT: could not stage the decoy" >&2
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
	echo "test-public-counts-verdicts: ⛔ CANNOT RUN: the UNMUTATED decoy already exits ${base_rc}, not 0." >&2
	echo "  The baseline is broken, so subsequent cells would measure the decoy rather than the gate." >&2

	# ⛔ EL DIAGNÓSTICO SE MIDE AQUÍ, NO SE RECITA.
	#
	# Hasta el 2026-08-31 este bloque afirmaba SIEMPRE «falta la cadena de herramientas WEB de ESTE
	# worktree», que fue la causa medida el 2026-08-20 — y la imprimía fuese cual fuese el motivo
	# real. Y enseñaba `head -12` del log, cuando check-public-counts.sh pone sus hallazgos AL
	# FINAL: las doce primeras líneas son OK/EXCLUDED/NOTE. O sea, un diagnóstico seguro que no
	# había comprobado, con evidencia que no contenía el fallo.
	#
	# Coste medido ese día: el hallazgo real era `video: manifest[render] ... reel.html changed`
	# —de otro carril, ya en curso— y se leyó como «27 variables documentadas fuera de
	# config_registry.go», que es una NOTE informativa que cae dentro de esas doce líneas. Mandó a
	# curar una cifra que no estaba rota. Es el mismo defecto que este fichero denuncia doce líneas
	# más arriba: **un recuento PARECE un diagnóstico**.
	if [ ! -d "$ARBOL/web/node_modules" ]; then
		echo "  Verified: \`web/node_modules\` is missing from the test tree. This is ONE known cause of a" >&2
		echo "  broken baseline — not necessarily TODAY'S cause: the findings below determine the verdict." >&2
		echo "  Documented remedy: \`task setup\` (Taskfile.yml:16 — «git hooks + cosign" >&2
		echo "  containment + commit tooling + web deps»). Session startup installs ONLY the commit tooling" >&2
		echo "  and refers to \`task setup\` when a session touches /web, so a newly created worktree" >&2
		echo "  does NOT have the toolchain and this gate cannot run there." >&2
	else
		echo "  \`web/node_modules\` IS present, ruling out the missing web toolchain. The cause appears in" >&2
		echo "  the findings below; fix them at their source." >&2
	fi

	echo "  What the gate actually found (its findings, not its preamble):" >&2
	if ! grep -aE '^\s*(FAIL|.*:[[:space:]]*(⛔|BROKEN))|^\s{2,}[a-z-]+:' "$BANCO/6.log" 2>/dev/null \
		| grep -avE 'NOTE|EXCLUDED|^\s*OK ' | head -12 | sed 's/^/    /' >&2; then
		echo "    (could not isolate findings; full log in $BANCO/6.log)" >&2
	fi
	exit 2
fi
comprobar "the UNMUTATED decoy passes (otherwise the test measures the decoy)" 0 "$base_rc"

# ── 7 · y mutado, el gate tiene que decir HALLAZGO ─────────────────────────────────────
# La cifra que se muta es la que el README declara hoy, no un literal: un literal caduca con
# el primer módulo o conector nuevo y deja la celda midiendo nada ("no quedó mutado").
# Uno menos cae dentro del vecindario ±8 que el gate prohíbe, así que tiene que ser HALLAZGO.
real_readme="$(sha256sum < "$RAIZ/README.md")"
n="$(grep -oE '(^|[^0-9])[0-9]+ integrations' "$ARBOL/README.md" | head -1 | grep -oE '[0-9]+')"
[ -n "$n" ] || {
	echo "test-public-counts-verdicts: ⛔ CANNOT INSPECT: the decoy README declares no integrations" >&2
	exit 2
}
m=$((n - 1))
sed -i "s/\b$n integrations\b/$m integrations/" "$ARBOL/README.md" || exit 2
grep -q "\b$m integrations\b" "$ARBOL/README.md" || {
	echo "test-public-counts-verdicts: ⛔ CANNOT INSPECT: the decoy was not mutated ($n → $m)" >&2
	exit 2
}
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/7.log" 2>&1
comprobar "an incorrect public count remains a FINDING" 1 "$?"

# ── 7b · a broken delegated leg must not silence the counts ────────
# The gate ran its three delegated legs BEFORE the count body and exited on the first
# failure, so a leg that could not look (2) hid every count finding behind it — measured
# on main 2026-10-08: exit at the config-env leg with zero count findings while the count
# body held 338. With the README still mutated by cell 7, break one leg and demand BOTH
# verdicts in one run: rc 1 (the false claim outranks the blindness) and the count
# finding still present. The fake leg lands through `mv` — a plain `>` would truncate the
# hardlink's shared inode and clobber the real tree, exactly what cell 8 guards against.
printf '#!/bin/sh\nexit 2\n' >"$BANCO/fake-leg.sh"
mv "$BANCO/fake-leg.sh" "$ARBOL/scripts/check-config-env-docs.sh"
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/7b.log" 2>&1
comprobar "a broken delegated leg does not silence the counts" 1 "$?"
if grep -q "integrations (measured" "$BANCO/7b.log" && grep -q "configuration reference" "$BANCO/7b.log"; then
	printf '  ok    %-56s\n' "broken leg recorded, count finding still visible"
	pass_count=$((pass_count + 1))
else
	printf '  FAIL %-56s\n' "the broken leg hid the count or was not recorded" >&2
	fail_count=$((fail_count + 1))
fi

# ── 7c · a DRIFTING delegated leg must not silence the counts either ─────────────────
# 7b covers the leg that could not look (2); the measured incident on main was DRIFT (1)
# at the config-env leg — and a plausible regression is "keep stopping on real drift".
# Same demand with an exit-1 stub: the count finding stays the verdict and the leg's
# drift wording is recorded in the same log.
printf '#!/bin/sh\nexit 1\n' >"$BANCO/fake-leg-drift.sh"
mv "$BANCO/fake-leg-drift.sh" "$ARBOL/scripts/check-config-env-docs.sh"
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/7c.log" 2>&1
comprobar "a drifting delegated leg does not silence the counts" 1 "$?"
if grep -q "integrations (measured" "$BANCO/7c.log" && grep -q "out of date" "$BANCO/7c.log"; then
	printf '  ok    %-56s\n' "drifting leg recorded, count finding still visible"
	pass_count=$((pass_count + 1))
else
	printf '  FAIL %-56s\n' "the drifting leg hid the count or was not recorded" >&2
	fail_count=$((fail_count + 1))
fi

# ── 7d/7e · the combined-exit TAIL itself, on counts that are CLEAN ────────────────────
# In 7b/7c the rc 1 comes from the count body (the mutated README), so a tail that
# swapped its branches — or never ran — would still pass those cells. These two run the
# UNMUTATED decoy (README restored to canon) so the verdict is produced by the tail
# alone: drift leg ⇒ 1 with "reported drift", blind leg ⇒ 2 with "counts themselves
# were checked".
sed -i "s/\b$m integrations\b/$n integrations/" "$ARBOL/README.md"
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/7d.log" 2>&1
comprobar "clean counts + drifting leg exits 1 via the tail" 1 "$?"
if grep -q "reported drift" "$BANCO/7d.log"; then
	printf '  ok    %-56s\n' "tail drift verdict recorded"
	pass_count=$((pass_count + 1))
else
	printf '  FAIL %-56s\n' "tail drift verdict missing" >&2
	fail_count=$((fail_count + 1))
fi
printf '#!/bin/sh\nexit 2\n' >"$BANCO/fake-leg-blind.sh"
mv "$BANCO/fake-leg-blind.sh" "$ARBOL/scripts/check-config-env-docs.sh"
bash "$ARBOL/scripts/check-public-counts.sh" >"$BANCO/7e.log" 2>&1
comprobar "clean counts + blind leg exits 2 via the tail" 2 "$?"
if grep -q "could not look (exit 2" "$BANCO/7e.log"; then
	printf '  ok    %-56s\n' "tail blind verdict recorded"
	pass_count=$((pass_count + 1))
else
	printf '  FAIL %-56s\n' "tail blind verdict missing" >&2
	fail_count=$((fail_count + 1))
fi

# Abnormal exits are inability to compare, not evidence of a false claim. Exercise
# each delegate through the real gate with clean counts, including shell noexec /
# not-found statuses and a simulated signal status. Restore files via rename.
cp "$ARBOL/scripts/check-config-env-docs.sh" "$BANCO/saved-env.sh"
printf '#!/bin/sh\nexit 0\n' >"$BANCO/clean-env.sh"
mv "$BANCO/clean-env.sh" "$ARBOL/scripts/check-config-env-docs.sh"
for leg in check-config-env-docs check-cli-ref-docs check-openapi-op-descriptions; do
	cp "$ARBOL/scripts/$leg.sh" "$BANCO/saved-leg.sh"
	for abnormal_rc in 126 127 137; do
		printf '#!/bin/sh\nexit %s\n' "$abnormal_rc" >"$BANCO/abnormal-leg.sh"
		mv "$BANCO/abnormal-leg.sh" "$ARBOL/scripts/$leg.sh"
		log="$BANCO/$leg-$abnormal_rc.log"
		bash "$ARBOL/scripts/check-public-counts.sh" >"$log" 2>&1
		comprobar "clean counts + $leg exit $abnormal_rc" 2 "$?"
		if grep -q "died abnormally (exit $abnormal_rc)" "$log" \
			&& grep -q "CANNOT LOOK" "$log" \
			&& grep -q "could not look (exit 2" "$log" \
			&& ! grep -qE 'reported drift|out of date|--write' "$log"; then
			printf '  ok    %-56s\n' "$leg exit $abnormal_rc explains inability to compare"
			pass_count=$((pass_count + 1))
		else
			printf '  FAIL %-56s\n' "$leg exit $abnormal_rc misclassified" >&2
			fail_count=$((fail_count + 1))
		fi
	done
	mv "$BANCO/saved-leg.sh" "$ARBOL/scripts/$leg.sh"
done
mv "$BANCO/saved-env.sh" "$ARBOL/scripts/check-config-env-docs.sh"

# ── 8 · y el árbol REAL no se ha tocado en ningún momento ──────────────────────────────
# Es la celda que responde por el arreglo entero, y además cubre un riesgo NUEVO que el
# hardlink introduce: si el gate escribiera EN SITIO sobre un fichero enlazado, corrompería el
# original. Si alguien reintroduce la mutación en el árbol, o el gate escribe, esto se pone rojo.
if [ "$(sha256sum < "$RAIZ/README.md")" != "$real_readme" ]; then
	printf '  FAIL %-56s\n' "the real tree was MUTATED" >&2
	fail_count=$((fail_count + 1))
else
	printf '  ok    %-56s\n' "the real tree is never touched"
	pass_count=$((pass_count + 1))
fi

# Y el árbol queda como estaba: una batería que deja el repo tocado es peor que no tenerla.
bash "$GATE" >"$BANCO/7.log" 2>&1
comprobar "the tree is clean again after the mutation" 0 "$?"

echo "test-public-counts-verdicts: $pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ] || exit 1
exit 0
