#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ensure-shellcheck.sh — resuelve un `shellcheck` ANCLADO POR HASH e imprime su ruta.
#
# ⛔ POR QUE EXISTE, con la medida delante. `actionlint` delega sus reglas de shell en
#    `shellcheck` y, si el binario NO esta, SALTA esa comprobacion en silencio y sale 0.
#    Medido el 2026-08-31 sobre `main`, mismo arbol y mismo comando, cambiando solo el PATH:
#
#        task lint:actions  SIN shellcheck ......  rc=0    (verde)
#        task lint:actions  CON shellcheck 0.10.0  rc=201  (45 hallazgos)
#
#    Y con un workflow señuelo de tres `$VAR` sin comillas: 0 hallazgos ciego, 3 SC2086 viendo.
#    Es decir: nuestras cajas llevaban el dia entero declarando limpio un gate que el CI del
#    espejo daba rojo, y el `rc 0` no era un veredicto sino un «no he podido mirar» disfrazado.
#
# ⛔ EL HASH SE ANCLA A UN BINARIO CUYA PROCEDENCIA SE VERIFICO, no al que uno se encuentre.
#    El 2026-08-31 habia dos copias en la caja, bajadas por dos carriles distintos, con el MISMO
#    sha256 — eso es corroboracion, no procedencia. Se descargo el tarball del release oficial
#    por TLS y se comparo: coinciden. Esos son los dos valores de abajo.
set -euo pipefail

VER=0.10.0
TARBALL_SHA=6c881ab0698e4e6ea235245f22832860544f17ba386442fe7e9d629f8cbedf87
BIN_SHA=f35ae15a4677945428bdfe61ccc297490d89dd1e544cc06317102637638c6deb
URL="https://github.com/koalaman/shellcheck/releases/download/v${VER}/shellcheck-v${VER}.linux.x86_64.tar.xz"

huella() { command sha256sum "$1" 2>/dev/null | command cut -d' ' -f1; }

# Se acepta un binario SOLO si su huella casa. Un `shellcheck` de otra version en el PATH
# daria otros hallazgos y haria incomparables las cifras entre cajas.
sirve() { [ -x "${1:-}" ] && [ "$(huella "$1")" = "$BIN_SHA" ]; }

# --- EL DESTINO ES UN HECHO DEL ENTORNO DE EJECUCION, NO UNA RUTA FIJA (2026-09-16) --------------
# `dest=/workspace/.olivares-bin` era la ruta de UN contenedor. En los runners de la VM y en los
# hospedados de GitHub `/workspace` no existe ni se puede crear, y el gate contesto «no he podido
# mirar» en vez de mirar: mainline-ci 35127191064 job control-plane en home-vm-1-2 y 35127694552
# en ubuntu-latest, `mkdir: cannot create directory '/workspace': Permission denied` → rc 2 →
# job rojo, con el MISMO arbol verde cuatro horas antes en un runner de Hetzner. Un veredicto que
# depende de en que maquina cae el job no es un veredicto. El destino se elige, por este orden:
#   1. OLIVARES_BIN_DIR              explicito; si no se puede escribir se REHUSA nombrandolo, no
#                                    se cae en silencio a un sitio que quien llama no pidio
#   2. $RUNNER_TEMP/olivares-bin     el runner de CI, efimero y propio de cada job
#   3. /workspace/.olivares-bin      la convencion de este contenedor (OLIVARES_HUB_BIN_DIR la
#                                    desplaza SOLO en la bateria, para simular que no existe)
#   4. ${XDG_CACHE_HOME:-$HOME/.cache}/olivares-bin
#   5. ${TMPDIR:-/tmp}/olivares-bin
# El primero que se pueda crear y escribir gana; si ninguno, rc 2 con TODOS los probados.
# `--destino` imprime la eleccion sin bajar nada: es el puerto por el que la bateria la mide.
HUB_BIN="${OLIVARES_HUB_BIN_DIR:-/workspace/.olivares-bin}"
escribible() { [ -n "${1:-}" ] && command mkdir -p -- "$1" 2>/dev/null && [ -w "$1" ]; }
candidatos_destino() {
	[ -n "${RUNNER_TEMP:-}" ] && printf '%s\n' "$RUNNER_TEMP/olivares-bin"
	printf '%s\n' "$HUB_BIN"
	printf '%s\n' "${XDG_CACHE_HOME:-${HOME:-/nonexistent}/.cache}/olivares-bin"
	printf '%s\n' "${TMPDIR:-/tmp}/olivares-bin"
}
elegir_destino() {
	if [ -n "${OLIVARES_BIN_DIR:-}" ]; then
		if escribible "$OLIVARES_BIN_DIR"; then printf '%s\n' "$OLIVARES_BIN_DIR"; return 0; fi
		echo "ensure-shellcheck: NO HE PODIDO MIRAR: OLIVARES_BIN_DIR=$OLIVARES_BIN_DIR no se puede crear o escribir." >&2
		return 2
	fi
	local d probados=""
	while IFS= read -r d; do
		if escribible "$d"; then printf '%s\n' "$d"; return 0; fi
		probados="$probados $d"
	done < <(candidatos_destino)
	echo "ensure-shellcheck: NO HE PODIDO MIRAR: ningun destino escribible donde instalar shellcheck v${VER}; probados:$probados" >&2
	return 2
}
if [ "${1:-}" = "--destino" ]; then elegir_destino; exit $?; fi

# Se busca primero uno ya valido: el explicito, el del PATH y el que cada destino posible ya tenga.
# Aqui NO se crea ningun directorio: mirar no es escribir.
_cands=("${OLIVARES_SHELLCHECK:-}" "$(command -v shellcheck 2>/dev/null || true)")
[ -n "${OLIVARES_BIN_DIR:-}" ] && _cands+=("$OLIVARES_BIN_DIR/shellcheck")
while IFS= read -r d; do _cands+=("$d/shellcheck"); done < <(candidatos_destino)
for cand in "${_cands[@]}"; do
	if sirve "$cand"; then printf '%s\n' "$cand"; exit 0; fi
done

# No hay ninguno valido: se baja y se VERIFICA antes de usarlo. Si no se puede, se rehusa
# ruidosamente — nunca se sigue sin el, que es justo el fallo que este guion cierra.
dest="$(elegir_destino)" || exit 2
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
if ! command curl -sS -L -o "$tmp/sc.tar.xz" "$URL" 2>/dev/null; then
	echo "ensure-shellcheck: NO HE PODIDO MIRAR: sin shellcheck v${VER} y sin red para bajarlo." >&2
	exit 2
fi
if [ "$(huella "$tmp/sc.tar.xz")" != "$TARBALL_SHA" ]; then
	echo "ensure-shellcheck: NO HE PODIDO MIRAR: el tarball descargado NO casa con el hash anclado." >&2
	echo "    esperado $TARBALL_SHA" >&2
	echo "    obtenido $(huella "$tmp/sc.tar.xz")" >&2
	exit 2
fi
command tar -xJf "$tmp/sc.tar.xz" -C "$tmp"
if [ "$(huella "$tmp/shellcheck-v${VER}/shellcheck")" != "$BIN_SHA" ]; then
	echo "ensure-shellcheck: NO HE PODIDO MIRAR: el binario extraido NO casa con el hash anclado." >&2
	exit 2
fi
command install -m 0755 "$tmp/shellcheck-v${VER}/shellcheck" "$dest/shellcheck"
printf '%s\n' "$dest/shellcheck"
