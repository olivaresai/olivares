#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Reject internal decision pages, citations, links and generators in the docs site.
# Exit status: 0 clean; 1 finding; 2 unreadable site tree.
set -uo pipefail

ROOT="${OLIVARES_ADR_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}"
SITE="$ROOT/docs-site"
CONTENT="$SITE/src/content/docs"

selftest() {
	# Both halves get a positive control. A gate that only proves its green is not proved at all.
	local base rc out fails=0
	base="$(mktemp -d "${TMPDIR:-/tmp}/adr-not-published.XXXXXX")" || { echo "selftest: COULD NOT CHECK: mktemp"; return 2; }
	trap 'rm -rf "$base"' RETURN

	# CASE 1 — a well-formed site with no ADR anywhere must be 0.
	mkdir -p "$base/limpio/docs-site/src/content/docs/explanation"
	printf -- '---\ntitle: Overview\n---\nprose\n' > "$base/limpio/docs-site/src/content/docs/explanation/index.md"
	out="$(OLIVARES_ADR_ROOT="$base/limpio" "$0" 2>&1)"; rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 1 (clean) expected 0, got $rc: $out"; fails=$((fails+1)); }

	# CASE 2 — an ADR directory back under content is a FINDING (1), not a warning.
	mkdir -p "$base/dir/docs-site/src/content/docs/explanation/adr"
	printf -- '---\ntitle: x\n---\n' > "$base/dir/docs-site/src/content/docs/explanation/adr/index.md"
	out="$(OLIVARES_ADR_ROOT="$base/dir" "$0" 2>&1)"; rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 2 (ADR directory) expected 1, got $rc: $out"; fails=$((fails+1)); }

	# CASE 3 — the DIRECTORY renamed away but the ADR pages kept: caught by filename shape.
	mkdir -p "$base/forma/docs-site/src/content/docs/explanation/decisiones"
	printf -- '---\ntitle: ADR-0009\n---\n' > "$base/forma/docs-site/src/content/docs/explanation/decisiones/0009-append-only-hash-chain-audit.md"
	out="$(OLIVARES_ADR_ROOT="$base/forma" "$0" 2>&1)"; rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 3 (NNNN-*.md filename) expected 1, got $rc: $out"; fails=$((fails+1)); }

	# CASE 4 — nothing published but the site still LINKS to the withdrawn route.
	mkdir -p "$base/enlace/docs-site/src/content/docs/start"
	printf -- 'see [the register](/explanation/adr/)\n' > "$base/enlace/docs-site/src/content/docs/start/how-the-docs-are-organized.md"
	out="$(OLIVARES_ADR_ROOT="$base/enlace" "$0" 2>&1)"; rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 4 (active link) expected 1, got $rc: $out"; fails=$((fails+1)); }

	# CASE 5 — the generator itself back in the tree, with no page published YET.
	mkdir -p "$base/gen/docs-site/src/content/docs/explanation" "$base/gen/docs-site/scripts"
	printf -- 'x\n' > "$base/gen/docs-site/src/content/docs/explanation/index.md"
	printf -- '// publisher\n' > "$base/gen/docs-site/scripts/sync-adr.mjs"
	out="$(OLIVARES_ADR_ROOT="$base/gen" "$0" 2>&1)"; rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 5 (generator restored) expected 1, got $rc: $out"; fails=$((fails+1)); }

	# CASE 6 — NO SUBJECT. An empty tree is «no he podido mirar» (2), never a green.
	mkdir -p "$base/vacio"
	out="$(OLIVARES_ADR_ROOT="$base/vacio" "$0" 2>&1)"; rc=$?
	[ "$rc" = 2 ] || { echo "selftest CASE 6 (missing input) expected 2, got $rc: $out"; fails=$((fails+1)); }

	# CASE 7-bis — the SAME string in docs-site/README.md is documentation of the withdrawal, green;
	#              the same string in a published page is a finding. Both halves, or the exclusion
	#              is an unproved hole.
	mkdir -p "$base/readme/docs-site/src/content/docs/explanation"
	printf -- 'x\n' > "$base/readme/docs-site/src/content/docs/explanation/index.md"
	printf -- 'the explanation/adr section was withdrawn on 2026-08-25\n' > "$base/readme/docs-site/README.md"
	out="$(OLIVARES_ADR_ROOT="$base/readme" "$0" 2>&1)"; rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 7-bis (README names the path) expected 0, got $rc: $out"; fails=$((fails+1)); }

	printf -- 'the explanation/adr section was withdrawn on 2026-08-25\n' >> "$base/readme/docs-site/src/content/docs/explanation/index.md"
	out="$(OLIVARES_ADR_ROOT="$base/readme" "$0" 2>&1)"; rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 7-ter (same text on a page) expected 1, got $rc: $out"; fails=$((fails+1)); }

	# CASE 7-quater — a numbered citation with no link at all is still a finding; naming the
	#                 PRACTICE is not.
	mkdir -p "$base/cita/docs-site/src/content/docs/reference"
	printf -- 'deliberately not JWTs (ADR-0008).\n' > "$base/cita/docs-site/src/content/docs/reference/glossary.md"
	out="$(OLIVARES_ADR_ROOT="$base/cita" "$0" 2>&1)"; rc=$?
	[ "$rc" = 1 ] || { echo "selftest CASE 7-quater (ADR-NNNN citation) expected 1, got $rc: $out"; fails=$((fails+1)); }

	printf -- 'the reasoning is recorded as architecture decision records.\n' > "$base/cita/docs-site/src/content/docs/reference/glossary.md"
	out="$(OLIVARES_ADR_ROOT="$base/cita" "$0" 2>&1)"; rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 7-quinquies (naming the practice) expected 0, got $rc: $out"; fails=$((fails+1)); }

	# CASE 8 — docs/adr/ in the DEV repo is NOT this gate's business: its presence must stay green.
	mkdir -p "$base/dev/docs-site/src/content/docs/explanation" "$base/dev/docs/adr"
	printf -- 'x\n' > "$base/dev/docs-site/src/content/docs/explanation/index.md"
	printf -- '# ADR-0001\n' > "$base/dev/docs/adr/0001-something.md"
	out="$(OLIVARES_ADR_ROOT="$base/dev" "$0" 2>&1)"; rc=$?
	[ "$rc" = 0 ] || { echo "selftest CASE 8 (private docs/adr) expected 0, got $rc: $out"; fails=$((fails+1)); }

	if [ "$fails" -eq 0 ]; then
		echo "check-adr-not-published --selftest: 11/11 (clean · directory · filename · link · generator · missing input · README path mention · page path mention · numbered citation · practice mention · private docs/adr)"
		return 0
	fi
	echo "check-adr-not-published --selftest: $fails failing case(s)"
	return 1
}

