#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-web-bundle-freshness.sh — si un push cambia las FUENTES de la consola y no cambia el bundle
# empotrado, lo dice AQUÍ y no dos horas después en `main`.
#
# ⛔ POR QUÉ EXISTE, y la cifra es de un solo día. El 2026-08-17 el bundle de
# `core/internal/webui/dist` quedó por detrás de `web/` **cuatro veces**. Cada una costó lo mismo: el
# job `web` de `mainline-ci` en rojo sobre `main`, es decir sobre el tronco de los cinco carriles, y
# la reparación siempre fue la misma orden de una línea (`task build:web`). Un fallo que se repite
# cuatro veces en una jornada y siempre se arregla igual **no es descuido: es un gate que falta**.
#
# **Y el nombre del paso hace daño de propina.** Se llama *«fail if the committed bundle is stale»*,
# así que CUALQUIER fallo de ese job —incluido uno de toolchain— se lee como «bundle obsoleto». Ese
# mismo día diagnostiqué un `exit 127` de `pnpm` como bundle obsoleto **por leer el nombre del paso en
# vez de su salida**. Cazar la causa real aquí deja aquel rojo para lo que de verdad sea.
#
# ⛔⛔ «SI CAMBIAN LAS FUENTES Y EL BUNDLE NO, SEGURO QUE ESTÁ OBSOLETO» — ESO DECÍA AQUÍ, Y ES FALSO.
#    El contraejemplo está medido el 2026-08-18: un cambio de TIPOS en TypeScript —53 ficheros
#    re-apuntando un `import type` de `ColumnDef` a un alias propio— toca 53 fuentes empaquetadas y
#    produce un bundle **byte a byte idéntico**, porque los tipos se borran al compilar. Se
#    reconstruyó (`task build:web`, rc=0) y `git status` sobre `dist` dio **0 ficheros**.
#
#    Y el daño no es un falso rojo más: es un gate **que no se puede poner verde haciendo lo
#    correcto**. El propio mensaje mandaba `task build:web`, reconstruir no cambiaba nada, y la única
#    salida que quedaba era `--no-verify`. Un gate cuyo remedio no funciona enseña a saltárselo — la
#    misma enfermedad que el `|| true`, por el otro extremo.
#
# ⇒ AHORA MIDE EN VEZ DE INFERIR. `scripts/web-bundle-source-digest.sh` da el digest del CONJUNTO de
#   fuentes empaquetadas, y `task build:web` lo deja sellado en `core/internal/webui/bundle-source.stamp`.
#   La pregunta pasa a ser la que de verdad importa —**¿de qué fuentes salió el bundle commiteado?**—
#   y se contesta comparando el sello del commit empujado con el digest de sus propias fuentes. Un
#   cambio de tipos reconstruye, el bundle no se mueve, el SELLO sí, y el gate queda verde por la
#   razón correcta.
#
#   Sigue siendo una condición NECESARIA y no suficiente: que el sello case no prueba que el bundle
#   sea el que produce ese árbol —eso lo demuestra `task web:check` reconstruyendo y comparando, que
#   cuesta ~35 s y por eso vive en el gate pesado—. Lo que sí prueba es que **alguien construyó desde
#   estas fuentes**, que es exactamente lo que el heurístico del diff pretendía y no lograba.
#
#   Sin sello en el commit empujado se cae al heurístico viejo y **se dice cuál se ha usado**: un
#   veredicto que no dice con qué instrumento se tomó no se puede auditar. Este guion sigue sin
#   compilar nada: es git puro y su sitio es el carril rápido.
#
# Los ficheros de prueba y de e2e quedan FUERA a propósito: no viajan en el bundle, y cobrarlos daría
# un falso rojo en el commit más inocente.
#
# Salida: 0 coherente · 1 fuentes cambiadas sin bundle · 2 NO HE PODIDO MIRAR.
set -uo pipefail
LC_ALL=C
export LC_ALL

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: no puedo cargar $_olivares_git_env" >&2
	exit 2
}
unset _olivares_git_env

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ" 2>/dev/null || {
	echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: no existe $RAIZ" >&2
	exit 2
}
DIST="${OLIVARES_WEBUI_DIST:-core/internal/webui/dist}"
SRC_DIR="${OLIVARES_WEB_DIR_REL:-web}"

[ -d "$DIST" ] || {
	echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: no existe $DIST." >&2
	exit 2
}

