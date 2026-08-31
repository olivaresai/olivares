#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-connectors.sh — connector inventory honesty guards (E4).
#
# This is intentionally a small POSIX-sh lint. It re-derives mechanical connector
# classes from source signals, rather than trusting docs/ai-context/CONNECTORS.md.
# The LiveSource Fetch check is a grep heuristic: live content connectors may keep
# s.Store.Fetch(docID) as the export-mode fallback, but the same file must dispatch
# to s.fetchLive(ctx, docID) first.

set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

fail=0
tmp_noops="$(mktemp)"
trap 'rm -f "$tmp_noops"' EXIT

is_contract_lib() {
	case "$1" in
		contentsource|datasourceacl|identitysource|internal|interop|modelprovider|modelrouter|redact|secretref|siemsink|threatfeed|vectorindex|voice)
			return 0
			;;
	esac
	return 1
}

is_noop_allowlisted() {
	case "$1" in
		aicontroltower|keycloak|spiffe)
			return 0
			;;
	esac
	return 1
}

# `find ... -exec grep -Eq {} +` reports FIND's status, and with `+` find exits 0 when it
# matched no files at all — grep never runs. So a connector directory with no Go sources
# answered "yes, the pattern is there", and every caller reads a 0 from this function as
# a class signal being PRESENT. The file list is now materialised first, and an empty
# list is a NO rather than a yes.
grep_go() {
	dir="$1"
	pattern="$2"
	# `testdata/` is pruned for the same reason `_test.go` already was: what lives under it
	# is INPUT to a test, not shipped code, and Go itself ignores those trees when building.
	# A fixture that happens to contain the pattern would otherwise classify a connector on
	# evidence that proves nothing about what the connector does.
	files="$(find "$dir" \
		-path '*/node_modules' -prune -o \
		-name 'testdata' -prune -o \
		-name '*_test.go' -prune -o \
		-name '*.go' -type f -print 2>/dev/null)"
	[ -n "$files" ] || return 1
	printf '%s\n' "$files" | tr '\n' '\0' | xargs -0 -r grep -Eq "$pattern" 2>/dev/null
}

has_plugin_main() {
	dir="$1"
	[ -d "$dir/cmd" ] || return 1
	find "$dir/cmd" -mindepth 2 -maxdepth 2 -name main.go -type f -print 2>/dev/null | grep -q .
}

# ⛔ SIN `connectors/` NO HAY NADA QUE EXAMINAR, Y ESO NO ES UN INVENTARIO DESHONESTO.
# Este gate contestaba a la ausencia con `find: 'connectors': No such file or directory` y un
# `exit 1`, que se lee como «hay conectores que mienten». Son dos hechos distintos y sólo uno es del
# producto: el otro es del árbol. Importa exactamente donde más duele —un clon superficial, un árbol
# exportado, un runner mal montado—, porque ahí manda a alguien a buscar un defecto inexistente
# mientras el hecho real queda sin decir. Medido el 2026-08-18 sobre un señuelo sin sujeto: éste era
# de los once gates que contestaban «roto» a una ausencia.
#
# ⚠ Y el CONTROL POSITIVO va con ello: un `connectors/` presente pero VACÍO tampoco se aprueba. «0
# problemas de 0 conectores» y «no encontré ningún conector» son la misma frase con distinto
# significado, y la segunda no es un verde.
if [ ! -d connectors ]; then
	echo "check-connectors: ⛔ NO HE PODIDO MIRAR: no existe connectors/ en este árbol." >&2
	echo "                  No es un inventario deshonesto: es que no hay inventario." >&2
	exit 2
fi
if [ -z "$(find connectors -name '*.go' -type f -print -quit 2>/dev/null)" ]; then
	echo "check-connectors: ⛔ NO HE PODIDO MIRAR: connectors/ no contiene ni un fichero .go." >&2
	echo "                  Un conjunto vacío no se aprueba: o el árbol está a medias, o se movieron." >&2
	exit 2
fi

find connectors \
	-path '*/node_modules' -prune -o \
	-name '*_test.go' -prune -o \
	-name '*.go' -type f -print |
