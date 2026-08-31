# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# gate-bin-cache.sh — construir un helper Go de gate UNA vez por contenido, no una vez por
# invocación. Se sourcea; no se ejecuta.
#
# ⛔ POR QUÉ EXISTE, CON LA MEDIDA QUE LO OBLIGA. `check-aws-estate.sh` construye dos
# helpers Go (`hcl-module-guard` y `aws-apply-guard`) en CADA invocación, y su batería lo
# invoca **cincuenta veces**. Medido el 2026-08-27 en esta caja: **50,37 s de usuario +
# 269,54 s de sistema** — unos 5 min 20 s de CPU real — y **1 h 02 min 41 s de reloj** con
# `load average` entre 159 y 318 sobre 16 cpu (ocho pushes de otros carriles compitiendo).
# La cifra de reloj NO se extrapola a una caja tranquila; la de CPU sí es el coste propio.
#
# Y no es coste de una sesión: `scripts/test-aws-estate.sh` lo invoca `lint:addon-sets-gate`
# y `scripts/check-aws-estate.sh` lo invoca `lint:addon-sets`, y **las dos tareas están en
# `.githooks/pre-push`** (líneas 709-710). Es decir, este coste lo paga TODO push de TODO
# carril, y el nombre de la tarea no lo dice. La clase está medida y escrita:
# «una pata del pre-push cuesta 8-12 min a CADA carril».
#
# CÓMO NO SE CONVIERTE EN UN FALSO VERDE, que es la única pregunta que importa de una caché:
#
#   · La clave es el CONTENIDO de las fuentes (SHA-256 de `go.mod`, `go.sum` y los `.go`),
#     no su fecha ni su ruta. Fuentes distintas ⇒ binario distinto ⇒ ruta distinta. Los dos
#     casos de la batería que mutan el guard siguen construyendo de verdad.
#   · Se construye a un nombre TEMPORAL ÚNICO y se renombra con `mv` dentro del mismo
#     sistema de ficheros. Un renombrado es atómico: nadie ve nunca un binario a medias,
#     ni siquiera con varios carriles construyendo a la vez.
#   · Si falta `sha256sum` o no se puede escribir la caché, **se construye igual** y se
#     sigue. Una caché que no se puede usar es un ahorro que no ocurre, nunca un veredicto.
#   · La caché NO decide nada. Si el `go build` falla, quien llama contesta lo de siempre:
#     `2 · no he podido mirar`.

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
