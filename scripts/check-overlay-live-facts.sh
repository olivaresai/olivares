#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-overlay-live-facts.sh — THE single live contract for the three overlay facts the
# 2026-08-20 actas froze as absences. 0 CLEAN · 1 finding · 2 COULD NOT LOOK.
#
# ⛔ POR QUE EXISTE, y no es una cuarta comprobacion mas: TRES gates rojos afirmaban una
# CARENCIA que el codigo integrado ya no tiene. `daa083e56f331af6158475fc304fee633acbfc2b`
# (2026-09-02, «feat(activation): enable packs and modules, refuse unpurchased») anadio la
# composicion de compra a `durableLicensed` y la fila `iso42001` al catalogo. Las tres actas
# del 20/08 eran CIERTAS sobre su pin `bada7f7` y dejaron de describir el main de hoy.
#
# La adjudicacion (an internal design note (not shipped)) separa las dos cosas
# que estaban fundidas en un solo guion:
#
#   · el REGISTRO HISTORICO — lo conservan `check-c03-06-needs-decision.sh` y las dos
#     `check-c13-01-iso42001-*.sh`, que desde hoy comprueban la integridad de SU acta y NO
#     comparan esa ausencia con ningun fichero vivo. Sus mensajes lo dicen.
#   · el HECHO PRESENTE — este guion, y solo este. Un contrato vivo, no tres.
#
# ⛔ TRES REGLAS QUE ESTE LECTOR NO PUEDE ROMPER, cada una por un defecto medido:
#
#   1. LEE BLOBS SELLADOS, NO EL ARBOL. Los tres guiones viejos leian ficheros del working
#      tree bajo `OLIVARES_ENT_DIR`: un fichero local modificado cambiaba su veredicto sin
#      mover `main`, asi que no podian acreditar que nada «aterrizo en main». Aqui se exige
#      el sello de frescura (a repository gate), se fija ESE `origin/main` y se leen sus objetos con
#      `git --no-replace-objects`. El indice, el arbol y un HEAD de otra rama no participan.
#   2. NO ES UN CENSO DE TOKENS. Un comentario que nombre `addonGate` no puede poner en rojo
#      la evaluacion del legal-hold, y conservar `fromGrants(` / `StatusExpired` / la llamada
#      AIMS no puede hacer pasar un cuerpo que ya no decide lo que el contrato afirma.
#      `scripts/overlay-ast` parsea los blobs con go/parser+go/ast, juzga predicados sobre
#      el arbol y compara el digest de la forma cerrada revisada. Una forma nueva legible
#      que el contrato no sabe verificar es 1, «estructura no verificada». La fila del
#      catalogo se lee como DATO del literal `[]AddonSpec`. Esa comparacion vive en
#      `scripts/lib/overlay-facts.py`, el evaluador que este lector COMPARTE con
#      `check-overlay-candidate-facts.sh`; el lector del candidato no reimplementa ningun predicado.
#   3. UN AVANCE DE SHA SOLO SE ACEPTA SI LAS PROPIEDADES SIGUEN SIENDO CIERTAS. El acta
#      pina `overlay_main_sha`, que `remeasure-overlay-actas.sh` ya sabe re-medir; si una
#      capacidad DESAPARECE, el candidato re-medido sigue rojo y la herramienta lo declara
#      «CHANGED FACT … needs a decision» en vez de curarlo. No se amplia su allowlist.
#
# ⚠ LO QUE ESTE GATE NO ACREDITA, dicho aqui para que no se cite de mas: no acredita panel
# comercial, firma de release, presencia de bytes en un artefacto publicado, certificacion
# ISO ni preparacion para produccion. `panel_executed` sigue false y `u_f`/`u_d` UNKNOWN, y
# este guion EXIGE que sigan asi: un contrato de hechos no puede cerrar una decision.
#
# ⛔ `go run` COLAPSA EL CODIGO DE SALIDA (un Exit(2) del lector se vuelve 1). Se construye
# el binario con GOWORK=off y se ejecuta; 126/127 (noexec) es «no he podido mirar».

