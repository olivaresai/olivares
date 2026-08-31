# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Cuenta, por linea, las tuberias de la forma 2: una asignacion por sustitucion de comando cuyo rc
# SE PRUEBA (`VAR="$( … | consumidor … )"` seguida de `||` o `&&`), con el consumidor en posicion de
# COMANDO y fuera de comillas.
function es_consumidor(resto,   pal, i) {
	# saltar prefijos admitidos: `command `, `builtin ` y asignaciones de entorno VAR=val
	while (1) {
		if (match(resto, /^(command|builtin)[ \t]+/)) { resto = substr(resto, RLENGTH + 1); continue }
		if (match(resto, /^[A-Za-z_][A-Za-z0-9_]*=[^ \t]+[ \t]+/)) { resto = substr(resto, RLENGTH + 1); continue }
		break
	}
	if (match(resto, /^(head|read)([ \t]|$|\))/)) return 1
	if (match(resto, /^grep[ \t]/)) {
		# `grep` solo cuenta si lleva -…q o --quiet ANTES del primer no-opcion
		pal = substr(resto, RLENGTH + 1)
		if (match(pal, /^[^)]*(-[a-zA-Z]*q([ \t]|$)|--quiet([ \t]|$))/)) return 1
	}
	return 0
}
{
	l = $0
	sub(/^[ \t]*#.*$/, "", l)
	if (l == "") next
	# la FORMA: asignacion `="$(` … `)"` y el rc probado con `||` o `&&`
	if (l !~ /="\$\(/) next
	if (l !~ /\)"[ \t]*(\|\||&&)/) next
	ini = index(l, "=\"$(") + 4
	q = ""; prof = 1; n = 0
	for (i = ini; i <= length(l); i++) {
		c = substr(l, i, 1)
		if (q == "") {
			if (c == "'" || c == "\"") { q = c; continue }
			if (c == "(") { prof++; continue }
			if (c == ")") { prof--; if (prof == 0) break; continue }
			if (c == "|") {
				if (substr(l, i + 1, 1) == "|") { i++; continue }   # un OR no es una tuberia
				resto = substr(l, i + 1); sub(/^[ \t]+/, "", resto)
				if (es_consumidor(resto)) n++
			}
		} else if (c == q) { q = "" }
	}
	total += n
}
END { print total + 0 }
