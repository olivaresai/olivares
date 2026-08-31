#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# Re-derives the misspell census across every module of go.work.
#
# Con `--formas` imprime TODAS las formas como `original → correccion` con su recuento, sin
# truncar. Existe porque el resumen corta a 15 de ~120 y NO decia la correccion, y la correccion
# es la mitad que distingue una errata de un cambio de significado: `cancelled`→`canceled` es
# britanico contra el locale US, pero `comando`→`commando` convierte una palabra española en un
# SOLDADO. Sin el par, las dos clases son indistinguibles y un barrido mecanico sobre el total
# editaria prosa española a ingles roto. La salida se adjudica a mano UNA vez; la lista de
# ignorados de misspell se alimenta DESDE ahi, no de un vocabulario que alguien se invente.
#
# Con `--clase <nombre>` imprime ADEMAS fichero:linea:columna de esa clase. Sin eso, el censo
# decia cuantos y no cuales, asi que la campana no era ejecutable: «95 identificadores» no dice
# sobre que fichero abrir el editor, y la clasificacion ya estaba calculada — solo se tiraba la
# clave al contarla. Clases: comentario · cadena de test · cadena de produccion · identificador. It replaces a table that
# was typed by hand and claimed to partition 817 occurrences while its rows summed to 1022.
# Numbers in prose age in silence; this one is re-derivable in one command, and the Go half
# EXITS NON-ZERO if the rows ever stop summing to the input.
#
#   bash scripts/misspell-census.sh
set -uo pipefail
cd "$(dirname "$0")/.."
raiz=$(pwd)

# -- THE CENSUS MUST NEVER ANSWER ZERO BECAUSE IT COULD NOT LOOK ----------------------------
# Measured 2026-08-20: this script printed `TOTAL 0` for all twelve modules and exited 0 on a
# tree that has hundreds. golangci-lint is not on PATH in the dev container -- it lives in
# $(go env GOPATH)/bin -- so every invocation died 127, `2>/dev/null` DISCARDED the reason, and
# `| grep | wc -l` turned a dead tool into a clean count. A/B on ./core: 0 without that
# directory on PATH, 298 with it.
#
# The damage is specific to what this file is FOR. It exists because a hand-typed table claimed
# to partition 817 occurrences while its rows summed to 1022, and CLAUDE.md now points here with
# "do not copy these figures by hand: run the census". An instrument named as the authority that
# reports zero when it cannot run does not merely fail -- it certifies. A campaign planned from
# its output would have been closed as already done.
#
# So the tool is resolved explicitly and every module's exit code is judged, on this
# repository's three-answer contract: 0 clean / 1 findings / anything else CANNOT LOOK, and the
# whole run exits 2. golangci-lint returns 1 when it finds issues, which is the EXPECTED case.
GCL=${GOLANGCI_LINT:-}
if [ -z "$GCL" ]; then
	if command -v golangci-lint >/dev/null 2>&1; then
		GCL=golangci-lint
	elif [ -x "$(go env GOPATH 2>/dev/null)/bin/golangci-lint" ]; then
		GCL="$(go env GOPATH)/bin/golangci-lint"
	fi
fi
# ⛔ «--version SALE 0» NO ES «es golangci-lint». Lo midio el contraste Codex `sol max` del
# 2026-08-20 (VERIFICADO) contra la version anterior de esta misma guarda, que es la que yo
# escribi para cerrar el TOTAL 0: con `GOLANGCI_LINT=/bin/true` y con `GOLANGCI_LINT=/usr/bin/git`
# el censo recorria los doce modulos, imprimia `TOTAL 0` y salia 0. `git --version` sale 0; el
# `git run ...` de despues sale 1, que aqui es el caso ESPERADO, asi que se aceptaba.
#
# Es la clase «un proxy siempre cierto no es una sonda»: comprobe que la herramienta ARRANCA y
# lo lei como que es la herramienta. Ahora se exige que se IDENTIFIQUE.
if [ -z "$GCL" ]; then
	echo "misspell-census: NO HE PODIDO MIRAR - golangci-lint no es ejecutable." >&2
	echo "                  buscado en PATH y en \$(go env GOPATH)/bin; fija GOLANGCI_LINT=<ruta>." >&2
	exit 2
fi
version=$("$GCL" --version 2>&1) || {
	echo "misspell-census: NO HE PODIDO MIRAR - '$GCL' --version fallo." >&2
	exit 2
}
case "$version" in
*golangci-lint*) ;;
*)
	echo "misspell-census: NO HE PODIDO MIRAR - '$GCL' no se identifica como golangci-lint." >&2
	printf '                  --version dijo: %s\n' "$(printf '%s' "$version" | head -1)" >&2
	exit 2
	;;
esac

# La cache compartida devuelve rutas de OTRO arbol, y la spec lo declaraba invalido... en prosa.
# Una precondicion que solo vive en la documentacion no es una precondicion: el script la impone.
if [ -z "${GOLANGCI_LINT_CACHE:-}" ]; then
	GOLANGCI_LINT_CACHE=$(mktemp -d "${TMPDIR:-/tmp}/misspell-census-cache.XXXXXX") || exit 2
	export GOLANGCI_LINT_CACHE
	trap 'rm -rf -- "$GOLANGCI_LINT_CACHE"' EXIT
