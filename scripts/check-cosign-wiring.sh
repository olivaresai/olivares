#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cosign-wiring.sh — thin wrapper over cmd/olivares/tools/checkcosignwiring, which
# enforces that the execution-time cosign control is LOAD-BEARING: nothing signs except
# through scripts/cosign-verified.sh, and every job that installs cosign asserts it under
# the same condition.
#
# WHY IT IS A GO TOOL AND NOT A GREP. The first version of this check was a set of greps and
# an adversarial review broke it with two ordinary edits: changing every `cmd: bash` to
# `cmd: true` (the counts stayed identical, nothing could execute), and replacing a chart
# publisher with `"$OLIVARES_COSIGN_BIN" sign …` (a real bypass of the per-invocation
# re-hash, containing no `cosign` token). Counting occurrences cannot express "this command
# runs that wrapper"; reading the YAML structure can.
#
# COSIGN_WIRING_ROOT overrides the tree under inspection; the mutation matrix
# (scripts/test-cosign-wiring.sh) uses it to point the checker at throwaway fixtures and
# assert that each bypass is REJECTED.
set -euo pipefail

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
# shellcheck source=lib/exec-workdir.sh
# lib/exec-workdir.sh ya PRUEBA que un candidato puede crear y EJECUTAR: no se elige el
# directorio, se demuestra. Antes esto era `mktemp -d "${VAR:-$HOME}/..."`, que bajo
# `set -u` MUERE con «HOME: unbound variable» en los runners sin $HOME — seis de nueve —
# antes siquiera de llegar a su propio respaldo.
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	# Sin la lib el guion esta CIEGO, y eso es 2 — no el error crudo del shell. La bateria
	# lo comprueba copiando este fichero solo a un arbol vacio.
	echo "check-cosign-wiring: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
export COSIGN_WIRING_ROOT="${COSIGN_WIRING_ROOT:-$ROOT}"

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
	echo "cosign-wiring: NO HE PODIDO MIRAR: falta $ROOT/cmd/olivares, así que no puedo construir la" >&2
	echo "cosign-wiring: herramienta que juzga. Esto no es un árbol limpio: es un árbol que no he leído." >&2
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
BINDIR="$(olivares_pick_exec_workdir gatebin)" || {
	echo "cosign-wiring: NO HE PODIDO MIRAR: no puedo crear el directorio del binario" >&2
	exit 2
}
cleanup() { rm -rf "$BINDIR"; }
trap cleanup EXIT HUP INT TERM

if ! go build -o "$BINDIR/checkcosignwiring" ./tools/checkcosignwiring; then
	echo "cosign-wiring: NO HE PODIDO MIRAR: la herramienta no compila" >&2
	exit 2
fi
"$BINDIR/checkcosignwiring"
exit $?