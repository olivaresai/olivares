#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Structural regression guard for the flat install manifest (C5). It asserts
# deploy/manifests/install.yaml exists, is the safe single-node posture, and keeps the
# C1-correct probe paths — so an over-broad chart edit (extra workloads, wrong probe
# path, multi-replica default) cannot silently ship in the no-Helm install path. The
# freshness (chart-in-sync) check is the CI `helm template … && git diff --exit-code`
# step; this complements it with a content contract. Prefers kubeconform when present.
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
# shellcheck source=lib/exec-workdir.sh
# lib/exec-workdir.sh ya PRUEBA que un candidato puede crear y EJECUTAR: no se elige el
# directorio, se demuestra. Antes esto era `mktemp -d "${VAR:-$HOME}/..."`, que bajo
# `set -u` MUERE con «HOME: unbound variable» en los runners sin $HOME — seis de nueve —
# antes siquiera de llegar a su propio respaldo.
. "$ROOT/scripts/lib/exec-workdir.sh" || {
	# Sin la lib el guion esta CIEGO, y eso es 2 — no el error crudo del shell. La bateria
	# lo comprueba copiando este fichero solo a un arbol vacio.
	echo "check-install-manifest: NO HE PODIDO MIRAR: falta scripts/lib/exec-workdir.sh" >&2
	exit 2
}
MANIFEST="$ROOT/deploy/manifests/install.yaml"

fail() { echo "check-install-manifest: FAIL — $1" >&2; exit 1; }

[ -f "$MANIFEST" ] || fail "missing $MANIFEST (run 'task manifests:gen')"

# Schema validation when kubeconform is installed (not required to be present).
if command -v kubeconform >/dev/null 2>&1; then
	kubeconform -strict -kubernetes-version 1.29.0 "$MANIFEST" || fail "kubeconform schema validation failed"
fi

# Content contract: parse the multi-doc YAML and assert the safe single-node
# shape. Implemented in Go (operator module, its K8s domain): Go is the ONE
# toolchain every environment that runs this gate is guaranteed to have — the
# dev container ships no pip/PyYAML, and runner images change their preinstalled
# Python packages without notice. Hermetic: no system-Python dependency.
# ⛔ DOS COLAPSOS APILADOS, y se arreglan los dos o no se arregla ninguno. Los localizó
# El carril de integración con `file:line` el 2026-08-15:
#
#   ( cd … && go run ./cmd/checkinstallmanifest "$MANIFEST" ) || exit 1
#        ↑ colapso 1: `go run` convierte el 2 de la herramienta en 1  ↑ colapso 2: aplasta CUALQUIER
#                                                                       código a 1
#
# Medido con la herramienta compilada: 0 o 2 argumentos → **2**, manifiesto ilegible → **1**,
# manifiesto real → **0**. Y una precisión que este comentario debe llevar para no prometer de más:
# **hoy el 2 no es alcanzable desde aquí**, porque este script pasa siempre exactamente un
# argumento, así que el arreglo es defensa en profundidad y no cierra una fuga viva. Lo que sí
# cierra es el futuro: el día que la herramienta añada un tercer veredicto, el `|| exit 1` lo
# aplastaría en silencio y nadie lo notaría.
BIN_DIR="$(olivares_pick_exec_workdir gatebin)" || {
	echo "check-install-manifest: NO HE PODIDO MIRAR: no puedo crear el directorio del binario" >&2
	exit 2
}
# `/tmp` NO sirve: está montado noexec en el contenedor de desarrollo (medido — execve da 126 allí
# y 7 bajo $HOME), así que el binario se compila donde sí se puede ejecutar.
cleanup_bin() { rm -rf "$BIN_DIR"; }
trap cleanup_bin EXIT HUP INT TERM

if ! ( cd "$ROOT/operator" && go build -o "$BIN_DIR/checkinstallmanifest" ./cmd/checkinstallmanifest ); then
	echo "check-install-manifest: NO HE PODIDO MIRAR: la herramienta no compila." >&2
	exit 2
fi
"$BIN_DIR/checkinstallmanifest" "$MANIFEST"
exit $?