while IFS= read -r file; do
	awk -v file="$file" '
		function check_body(body) {
			sub(/\}.*/, "", body)
			gsub(/[[:space:];]/, "", body)
			if (body == "returnnil") {
				print file ":" start
			}
		}
		/func[[:space:]].*Gather[[:space:]]*\(.*\).*error[[:space:]]*\{/ {
			in_gather = 1
			start = FNR
			body = $0
			sub(/^[^{]*\{/, "", body)
			if (body ~ /\}/) {
				check_body(body)
				in_gather = 0
			}
			next
		}
		in_gather {
			body = body "\n" $0
			if ($0 ~ /\}/) {
				check_body(body)
				in_gather = 0
			}
		}
	' "$file"
done >"$tmp_noops"

allowed_noops=0
while IFS= read -r hit; do
	[ -n "$hit" ] || continue
	dir="${hit#connectors/}"
	dir="${dir%%/*}"
	if is_noop_allowlisted "$dir"; then
		allowed_noops=$((allowed_noops + 1))
	else
		echo "connector lint: literal no-op Gather outside allowlist: $hit" >&2
		fail=1
	fi
done <"$tmp_noops"

if [ "$allowed_noops" -ne 3 ]; then
	echo "connector lint: expected 3 documented no-op Gather connectors, found $allowed_noops" >&2
	fail=1
fi

for dir in connectors/*; do
	[ -d "$dir" ] || continue
	base="${dir#connectors/}"

	class_signal=0
	if has_plugin_main "$dir"; then class_signal=1; fi
	if grep_go "$dir" '_[[:space:]]+sdk\.SourceConnector[[:space:]]*='; then class_signal=1; fi
	if grep_go "$dir" '_[[:space:]]+sdk\.OutputConnector[[:space:]]*='; then class_signal=1; fi
	if grep_go "$dir" '_[[:space:]]+identitysource\.GraphProvider[[:space:]]*='; then class_signal=1; fi
	if grep_go "$dir" '_[[:space:]]+contentsource\.Source[[:space:]]*='; then class_signal=1; fi
	if is_contract_lib "$base"; then class_signal=1; fi
	if [ "$base" = "backstage" ]; then class_signal=1; fi

	if [ "$class_signal" -eq 0 ]; then
		# ⛔ SE DISTINGUE «SIN CLASIFICAR» DE «NO ES UN CONECTOR», y no es cosmética: el mensaje de
		#    abajo manda buscar una interfaz que falte registrar, y en el caso más frecuente no falta
		#    nada — es un directorio de RESIDUO que alguien dejó bajo `connectors/`. Le pasó a
		#    Otro carril el 2026-08-18: un `connectors/tools/surfacesdump` VACÍO y sin trackear,
		#    sobrante de un censo, tumbó su push y le costó diez minutos buscando un registro ausente.
		#    Git no versiona directorios vacíos, así que ni siquiera sale en un `git status`.
		if [ -z "$(find "$dir" -name '*.go' -not -path '*/node_modules/*' -print -quit 2>/dev/null)" ]; then
			echo "connector lint: $dir no contiene NINGÚN fichero .go — no es un conector sin clasificar, es residuo bajo connectors/. Bórralo o muévelo fuera." >&2
		else
			echo "connector lint: unclassified top-level connector dir: $dir" >&2
		fi
		fail=1
	fi

	if grep_go "$dir" '_[[:space:]]+contentsource\.LiveSource[[:space:]]*='; then
		find "$dir" \
			-path '*/node_modules' -prune -o \
			-name '*_test.go' -prune -o \
			-name '*.go' -type f -print |
		while IFS= read -r gofile; do
			if grep -q 'return[[:space:]]*s\.Store\.Fetch(docID)' "$gofile" &&
				! grep -q 'return[[:space:]]*s\.fetchLive(ctx, docID)' "$gofile"; then
				echo "connector lint: LiveSource Fetch fallback without live dispatch: $gofile" >&2
				exit 9
			fi
		done || fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "Connector lint FAILED." >&2
	exit 1
fi

echo "Connector lint OK: no unexpected no-op Gather, no unclassified dirs, LiveSource Fetch dispatch present."
