#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md.
#
# check-docsite-versions.sh — cruza `VERSIONS` de docs-site/src/site-locales.mjs con los
# directorios de instantánea que hay en docs-site/src/content/docs/. En los DOS sentidos.
#
# ⛔ POR QUÉ EXISTE, y está medido con mutante, no argumentado. El 2026-08-24 se añadió a
#    `VERSIONS` un slug inventado —`{ slug: '2099-99', label: 'MUTANTE INEXISTENTE' }`— sin crear
#    su directorio, y los tres gates de documentación siguieron VERDES:
#
#      lint:docs-parity    rc=0    105 fixtures passed
#      lint:i18n-anchors   rc=0    7595 anclajes resueltos
#      lint:adr-sync       rc=0    59 fixtures passed   (gate retirado el 2026-08-25 con la
#                                                          sección ADR; la medida queda como se tomó)
#
#    `check-docs-parity.mjs:212-229` valida que `ARCHIVED_SLUGS` concuerde con `VERSIONS` —
#    consistencia INTERNA del manifiesto— y nunca comprueba que un slug TENGA contenido. Y
#    `ARCHIVED_SLUGS`, exportado en site-locales.mjs:54, no lo consume nadie más en el árbol.
#
# QUÉ ROMPE SI NADIE LO MIRA: `starlight-versions` publica un selector con una versión que no
# existe (404 en cada enlace), o al revés, una instantánea archivada queda fuera del selector y
# se vuelve inalcanzable sin dejar de ocupar sitio. Importa AHORA porque el corte de versión del
# T3 es exactamente la operación que añade una entrada a esa lista.
#
# Tres respuestas, nunca dos: 0 LIMPIO · 1 SUCIO · 2 NO HE PODIDO MIRAR.
# Uso: check-docsite-versions.sh [--self-test]
set -uo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)" || {
	echo "check-docsite-versions: COULD NOT CHECK — cannot resolve the root" >&2; exit 2; }
cd "$ROOT" || exit 2

MANIFIESTO="${OLIVARES_DOCSITE_MANIFEST:-docs-site/src/site-locales.mjs}"
CONTENIDO="${OLIVARES_DOCSITE_CONTENT:-docs-site/src/content/docs}"

cannot_read() { echo "check-docsite-versions: COULD NOT CHECK — $1" >&2; exit 2; }

# Los slugs se DERIVAN del manifiesto. No se teclean: una lista tecleada salta lo que no nombra.
# Se lee la forma `slug: '...'` dentro del array VERSIONS, acotando por sus corchetes, porque el
# fichero exporta más de un array y casar `slug:` a secas cogería los del vecino.
slugs_declarados() {
	local f="$1"
	[ -f "$f" ] || return 2
	awk '
		/export const VERSIONS/ { dentro = 1 }
		dentro { print }
		dentro && /\]/ { exit }
	' "$f" | grep -oE "slug: *'[^']+'" | sed "s/.*'\([^']*\)'.*/\1/"
}

# Un directorio de instantánea tiene forma de versión: AAAA-MM. Las carpetas de idioma (de, es,
# fr, ja, ru, zh) y las páginas sueltas no la tienen, así que no hace falta lista de exclusión.
dirs_de_instantanea() {
	local d="$1"
	[ -d "$d" ] || return 2
	find "$d" -maxdepth 1 -mindepth 1 -type d -printf '%f\n' 2>/dev/null | grep -E '^[0-9]{4}-[0-9]{2}$' | sort
}

comprobar() {
	local man="$1" cont="$2" decl dirs s huecos=0 n_decl=0 n_dir=0
	decl="$(slugs_declarados "$man")" || cannot_read "cannot read $man"
	[ -n "$decl" ] || cannot_read "$man declares no slugs in VERSIONS; either its format changed or this probe cannot measure it"
	dirs="$(dirs_de_instantanea "$cont")" || cannot_read "cannot read $cont"

	while IFS= read -r s; do
		[ -n "$s" ] || continue
		n_decl=$((n_decl + 1))
		if [ ! -d "$cont/$s" ]; then
			echo "  ⛔ DECLARED WITHOUT CONTENT: VERSIONS includes '$s', but $cont/$s is missing"
			huecos=$((huecos + 1))
		elif [ -z "$(find "$cont/$s" -type f -name '*.md*' -print -quit 2>/dev/null)" ]; then
			echo "  ⛔ DECLARED AND EMPTY: $cont/$s exists but contains no pages"
			huecos=$((huecos + 1))
		else
			echo "  ok '$s' declared and populated ($(find "$cont/$s" -type f -name '*.md*' 2>/dev/null | grep -c .) page(s))"
		fi
	done <<<"$decl"

	while IFS= read -r s; do
		[ -n "$s" ] || continue
		n_dir=$((n_dir + 1))
		if ! grep -qxF "$s" <<<"$decl"; then
			echo "  ⛔ UNDECLARED CONTENT: $cont/$s exists, but VERSIONS does not name it; inaccessible from the selector"
			huecos=$((huecos + 1))
		fi
	done <<<"$dirs"

	if [ "$huecos" -gt 0 ]; then
		echo "check-docsite-versions: FAIL — $huecos mismatch(es) between VERSIONS ($n_decl) and snapshots on disk ($n_dir)."
		return 1
	fi
	echo "check-docsite-versions: CLEAN — $n_decl declared version(s) and $n_dir snapshots match exactly."
	return 0
}

