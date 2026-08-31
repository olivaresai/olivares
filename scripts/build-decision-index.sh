#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# build-decision-index.sh — MEM-03 entry point: preflight, dispatch, and the self-test battery.
#
# The derivation itself is scripts/build-decision-index.py; this file is the part that must be
# true BEFORE python runs, plus the battery that keeps the python honest. Split that way for a
# measured reason: the interpreter is the dependency most likely to be missing or wrong, and a
# builder that reports its own absence as "nothing to index" is the blind gate this repository
# has already paid for more than once.
#
# THREE ANSWERS, NEVER TWO:
#   0  the index is current / was written
#   1  BROKEN — the tree and the index disagree, and every reason is named
#   2  UNVERIFIED — python3 absent, the repo unreadable, the census empty, front-matter
#      malformed. "I could not look" never exits 0.
#
# Usage:
#   scripts/build-decision-index.sh                 # regenerate an internal design note (not shipped) (+ FTS5)
#   scripts/build-decision-index.sh --check         # the GATE: stale index => rc 1, named
#   scripts/build-decision-index.sh --with-memory   # regenerate and ingest the memory notes
#   scripts/build-decision-index.sh --ingest-memory # MEM-07 only, refresh the memory half
#   scripts/build-decision-index.sh --query 'texto' # three answers, never two
#   scripts/build-decision-index.sh --self-test     # 21 named witnesses
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)"

# ⛔ AISLAMIENTO DE ENTORNO GIT. Required of every script that pairs `mktemp -d` with git —
# and the self-test below does exactly that. git exports GIT_DIR to hooks from a LINKED
# worktree, and GIT_DIR OUTRANKS `-C`: without this, `git -C "$fixture" init` would stamp
# core.bare=true on the REAL repository this script was run from. Measured 2026-08-06; the
# mechanism is scripts/lib/git-env.sh and the gate is `task lint:git-env`.
# shellcheck source=/dev/null
. "${HERE}/lib/git-env.sh" || {
	echo "FATAL: cannot source ${HERE}/lib/git-env.sh (git-env isolation)" >&2
	exit 2
}

PY="${HERE}/build-decision-index.py"

preflight() {
	if ! command -v python3 >/dev/null 2>&1; then
		echo "build-decision-index: python3 is NOT on PATH. The index is UNVERIFIED." >&2
		echo "  This builder uses the python3 sqlite3 MODULE on purpose: measured in this" >&2
		echo "  container 2026-08-16, the \`sqlite3\` BINARY is not installed, so a shell-out" >&2
		echo "  builder could never have run here." >&2
		exit 2
	fi
	if [ ! -f "${PY}" ]; then
		echo "build-decision-index: ${PY} is missing; UNVERIFIED." >&2
		exit 2
	fi
}

# ---------------------------------------------------------------------------------------
# SELF-TEST. Every case has a NAME, and the name is what a mutant kills. A battery that only
# reports "N failed" cannot tell you WHICH guard is load-bearing, and this repository has
# already shipped a witness that measured the wrong guard because two of them could refuse
# the same input.
# ---------------------------------------------------------------------------------------

SELFTEST_OK=0
SELFTEST_KO=0

