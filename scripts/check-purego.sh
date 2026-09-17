#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-purego.sh — verify every workspace module builds with CGO_ENABLED=0
# (ARCHITECTURE.md: the default `olivares` binary is pure-Go, static and
# reproducible — modernc.org/sqlite, no cgo). A cgo import anywhere the default
# binary links breaks the static build; this gate catches it in CI before a
# release does, and is the guard for the off-box ledger signer: it
# is KMS-via-REST (pure-Go) and a native PKCS#11/HSM is OUT-OF-PROCESS, so adding
# the off-box signer must NEVER drag cgo into /core. Same principle as the
# SQLCipher decision.
#
# It uses the same per-module iteration as the rest of the build (go.work modules),
# but forces CGO_ENABLED=0 so a `import "C"` fails the build instead of silently
# linking a non-static binary. `task test` deliberately uses -race (which needs
# cgo) for the detector — that is a TEST toolchain choice and does not relax this
# BUILD invariant, which is exactly why it is a separate gate.
#
# Usage:  scripts/check-purego.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# ⛔ «NO HE PODIDO MIRAR» NO ES «ESTÁ ROTO», y este gate las fundía en el mismo `exit 1`.
# Sin `go.work` no hay nada que examinar: el veredicto correcto es 2, no 1. Importa justo cuando más
# —un clon superficial, un árbol exportado, un runner mal montado—: ahí un `1` se lee como «el
# producto tiene un defecto de purego» y manda a alguien a buscar un fallo que no existe, mientras
# el hecho real (no hay árbol que mirar) queda sin decir. Medido el 2026-08-18 corriendo los 63
# gates contra un señuelo sin sujeto: éste era uno de los once que contestaban «roto» a una ausencia.
[ -f go.work ] || {
	echo "check-purego: ⛔ NO HE PODIDO MIRAR: no hay go.work en ${ROOT}, así que no hay módulos" >&2
	echo "              que examinar. Eso no es un fallo de purego: es un árbol sin nada que mirar." >&2
	exit 2
}
mapfile -t MODULES < <(go work edit -json | sed -n 's/.*"DiskPath": "\(.*\)".*/\1/p')
# Misma clase: un `go.work` que no enumera módulos no es un producto roto, es un examen imposible.
[ "${#MODULES[@]}" -gt 0 ] || {
	echo "check-purego: ⛔ NO HE PODIDO MIRAR: go.work no enumera ningún módulo." >&2
	exit 2
}

rc=0
for m in "${MODULES[@]}"; do
  echo "==> CGO_ENABLED=0 go build ./...  (${m})"
  if ! ( cd "${m}" && CGO_ENABLED=0 go build ./... ); then
    echo "    PURE-GO BUILD FAILED: ${m} does not build with CGO_ENABLED=0."
    echo "    The static binary cannot link a cgo dependency. A native HSM/PKCS#11 signer"
    echo "    belongs OUT-OF-PROCESS (build tag + sidecar), never inside the core."
    rc=1
  fi
done

if [ "${rc}" -ne 0 ]; then
  echo
  echo "Pure-Go check FAILED: a workspace module needs cgo."
  exit 1
fi
echo "Pure-Go check OK: every workspace module builds with CGO_ENABLED=0 (static binary intact)."