fi

crudo=$(mktemp "${TMPDIR:-/tmp}/misspell-census.XXXXXX") || exit 2
# El rc de esta sustitucion no se miraba: con `go work edit -json` fallando salia un traceback de
# Python, luego `TOTAL 0` y rc 0 (medido). Y una lista VACIA recorria cero modulos y sumaba cero,
# que es la misma respuesta que un arbol limpio.
mods=$(go work edit -json 2>/dev/null | python3 -c 'import json,sys; print("\n".join(u["DiskPath"] for u in json.load(sys.stdin)["Use"]))' 2>/dev/null) || {
	echo "misspell-census: NO HE PODIDO MIRAR - no pude leer los modulos de go.work." >&2
	exit 2
}
n_mods=$(printf '%s\n' "$mods" | grep -c .) || n_mods=0
if [ "$n_mods" -eq 0 ]; then
	echo "misspell-census: NO HE PODIDO MIRAR - go.work no declaro ni un modulo." >&2
	exit 2
fi
while IFS= read -r m; do
	[ -n "$m" ] || continue
	# `cd` a un modulo inexistente devolvia 1 — que aqui es el caso ESPERADO del linter — asi
	# que el modulo contaba 0 y la corrida seguia. Medido: `cd: ./NO_SUCH_MODULE: No such file
	# or directory` y la corrida termino en 0. Se separa con un rc que no puede confundirse.
	salida=$( cd "$m" 2>/dev/null || exit 126
		timeout 900 "$GCL" run --default=none -E misspell \
		--max-issues-per-linter=0 --max-same-issues=0 ./... 2>&1 )
	rc=$?
	if [ "$rc" -eq 126 ]; then
		echo "misspell-census: NO HE PODIDO MIRAR ${m}: el modulo de go.work no existe." >&2
		exit 2
	fi
	if [ "$rc" -gt 1 ]; then
		echo "misspell-census: NO HE PODIDO MIRAR ${m}: golangci-lint salio ${rc}." >&2
		printf '%s\n' "$salida" | tail -3 | sed 's/^/                  /' >&2
		exit 2
	fi
	n=$( printf '%s\n' "$salida" | grep -aE '\(misspell\)$' | tee -a "$crudo" | wc -l )
	# rc 1 es «encontro hallazgos», asi que rc 1 con CERO filas parseables es una contradiccion:
	# o el linter fallo de una forma que no sube el rc, o su salida dejo de tener la forma que
	# este script lee. Las dos son «no he podido mirar», y sin esta invariante las dos contaban 0.
	if [ "$rc" -eq 1 ] && [ "$n" -eq 0 ]; then
		echo "misspell-census: NO HE PODIDO MIRAR ${m}: rc 1 sin una sola fila de misspell." >&2
		printf '%s\n' "$salida" | tail -3 | sed 's/^/                  /' >&2
		exit 2
	fi
	printf 'modulo  %-40s %s\n' "$m" "$n"
done <<CENSO_MODULOS
$mods
CENSO_MODULOS
printf '\n'
# ⛔ AQUI HABIA UN `go run`, Y APLASTA EL CODIGO DE SALIDA. Medido en esta caja con un programa
# de una linea: `os.Exit(7)` bajo `go run` devuelve **1**. La mitad Go usa 1 para «las filas no
# suman» y 2 para «no he podido mirar», y `go run` las hacia indistinguibles: todas las negativas
# de la mitad Go llegaban aqui como 1, es decir, como un hallazgo en vez de como una ceguera.
#
# El contraste de hoy dio por bueno que el rc se propagaba («devolvio 42»); lo comprobe con un
# control directo y no se sostiene en este contenedor. Se compila y se ejecuta el binario, que es
# la unica forma de que el codigo llegue entero.
bin=$(mktemp -d "${TMPDIR:-/tmp}/misspell-census-bin.XXXXXX") || exit 2
trap 'rm -f -- "$crudo"; rm -rf -- "$bin"' EXIT
if ! go build -o "$bin/censo" "$raiz/scripts/misspell-census.go" 2>"$bin/err"; then
	echo "misspell-census: NO HE PODIDO MIRAR - no pude compilar la mitad Go." >&2
	sed 's/^/                  /' "$bin/err" >&2
	exit 2
fi
sort -u "$crudo" | "$bin/censo" "$@"
rc_go=$?
if [ "$rc_go" -eq 126 ] || [ "$rc_go" -eq 127 ]; then
	echo "misspell-census: NO HE PODIDO MIRAR - no pude ejecutar la mitad Go (rc ${rc_go});" >&2
	echo "                  \$TMPDIR pudiera estar montado noexec. Fija TMPDIR a un sitio ejecutable." >&2
	exit 2
fi
exit "$rc_go"