# ⛔ LOS FIXTURES SE RECOGEN, y el contraste `sol max` del 2026-08-22 midio por que importa: nueve
# llamantes de `mkfixture` dejaban **9 directorios y 3408 KiB POR EJECUCION**. Mientras esto era una
# tarea bajo demanda el residuo era de quien la corriera; al cablearlo al pre-push (`#1573`) pasa a
# ser coste de CADA push con commits, sobre un `/tmp` de 4 GB compartido por varios carriles — que
# ese mismo dia se lleno al 100 % y rompio la publicacion de otro proceso.
#
# ⛔ Y NO SE LLEVA UN REGISTRO EN UNA VARIABLE, aunque sea un array: MEDIDO aqui mismo, esa version
# dejaba la fuga intacta en 9. `mkfixture` se invoca con `$(...)`, o sea en un SUBSHELL, asi que
# cualquier `+=` muere con el y el trap del padre recoge una lista vacia. El arreglo PARECIA correcto
# y no hacia nada; sin medir la fuga antes y despues se habria publicado como cerrado.
#
# Por eso el ancla es un DIRECTORIO RAIZ creado en el shell padre: los fixtures nacen dentro y mueren
# con el, sin que nadie tenga que apuntarlos. El trap se COMPONE — un `trap ... EXIT` a secas
# sustituye al que hubiera antes y entonces no hay segunda oportunidad de borrar nada.
# ⛔ Y EL NOMBRE NO EMPIEZA POR PUNTO, a proposito. Los fixtures se llamaban `.bdi.XXXXXX` y por eso
# la fuga vivio DOS DIAS sin que nadie la viera: un punto delante los esconde de `ls` y **ningun glob
# `/tmp/*` los alcanza**, asi que cualquier limpieza escrita con esa forma pasaba de largo. Lo cazo el
# integrador el 2026-08-22 midiendo «0 entradas dejadas · 10 652 KiB» — dos cosas imposibles a la vez,
# y la contradiccion era su `ls -1`. Si el trap fallara alguna vez, el residuo tiene que VERSE.
# ⛔ EL ROOT SE CREA PEREZOSAMENTE, Y NO ES ESTILO: creado aqui, arriba y sin condicion, FUGABA.
# Las cinco ramas del despacho terminan en `exec python3` (:374-378), y `exec` REEMPLAZA el
# proceso, asi que el `trap ... EXIT` de abajo no llega a dispararse nunca. Medido el 2026-08-23
# sobre esta misma rama: tres `--check` seguidos dejaban TRES `bdi-root.*` (1 -> 4 en el
# directorio), cada uno vacio, y uno de ellos con `rc=0` y el veredicto en verde.
#
# El balance real del arreglo anterior era 9 directorios poblados -> 1 vacio POR PUSH, no 0, y el
# cuerpo del PR decia «fuga medida: 0» porque midio solo `--self-test`. El unico consumidor de
# esta variable es `mkfixture` (:105), y `mkfixture` solo lo llama `self_test`: en las rutas que
# hacen exec, python recibe `--root "${ROOT}"`, que es el repositorio, y no toca este directorio.
# ⇒ crear el root solo cuando de verdad hace falta deja la fuga en CERO por construccion, en vez
# de confiar en un trap que esa ruta no puede ejecutar.
bdi_crea_root() {
	[ -n "${BDI_TMPROOT:-}" ] && return 0
	BDI_TMPROOT="$(mktemp -d "${TMPDIR:-${HOME:-/tmp}}/bdi-root.XXXXXX")" || {
		echo "build-decision-index: no he podido crear el directorio de fixtures." >&2
		exit 2
	}
}
bdi_limpia_fixtures() {
	[ -n "${BDI_TMPROOT:-}" ] && [ -d "${BDI_TMPROOT}" ] && rm -rf -- "${BDI_TMPROOT}"
}
trap 'bdi_limpia_fixtures' EXIT

mkfixture() {
	# A real git repository, because the census IS the git index: `git ls-files` is the
	# enumeration under test, and a bare directory would exercise a different program.
	local d
	# ${HOME:-/tmp} y no $HOME a secas: HOME falta en seis de los nueve runners, y bajo
	# `set -u` la linea muere con el nombre del PASO y no con el de la variable — el
	# 2026-08-19 un $HOME sin guarda tumbo `control-plane` bajo el rotulo «license
	# boundary» con la licencia impecable. Aqui solo se crea un repo de fixture, sin
	# ejecutar binarios, asi que /tmp sirve de ultimo recurso aunque este montado noexec.
	d="$(mktemp -d "${BDI_TMPROOT}/f.XXXXXX")"
	# ⛔ Y SI `mktemp` FALLA, SE PARA AQUÍ. No es defensa de cortesía: con `$d` vacío las tres
	# líneas de abajo son `git -C "" config user.email …`, y `git -C ""` opera sobre el
	# DIRECTORIO ACTUAL — o sea, escribe la identidad del señuelo en el repositorio REAL desde
	# el que se invoca. Reproducido el 2026-08-20 en un repo de usar y tirar: `real@example.com`
	# pasó a `t@example.invalid` con sólo esa línea. En un clon COMPARTIDO reescribe el autor de
	# los cinco carriles, y el rojo llega mucho después, en el `lint:commit-identity` de un push
	# ajeno, con el commit ya hecho y la dirección equivocada dentro. Me pasó en vivo.
	if [ -z "$d" ] || [ ! -d "$d" ]; then
		echo "build-decision-index: NO HE PODIDO MIRAR: mktemp no devolvió un directorio usable" >&2
		return 2 2>/dev/null || exit 2
	fi
	git init -q "$d"
	git -C "$d" config user.email "t@example.invalid"
	git -C "$d" config user.name "t"
	git -C "$d" config commit.gpgsign false
	mkdir -p "$d/design" "$d/scripts"
	printf '%s\n' "$d"
}