set -euo pipefail
NAME=check-overlay-live-facts
say() { printf '%s\n' "$*"; }
fail() { say "$NAME: FAIL — $*" >&2; exit 1; }
cannot() { say "$NAME: COULD NOT LOOK — $*" >&2; exit 2; }

# ⛔ AISLAMIENTO DE SELECTORES GIT HEREDADOS, antes del primer git. Este lector invoca git
# contra DOS repositorios (overlay y Community) y reserva scratch; `GIT_DIR` manda sobre
# `-C`, asi que un selector heredado leeria OTRO object store. Fail-closed: no poder
# cargar el saneador es «no he podido mirar», nunca CLEAN. OLIVARES_ROOT no se toca.
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	cannot "cannot source $_olivares_git_env (git-env isolation)"
}
unset _olivares_git_env

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || cannot "cannot enter $ROOT"

JSON="${OLIVARES_OVERLAY_LIVE_FACTS_JSON:-design/overlay-live-facts.json}"
DOC="${OLIVARES_OVERLAY_LIVE_FACTS_DOC:-design/OVERLAY-LIVE-FACTS-2026-09-05.md}"

[ -f "$JSON" ] || cannot "missing $JSON"
[ -f "$DOC" ] || cannot "missing $DOC"
command -v python3 >/dev/null 2>&1 || cannot "python3 is not on PATH"
command -v git >/dev/null 2>&1 || cannot "git is not on PATH"
command -v go >/dev/null 2>&1 || cannot "go is not on PATH: the AST reader was not built"

AST_SRC="$ROOT/scripts/overlay-ast"
[ -d "$AST_SRC" ] && [ -f "$AST_SRC/main.go" ] && [ -f "$AST_SRC/go.mod" ] \
	|| cannot "missing $AST_SRC: the live contract cannot look without its AST reader"
FACTS="$ROOT/scripts/lib/overlay-facts.py"
[ -r "$FACTS" ] || cannot "missing $FACTS: the live contract cannot look without its fact evaluator"

# El documento del contrato vivo dice lo que el contrato NO cierra. Si pierde esa frase, el
# gate deja de ser legible como lo que es y se rehusa antes de mirar nada.
grep -q 'does not close the panel' "$DOC" || fail "$DOC lost the non-closure statement"
grep -q 'historical record' "$DOC" || fail "$DOC lost the link to the historical record"

# ⚠ `$OLIVARES_ENT_DIR` se resuelve AQUI, en codigo, para que el clasificador de actas siga
# viendo esta acta como VIVA (`check-overlay-actas-class.sh:56`).
ENT="${OLIVARES_ENT_DIR:-}"
[ -n "$ENT" ] || cannot "OLIVARES_ENT_DIR unset: no overlay is named, so nothing was measured"
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory: $ENT"
git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1 || cannot "OLIVARES_ENT_DIR is not a git repo: $ENT"

# ⛔ LA FRESCURA SE EXIGE, NO SE SUPONE (a repository gate), y desde LT1 la exige el Modulo, que es
# quien captura: un `origin/main` congelado no es un veredicto — el 2026-08-29 la caja entera
# comparo contra un ref con un merge de retraso y todos los gates dijeron CLEAN.
#
# ⛔ LT1 · ESTE LECTOR YA ERA EL UNICO DE LOS SIETE QUE LEIA TODO A UN 40-HEX CAPTURADO, y lo
# capturaba del ACTA (`pin = acta["overlay_main_sha"]`) tras exigir que coincidiera con el ref
# vivo. Esa exigencia es justo el acoplamiento que LT1 retira: ahora el SHA lo captura el
# Modulo del clon SELLADO, y el `overlay_main_sha` del acta pasa a ser la LINEA BASE que el
# Modulo valida contra sus propios objetos (existe, sus distancias son ciertas de el, y es
# ANCESTRO del main capturado). El acta no se reescribe para que este gate pase.
#
# ⚠ Y su fallo con clon ausente NO se relaja: este lector exige el clon y sale 2 sin el. La
# ruta `static-only` que los otros seis conservan NO existe aqui, a proposito.
. "$ROOT/scripts/lib/overlay-measurement.sh" || cannot "cannot load scripts/lib/overlay-measurement.sh"
_mrc=0
olivares_overlay_measure_open overlay-live-facts "$JSON" current "$ENT" || _mrc=$?
if [ "$_mrc" = 2 ]; then
	cannot "$OLIVARES_OVERLAY_MEASUREMENT_WHY"
