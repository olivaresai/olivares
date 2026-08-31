# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# lane-artefacts.sh — SOURCE THIS to read the live process-artefact name patterns.
#
# This file carries NO pattern of its own: only the reading. The patterns live in a curated file
# outside the export surface, so the literals never reach a public tree. See that file for why.
#
# THREE ANSWERS, and the third is the point: if the curated file is missing or carries no usable
# entry, the caller must REFUSE (exit 2, "I could not look"). It must never fall back to embedded
# patterns -- a fallback would reintroduce the very literal the curation removes, and would leave
# two sources of truth that drift.
#
# Usage:
#   . "$(dirname "$0")/lib/lane-artefacts.sh"
#   lane_artefacts_load "$RAIZ" || exit 2      # sets LA_BRIEF_GLOBS (array), LA_RELAY_GLOB,
#                                              #      LA_RELAY_INDEX, LA_RELAY_REF_REGEX
# export-closure: hub-only sessions/lane-artefact-globs.txt — carries the live process-artefact
# name patterns; it is curated OUT of the export on purpose, because publishing it would
# republish exactly the lane literals this indirection removes. The published tree does NOT
# exit 127 on its absence: lane_artefacts_load() tests for it and REFUSES with 2, naming the
# file — the third answer, not a crash and not a silent clean.

lane_artefacts_load() {
	local raiz="${1:?lane_artefacts_load necesita la raiz del repositorio}"
	# El literal vive AQUI y en ningun otro sitio: la linea siguiente es su test de presencia, que
	# es lo que `check-export-closure.sh` exige para aceptar la declaracion hub-only de arriba.
	local spec="${raiz}/sessions/lane-artefact-globs.txt"
	LA_BRIEF_GLOBS=(); LA_RELAY_GLOB=""; LA_RELAY_INDEX=""; LA_RELAY_REF_REGEX=""
	if [ ! -r "$spec" ]; then
		echo "NO HE PODIDO MIRAR: no leo ${spec} — sin el no se que ficheros vigilar." >&2
		echo "                    NO caigo a patrones embebidos: eso republicaria el literal." >&2
		return 2
	fi
	local clave valor
	while IFS=$'\t' read -r clave valor; do
		case "$clave" in ''|'#'*) continue ;; esac
		[ -n "$valor" ] || continue
		case "$clave" in
		brief-glob)      LA_BRIEF_GLOBS+=("$valor") ;;
		relay-glob)      LA_RELAY_GLOB="$valor" ;;
		relay-index)     LA_RELAY_INDEX="$valor" ;;
		relay-ref-regex) LA_RELAY_REF_REGEX="$valor" ;;
		esac
	done < "$spec"
	if [ "${#LA_BRIEF_GLOBS[@]}" -eq 0 ] || [ -z "$LA_RELAY_GLOB" ] || [ -z "$LA_RELAY_INDEX" ] || [ -z "$LA_RELAY_REF_REGEX" ]; then
		echo "NO HE PODIDO MIRAR: el fichero curado de patrones no trae las cuatro claves" >&2
		echo "                    (brief-glob, relay-glob, relay-index, relay-ref-regex)." >&2
		echo "                    Un gate a medias no es un gate." >&2
		return 2
	fi
	return 0
}
