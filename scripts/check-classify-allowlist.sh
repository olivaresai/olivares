#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# check-classify-allowlist.sh — guarda la ÚNICA lista que puede apagar mainline-ci en silencio: el
# `case` del job `classify` que declara un push "journal-only" y con eso SALTA los nueve jobs
# gateados. Un job saltado NO se pone rojo: desaparece del cuadro.
#
# ⛔ LA PREGUNTA CORRECTA COSTÓ TRES INTENTOS, y los tres errores están aquí porque son el valor.
#
#   (1) «¿lee alguien esta ruta?» — daba SUCIO en las tres entradas de un árbol limpio: `sessions/`
#       lo leen 45 guiones y ninguno deja de correr. Retirado sin publicar.
#   (2) «¿lo lee algo que no sea fast-lint?» — seguía acusando, porque contaba *_test.go que sólo
#       llevan la CADENA en una ruta de fixture.
#   (3) La buena: **¿lo lee algo que DEJE DE CORRER cuando el job se salta?** Dos ramas:
#         · guion  -> hueco si mainline-ci lo invoca Y los fast-lints NO.
#         · test   -> hueco si ESCAPA de su directorio con `../` hacia esa ruta del repositorio.
#                     Los tests sólo corren en los jobs gateados.
#
# Y el número que esa pregunta destapa, medido el 2026-08-24: de 56 menciones de `design/` en
# *_test.go, **UNA** escapa — `commercial/commerce-lint/canon_test.go:12`,
# `realCanon = "../../design/PRICING-CANON.md"` — y sus tests corren bajo `task test:commerce-core`
# dentro de `race-rest`, que ESTÁ gateado (mainline-ci.yml:1409-1680). Ése es el hueco real de
# `design/*`, y no los que este carril publicó dos veces: los lectores de an internal design note (not shipped)
# y an internal design note (not shipped) son fast-lints y NO dejan de correr.
#
# De 232 menciones de `sessions/` en tests, la única con `../` es `../sessions/…` desde
# modules/eventing, que resuelve a **modules/sessions**, el PAQUETE Go, no al diario del repo. Casar
# el prefijo la contaría como lector y acusaría a un árbol sano: por eso aquí se RESUELVE la ruta.
#
# Tres respuestas, nunca dos: 0 LIMPIO · 1 SUCIO · 2 NO HE PODIDO MIRAR.
# Uso: check-classify-allowlist.sh [--self-test]
set -uo pipefail

# ⛔ AISLAMIENTO DEL ENTORNO GIT — obligatorio para cualquier miembro de la clase que empareje
#    `mktemp -d` con git, y este guion lo hace (`git grep` arriba, `mktemp -d` mas abajo).
#
#    La razon, de la cabecera de `check-git-env-isolation.sh`: git EXPORTA `GIT_DIR` a los hooks
#    desde un worktree ENLAZADO, y **`GIT_DIR` manda sobre `-C`**. Sin neutralizarlo, un
#    `git -C "$tmp" ...` actua sobre el repositorio VIVO y un `git init "$tmp"` inicializa el
#    `GIT_DIR` real. No es teorico: es la familia que ya se ha llevado un worktree en este arbol.
#
#    Medido el 2026-08-24: sin estas lineas, `lint:git-env` daba VERDICT: BROKEN nombrando a este
#    guion, y como corre en la posicion 83 del carril rapido paraba TODO push de rama de los cinco
#    carriles tras ~80 minutos de gate. Los otros 60 miembros de la clase ya lo hacian.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || { echo "check-classify-allowlist: ⛔ NO HE PODIDO MIRAR: no puedo cargar $_olivares_git_env" >&2; exit 2; }
unset _olivares_git_env

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)" || {
	echo "check-classify-allowlist: NO HE PODIDO MIRAR — no resuelvo la raíz del repositorio" >&2; exit 2; }
cd "$ROOT" || exit 2

# ⛔ AISLAMIENTO DE GIT_DIR, Y ENTRO TARDE: este guion empareja `mktemp -d` con `git`, y el
#    trinquete `check-git-env-isolation.sh` exige que los miembros de esa clase o bien
#    sourceen esta biblioteca o bien RECHACEN un `GIT_DIR` envenenado. Este no hacia ninguna
#    de las dos, asi que `task lint:git-env` daba BROKEN sobre `main` — y ese gate corre en
#    `.githooks/pre-push:1051` SIN `|| true`, o sea que **dejo a todos los carriles sin poder
#    empujar con hook**. Lo detecto otro carril cuando su push murio ahi.
#
#    La forma del defecto es la que este mismo dia ya nombramos dos veces: un gate NUEVO que
#    rompe a otro. La primera vez fueron mis dos gates contra `sigpipe` y el trinquete del
#    export; esta es la tercera, y el roto es el trinquete de `git-env`.
#
#    Y la trampa de diagnosticarlo, que conviene dejar escrita: el mensaje del gate NOMBRA
#    ESTE FICHERO, asi que quien vea el rojo va a creer que rompio el algo. El gate acusa
#    donde el desfase SE VE, no donde se ORIGINO — igual que el sello del bundle.
# shellcheck source=/dev/null
. "${ROOT}/scripts/lib/git-env.sh" || {
	echo "check-classify-allowlist: NO HE PODIDO MIRAR — no puedo sourcear scripts/lib/git-env.sh" >&2
	exit 2
}

