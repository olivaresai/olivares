#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# fetch-overlay-seal.sh — a repository gate. EL UNICO fetch del clon hermano del overlay en un acto, y el
# sello de frescura que los checkers leen despues.
# 0 sello escrito (o no hay overlay que sellar) · 2 no he podido mirar.
#
# ⛔ EL DISPARADOR, medido: el 2026-08-29 la caja entera comparo contra un `origin/main` del overlay
# con UN MERGE DE RETRASO durante horas. El clon TRAE por SSH —que falla EN SILENCIO sin clave— y
# EMPUJA por HTTPS, asi que `git fetch origin` no traia nada y no lo decia. Ningun gate fallo:
# todos midieron bien contra un ref viejo. **Un ref congelado no es un veredicto.**
#
# ⛔ POR QUE UN SOLO FETCH Y NO UNO POR CHECKER: medido, un fetch cuesta 2 307 ms. Los SIETE
# gobernados haciendo el suyo son ~16 s por corrida y siete llamadas de red por push, desde cada
# carril a la vez. r4 lo decidio el 2026-08-30: uno por ACTO. Esta pata es ese acto.
#
# Va JUNTO A LOS REGISTROS, al principio del gancho (a repository gate): es barata y lo que sella lo consumen
# patas posteriores. Si se pusiera al final, sellaria despues de que los checkers ya hubieran leido.

set -uo pipefail
NAME=fetch-overlay-seal
say() { printf '%s\n' "$*"; }
# ⛔ A-02 (contraste sol max): `cannot` SOLO salia 2 y dejaba el sello ANTERIOR en su sitio, asi
# que un repo invalido o un remoto ausente conservaban la frescura del acto pasado y los lectores
# la aceptaban. Ahora CUALQUIER salida que no sea exito sella un fallo ANTES de salir: un sello con
# `rc!=0` hace salir 2 a los siete CON su razon, y lo que nunca puede quedar es el sello viejo.
sella_fallo() {
	_seal_atomico "$(date -u +%s) ${ACT_ID:-noact} 0000000000000000000000000000000000000000 rc=${1:-1}" || true
}
cannot() { sella_fallo "${2:-1}"; say "$NAME: COULD NOT LOOK — $1" >&2; exit 2; }

# Escritura ATOMICA (temporal + rename) y COMPROBADA: si el sello no queda escrito, no se declara
# exito. Un sello a medias es peor que ninguno — se lee como frescura.
_seal_atomico() {
	_t="$(mktemp "${SEAL}.XXXXXX" 2>/dev/null)" || return 1
	printf '%s\n' "$1" >"$_t" || { rm -f "$_t"; return 1; }
	mv -f "$_t" "$SEAL" || { rm -f "$_t"; return 1; }
	[ -s "$SEAL" ] || return 1
	return 0
}

ROOT="${OLIVARES_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
SEAL="${OLIVARES_OVERLAY_SEAL:-$ROOT/.overlay-fetch-seal}"
# ⛔ A-04 (contraste sol max): «mismo acto» no se demostraba con una EDAD. Un sello del push
# anterior, con el mismo SHA y menos de N segundos, pasaba — y eso es otro acto. El sello lleva
# ahora un ID DE ACTO que los siete exigen igual: por defecto el SHA de HEAD que se esta
# empujando, que es lo unico que identifica ESTE push y no el de hace diez minutos.
# ⚠ El id lo GENERA el gancho (nonce por corrida: HEAD + pid + epoch). Aqui no se inventa uno
# derivado del HEAD, porque dos reintentos del mismo commit compartirian acto (A-04) y el sello del
# intento anterior se leeria como de este. Sin id no hay acto que sellar.
ACT_ID="${OLIVARES_ACT_ID:-}"
[ -n "$ACT_ID" ] || cannot "no OLIVARES_ACT_ID in the environment: the caller must identify the act"
cd "$ROOT" || cannot "cannot enter $ROOT"

# ⛔ EL FALLBACK NO ES UN ADORNO: SIN EL, ESTA PATA MATA TODOS LOS PUSHES. Medido antes de dejarlo.
# `.githooks/pre-push` **no exporta `OLIVARES_ENT_DIR` ni una vez** (medido: 0 apariciones), y
# `check-int-12-no-land.sh` —uno de los siete gobernados— resuelve el clon hermano LEYENDO
# `sibling-clone-dir:` del doc de INT-12 (`:32`). Si esta pata se rindiera cuando la variable no
# esta, no sellaria nada, e int-12 encontraria el sello ausente y saldria 2 **en cada push de la
# caja**. Sella EXACTAMENTE el clon que ese checker va a leer, resolviendolo igual que el.
ENT="${OLIVARES_ENT_DIR:-}"
# ⛔ NOMBRADO-PERO-ROTO NO ES LO MISMO QUE NO NOMBRADO, y confundirlos deja el sello VIEJO en pie.
# Lo caza el control de A-02 de la bateria: con `OLIVARES_ENT_DIR` apuntando a un directorio que no
# existe, la version anterior caia en la rama «no hay overlay», salia 0 y conservaba la frescura del
# acto ANTERIOR — que es justo lo que este fichero existe para impedir. Si alguien NOMBRA un clon,
# que ese clon no sirva es un FALLO (se sella como tal); sólo la ausencia total es «nada que sellar».
ENT_EXPLICITO=0
[ -n "$ENT" ] && ENT_EXPLICITO=1
if [ "$ENT_EXPLICITO" = 1 ] && { [ ! -d "$ENT" ] || ! git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1; }; then
	cannot "OLIVARES_ENT_DIR names '$ENT', which is not a usable git repo: sealed as failed"