# --- el rango ---------------------------------------------------------------------------------
# En un push, del protocolo. Fuera de un push, contra `origin/main`. Nunca se adivina: sin rango no
# hay veredicto.
rangos=()
if [ -n "${OLIVARES_PUSH_REFS_FILE:-}" ]; then
	[ -r "${OLIVARES_PUSH_REFS_FILE}" ] || {
		echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: OLIVARES_PUSH_REFS_FILE apunta a" >&2
		echo "  '${OLIVARES_PUSH_REFS_FILE}', que no puedo leer." >&2
		exit 2
	}
	while read -r _lref lsha _rref rsha; do
		[ -n "${lsha:-}" ] || continue
		case "$lsha" in *[!0-9a-f]*) continue ;; esac
		# Borrado: nada que empaquetar.
		case "$lsha" in 0000000000000000000000000000000000000000 | 0000000000000000000000000000000000000000000000000000000000000000) continue ;; esac
		case "${rsha:-}" in
		0000000000000000000000000000000000000000 | 0000000000000000000000000000000000000000000000000000000000000000 | "")
			base="$(git merge-base "$lsha" origin/main 2>/dev/null)" || base=""
			[ -n "$base" ] && rangos+=("${base}..${lsha}")
			;;
		*)
			if git cat-file -e "${rsha}^{commit}" 2>/dev/null; then
				rangos+=("${rsha}..${lsha}")
			else
				base="$(git merge-base "$lsha" origin/main 2>/dev/null)" || base=""
				[ -n "$base" ] && rangos+=("${base}..${lsha}")
			fi
			;;
		esac
	done <"${OLIVARES_PUSH_REFS_FILE}"
else
	base="$(git merge-base HEAD origin/main 2>/dev/null)" || base=""
	[ -n "$base" ] || {
		echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: no hay base común con origin/main." >&2
		echo "  Sin rango no hay veredicto: «no he podido mirar» no es «está fresco»." >&2
		exit 2
	}
	rangos+=("${base}..HEAD")
fi

if [ "${#rangos[@]}" -eq 0 ]; then
	echo "check-web-bundle-freshness: OK — nada que empaquetar en este push (borrados o sin commits)."
	exit 0
fi

cambiados=""
for r in "${rangos[@]}"; do
	salida="$(git diff --name-only "$r" 2>/dev/null)" || {
		echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: git diff falló sobre $r." >&2
		exit 2
	}
	cambiados="${cambiados}
${salida}"
done

# Fuentes que SÍ viajan en el bundle. Las pruebas y el e2e no.
fuentes="$(printf '%s\n' "$cambiados" |
	grep -E "^${SRC_DIR}/(src/|public/|index\.html|vite\.config|tsconfig|package\.json|pnpm-lock\.yaml)" |
	grep -vE '\.(test|spec)\.(ts|tsx|js|jsx)$' |
	grep -vE "^${SRC_DIR}/(e2e|tests)/" | LC_ALL=C sort -u)"
n_src="$(printf '%s\n' "$fuentes" | grep -c . || true)"
n_dist="$(printf '%s\n' "$cambiados" | grep -cE "^${DIST}/" || true)"

echo "check-web-bundle-freshness: fuentes de la consola cambiadas=${n_src:-0} · ficheros del bundle cambiados=${n_dist:-0}"

# --- el sello, que es la medida ----------------------------------------------------------------
# Se juzga el commit EMPUJADO, no el árbol de trabajo: lo que aterriza en el remoto es el commit.
STAMP_REL="core/internal/webui/bundle-source.stamp"
tip=""
for r in "${rangos[@]}"; do tip="${r##*..}"; done
[ "$tip" = "HEAD" ] && tip="$(git rev-parse HEAD 2>/dev/null)"

sellado=""
if [ -n "$tip" ]; then
	sellado="$(git show "${tip}:${STAMP_REL}" 2>/dev/null | head -1)"
fi

