#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-openapi-op-descriptions.sh — build and run the published-operation description gate.
#
# THE REGRESSION IT FORBIDS. Measured 2026-08-16: web/openapi/openapi.json publishes 71
# operations and web/openapi/openapi.beta.json publishes 686, and NOT ONE of the 757
# carried a description. Every one had a summary; for the beta document that summary is
# the registration restated ("finops module route (requires finops:spend:read)"), so an
# integrator reading the published contract learned which permission to grant and
# nothing about what the call does. Nothing was red.
#
# WHY IT IS NOT A TABLE SOMEBODY MAINTAINS. The beta document is a REFLECTOR: it is
# built from the routes the modules register, so a module that adds a route adds it to
# the contract for free (core/api/openapi_modules.go). A hand-kept description table
# would be silently short by one the day a module registers route 687. So the
# description is read from the prose that already lives beside the route — the
# handler's Go doc comment — and the roster is enumerated from the same registrations
# the document is built from.
#
# The gate itself is Go (scripts/openapi-op-descriptions) because the question is "which
# function does this route mount, and what does its doc comment say": an AST question,
# and an AST is not a grep. Everything it enforces, and why, is in that package's doc
# comment.
#
# This wrapper exists for the reason scripts/check-config-env-docs.sh has one: the
# helper is a standalone module built with GOWORK=off so a broken module elsewhere in
# the workspace cannot stop this gate from looking, and /tmp is NOEXEC in the dev
# container — a binary built there cannot be run, and the failure reads as a mysterious
# "permission denied" rather than as a mount option. TMPDIR is honoured and named in
# the error.
#
#   scripts/check-openapi-op-descriptions.sh              check the published documents against the code
#   scripts/check-openapi-op-descriptions.sh --write      regenerate core/api/openapi_op_descriptions.gen.go
#   scripts/check-openapi-op-descriptions.sh --missing    list the operations that still need a catalog row
#   scripts/check-openapi-op-descriptions.sh --list       print the composed roster (key, source, description);
#                                                        exits 1, not 0, when any operation has none
#   scripts/check-openapi-op-descriptions.sh --self-test  build throwaway trees and prove it can fail
#
# THREE ANSWERS: 0 clean / 1 the documents and the code disagree, every operation
# printed / 2 CANNOT LOOK. Never two: "I could not enumerate" is not "in sync".
#
# After --write, the published documents still have to be refreshed — the table feeds
# them, it is not them:
#
#   task openapi:dump && pnpm --dir web run codegen
set -uo pipefail
export LC_ALL=C

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
SRC="$ROOT/scripts/openapi-op-descriptions"

if [ ! -d "$SRC" ]; then
	echo "check-openapi-op-descriptions: CANNOT LOOK — the gate's own source is missing at $SRC." >&2
	exit 2
fi

if ! command -v go >/dev/null 2>&1; then
	echo "check-openapi-op-descriptions: CANNOT LOOK — no Go toolchain on PATH, so the gate was not built." >&2
	echo "  A gate that could not run is not a gate that passed." >&2
	exit 2
fi

# ⛔ AQUI DECIA «The caller points TMPDIR at a repo-local directory precisely because /tmp is
#    noexec here», Y NO HABIA LLAMADOR. Este guion no lo invocaba NINGUNA tarea del Taskfile ni
#    ninguna pata del hook: estaba escrito, commiteado y muerto. Un requisito que vive en un
#    comentario es una suposicion sobre un llamador que puede no existir — y aqui no existia, asi
#    que el gate NO PODIA PASAR NUNCA en el contenedor de desarrollo: corrido a mano daba
#    `exit 2 … the shell could not execute it (exit 126)`.
#
# ⛔ Y LA TAREA TAMPOCO DEBE IMPONER EL SCRATCH, que era el arreglo comodo. Lo fija la doctrina
#    que este repositorio ya escribio en `Taskfile.yml` (test:cli-walk): fijar TMPDIR desde la
#    tarea «rechaza en un checkout de CI read-only, y ANULA un TMPDIR bueno que el runner pudiera
#    aportar». Asi que el scratch SE ELIGE, no se recibe: se prueba $TMPDIR, luego un directorio
#    repo-local ignorado, luego /var/tmp, y gana el primero que sea escribible Y EJECUTABLE —
#    que es lo que este gate necesita, porque compila un binario y lo ejecuta.
elige_scratch() {
	local base d
	for base in "${TMPDIR:-}" "$ROOT/.openapi-op-tmp" /var/tmp; do
		[ -n "$base" ] || continue
		mkdir -p "$base" 2>/dev/null || continue
		d="$(TMPDIR="$base" mktemp -d 2>/dev/null)" || continue
		printf '#!/bin/sh\nexit 0\n' > "$d/.probe" 2>/dev/null
		chmod +x "$d/.probe" 2>/dev/null
		if "$d/.probe" >/dev/null 2>&1; then rm -f "$d/.probe"; printf '%s' "$d"; return 0; fi
		rm -rf "$d" 2>/dev/null
	done
	return 1
}
BIN_DIR="$(elige_scratch)" || {
	echo "check-openapi-op-descriptions: CANNOT LOOK — ningun scratch dir sirve: hace falta uno" >&2
	echo "  escribible Y EJECUTABLE porque este gate compila un binario y lo corre." >&2
	echo "  Probados: TMPDIR=${TMPDIR:-sin fijar}, \$ROOT/.openapi-op-tmp, /var/tmp." >&2
	exit 2
}
trap 'rm -rf "$BIN_DIR"' EXIT
BIN="$BIN_DIR/openapi-op-descriptions"

build_err="$(cd "$SRC" && GOWORK=off go build -o "$BIN" . 2>&1)" || {
	echo "check-openapi-op-descriptions: CANNOT LOOK — the gate did not build." >&2
	printf '%s\n' "$build_err" | sed 's/^/    /' >&2
	exit 2
}

# Run it ONCE and read the shell's own verdict. 126/127 mean the shell could not execute
# the file at all — which in this container means /tmp is mounted noexec, and the bare
# message for that is "permission denied" on a file whose exec bit is set.
case "${1:-}" in
--self-test) "$BIN" --self-test ;;
--write) "$BIN" -root "$ROOT" -write ;;
--missing) "$BIN" -root "$ROOT" -missing ;;
--list) "$BIN" -root "$ROOT" -list ;;
"") "$BIN" -root "$ROOT" ;;
*)
	echo "check-openapi-op-descriptions: CANNOT LOOK — unknown argument '$1' (want --write, --missing, --list, --self-test or nothing)." >&2
	exit 2
	;;
esac
rc=$?

if [ "$rc" -eq 126 ] || [ "$rc" -eq 127 ]; then
	echo "check-openapi-op-descriptions: CANNOT LOOK — built the gate under TMPDIR=${TMPDIR:-/tmp} but the" >&2
	echo "  shell could not execute it (exit $rc). /tmp is mounted noexec in this container; set" >&2
	echo "  TMPDIR=/workspace/.olivares-tmptest and run again." >&2
	exit 2
fi
exit "$rc"