if [ "${1:-}" = "--self-test" ]; then
	fallos=0
	ok() { printf '  ok    %-52s %s\n' "$1" "$2"; }
	mal() { printf '  FAIL  %-52s %s\n' "$1" "$2"; fallos=$((fallos + 1)); }

	if grep -qx '2026-06' <<<"$(slugs_declarados "$MANIFIESTO")"; then
		ok "slugs are derived from the manifest" "found 2026-06"
	else mal "slugs are derived from the manifest" "the known slug was not found"; fi

	if grep -qx '2026-06' <<<"$(dirs_de_instantanea "$CONTENIDO")"; then
		ok "snapshots are derived from disk" "found 2026-06"
	else mal "snapshots are derived from disk" "the known directory was not found"; fi

	if grep -qxE 'de|es|fr|ja|ru|zh' <<<"$(dirs_de_instantanea "$CONTENIDO")"; then
		mal "a language directory is not a snapshot" "a language directory was included"
	else ok "a language directory is not a snapshot" "the YYYY-MM pattern excludes it"; fi

	rc=0; comprobar "$MANIFIESTO" "$CONTENIDO" >/dev/null 2>&1 || rc=$?
	if [ "$rc" -eq 0 ]; then ok "the current tree is CLEAN" "rc=0"
	else mal "the current tree is CLEAN" "rc=$rc"; fi

	# MUTANTE A — el que ningún gate cazaba: un slug declarado sin directorio.
	tmp="$(mktemp -d "${TMPDIR:-/tmp}/cdv.XXXXXX")" || cannot_read "could not create a temporary directory"
	sed "s#export const VERSIONS = \[#export const VERSIONS = [{ slug: '2099-99', label: 'MUTANTE' },#" \
		"$MANIFIESTO" > "$tmp/m.mjs"
	if grep -q '2099-99' "$tmp/m.mjs"; then
		rc=0; output="$(comprobar "$tmp/m.mjs" "$CONTENIDO" 2>&1)" || rc=$?
		if [ "$rc" -eq 1 ] && grep -q 'DECLARED WITHOUT CONTENT' <<<"$output"; then
			ok "MUTANT slug without directory fails" "rc=1 and names it"
		else mal "MUTANT slug without directory fails" "rc=$rc — this is the previously undetected case"; fi
	else mal "mutant A was injected" "sed made no changes"; fi

	# MUTANTE B — el sentido contrario: contenido en disco que nadie declara.
	mkdir -p "$tmp/contenido/2098-01" && : > "$tmp/contenido/2098-01/x.md"
	mkdir -p "$tmp/contenido/2026-06" && : > "$tmp/contenido/2026-06/x.md"
	rc=0; output="$(comprobar "$MANIFIESTO" "$tmp/contenido" 2>&1)" || rc=$?
	if [ "$rc" -eq 1 ] && grep -q 'UNDECLARED CONTENT' <<<"$output"; then
		ok "MUTANT undeclared snapshot fails" "rc=1 and names it"
	else mal "MUTANT undeclared snapshot fails" "rc=$rc"; fi
	rm -rf "$tmp"

	# En SUBSHELL: `no_pude` hace `exit 2` y sin el subshell se lleva el autotest por delante —
	# medido, el caso 7 mataba los anteriores sin imprimir nada.
	rc=0; ( comprobar "/no/existe.mjs" "$CONTENIDO" >/dev/null 2>&1 ) || rc=$?
	if [ "$rc" -eq 2 ]; then ok "an unreadable manifest returns 'could not check'" "rc=2"
	else mal "an unreadable manifest returns 'could not check'" "rc=$rc"; fi

	echo "check-docsite-versions --self-test: $((7 - fallos)) passed, $fallos failed"
	[ "$fallos" -eq 0 ] || exit 1
	exit 0
fi

comprobar "$MANIFIESTO" "$CONTENIDO"
