#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-ci-ports.sh — thin wrapper over cmd/olivares/tools/checkciports.
#
# The enforcement lives in Go because the thing being checked is YAML. The policy: no
# fixed HOST ports in any workflow — every jobs.<id>.services.<sid>.ports[] /
# jobs.<id>.container.ports[] entry must be a bare container port (`- 5432`), never a
# host mapping (`- 5432:5432`). The fixed mapping is how two jobs on one self-hosted
# runner host collided: "Bind for :::5432 failed: port is already allocated" killed the
# second job in "Initialize containers" (run 30541550811). A structural YAML walk, not a
# grep: e2e-operator-kind.yml carries Kubernetes `ports:` keys inside a `run: |` heredoc
# that a key scanner would flag and a decoder ignores by construction. See the header of
# the Go program for the full rationale.
#
# This wrapper exists so the call sites (Taskfile `lint:ci-ports`, `.githooks/pre-push`
# via `task lint:actions`, `.github/workflows/mainline-ci.yml`) stay stable and
# shell-shaped like every other lint in this repository.
#
# CI_PORTS_ROOT overrides the tree under inspection; the mutation matrix
# (scripts/test-ci-ports-gate.sh) uses it to point the gate at throwaway fixtures.
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
	echo "check-ci-ports: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
export CI_PORTS_ROOT="${CI_PORTS_ROOT:-$ROOT}"

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
	echo "ci-ports: NO HE PODIDO MIRAR: falta $ROOT/cmd/olivares, así que no puedo construir la" >&2
	echo "ci-ports: herramienta que juzga. Esto no es un árbol limpio: es un árbol que no he leído." >&2
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
	echo "check-ci-ports: NO HE PODIDO MIRAR: no puedo crear el directorio del binario" >&2
	exit 2
}
cleanup() { rm -rf "$BINDIR"; }
trap cleanup EXIT HUP INT TERM

if ! go build -o "$BINDIR/checkciports" ./tools/checkciports; then
	echo "check-ci-ports: NO HE PODIDO MIRAR: la herramienta no compila" >&2
	exit 2
fi
"$BINDIR/checkciports"
exit $?