doc() { # <fixture> <name> <key> <status> <decided> <authority> [superseded-by]
	local d="$1" name="$2" key="$3" status="$4" decided="$5" auth="$6" sb="${7:-}"
	{
		printf -- '---\n'
		printf 'decision: %s\n' "$key"
		printf 'status: %s\n' "$status"
		[ -n "$sb" ] && printf 'superseded-by: %s\n' "$sb"
		printf 'decided: %s\n' "$decided"
		printf 'authority: %s\n' "$auth"
		printf -- '---\n\n'
		printf '# %s\n\nCuerpo de la decision %s.\n' "$name" "$key"
	} > "$d/design/$name.md"
}

run_sut() { # <fixture> <args...> -> prints output, returns rc
	local d="$1"; shift
	# rc captured ON THE LINE: under `set -e` a separate `rc=$?` never runs.
	python3 "${PY}" --root "$d" --db "$d/idx.sqlite3" "$@" 2>&1 || return $?
}

case_rc() { # <name> <want-rc> <fixture> <args...>
	local name="$1" want="$2" d="$3"; shift 3
	local out rc=0
	out="$(run_sut "$d" "$@")" || rc=$?
	if [ "$rc" = "$want" ]; then
		SELFTEST_OK=$((SELFTEST_OK + 1))
		printf '  ok   %-38s rc=%s\n' "$name" "$rc"
	else
		SELFTEST_KO=$((SELFTEST_KO + 1))
		printf '  FAIL %-38s rc=%s want=%s\n' "$name" "$rc" "$want"
		printf '%s\n' "$out" | sed 's/^/       | /' | head -8
	fi
}

case_grep() { # <name> <pattern> <fixture> <args...>  — rc ignored, OUTPUT must name it
	local name="$1" pat="$2" d="$3"; shift 3
	local out rc=0
	out="$(run_sut "$d" "$@")" || rc=$?
	# NO PIPE: `grep -q` exits on the first match, SIGPIPEs the printf, and under
	# pipefail the pipeline returns 141 WHEN IT SUCCEEDS (lint:sigpipe-booleans).
	if grep -qF -- "$pat" <<<"$out"; then
		SELFTEST_OK=$((SELFTEST_OK + 1))
		printf '  ok   %-38s (named %%s)\n' "$name" >/dev/null
		printf '  ok   %-38s names %s\n' "$name" "$pat"
	else
		SELFTEST_KO=$((SELFTEST_KO + 1))
		printf '  FAIL %-38s did NOT name %s (rc=%s)\n' "$name" "$pat" "$rc"
		printf '%s\n' "$out" | sed 's/^/       | /' | head -8
	fi
}

case_assert() { # <name> <0|1 result> ; 0 = pass
	local name="$1" res="$2"
	if [ "$res" = 0 ]; then
		SELFTEST_OK=$((SELFTEST_OK + 1)); printf '  ok   %s\n' "$name"
	else
		SELFTEST_KO=$((SELFTEST_KO + 1)); printf '  FAIL %s\n' "$name"
	fi
}