# Sustituible por entorno para poder EJERCITAR el gate contra un workflow mutado sin tocar el del
# repositorio. Un control que no se puede poner rojo a voluntad no se ha probado.
WF="${OLIVARES_CLASSIFY_WF:-.github/workflows/mainline-ci.yml}"
HOOK=".githooks/pre-push"
TASKS="Taskfile.yml"

morir2() { echo "check-classify-allowlist: NO HE PODIDO MIRAR — $1" >&2; exit 2; }

# ── la lista se DERIVA del workflow, anclada en la ESTRUCTURA ────────────────────────────
# El arm es la primera línea tras `case "$f" in` que termina en `) ;;`. Anclar en un valor concreto
# sería teclear media lista, y una lista tecleada salta justo lo que no nombra.
derivar_lista() {
	local wf="$1" linea
	[ -f "$wf" ] || return 2
	linea="$(awk '/case "\$f" in/ { f=1; next } f && /\) ;;$/ { print; exit }' "$wf")" || return 2
	[ -n "$linea" ] || return 2
	printf '%s\n' "$linea" | sed 's/) ;;$//' | tr '|' '\n' \
		| sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | grep -v '^$' | grep -v '^#'
}

# ── indirección hook/workflow -> `task X` -> Taskfile -> scripts/ ────────────────────────
# Buscar el nombre del GUION dentro del hook clasifica el envoltorio: da 2 de 40 donde son 23 de 45.
scripts_de() {
	local origen="$1" patron="$2" t
	while IFS= read -r t; do
		[ -n "$t" ] || continue
		awk -v want="$t" '
			$0 == "  " want ":" { f=1; next }
			f && /^  [A-Za-z0-9:_.-]+:$/ { f=0 }
			f { print }
		' "$TASKS" 2>/dev/null | grep -oE 'scripts/[A-Za-z0-9_.-]+'
	done < <(grep -oE "$patron" "$origen" 2>/dev/null | sed 's/task *//' | sort -u)
	grep -ohE 'scripts/[A-Za-z0-9_.-]+' "$origen" 2>/dev/null
}

# ── lectores de GUION: referencia ejecutable, no comentario ──────────────────────────────
# NO `awk -F:` sobre `git grep -n`: la salida es `ruta:linea:CONTENIDO` y el contenido lleva dos
# puntos en casi toda línea de shell, así que awk la trocea y la rejunta mal. Devolvió un CERO
# cómodo el 2026-08-23 y la cifra viajó a cinco buzones. Aquí se trocea por parámetro.
# Tampoco `\s` en `grep -E`: no es ERE POSIX y el `grep` de estas cajas es un shim de ugrep.
lectores_script() {
	local aguja="$1" hit f resto cuerpo limpio
	while IFS= read -r hit; do
		f=${hit%%:*}; resto=${hit#*:}; cuerpo=${resto#*:}
		limpio=$(printf '%s' "$cuerpo" | sed 's/^[[:space:]]*//')
		case "$limpio" in '#'* | '//'* | '*'* | '/*'* | '') continue ;; esac
		printf '%s\n' "$f"
	done < <(git grep -n -F -- "$aguja" -- 'scripts/*' 2>/dev/null) | sort -u
}

# ── lectores de TEST: los que ESCAPAN con `../` y resuelven a esa ruta del repo ──────────
lectores_test() {
	local aguja="$1" hit f resto cuerpo rel dir abs
	while IFS= read -r hit; do
		f=${hit%%:*}; resto=${hit#*:}; cuerpo=${resto#*:}
		for rel in $(printf '%s' "$cuerpo" | grep -oE '"[^"]*\.\./[^"]*"' | tr -d '"'); do
			dir=$(dirname "$f")
			abs=$(cd "$dir" 2>/dev/null && cd "$(dirname "$rel")" 2>/dev/null && pwd)/$(basename "$rel")
			case "$abs" in "$ROOT/$aguja"*) printf '%s\n' "$f" ;; esac
		done
	done < <(git grep -n -F -- "$aguja" -- '*_test.go' 2>/dev/null) | sort -u
}

sonda_de() { case "$1" in *'/*') printf '%s' "${1%/\*}/" ;; *) printf '%s' "$1" ;; esac; }

