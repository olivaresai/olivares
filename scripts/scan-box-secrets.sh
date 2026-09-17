#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md
#
# scan-box-secrets.sh — busca credenciales EN CLARO en artefactos que ninguna sesión mira:
# volcados de fallo del navegador, cores y restos de TMPDIR.
#
# ⛔ POR QUÉ EXISTE, Y ES UNA FUGA MEDIDA, NO UNA MEJORA. El 2026-09-01 se encontraron
# `CLOUDFLARE_API_TOKEN=<53 caracteres>` en 18 de 70 volcados de
# `~/.config/google-chrome-for-testing/Crash Reports/pending/`, con fechas de 2026-05-07 a
# 2026-08-31. El mecanismo NO es del navegador: es que la credencial era AMBIENTAL —estaba
# exportada al entorno del proceso— y todo hijo la hereda, así que cuando uno revienta, el
# manejador vuelca su memoria y la asignación `NOMBRE=valor` viaja dentro. La prueba de que
# ése es el mecanismo y no otro: en los mismos 70 ficheros se buscaron con valor
# AWS_SECRET_ACCESS_KEY, ANTHROPIC_API_KEY, DODO*KEY y GITHUB_TOKEN y salieron CERO — porque
# sólo una credencial era ambiental.
#
# ⛔ NO ES UN GATE DE PUSH, Y LA DISTINCIÓN NO ES DE ESTILO. Esto lee el DISCO DE UNA CAJA, no
# el árbol que un push publica: dos cajas dan veredictos distintos sobre el mismo commit, que
# es exactamente la clase de control irreproducible que esta casa ya pagó una vez. Se corre
# como monitor/cron POR CAJA. Su batería sí es determinista y ésa sí puede gatearse.
#
# ⛔ NUNCA IMPRIME UN VALOR. Un informe de fugas que cita el secreto es una copia más del
# secreto, y encima en un sitio que la gente pega en chats. Imprime NOMBRE, FICHERO y la
# LONGITUD del valor. Quien tenga que verlo, lo mira en el fichero.
#
# ⛔ Y DICE SIEMPRE QUÉ MIRÓ. Cada raíz sale en el informe con su veredicto —revisada, no
# existe, ilegible—, porque «0 hallazgos» sin la lista de lo escaneado es indistinguible de
# «no miré». Ésa es la diferencia entre limpio y ciego.
#
# Salidas: 0 = limpio · 1 = hay hallazgos · 2 = NO HE PODIDO MIRAR (nunca un verde silencioso).
set -uo pipefail
export LC_ALL=C

HOME_DIR="${HOME:-/home/claude}"

# Las raíces se declaran AQUÍ y se imprimen TODAS. Añadir una es una línea; que aparezca en el
# informe no es opcional.
RAICES=(
	"${HOME_DIR}/.config"
	"${HOME_DIR}/.cache"
	"${TMPDIR:-/tmp}"
	"/var/lib/systemd/coredump"
)

# El override existe para la BATERIA, y se DICE EN VOZ ALTA cuando se usa: un informe que mide
# otras rutas que las declaradas y no lo cuenta es el mismo defecto que un cero sin la lista.
if [ -n "${OLIVARES_SCAN_ROOTS:-}" ]; then
	IFS=':' read -r -a RAICES <<<"${OLIVARES_SCAN_ROOTS}"
	echo "scan-box-secrets: AVISO — raices sustituidas por OLIVARES_SCAN_ROOTS (${#RAICES[@]})"
fi

# Nombres de credencial que se buscan seguidos de un valor largo. La lista es explícita: un
# regex genérico de «algo=algo largo» dispara con cada hash de cada volcado y su ruido esconde
# el hallazgo real.
NOMBRES='CLOUDFLARE_API_TOKEN|CF_API_TOKEN|AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN|ANTHROPIC_API_KEY|OPENAI_API_KEY|GITHUB_TOKEN|GH_TOKEN|DODO_[A-Z_]*KEY|DODO_[A-Z_]*SECRET|STRIPE_[A-Z_]*KEY|NPM_TOKEN|OLIVARES_[A-Z_]*(KEY|TOKEN|SECRET)'
MIN_VALOR=20

# ⛔ EXCLUSIONES POR RUTA, Y VAN IMPRESAS. Las cachés de compilación guardan binarios cuya
# TABLA DE CADENAS pega literales contiguos, así que un fixture como el de
# `cmd/olivares/cmd_support_test.go:318` —que contiene a propósito el texto
# `export\tOLIVARES_SECRET_STORE_KEY=[REDACTED]` para probar el redactado— se lee como un
# valor largo y sale como hallazgo.
#
# ⛔ Y NO SE PUEDEN SEPARAR POR FORMA: lo medí antes de elegir la exclusión. El match real de
# un volcado de Chrome mide 74 caracteres y el falso positivo del `go-build` mide 60; ni la
# longitud, ni los dígitos, ni el primer carácter los distinguen. Una heurística aquí sería
# adivinar, y adivinar en silencio es peor que excluir en voz alta.
#
# Por eso la exclusión es por RUTA, se IMPRIME con su motivo, y `--incluir-caches` la levanta.
# Una exclusión declarada es una decisión; una silenciosa es un punto ciego.
EXCLUIDAS=(go-build node_modules ms-playwright .git)
INCLUIR_CACHES=0
[ "${1:-}" = "--incluir-caches" ] && INCLUIR_CACHES=1