self_test() {
	preflight
	bdi_crea_root
	echo "build-decision-index --self-test"

	# --- 1/2: a well-formed decision is parsed, and its STATUS reaches the record --------
	local d; d="$(mkfixture)"
	doc "$d" A anillo-unico vigente 2026-08-16 fran
	# ⛔ A SECOND document with a DIFFERENT status, and it is not decoration. With only a
	# `vigente` document in the fixture, a mutant that hardcodes `"status": "vigente"` in the
	# record produces byte-identical output and SURVIVES — measured, it did. A battery that
	# only ever sees one value of a field cannot tell "reads the field" from "prints a
	# constant".
	doc "$d" P propuesta-abierta propuesta 2026-08-16 sesion
	run_sut "$d" build --no-db >/dev/null
	local rc
	# The two names say EXACTLY which field each one reads. They did not, briefly: the case
	# called `status_present_in_record` was asserting the decision KEY, so a mutant that
	# dropped `status` would have been reported dead by a witness that never looked at it —
	# the "witness measuring the wrong guard" defect, in miniature, inside its own battery.
	rc=0; grep -qF '"status":"vigente"' "$d/design/DECISIONES.ndjson" || rc=1
	case_assert "status_present_in_record" "$rc"
	rc=0; grep -qF '"decision":"anillo-unico"' "$d/design/DECISIONES.ndjson" || rc=1
	case_assert "decision_key_present_in_record" "$rc"
	rc=0; grep -qF '"status":"propuesta"' "$d/design/DECISIONES.ndjson" || rc=1
	case_assert "status_propuesta_reflected" "$rc"

	# --- 3: index current => GREEN. The (b) direction of the counterfactual -------------
	case_rc "check_green_when_current" 0 "$d" check --no-db

	# --- 4: a NEW decision that was not regenerated => RED, and NAMED. Direction (a) ----
	doc "$d" B segundo-anillo vigente 2026-08-16 sesion
	case_rc "check_reddens_on_new_decision" 1 "$d" check --no-db
	case_grep "check_names_the_new_decision" "design/B.md" "$d" check --no-db

	# --- 5: an EDITED decision body => RED. Without the content hash the index would
	#        only ever notice files appearing and disappearing. -------------------------
	run_sut "$d" build --no-db >/dev/null
	printf 'una linea mas que cambia el contenido\n' >> "$d/design/B.md"
	case_rc "check_reddens_on_edited_decision" 1 "$d" check --no-db

	# --- 6: an annotation REMOVED => RED (the index would otherwise keep a ghost) -------
	run_sut "$d" build --no-db >/dev/null
	printf '# B sin front-matter\n' > "$d/design/B.md"
	case_rc "check_reddens_on_removed_annotation" 1 "$d" check --no-db

	# --- 7: determinism — two builds over one tree are byte-identical -------------------
	run_sut "$d" build --no-db >/dev/null
	cp "$d/design/DECISIONES.ndjson" "$d/first.ndjson"
	run_sut "$d" build --no-db >/dev/null
	rc=0; cmp -s "$d/first.ndjson" "$d/design/DECISIONES.ndjson" || rc=1
	case_assert "deterministic_twice" "$rc"

	# --- 7-bis: the SUPERSEDED axis end to end — recorded, and reported as a verdict -----
	# This is the whole point of the register: a superseded decision must be able to SAY so,
	# and the successor must come back with it. A document superseded in silence is exactly
	# the failure that put a retired document into a live citation this week.
	local s; s="$(mkfixture)"
	doc "$s" T sucesor-manda vigente 2026-08-16 fran
	doc "$s" S sucesor-manda superseded 2026-08-10 sesion "design/T.md"
	run_sut "$s" build >/dev/null 2>&1 || true
	rc=0; grep -qF '"superseded_by":"design/T.md"' "$s/design/DECISIONES.ndjson" || rc=1
	case_assert "superseded_chain_recorded" "$rc"
	rc=0
	# NO PIPE, same reason: grep -q short-circuits and 141 would read as success.
	_q="$(run_sut "$s" query "sucesor")"
	grep -qF 'SUPERSEDED por design/T.md' <<<"$_q" || rc=1
	case_assert "query_reports_superseded_verdict" "$rc"

	# --- 8: the date is the COMMIT date, never the mtime --------------------------------
	local e; e="$(mkfixture)"
	doc "$e" C fecha-de-commit vigente 2026-08-16 fran
	git -C "$e" add -A >/dev/null 2>&1
	GIT_AUTHOR_DATE="2021-03-04T05:06:07+00:00" GIT_COMMITTER_DATE="2021-03-04T05:06:07+00:00" \
		git -C "$e" commit -q -m "fixture" >/dev/null 2>&1
	touch -d "2099-01-01T00:00:00" "$e/design/C.md"
	run_sut "$e" build >/dev/null 2>&1 || true
	rc=1
	if [ -f "$e/idx.sqlite3" ]; then
		local got
		got="$(python3 - "$e/idx.sqlite3" <<'PYEOF'
import sqlite3, sys
con = sqlite3.connect(sys.argv[1])
row = con.execute("SELECT committed FROM decisions WHERE path='design/C.md'").fetchone()
print(row[0] if row else "")
PYEOF
)"
		case "$got" in
			2021-03-04*) rc=0 ;;
			*) rc=1; printf '       | committed=%s (want 2021-03-04*, mtime was 2099)\n' "$got" ;;
		esac
	fi
	case_assert "date_is_commit_not_mtime" "$rc"

	# --- 9: a typo'd key is REFUSED, not ignored ----------------------------------------
	local f; f="$(mkfixture)"
	doc "$f" D clave-typo vigente 2026-08-16 fran
	# `superceded-by` is the typo a 205-file mechanical sweep produces. Ignored, it reads as
	# "no successor" and republishes a retired decision as current.
	printf -- '---\ndecision: typo\nstatus: vigente\nsuperceded-by: design/D.md\ndecided: 2026-08-16\nauthority: fran\n---\n\n# E\n' > "$f/design/E.md"
	# ⛔ `build`, NOT `check`. This case used to run `check` and it was GREEN FOR THE WRONG
	# REASON: with no index written yet, `check` refuses because the NDJSON is missing and
	# exits 1 long before the front-matter is ever judged. The mutation run proved it — a
	# mutant that ignored unknown keys entirely was still reported dead by this case. `build`
	# reaches the parser, which is the guard the name claims to measure.
	case_rc "unknown_key_rejected" 1 "$f" build --no-db
	case_grep "unknown_key_is_named" "superceded-by" "$f" build --no-db

	# --- 10: two VIGENTE under one key => refused at BUILD time (measured failure #1) ----
	local g; g="$(mkfixture)"
	doc "$g" F entitlement-corte vigente 2026-08-15 fran
	doc "$g" G entitlement-corte vigente 2026-08-16 sesion
	case_rc "duplicate_vigente_rejected" 1 "$g" build --no-db
	case_grep "duplicate_vigente_names_both" "design/F.md" "$g" build --no-db

	# --- 11/12: a successor that does not exist, and a supersede with no successor -------
	local h; h="$(mkfixture)"
	doc "$h" H cadena-rota superseded 2026-08-16 fran "design/NO-EXISTE.md"
	case_rc "broken_superseded_by_rejected" 1 "$h" build --no-db
	local i; i="$(mkfixture)"
	printf -- '---\ndecision: sin-sucesor\nstatus: superseded\ndecided: 2026-08-16\nauthority: fran\n---\n\n# I\n' > "$i/design/I.md"
	case_rc "superseded_without_successor_rejected" 1 "$i" build --no-db

	# --- 13: an EMPTY census is UNVERIFIED (exit 2), never a clean green -----------------
	local j; j="$(mkfixture)"
	case_rc "empty_census_unverified" 2 "$j" build --no-db

	# --- 14/15: MEM-07 — the ingest is VERBATIM, and what it ingests is findable ---------
	local k; k="$(mkfixture)"
	doc "$k" K nota-memoria vigente 2026-08-16 sesion
	local fake="$k/home"
	mkdir -p "$fake/.claude/projects/-workspace-proj/memory"
	# Deliberately awkward bytes: trailing spaces, a blank line, accents, a tab. A builder
	# that "tidies" notes belonging to other lanes destroys the evidence it was asked to keep.
	printf -- '---\nname: nota-rara\ndescription: "una nota con espacios   "\n---\n\nCuerpo con acentuación   \n\n\tsangrado con tabulador\n' \
		> "$fake/.claude/projects/-workspace-proj/memory/nota-rara.md"
	HOME="$fake" python3 "${PY}" --root "$k" --db "$k/idx.sqlite3" ingest-memory >/dev/null 2>&1 || true
	rc=1
	if [ -f "$k/idx.sqlite3" ]; then
		rc=0
		python3 - "$k/idx.sqlite3" "$fake/.claude/projects/-workspace-proj/memory/nota-rara.md" <<'PYEOF' || rc=1