elif [ "$_mrc" != 0 ]; then
	fail "$OLIVARES_OVERLAY_MEASUREMENT_WHY"
fi

[ -n "${TMPDIR:-}" ] && mkdir -p "$TMPDIR" 2>/dev/null
_bin_dir="$(mktemp -d "${TMPDIR:-/tmp}/overlayfacts-bin.XXXXXX")" \
	|| cannot "cannot create a scratch dir (TMPDIR=${TMPDIR:-unset})"
_err="$(mktemp "${TMPDIR:-/tmp}/overlayfacts.XXXXXX")" \
	|| cannot "cannot create a scratch file (TMPDIR=${TMPDIR:-unset})"
cleanup() { rm -rf "$_bin_dir" "$_err"; }
trap cleanup EXIT

AST_BIN="$_bin_dir/overlay-ast"
build_err="$(cd "$AST_SRC" && GOWORK=off go build -o "$AST_BIN" . 2>&1)" || {
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	cannot "the overlay AST reader did not build"
}
if [ ! -x "$AST_BIN" ]; then
	cannot "built overlay-ast under TMPDIR=${TMPDIR:-/tmp} but it is not executable (noexec?)"
fi

_rc=0
# The fact evaluator is SHARED with `check-overlay-candidate-facts.sh` (OGL2 release-boundary
# decision 1): one file, the same predicates, digests and 0/1/2. This adapter hands it the
# Module's captured main and nothing else — there is no candidate SHA to override here.
python3 "$FACTS" main "$ENT" "$ROOT" "$JSON" "$AST_BIN" "$OLIVARES_OVERLAY_CURRENT_SHA" 2>"$_err" || _rc=$?

cat "$_err" >&2
# 126/127 is "python3 could not run": an unobserved input, so it enters the contracted
# translation as a 2 rather than as a code the record has no meaning for.
_reader_rc="$_rc"
if [ "$_rc" -eq 126 ] || [ "$_rc" -eq 127 ]; then
	say "$NAME: python3 could not run (exit $_rc)" >&2
	_reader_rc=2
fi
_frc=0
olivares_overlay_measure_finish "$_reader_rc" \
	"the captured current overlay main contradicts the live acta: legal-hold consults no entitlement; durableLicensed applies the purchase composition; iso42001 catalogued with its build-tag cut; public slug map read at the CURRENT gitlink" \
	|| _frc=$?
case "$_frc" in
2) cannot "$OLIVARES_OVERLAY_FINAL_WHY" ;;
1) fail "$OLIVARES_OVERLAY_FINAL_WHY" ;;
esac

say "$NAME: CLEAN — on the SEALED overlay main: legal-hold override evaluation consults no"
say "  commercial entitlement; durableLicensed keeps the term AND applies the current"
say "  purchase composition to its pack; iso42001 is catalogued in compliance-packs with its"
say "  disposition and its build-tag cut. Panel/release stay OPEN (panel_executed=false,"
say "  u_f/u_d=UNKNOWN) and no certification is claimed. Bodies were judged by go/ast, not"
say "  by token presence."
exit 0
