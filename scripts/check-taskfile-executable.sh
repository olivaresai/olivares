#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-taskfile-executable.sh [--base <ref>] [--self-test]
#
# Rechaza una tarea del `Taskfile.yml` que no puede ejecutar nada: sin `cmds:`, sin `deps:` y sin
# `cmd:`. Contrato de tres respuestas, como el resto del arbol:
#
#   clean<TAB><razon>        exit 0   ninguna tarea NUEVA quedo inejecutable
#   finding<TAB><razon>      exit 1   nombradas: las tareas y por que
#   unreadable<TAB><razon>   exit 2   NO HE PODIDO MIRAR
#
# ── DE DONDE SALE, MEDIDO EL 2026-08-20 SOBRE `main` ──────────────────────────────────────────
# DIECISEIS tareas habian perdido sus claves, en un bloque CONTIGUO de 77 lineas (1522-1599) — una
# sola operacion, no dieciseis descuidos. El integrador las partio en dos formas y la segunda es la
# que da miedo:
#
#   7 · el VALOR de la tarea es una CADENA  ->  `task` la manda al shell:
#         task lint:c05-08-legal-entities  ->  "C05-08": executable file not found  ·  rc=201
#         y `scripts/check-c05-08-legal-entities.sh` (49 lineas, existe) NO SE EJECUTA NUNCA.
#   9 · la tarea solo tiene `desc:`         ->  correrla NO HACE NADA y **sale 0**.
#
# ⇒ Nueve gates que responderian «limpio» sin haber mirado. **Un gate muerto no falla: certifica.**
# Y ninguna de las 16 la llamaba el hook ni el CI, asi que nada se puso rojo en ningun momento.
#
# ── POR QUE LA LINEA BASE SE DERIVA Y NO SE ESCRIBE ───────────────────────────────────────────
# `main` arrastra hoy esas 16. Un gate absoluto rechazaria TODOS los push por un daño que no es de
# quien empuja — «baseline rojo», que convierte un control en un bloqueo y termina desactivado. Y un
# fichero de linea base envejece: hay que acordarse de encogerlo. Aqui la base se deriva de `$BASE`
# en cada corrida, asi que **el gate se aprieta solo** segun se reparan, sin que nadie mantenga nada,
# y llega a tolerancia cero sin un commit mas.
#
# ── LO QUE NO MIRA, DECLARADO ─────────────────────────────────────────────────────────────────
# L-1 · No comprueba que el comando exista ni que haga algo util: sólo que la tarea TENGA comando.
#       `lint:export-closure` y `lint:taskfile-graph` cubren otras mitades de esa pregunta.
# L-2 · Lee el YAML con una gramatica de dos espacios de indentacion, que es la del fichero. Un
#       Taskfile con otra indentacion se leeria mal, asi que si no encuentra NINGUNA tarea sale 2:
#       «no he podido mirar» y no «esta limpio».
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/git-env.sh 2>/dev/null || {
	printf 'check-taskfile-executable: FATAL: no puedo cargar scripts/lib/git-env.sh\n' >&2
	exit 2
}

di() { printf '%s\t%s\n' "$1" "$2"; }
no_puedo() { di unreadable "$1"; exit 2; }

BASE="${OLIVARES_TASKFILE_BASE:-origin/main}"
[ "${1:-}" = "--self-test" ] && exec bash scripts/test-check-taskfile-executable.sh
[ "${1:-}" = "--base" ] && { BASE="${2:-}"; [ -n "$BASE" ] || no_puedo "--base sin valor"; }

command -v git >/dev/null 2>&1 || no_puedo "git no es ejecutable"
[ -f Taskfile.yml ] || no_puedo "no encuentro Taskfile.yml en la raiz"

# Las tareas sin nada ejecutable, de un fichero cualquiera. `deps:` cuenta: una tarea que solo
# depende de otras es legitima, y exigir `cmds:` a secas produce falsos y acaba con el gate apagado.
# Esa mitad la aporto otro carril el 2026-08-20; sin ella yo lo habia dejado escrito como
# limitacion en vez de comprobarlo.
sin_ejecutable() {
	awk '
		/^  [A-Za-z][A-Za-z0-9:._-]*:$/ {
			if (t != "") { vistas++; if (!ok) print t }
			t = $0; sub(/^  /, "", t); sub(/:$/, "", t); ok = 0; next
		}
		/^[ \t]*(cmds|deps|cmd)[ \t]*:/ { ok = 1 }
		END {
			if (t != "") { vistas++; if (!ok) print t }
			if (vistas == 0) exit 9
		}
	' "$1"
}

AHORA=$(sin_ejecutable Taskfile.yml | LC_ALL=C sort) ||
	no_puedo "no he reconocido NINGUNA tarea en Taskfile.yml — la gramatica no encaja y un cero asi no es 'limpio'"

BASE_SHA=$(git rev-parse -q --verify "${BASE}^{commit}" 2>/dev/null) ||
	no_puedo "no puedo resolver la base '${BASE}' — sin base no se puede separar lo nuevo de lo heredado"
BTMP=$(mktemp "${TMPDIR:-/tmp}/tfexec.XXXXXX") || no_puedo "mktemp fallo"
trap 'rm -f "$BTMP"' EXIT
git show "${BASE_SHA}:Taskfile.yml" > "$BTMP" 2>/dev/null ||
	no_puedo "la base '${BASE}' no tiene Taskfile.yml"
ANTES=$(sin_ejecutable "$BTMP" | LC_ALL=C sort) ||
	no_puedo "no he reconocido ninguna tarea en el Taskfile de la base"

NUEVAS=$(comm -13 <(printf '%s\n' "$ANTES") <(printf '%s\n' "$AHORA") 2>/dev/null | grep . || true)
HEREDADAS=$(printf '%s\n' "$ANTES" | grep -c . || true)

if [ -n "$NUEVAS" ]; then
	{
		printf 'check-taskfile-executable: estas tareas NO PUEDEN EJECUTAR NADA y no estaban asi en %s:\n' "$BASE"
		printf '%s\n' "$NUEVAS" | sed 's/^/  · /'
		printf '\n  Una tarea sin `cmds:`, `deps:` ni `cmd:` tiene dos finales, y el segundo es peor:\n'
		printf '    · si su valor quedo como CADENA, `task` la manda al shell y muere con 127;\n'
		printf '    · si solo le quedo `desc:`, correrla NO HACE NADA y **sale 0** — un gate que\n'
		printf '      responde «limpio» sin haber mirado.\n'
		printf '\n  Casi siempre es una fusion que conservo la prosa y perdio la clave. Busca la\n'
		printf '  descripcion que quedo sin su `desc:` o el `- bash ...` sin su `cmds:` encima.\n'
	} >&2
	di finding "$(printf '%s' "$NUEVAS" | tr '\n' ' ' | sed 's/ $//')"
	exit 1
fi

di clean "ninguna tarea nueva quedo inejecutable (heredadas de ${BASE}: ${HEREDADAS}, no son de este push)"
exit 0