import sqlite3, sys
con = sqlite3.connect(sys.argv[1])
row = con.execute("SELECT body FROM memory WHERE name='nota-rara'").fetchone()
if not row:
    sys.exit(1)
want = open(sys.argv[2], "rb").read().decode("utf-8")
sys.exit(0 if row[0] == want else 1)
PYEOF
	fi
	case_assert "memory_ingest_verbatim" "$rc"
	rc=0
	HOME="$fake" python3 "${PY}" --root "$k" --db "$k/idx.sqlite3" query --memory "acentuación" \
		>/dev/null 2>&1 || rc=1
	case_assert "memory_ingest_findable" "$rc"

	echo "build-decision-index --self-test: ${SELFTEST_OK} ok, ${SELFTEST_KO} failed"
	[ "${SELFTEST_KO}" -eq 0 ] || exit 1
	# A battery that can only pass is not a battery. Zero cases means the harness stopped
	# building fixtures, which is UNVERIFIED, not clean.
	[ "${SELFTEST_OK}" -ge 21 ] || {
		echo "build-decision-index: only ${SELFTEST_OK} cases ran; the battery is UNVERIFIED." >&2
		exit 2
	}
	# ⛔ LA FUGA NO VUELVE EN SILENCIO. La propiedad que hace que el trap sirva es estructural:
	# TODO fixture nace dentro de ${BDI_TMPROOT}. Si una edicion futura llama a `mktemp` con otra
	# plantilla, el trap no lo recogera y volveremos a dejar nueve directorios por push — que es
	# como estaba antes de que el contraste lo midiera. Se comprueba por FORMA, no contando
	# ficheros, porque contar depende de lo que otros carriles dejen en el mismo TMPDIR.
	# Se excluyen los COMENTARIOS: la cabecera de este guion menciona `mktemp -d` al explicar el
	# aislamiento de git, y contarla daba una linea base de 1 en vez de 0 — una sonda que cuenta
	# prosa como codigo no mide lo que dice medir.
	# ⛔ Y AHORA POR CONDUCTA, porque la guarda de FORMA de abajo tiene un punto ciego que costo
	# dos dias: mira que todo `mktemp -d` nombre BDI_TMPROOT, y la linea que CREA el root tambien
	# lo nombra — asi que pasaba en verde mientras cada ruta de despacho dejaba un root vacio.
	# Medido el 2026-08-23: tres `--check` seguidos llevaban el directorio de 1 a 4. El motivo es
	# que esas rutas terminan en `exec`, que reemplaza el proceso y no ejecuta el trap EXIT.
	#
	# Aqui se corre una ruta de despacho REAL con su propio TMPDIR y se cuenta lo que queda. El
	# directorio del testigo se hace con `mkdir` DENTRO de ${BDI_TMPROOT} a proposito: un
	# `mktemp -d` nuevo se contaria a si mismo en la guarda de forma de abajo.
	_fuga="${BDI_TMPROOT}/fuga-testigo"
	mkdir -p "${_fuga}"
	TMPDIR="${_fuga}" bash "$0" --check >/dev/null 2>&1
	_resto="$(find "${_fuga}" -maxdepth 1 -mindepth 1 2>/dev/null | wc -l)"
	if [ "${_resto}" -eq 0 ]; then
		echo "  ok    una ruta de despacho real (--check) no deja NADA en su TMPDIR"
		SELFTEST_OK=$((SELFTEST_OK + 1))
	else
		echo "  FAIL  --check dejo ${_resto} entrada(s) en su TMPDIR: el trap no alcanza a las rutas con exec" >&2
		SELFTEST_KO=$((SELFTEST_KO + 1))
	fi

	fuera="$(grep -n 'mktemp -d' "$0" | grep -v ':[[:space:]]*#' | grep -cv 'BDI_TMPROOT' || true)"
	if [ "${fuera}" -eq 0 ]; then
		echo "  ok    todos los fixtures nacen bajo BDI_TMPROOT (el trap los alcanza)"
		SELFTEST_OK=$((SELFTEST_OK + 1))
	else
		echo "  FAIL  ${fuera} mktemp fuera de BDI_TMPROOT: el trap NO los recoge" >&2
		SELFTEST_KO=$((SELFTEST_KO + 1))
	fi
	[ "${SELFTEST_KO}" -eq 0 ] || exit 1
	echo "✓ build-decision-index self-test: ${SELFTEST_OK} named witnesses green"
}

# ---------------------------------------------------------------------------------------

ROOT="$(cd -- "${HERE}/.." && pwd)"

case "${1:-}" in
	--self-test) self_test ;;
	--check)     preflight; exec python3 "${PY}" --root "${ROOT}" check --no-db ;;
	--query)     preflight; shift; exec python3 "${PY}" --root "${ROOT}" query "$@" ;;
	--ingest-memory) preflight; exec python3 "${PY}" --root "${ROOT}" ingest-memory ;;
	--with-memory)   preflight; exec python3 "${PY}" --root "${ROOT}" build --with-memory ;;
	"")          preflight; exec python3 "${PY}" --root "${ROOT}" build ;;
	*)
		echo "build-decision-index: unknown option '$1'" >&2
		sed -n '18,26p' "${BASH_SOURCE[0]:-$0}" >&2
		exit 2
		;;
esac
