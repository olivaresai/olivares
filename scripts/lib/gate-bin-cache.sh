# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Cache compiled helpers by source content. Publish completed binaries by atomic rename.
# Build failures remain failures; an unavailable cache falls back to building.

# olivares_cached_gate_bin <directorio-del-módulo> <nombre>
#
# Imprime en stdout la ruta de un binario construido desde ese módulo. Devuelve 1 si no se
# pudo construir — quien llama traduce eso a su propio «no he podido mirar».
olivares_cached_gate_bin() {
	local srcdir="$1" name="$2"
	local base="${TMPDIR:-/workspace/.olivares-tmptest}/olivares-gate-bin"

	# Sin sha256sum no hay clave posible: se construye a un temporal y se devuelve. El
	# ahorro se pierde; la corrección no.
	if ! command -v sha256sum >/dev/null 2>&1 || ! mkdir -p "$base" 2>/dev/null; then
		local fallback
		fallback="$(mktemp "${TMPDIR:-/workspace/.olivares-tmptest}/${name}.XXXXXX")" || return 1
		(cd "$srcdir" || exit 1; GOWORK=off go build -o "$fallback" .) || { rm -f "$fallback"; return 1; }
		printf '%s\n' "$fallback"
		return 0
	fi

	# La clave: el contenido de TODO lo que entra en el binario. `LC_ALL=C sort` fija el
	# orden para que la clave no dependa del locale de quien la calcule.
	local key
	key="$(find "$srcdir" -maxdepth 1 -type f \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) \
		-print0 2>/dev/null | LC_ALL=C sort -z | xargs -0 sha256sum 2>/dev/null | sha256sum | cut -c1-32)" || return 1
	[ -n "$key" ] || return 1

	# ⛔ UNA CACHÉ SIN PODA ES UN FUGA DE DISCO LENTA, y aquí el disco es un recurso
	# medido: `/workspace` estaba al **96 %** el 2026-08-27 y `disk-headroom` rechaza
	# pushes por debajo de su suelo. Cada binario pesa unos pocos MB y hay uno por
	# VERSIÓN de fuente, así que la cuenta la fija cuánta gente edita los guards.
	# Se podan los de más de siete días, sin ruido y sin bloquear: en Linux desenlazar
	# un binario que otro proceso está ejecutando es seguro —el inodo vive hasta que se
	# cierra—, y borrar el fichero de OTRO carril no le rompe nada: lo reconstruye, que
	# es lo que hacía antes de que existiera esta caché.
	find "$base" -maxdepth 1 -type f -mtime +7 -delete 2>/dev/null || true

	local cached="$base/$name-$key"
	if [ -x "$cached" ]; then
		printf '%s\n' "$cached"
		return 0
	fi

	local tmp
	tmp="$(mktemp "$base/$name-$key.XXXXXX")" || return 1
	if ! (cd "$srcdir" || exit 1; GOWORK=off go build -o "$tmp" .); then
		rm -f "$tmp"
		return 1
	fi
	# Renombrado atómico dentro del mismo sistema de ficheros. Si otro carril ganó la
	# carrera, el suyo es byte a byte el mismo binario: la clave es el contenido.
	mv -f "$tmp" "$cached" 2>/dev/null || { rm -f "$tmp"; return 1; }
	printf '%s\n' "$cached"
}