if [ "${1:-}" = "--selftest" ]; then selftest; exit $?; fi

# ── ¿Hay sujeto? ─────────────────────────────────────────────────────────────────────────────
[ -d "$CONTENT" ] || {
	echo "check-adr-not-published: COULD NOT CHECK: missing $CONTENT" >&2
	exit 2
}

hallazgos=0
nombra() { hallazgos=$((hallazgos+1)); printf '  ✗ %s\n' "$1" >&2; }

# A · ningún directorio de registro publicado.
while IFS= read -r d; do
	[ -n "$d" ] || continue
	nombra "published ADR directory: ${d#"$ROOT/"}"
done < <(find "$CONTENT" -type d -name adr 2>/dev/null)

# B · ni con el directorio renombrado: la FORMA del fichero MADR delata la página.
#     `NNNN-slug.md` bajo content/docs no es ninguna otra cosa en este sitio.
while IFS= read -r f; do
	[ -n "$f" ] || continue
	nombra "page with an ADR filename: ${f#"$ROOT/"}"
done < <(find "$CONTENT" -type f -regextype posix-extended -regex '.*/[0-9]{4}-[^/]+\.mdx?$' 2>/dev/null)

# C · ni un enlace vivo a la ruta retirada: un enlace roto es una fuga a medias y una 404 pública.
#     Se barre TODO docs-site y no sólo src/content/docs, porque un enlace escondido en un
#     componente, en un layout o en el JSON de una versión archivada publica igual que uno en prosa
#     — y el de `src/content/versions/2026-06.json` es precisamente el que esta retirada encontró
#     y ninguna lectura del contenido habría visto.
#
#     ⛔ UNA SOLA EXCLUSIÓN, Y ES DE SUJETO, NO DE COMODIDAD: `docs-site/README.md` no produce
#     ninguna ruta pública — es documentación del repositorio para quien mantiene el sitio, y ahí
#     tiene que poder NOMBRAR la ruta retirada para explicar qué se fue y por qué. Un gate que
#     prohibiese nombrarla obligaría a documentar la retirada en clave. El self-test prueba las dos
#     mitades: mención en el README = verde, el MISMO texto en una página = rojo.
while IFS= read -r hit; do
	[ -n "$hit" ] || continue
	nombra "link to the withdrawn path: ${hit#"$ROOT/"}"
done < <(grep -rIl -e 'explanation/adr' "$SITE" \
	--exclude-dir=node_modules --exclude-dir=dist --exclude-dir=.astro 2>/dev/null \
	| grep -v -x -e "$SITE/README.md")

# Reject numbered citations to internal records, even without a link.
while IFS= read -r hit; do
	[ -n "$hit" ] || continue
	nombra "numbered citation to an internal record: ${hit#"$ROOT/"}"
done < <(grep -rIlE 'ADR-[0-9]{4}' "$CONTENT" 2>/dev/null)

# D · ni el generador de vuelta: sin él la sección no puede reaparecer sola.
#
#     ⛔ SE BUSCA POR PATRÓN, NO POR EL NOMBRE EXACTO, y las dos razones importan:
#
#     (1) `lint:export-closure` rechazó este guion cuando nombraba `docs-site/scripts/sync-adr.mjs`
#         y `adr-i18n.json` literalmente: los lee como DEPENDENCIAS de un guion publicado, y esos
#         ficheros ya no existen — «dangling reference». Tenía razón desde su lado: no puede
#         distinguir «esto lo necesito» de «esto NO debe existir», y desde el mío la frase correcta
#         es la segunda. Un gate de AUSENCIA no puede escribirse como una referencia.
#     (2) Y es mejor comprobación: un generador que vuelva con otro nombre —`publish-adr.mjs`,
#         `adr-sync.mjs`— pasaría por delante de una lista de dos nombres. Una base de nombres no
#         puede trinquetar; un patrón sobre el directorio sí.
while IFS= read -r gen; do
	[ -n "$gen" ] || continue
	nombra "generator restored: ${gen#"$ROOT/"}"
done < <(find "$SITE/scripts" -maxdepth 1 -type f \( -iname '*adr*' \) 2>/dev/null)

if [ "$hallazgos" -gt 0 ]; then
	{
		echo "check-adr-not-published: $hallazgos finding(s) — the ADR section is returning to the public site."
		echo "  Internal development records must remain outside the public repository and site."
	} >&2
	exit 1
fi

echo "check-adr-not-published: the ADR section is not published (checked $CONTENT)."
exit 0
