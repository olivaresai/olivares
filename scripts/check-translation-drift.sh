#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-translation-drift.sh — a repository gate: detect translations older than their source pages.
# lint:i18n detects missing keys and lint:docs-parity detects missing pages; neither
# detects existing but stale translations. Measured 2026-08-16: 834 source/translation
# pairs, 75 translations older than the source's latest change.
# This does not measure semantic drift. CLAUDE.md record says terra translated
# 44 pages with green parity/lints, but sol max found ~60 defects, including reversed
# deny-closed meaning on five governed-data pages. Fresh commits can say the opposite
# of their source; review detects that, timestamps do not. A pass is not translation approval.
# Ratchet by pair list, not count: updating one page while another becomes stale can
# leave the total unchanged. Name new stale pairs even when the overall count falls.
# Exit: 0 no growth · 1 named new pair · 2 could not check.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-translation-drift: ⛔ COULD NOT CHECK: missing $RAIZ" >&2
	exit 2
}
git rev-parse --git-dir >/dev/null 2>&1 || {
	echo "check-translation-drift: ⛔ COULD NOT CHECK: outside a repository; timestamps require history." >&2
	exit 2
}
DOCS="docs-site/src/content/docs"
BASE="${OLIVARES_TRANSLATION_BASELINE:-docs/translation-drift-baseline.txt}"
IDIOMAS="${OLIVARES_TRANSLATION_LANGS:-es fr de ja ru zh}"
[ -d "$DOCS" ] || {
	echo "check-translation-drift: ⛔ COULD NOT CHECK: missing $DOCS" >&2
	exit 2
}

# Fuentes: las páginas en inglés, que viven en la raíz del árbol de docs. Se EXCLUYEN los
# directorios de idioma y `2026-06/`, que es un archivo congelado declarado: una instantánea
# histórica no se pone al día, así que contarla metería en la línea base pares que nadie va a tocar.
PRUNE="-path $DOCS/2026-06 -prune"
for l in $IDIOMAS; do PRUNE="$PRUNE -o -path $DOCS/$l -prune"; done

pares=0
derivados=""
while IFS= read -r f; do
	[ -n "$f" ] || continue
	rel="${f#"$DOCS"/}"
	ts="$(git log -1 --format=%ct -- "$f" 2>/dev/null)"
	[ -n "${ts:-}" ] || continue
	for l in $IDIOMAS; do
		t="$DOCS/$l/$rel"
		[ -f "$t" ] || continue
		pares=$((pares + 1))
		tt="$(git log -1 --format=%ct -- "$t" 2>/dev/null)"
		[ -n "${tt:-}" ] || continue
		if [ "$tt" -lt "$ts" ]; then
			derivados="${derivados}${l}/${rel}
"
		fi
	done
done < <(eval "find \"$DOCS\" $PRUNE -o -name '*.md' -print -o -name '*.mdx' -print" 2>/dev/null)


# Second family: root pages. Until 2026-08-25 only docs-site/src/content/docs was
# covered. Commit 05b139b67 on 08-22 removed a production-maturity claim only from
# README.md and README.es.md; the other five translations retained it, all with
# delta -14,940 s. Four README gates passed because they checked task sets,
# digit/noun pairs and CalVer tokens, not freshness. semantic audit confirmed C03.
# Keep a separate pass: docs-site locales are directories (docs/es/x.md), whereas
# root locales are suffixes (README.es.md). Mixing these forms can mispair new files.
RAIZ_FAMILIAS="${OLIVARES_TRANSLATION_ROOT_FAMILIES:-README}"
for fam in $RAIZ_FAMILIAS; do
	fuente="$RAIZ/$fam.md"
	[ -f "$fuente" ] || continue
	ts="$(git log -1 --format=%ct -- "$fuente" 2>/dev/null)"
	[ -n "${ts:-}" ] || continue
	for l in $IDIOMAS; do
		t="$RAIZ/$fam.$l.md"
		[ -f "$t" ] || continue
		pares=$((pares + 1))
		tt="$(git log -1 --format=%ct -- "$t" 2>/dev/null)"
		[ -n "${tt:-}" ] || continue
		if [ "$tt" -lt "$ts" ]; then
			derivados="${derivados}${fam}.${l}.md
"
		fi
	done
done

ACTUALES="$(printf '%s' "$derivados" | grep -c . || true)"

# `--list`: imprime el censo COMPLETO y sale. Existe porque sembrar o bajar la línea base leyendo el
# INFORME es una trampa — el informe corta a 20 entradas a propósito para no ahogar un log, y una
# línea base sembrada así nace con 22 de 75 y llama «nuevas» a las 53 que ya estaban. Me pasó al
# sembrarla, y el modo existe para que no le pase a nadie más.
for _a in "$@"; do
	if [ "$_a" = "--list" ]; then
		printf '%s' "$derivados" | LC_ALL=C sort -u
		exit 0
	fi
done

# CONTROL POSITIVO: sin pares no se aprueba nada. «0 derivadas de 0» y «no encontré traducciones»
# son la misma frase con distinto significado, y la segunda no es un verde.
if [ "${pares:-0}" -lt 50 ]; then
	echo "check-translation-drift: ⛔ COULD NOT CHECK: only ${pares:-0} source/translation pair(s)." >&2
	echo "                        The language structure may have changed or the scan may be incomplete." >&2
	echo "                        An empty denominator would make any numerator look like full coverage." >&2
	exit 2
fi
if [ ! -r "$BASE" ]; then
	echo "check-translation-drift: ⛔ COULD NOT CHECK: cannot read baseline $BASE" >&2
	echo "                        A missing baseline cannot prove zero drift; nothing was checked." >&2
	exit 2
fi

LISTA="$(printf '%s' "$derivados" | LC_ALL=C sort -u)"
NUEVOS="$(printf '%s\n' "$LISTA" | grep -vxF -f "$BASE" 2>/dev/null | grep -c . || true)"
PUESTOS="$(grep -vxF -f <(printf '%s\n' "$LISTA") "$BASE" 2>/dev/null | grep -c . || true)"

echo "check-translation-drift: $ACTUALES translation(s) older than their source, out of $pares pair(s) · baseline $(grep -c . <"$BASE") · new $NUEVOS · current $PUESTOS"

if [ "${NUEVOS:-0}" -gt 0 ]; then
	echo "check-translation-drift: ⛔ NEW DRIFT — readers in this language see an older product:" >&2
	printf '%s\n' "$LISTA" | grep -vxF -f "$BASE" | head -20 | sed 's/^/                          /' >&2
	echo "                        Retranslate, or reduce the baseline if the source changed only in a" >&2
	echo "                        way that does not affect the translated text; explain that in the commit." >&2
	exit 1
fi
if [ "${PUESTOS:-0}" -gt 0 ]; then
	echo "check-translation-drift: ✔ $PUESTOS updated; reduce the baseline in this commit."
fi
echo "check-translation-drift: OK — drift has not increased."
exit 0
