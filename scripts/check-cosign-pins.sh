#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cosign-pins.sh — thin wrapper over cmd/olivares/tools/checkcosignpins.
#
# The enforcement lives in Go because the thing being checked is YAML, and two successive
# text-scanning versions of this gate were broken by an adversarial review: the first
# credited a pin belonging to the next action and accepted `env:` in place of `with:`; the
# second still missed a YAML-ESCAPED action name that GitHub executes but a substring
# search cannot see, and credited a fake `cosign-release:` line inside a block scalar under
# the real `install-dir:` input. Decoding the document removes both classes by
# construction — YAML resolves escapes and aliases before anything is compared, and a block
# scalar is a value, not a mapping key. See the header of that program for the full
# rationale.
#
# This wrapper exists so the call sites (Taskfile `lint:cosign-pins`, `.githooks/pre-push`,
# `.github/workflows/mainline-ci.yml`) stay stable and shell-shaped like every other lint
# in this repository.
#
# COSIGN_PINS_ROOT overrides the tree under inspection; the mutation matrix
# (scripts/test-cosign-pins-gate.sh) uses it to point the gate at throwaway fixtures.
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
export COSIGN_PINS_ROOT="${COSIGN_PINS_ROOT:-$ROOT}"

# ⛔ EL `cd` VA GUARDADO, Y NO ES ESTILO: SIN ESTO LA GUARDA DE ABAJO NO SE ALCANZA NUNCA.
# La herramienta ya sabe negarse bien —«no workflow files under … — wrong CI_PORTS_ROOT? refusing
# to report a vacuous pass», con rc 2—, pero si el layout no está, este `cd` falla ANTES y `set -e`
# corta con el error crudo del shell: `line 29: cd: …: No such file or directory`, rc 1. Un
# veredicto de ceguera perfecto, inalcanzable por la línea que tiene delante.
#
# Lo midió el carril de integración con el señuelo correcto —copiar el script a un árbol vacío, no un `cd`
# del llamante, porque la raíz se resuelve desde `$0`— y aquí queda reproducido en los dos
# sentidos: sin guarda rc=1 con el error del shell; con ella, rc=2 nombrando lo que falta.
if ! cd "$ROOT/cmd/olivares" 2>/dev/null; then
	echo "cosign-pins: NO HE PODIDO MIRAR: falta $ROOT/cmd/olivares, así que no puedo construir la" >&2
	echo "cosign-pins: herramienta que juzga. Esto no es un árbol limpio: es un árbol que no he leído." >&2
	exit 2
fi
# ⛔ `go run` COLAPSA EL CÓDIGO DE SALIDA DE LA HERRAMIENTA, y con él la TERCERA RESPUESTA.
# Medido el 2026-08-15, aislado con un programa que hace `os.Exit(2)`: `go run` imprime
# «exit status 2» y **sale 1**. Aquí eso importa más que en otros sitios: la herramienta usa el 2
# para decir «no he podido mirar» —p.ej. raíz vacía, «no metadata found»— y el llamante recibía
# «está roto». Un veredicto de ceguera degradado a rojo manda a alguien a arreglar lo que no
# existe, y es justo la distinción que este árbol lleva el día entero defendiendo.
#
# Se compila y se ejecuta, que sí propaga. El directorio del binario NO puede ser `/tmp`: en este
# contenedor está montado noexec (medido: execve da 126 allí y 7 en `$HOME`, `$HOME/.cache` y el
# propio repo). Si no se puede compilar o ejecutar, el veredicto es 2 — no un verde.
# ⛔ Y `$HOME` NO SE DA POR SUPUESTO. Medido el 2026-08-18 en ci-runner-9: este guion murio en
# «HOME: unbound variable» bajo `set -u`, ANTES de llegar a su propio respaldo, porque el runner
# corre sin `$HOME`. El gate contesto 2 —«no he podido mirar»— que es lo correcto, pero la
# consecuencia es que el gate no corre en seis de las nueve maquinas.
#
# El directorio no se elige: se PRUEBA. Un candidato vale solo si se puede crear ahi y EJECUTAR
# algo — que es la propiedad que hace falta y la que `/tmp` no cumple en este contenedor. Se
# prueba con un ejecutable de verdad, no mirando el modo ni el punto de montaje: `noexec` y un
# bit ausente dan el mismo `permission denied` y aqui solo importa el resultado.
# shellcheck source=lib/exec-workdir.sh
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	# Sin la lib el guion esta CIEGO, y eso es 2 — no el error crudo del shell. La bateria
	# lo comprueba copiando este fichero solo a un arbol vacio.
	echo "check-cosign-pins: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
BINDIR="$(olivares_pick_exec_workdir gatebin)" || {
	echo "cosign-pins: NO HE PODIDO MIRAR: ningun candidato permite crear y EJECUTAR un binario" >&2
	echo "             probados: OLIVARES_GATE_BINDIR, RUNNER_TEMP, TMPDIR, /tmp, HOME y el scratch del contenedor" >&2
	exit 2
}

# Y la cache de Go tampoco se da por supuesta, por la MISMA razon y con otro mensaje: sin `$HOME`,
# `go build` muere con «build cache is required, but could not be located: GOCACHE is not defined
# and neither $XDG_CACHE_HOME nor $HOME are defined» — que no menciona HOME como causa hasta el
# final de la frase. Si no viene fijada, cuelga del directorio que acabamos de PROBAR.
if [ -z "${GOCACHE:-}" ] && [ -z "${HOME:-}" ] && [ -z "${XDG_CACHE_HOME:-}" ]; then
	GOCACHE="$BINDIR/gocache"
	mkdir -p "$GOCACHE" || {
		echo "cosign-pins: NO HE PODIDO MIRAR: sin GOCACHE ni HOME, y no puedo crear una cache" >&2
		exit 2
	}
	export GOCACHE
fi
cleanup() { rm -rf "$BINDIR"; }
trap cleanup EXIT HUP INT TERM

if ! go build -o "$BINDIR/checkcosignpins" ./tools/checkcosignpins; then
	echo "cosign-pins: NO HE PODIDO MIRAR: la herramienta no compila" >&2
	exit 2
fi
"$BINDIR/checkcosignpins"
exit $?