comprobar() {
	local wf="$1" lista fast ci pat sonda lector huecos=0 total=0 guardados solo_gateado por_test
	lista="$(derivar_lista "$wf")" || return 2
	[ -n "$lista" ] || return 2
	fast="$(scripts_de "$HOOK" 'task +lint:[A-Za-z0-9:_-]+' | sort -u)"
	[ -n "$fast" ] || morir2 "no derivé un solo script de los fast-lints; la indirección no resolvió"
	ci="$(scripts_de "$wf" 'task +[a-z:_-]+' | sort -u)"
	[ -n "$ci" ] || morir2 "no derivé un solo script de mainline-ci"

	while IFS= read -r pat; do
		total=$((total + 1))
		sonda="$(sonda_de "$pat")"
		guardados=0; solo_gateado=""
		while IFS= read -r lector; do
			[ -n "$lector" ] || continue
			if grep -qxF "$lector" <<<"$fast"; then
				guardados=$((guardados + 1))
			elif grep -qxF "$lector" <<<"$ci"; then
				solo_gateado="$solo_gateado $lector"
			fi
		done < <(lectores_script "$sonda")
		por_test="$(lectores_test "$sonda" | tr '\n' ' ')"
		if [ -n "$solo_gateado" ] || [ -n "${por_test// /}" ]; then
			[ -n "$solo_gateado" ] && echo "  ⛔ $pat — guion que SÓLO corre en un job gateado:$solo_gateado"
			[ -n "${por_test// /}" ] && echo "  ⛔ $pat — test que lee esta ruta (sólo corre gateado): $por_test"
			huecos=$((huecos + 1))
		else
			echo "  ok $pat — $guardados lector(es) de guion, todos en fast-lints; 0 tests la leen"
		fi
	done <<<"$lista"

	if [ "$huecos" -gt 0 ]; then
		echo "check-classify-allowlist: SUCIO — $huecos de $total prefijo(s) perdonados los lee algo que"
		echo "  DEJA DE CORRER cuando classify salta los jobs. Ese gate no se pondría rojo: desaparece."
		return 1
	fi
	echo "check-classify-allowlist: LIMPIO — $total prefijo(s) perdonados; nada que deje de correr los lee."
	return 0
}

if [ "${1:-}" = "--self-test" ]; then
	fallos=0
	ok() { printf '  ok    %-56s %s\n' "$1" "$2"; }
	mal() { printf '  FAIL  %-56s %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

	if grep -qx 'ESTADO-PROYECTO.md' <<<"$(derivar_lista "$WF")"; then
		ok "la lista se deriva del workflow" "encontrada ESTADO-PROYECTO.md"
	else mal "la lista se deriva del workflow" "no salió la entrada conocida"; fi

	n=$(scripts_de "$HOOK" 'task +lint:[A-Za-z0-9:_-]+' | sort -u | grep -c .)
	if [ "$n" -gt 300 ]; then ok "la indirección del hook resuelve" "$n scripts"
	else mal "la indirección del hook resuelve" "sólo $n: se quedó en el envoltorio"; fi

	if grep -q 'check-public-counts.sh' <<<"$(lectores_script 'docs/ai-context/')"; then
		ok "el troceo ve un lector real" "check-public-counts.sh"
	else mal "el troceo ve un lector real" "cero: el troceo está roto otra vez"; fi

	if grep -q 'check-connectors.sh' <<<"$(lectores_script 'docs/ai-context/')"; then
		mal "un comentario no es un lector" "check-connectors.sh sólo la cita en un #"
	else ok "un comentario no es un lector" "check-connectors.sh descartado"; fi

	if grep -q 'commercial/commerce-lint/canon_test.go' <<<"$(lectores_test 'design/')"; then
		ok "un test que ESCAPA sí es lector" "canon_test.go -> ../../design/PRICING-CANON.md"
	else mal "un test que ESCAPA sí es lector" "no lo vio: la resolución de ruta está rota"; fi

	if grep -q . <<<"$(lectores_test 'sessions/')"; then
		mal "../sessions es el PAQUETE, no el diario" "lo contó como lector del diario"
	else ok "../sessions es el PAQUETE, no el diario" "resuelto a modules/sessions y descartado"; fi

	rc=0; comprobar "$WF" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq 0 ]; then ok "el árbol de hoy sale LIMPIO" "rc=0"
	else mal "el árbol de hoy sale LIMPIO" "rc=$rc — acusa a un árbol sano"; fi

	tmp="$(mktemp -d "${TMPDIR:-/tmp}/cca.XXXXXX")" || morir2 "sin directorio temporal"
	sed 's#sessions/\* | design/audits/\*#sessions/* | design/* | design/audits/*#' "$WF" > "$tmp/mut.yml"
	if grep -q 'design/\* |' "$tmp/mut.yml"; then
		rc=0; salida="$(comprobar "$tmp/mut.yml" 2>&1)" || rc=$?
		if [ "$rc" -eq 1 ] && grep -q 'canon_test.go' <<<"$salida"; then
			ok "MUTANTE design/* sale SUCIO por PRICING-CANON" "rc=1, nombra canon_test.go"
		else mal "MUTANTE design/* sale SUCIO por PRICING-CANON" "rc=$rc y/o no nombra canon_test.go"; fi
	else mal "el mutante se inyectó" "sed no cambió nada"; fi
	rm -rf "$tmp"

	rc=0; comprobar "/no/existe.yml" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq 2 ]; then ok "un workflow ilegible es 'no he podido mirar'" "rc=2"
	else mal "un workflow ilegible es 'no he podido mirar'" "rc=$rc"; fi

	echo "check-classify-allowlist --self-test: $((9 - fallos)) pasan, $fallos fallan"
	[ "$fallos" -eq 0 ] || exit 1
	exit 0
fi

comprobar "$WF"