hallazgos=0
mirados=0
no_mirados=0
excluidos=0

echo "scan-box-secrets: raices declaradas y su veredicto"
for r in "${RAICES[@]}"; do
	if [ ! -e "$r" ]; then
		printf '  %-44s NO EXISTE\n' "$r"
		continue
	fi
	if [ ! -r "$r" ]; then
		printf '  %-44s ILEGIBLE (no es un verde)\n' "$r"
		no_mirados=$((no_mirados + 1))
		continue
	fi
	printf '  %-44s revisada\n' "$r"
	mirados=$((mirados + 1))
done

if [ "$mirados" -eq 0 ]; then
	echo "scan-box-secrets: NO HE PODIDO MIRAR — ninguna raiz revisable" >&2
	exit 2
fi

echo
echo "scan-box-secrets: hallazgos (NOMBRE · fichero · longitud del valor; el valor NUNCA se imprime)"
for r in "${RAICES[@]}"; do
	[ -d "$r" ] && [ -r "$r" ] || continue
	# -s: sólo ficheros regulares no vacíos. El barrido es binario a propósito: un volcado no
	# es texto y `grep` sin -a lo saltaria en silencio, que es como esta fuga sobrevivio meses.
	while IFS= read -r f; do
		if [ "$INCLUIR_CACHES" -eq 0 ]; then
			saltar=0
			for x in "${EXCLUIDAS[@]}"; do
				case "$f" in *"/$x/"*) saltar=1; break ;; esac
			done
			[ "$saltar" -eq 1 ] && { excluidos=$((excluidos + 1)); continue; }
		fi
		[ -r "$f" ] || { no_mirados=$((no_mirados + 1)); continue; }
		n="$(LC_ALL=C grep -aoE "(${NOMBRES})=[A-Za-z0-9_.:/+-]{${MIN_VALOR},}" "$f" 2>/dev/null | wc -l)"
		[ "${n:-0}" -gt 0 ] || continue
		nombre="$(LC_ALL=C grep -aoE "(${NOMBRES})=" "$f" 2>/dev/null | head -1 | tr -d '=')"
		largo="$(LC_ALL=C grep -aoE "(${NOMBRES})=[A-Za-z0-9_.:/+-]{${MIN_VALOR},}" "$f" 2>/dev/null \
			| head -1 | sed 's/^[^=]*=//' | wc -c)"
		# ⛔ LA ETIQUETA DEL VALOR, Y NACE DE UN ERROR MIO DEL 2026-09-01. Sin ella el informe
		# lista nombre y fichero, y DOS VALORES DISTINTOS DEL MISMO NOMBRE se leen como uno:
		# publiqué «23 ficheros con el token» cuando eran DOS tokens —18 con el ambiental y 5
		# con un tercer valor— y eso cambia QUÉ HAY QUE ROTAR, que es la única decisión que
		# este informe existe para alimentar. La etiqueta son los 12 primeros hex de un
		# sha256 del valor: agrupa sin revelar, porque un sha256 truncado no se invierte y
		# ni siquiera identifica al secreto fuera de esta lista.
		etiqueta="$(LC_ALL=C grep -aoE "(${NOMBRES})=[A-Za-z0-9_.:/+-]{${MIN_VALOR},}" "$f" 2>/dev/null \
			| head -1 | sed 's/^[^=]*=//' | tr -d '\n' | sha256sum | cut -c1-12)"
		printf '  %-28s [%s] %s  (%d ocurrencia(s), valor de ~%d caracteres)\n' \
			"${nombre:-?}" "$etiqueta" "$f" "$n" "$((largo - 1))"
		hallazgos=$((hallazgos + n))
	done < <(find "$r" -type f -size +0c 2>/dev/null)
done

echo
if [ "$INCLUIR_CACHES" -eq 0 ]; then
	echo "scan-box-secrets: exclusiones por ruta aplicadas (--incluir-caches las levanta):"
	for x in "${EXCLUIDAS[@]}"; do
		printf '  %-20s tabla de cadenas de binarios: literales contiguos se leen como un valor\n' "$x"
	done
	echo "  ficheros saltados por esas rutas: ${excluidos}"
fi
if [ "$no_mirados" -gt 0 ]; then
	echo "scan-box-secrets: ${no_mirados} ruta(s) ILEGIBLES — el veredicto es PARCIAL" >&2
fi
if [ "$hallazgos" -gt 0 ]; then
	echo "scan-box-secrets: ${hallazgos} hallazgo(s) en ${mirados} raiz(ces) revisada(s)." >&2
	echo "  ⛔ Borrar el fichero NO cierra la exposicion: la credencial se ROTA primero." >&2
	exit 1
fi
echo "scan-box-secrets: CLEAN — 0 hallazgos en ${mirados} raiz(ces) revisada(s), con la lista arriba."
exit 0
