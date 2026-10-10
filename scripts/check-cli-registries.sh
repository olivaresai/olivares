#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-cli-registries.sh — run the three existing tests for complete CLI registries.
# A visible command must appear in commandGroups, each help group must name an existing
# command, and config_registry.go must register every environment key this package reads.
#
# `cmd_s438_test.go` already had `TestEveryVisibleCommandIsGrouped` (“visible command %q
# has no help group — add it to commandGroups”); `commandgroups_test.go` checked the
# reverse direction. Still, visible `grok-hook` reached main without a group on
# 2026-08-19. Like the wired but unoffered `connectors/grok`, those tests ran only in
# the heavy `go test` for `cmd/olivares`, outside the branch push path.
#
# The same PR added seven unregistered environment keys: six in `cmd_grokhook.go` and
# `OLIVARES_GROK_HOOK_PEP_CONFIG` in `grokhookpepserver.go`. The existing
# `TestConfigRegistryCoversEveryEnvKeyThisPackageReads` already documented a gate that
# “was green while four honored keys were unregistered”. Three registries had
# correct tests that never ran during branch pushes; hence the broader script name.
#
# `go test -run` returns 0 when no test matches. Require each named `--- PASS:` line,
# or renaming a test would silently disable this guard.
# Exit: 0 clean · 1 registry finding · 2 could not check.
set -uo pipefail
LC_ALL=C
export LC_ALL

RAIZ="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$RAIZ/cmd/olivares" 2>/dev/null || {
	echo "check-cli-registries: ⛔ COULD NOT CHECK: missing $RAIZ/cmd/olivares" >&2
	exit 2
}

PRUEBAS="TestEveryVisibleCommandIsGrouped|TestCadaGrupoDeAyudaNombraUnComandoQueExiste|TestConfigRegistryCoversEveryEnvKeyThisPackageReads"
output="$(go test -run "$PRUEBAS" -count=1 -v . 2>&1)"
rc=$?

# ── El control positivo, ANTES de mirar el rc ────────────────────────────────────────────
# Cada prueba, por su nombre. Un `-run` que no casa nada, un paquete que no compila con el filtro
# puesto, o una prueba renombrada salen todos por aquí y NUNCA por 0.
#
# La comprobación va con here-string y no con tubería: bajo `pipefail`, `printf | grep -q` devuelve
# **141 EN ÉXITO** porque `grep -q` cierra su entrada al primer acierto y `printf` recibe SIGPIPE.
# El caso que ACIERTA se leería como fallo. Es la trampa que `lint:sigpipe-booleans` vigila.
missing_inputs=""
for t in TestEveryVisibleCommandIsGrouped TestCadaGrupoDeAyudaNombraUnComandoQueExiste TestConfigRegistryCoversEveryEnvKeyThisPackageReads; do
	grep -qE "^(--- )?(PASS|FAIL): +${t}\b" <<<"$output" || missing_inputs="${missing_inputs} ${t}"
done
if [ -n "$missing_inputs" ]; then
	echo "check-cli-registries: ⛔ COULD NOT CHECK: these tests did not run:${missing_inputs}" >&2
	echo "                      'go test -run' exits 0 when no tests match; that is not a pass." >&2
	echo "                      Were they renamed or moved to another package? Update PRUEBAS here." >&2
	printf '%s\n' "$output" | tail -12 | sed 's/^/  /' >&2
	exit 2
fi

if [ "$rc" -ne 0 ]; then
	echo "check-cli-registries: ⛔ a CLI registry is incomplete:" >&2
	printf '%s\n' "$output" | grep -E "^\s+--- FAIL|_test\.go:|help group|does not exist" | head -12 | sed 's/^/  /' >&2
	echo "                      Help groups: \`commandGroups\` in cmd/olivares/main.go — users cannot" >&2
	echo "                      discover a command that exists but is missing from help." >&2
	echo "                      Environment keys: cmd/olivares/config_registry.go — a key that is read" >&2
	echo "                      but not declared is absent from \`config effective\` and cannot be redacted." >&2
	exit 1
fi

echo "check-cli-registries: OK — CLI registries are complete: help groups and environment keys (3 tests executed)."
