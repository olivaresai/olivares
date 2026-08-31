#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-translation-drift.sh — a repository gate, la mitad MECÁNICA. ¿Se ha quedado una traducción por detrás
# de la página que traduce?
#
# ⛔ QUÉ MIDE, Y QUÉ DELIBERADAMENTE NO. La paridad ya existe: `lint:i18n` caza claves que no
# resuelven y `lint:docs-parity` caza páginas AUSENTES. Ninguna de las dos ve una traducción que
# EXISTE y está VIEJA — la fuente cambió y la traducción no, así que el lector en su idioma lee un
# producto anterior. Medido el 2026-08-16: **834 pares fuente↔traducción, 75 con la traducción
# anterior al último cambio de su fuente.** Nada en el repositorio lo decía.
#
# Y NO mide deriva de SIGNIFICADO, que es la otra mitad de la fila y no es automatizable con git:
# el dato de está escrito en `CLAUDE.md` — `terra` tradujo 44 páginas con paridad y lints
# verdes y una auditoría `sol max` encontró ~60 defectos, **el peor una polaridad deny-closed
# INVERTIDA en cinco páginas de datos gobernados**. Un fichero puede estar recién commiteado y decir
# lo contrario que su fuente. Eso lo caza una lectura, no una marca de tiempo, y esta cabecera lo
# dice para que nadie lea un verde de aquí como «las traducciones están bien».
#
# ⛔ TRINQUETE CON LISTA, NO CON NÚMERO. Un contador deja pasar la SUSTITUCIÓN: se pone al día una
# página, se queda atrás otra, el total no se mueve y el gate calla. La lista nombra al par nuevo
# aunque el total baje. Es la misma lección que el trinquete de formato ya tiene escrita.
#
# Salida: 0 la deriva no crece · 1 hay un par NUEVO (lo nombra) · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-translation-drift: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
git rev-parse --git-dir >/dev/null 2>&1 || {
	echo "check-translation-drift: ⛔ NO HE PODIDO MIRAR: no es un repositorio; sin historia no hay fechas." >&2
	exit 2
}
DOCS="docs-site/src/content/docs"
BASE="${OLIVARES_TRANSLATION_BASELINE:-docs/translation-drift-baseline.txt}"
IDIOMAS="${OLIVARES_TRANSLATION_LANGS:-es fr de ja ru zh}"
[ -d "$DOCS" ] || {
	echo "check-translation-drift: ⛔ NO HE PODIDO MIRAR: no existe $DOCS" >&2
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


# SEGUNDA FAMILIA DE PARES: LA RAIZ. Hasta el 2026-08-25 este gate SOLO miraba
#    `docs-site/src/content/docs`, y la familia README de la raiz no la vigilaba NADIE. El coste
#    no fue teorico: el 08-22 el commit 05b139b67 retiro del README ingles una promesa de MADUREZ
#    DE PRODUCCION y toco solo `README.md` y `README.es.md`. Las otras CINCO traducciones siguieron
#    publicandola, con un delta identico de -14 940 s, y los cuatro gates que si recorren la
#    familia README salieron VERDES: miran conjunto de tareas, pares digito-sustantivo y tokens
#    CalVer. Ninguno mira frescura. La auditoria semantica de lo confirmo como su C03.
#
#    LA FORMA ES DISTINTA, y por eso es una pasada aparte y no un `find` mas ancho: en docs-site
#    el locale es un DIRECTORIO (`docs/es/x.md`); en la raiz es un SUFIJO (`README.es.md`). Un
#    solo barrido que intentara las dos formas emparejaria mal en cuanto apareciera un tercero.
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
	echo "check-translation-drift: ⛔ NO HE PODIDO MIRAR: sólo ${pares:-0} par(es) fuente↔traducción." >&2
	echo "                        O cambió la estructura de idiomas, o el barrido no llega. Un" >&2
	echo "                        denominador vacío haría que cualquier numerador pareciera perfecto." >&2
	exit 2
fi
if [ ! -r "$BASE" ]; then
	echo "check-translation-drift: ⛔ NO HE PODIDO MIRAR: no leo la línea base $BASE" >&2
	echo "                        Una línea base ausente no es «cero deriva»; es no haber mirado." >&2
	exit 2
fi

LISTA="$(printf '%s' "$derivados" | LC_ALL=C sort -u)"
NUEVOS="$(printf '%s\n' "$LISTA" | grep -vxF -f "$BASE" 2>/dev/null | grep -c . || true)"
PUESTOS="$(grep -vxF -f <(printf '%s\n' "$LISTA") "$BASE" 2>/dev/null | grep -c . || true)"

echo "check-translation-drift: $ACTUALES traducción(es) anterior(es) a su fuente, de $pares par(es) · línea base $(grep -c . <"$BASE") · nuevas $NUEVOS · al día $PUESTOS"

if [ "${NUEVOS:-0}" -gt 0 ]; then
	echo "check-translation-drift: ⛔ DERIVA NUEVA — el lector en ese idioma lee un producto anterior:" >&2
	printf '%s\n' "$LISTA" | grep -vxF -f "$BASE" | head -20 | sed 's/^/                          /' >&2
	echo "                        Traducir de nuevo, o bajar la línea base si la fuente sólo cambió" >&2
	echo "                        en algo que no altera el texto traducido — y decir cuál en el commit." >&2
	exit 1
fi
if [ "${PUESTOS:-0}" -gt 0 ]; then
	echo "check-translation-drift: ✔ $PUESTOS puesta(s) al día — baja la línea base en el mismo commit."
fi
echo "check-translation-drift: OK — la deriva no crece."
exit 0