if [ -n "$sellado" ]; then
	actual="$(bash "$RAIZ/scripts/web-bundle-source-digest.sh" "$tip" 2>/dev/null)"
	if [ -z "$actual" ]; then
		echo "check-web-bundle-freshness: ⛔ NO HE PODIDO MIRAR: no he podido calcular el digest de $tip." >&2
		exit 2
	fi
	if [ "$sellado" = "$actual" ]; then
		echo "check-web-bundle-freshness: OK — el sello de origen casa con las fuentes de $tip ($actual)."
		exit 0
	fi
	# ⛔ DOS CAUSAS, DOS REMEDIOS OPUESTOS — y el mensaje los distinguía con un dato que YA TENÍA.
	#
	# Hasta el 2026-08-24 esta rama decía siempre «Arréglalo aquí y no en main». Es correcto cuando
	# TÚ moviste las fuentes; es la peor instrucción posible cuando el sello viene rancio DE `main`:
	# manda reconstruir a quien no ha tocado la consola, y el remedio que prescribe (`build:web` +
	# `git add -A dist`) puede arrastrar a su PR ficheros que no son suyos.
	#
	# La señal que separa los dos casos se imprime TRES LÍNEAS más arriba: `cambiadas=0 ·
	# cambiados=0` significa que esta punta no tocó ni fuentes ni bundle, así que la vejez sólo
	# puede venir heredada. Medido ese día: `74ab2d201` (una línea de `openapi.gen.ts`, cero
	# ficheros de bundle) dejó `main` con el sello desalineado, y a partir de ahí TODA rama fallaba
	# aquí en la posición 103 de 124 — tras ~80 minutos de carril— sin haber tocado nada.
	#
	# ⇒ El predicado NO cambia: el sello sigue sin casar y el rojo sigue siendo verdad. Lo que
	#   cambia es a quién se le manda arreglarlo.
	echo "check-web-bundle-freshness: ⛔ EL SELLO NO CASA CON LAS FUENTES." >&2
	echo "  sellado: $sellado" >&2
	echo "  fuentes: $actual" >&2
	if [ "${n_src:-0}" -eq 0 ] && [ "${n_dist:-0}" -eq 0 ]; then
		echo "  Esta punta NO tocó fuentes de consola ni ficheros del bundle (cambiadas=0 · cambiados=0)," >&2
		echo "  así que la vejez es HEREDADA: viene de la base, no de tu trabajo." >&2
		echo "  ⛔ NO reconstruyas aquí: meterías en tu PR un cambio que no es tuyo." >&2
		# ⛔ Y LA BASE PUEDE ESTAR YA ARREGLADA, que es el tercer caso y el que more confunde:
		#    tu rama arrastra el sello viejo aunque `main` lleve el bueno. Medido el 2026-08-24
		#    usando este mismo mensaje: `main` ya sellaba bien y mi rama, 33 commits por detrás,
		#    seguía roja. El remedio ahí no es reconstruir NI esperar: es TRAER la base.
		base_sello="$(git show "origin/main:${STAMP_REL}" 2>/dev/null | head -1 | awk '{print $1}')"
		base_actual="$(bash "$RAIZ/scripts/web-bundle-source-digest.sh" origin/main 2>/dev/null | awk '{print $1}')"
		if [ -n "$base_sello" ] && [ "$base_sello" = "$base_actual" ]; then
			echo "  ✅ La base YA está arreglada (origin/main sella ${base_sello:0:12}…) y tu rama arrastra" >&2
			echo "     el sello viejo. El remedio es TRAER la base, no reconstruir:" >&2
			echo "       git fetch origin main && git merge --no-edit origin/main" >&2
		else
			echo "  La base TAMBIÉN está rancia: el arreglo es de \`main\`, no tuyo. Espera a que aterrice." >&2
			echo "  Compruébalo tú mismo en dos líneas:" >&2
			echo "    git show \"origin/main:core/internal/webui/bundle-source.stamp\" | awk '{print \$1}'" >&2
			echo "    git show \"HEAD:core/internal/webui/bundle-source.stamp\"        | awk '{print \$1}'" >&2
		fi
	else
		echo "  Esta punta SÍ movió fuentes o bundle (cambiadas=${n_src:-0} · cambiados=${n_dist:-0})," >&2
		echo "  así que el sello es tuyo. Arréglalo aquí:" >&2
		echo "    task build:web && git add -A $DIST core/internal/webui/bundle-source.stamp" >&2
	fi
	exit 1
fi

# --- sin sello: el heurístico viejo, DICIENDO que es el viejo ----------------------------------
echo "check-web-bundle-freshness: ⚠ sin sello de origen en $tip — cayendo al heurístico del diff," \
	"que da falsos rojos en cambios que sólo tocan TIPOS. Corre 'task build:web' para sellar."
if [ "${n_src:-0}" -gt 0 ] && [ "${n_dist:-0}" -eq 0 ]; then
	echo "check-web-bundle-freshness: ⛔ EL BUNDLE EMPOTRADO SE QUEDA ATRÁS." >&2
	echo "  Este push cambia ${n_src} fichero(s) que SÍ se empaquetan y ni uno de $DIST:" >&2
	printf '%s\n' "$fuentes" | head -8 | sed 's/^/    /' >&2
	[ "${n_src}" -gt 8 ] && echo "    … y $((n_src - 8)) más" >&2
	echo "  Arréglalo aquí y no en main:" >&2
	echo "    task build:web && git add -A $DIST" >&2
	echo "  (Esto es una condición NECESARIA: que el bundle cambiara no probaría que es el correcto." >&2
	echo "   Eso lo demuestra 'task web:check', que reconstruye y compara.)" >&2
	exit 1
fi
echo "check-web-bundle-freshness: OK."
exit 0