fi
DOC_NOMBRA=0
if [ -z "$ENT" ]; then
	_doc="${OLIVARES_INT12_DOC:-design/INT-12-NO-LAND-ENT58-2026-08-19.md}"
	if [ -r "$_doc" ]; then
		_sib="$(sed -n 's/^sibling-clone-dir: *//p' "$_doc" | head -1)"
		if [ -n "$_sib" ]; then
			ENT="$(CDPATH= cd -- "$ROOT/.." && pwd -P)/$_sib"
			DOC_NOMBRA=1
		fi
	fi
fi
# ⛔ NOMBRADO POR EL DOC Y AUSENTE TAMBIEN ES UN FALLO. Lo señalo el contraste (A-02, segunda
# vuelta): si el doc NOMBRA un clon hermano y esa ruta no existe, la version anterior lo trataba
# como «no nombrado», salia 0 y DEJABA EL SELLO ANTERIOR — la frescura del acto pasado sobrevivia
# a un clon que no esta. Nombrar es nombrar, lo haga la variable o el documento.
if [ "$DOC_NOMBRA" = 1 ] && { [ ! -d "$ENT" ] || ! git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1; }; then
	cannot "$_doc names sibling clone '$_sib', which is not a usable git repo here: sealed as failed"
fi
if [ -z "$ENT" ] || [ ! -d "$ENT" ]; then
	# Ni nombrado ni deducible: no hay nada que sellar y NO se inventa frescura. Quien necesite el
	# clon saldra 2 por su cuenta, que es la respuesta correcta — pero no la fabrica esta pata.
	say "$NAME: NOTICE — no sibling overlay named or resolvable: nothing fetched, nothing sealed"
	exit 0
fi
[ -d "$ENT" ] || cannot "OLIVARES_ENT_DIR is not a directory: $ENT"
git -C "$ENT" rev-parse --git-dir >/dev/null 2>&1 || cannot "not a git repo: $ENT"

# ⛔ LA URL SE TOMA DEL PROPIO REMOTO, y se fuerza HTTPS si viene en forma SSH: ese es el defecto
# que origino la fila. Un `git fetch origin` a secas es exactamente lo que fallaba en silencio.
URL="$(git -C "$ENT" remote get-url origin 2>/dev/null || true)"
# ⚠ El reescrito se hace por PARTES, y la version ingenua estaba MAL: `https://${URL#git@}` y luego
# sustituir el primer `:` toca el DE `https://`, no el que separa host de ruta — salia
# `https///github.com:...` y el fetch moria con rc=128. Medido antes de dejarlo.
case "$URL" in
git@*:*)
	_rest="${URL#git@}"
	URL="https://${_rest%%:*}/${_rest#*:}"
	;;
ssh://git@*)
	_rest="${URL#ssh://git@}"
	URL="https://${_rest%%/*}/${_rest#*/}"
	;;
esac
[ -n "$URL" ] || cannot "the clone has no origin remote to fetch from"

rc=0
git -C "$ENT" fetch -q "$URL" main:refs/remotes/origin/main 2>/dev/null || rc=$?
SHA="$(git -C "$ENT" rev-parse origin/main 2>/dev/null || true)"

if [ "$rc" != 0 ] || [ -z "$SHA" ]; then
	# ⛔ SE SELLA EL FALLO, no se borra el sello: un sello que dice `rc=N` hace salir 2 a los
	# lectores CON SU RAZON. Borrarlo diria lo mismo con menos informacion, y dejar el anterior
	# seria peor: frescura de otro acto leida como de este.
	_seal_atomico "$(date -u +%s) $ACT_ID ${SHA:-0000000000000000000000000000000000000000} rc=$rc"
	cannot "fetch of the sibling clone FAILED (rc=$rc); sealed as failed so every reader exits 2"
fi

# El write de exito SE COMPRUEBA: si no queda sello, esto NO es un acto sellado.
_seal_atomico "$(date -u +%s) $ACT_ID $SHA rc=0" \
	|| cannot "the seal could not be written to $SEAL; nothing may claim freshness"
say "$NAME: CLEAN — sibling overlay fetched; origin/main ${SHA:0:9} sealed for act ${ACT_ID:0:9}"
exit 0
