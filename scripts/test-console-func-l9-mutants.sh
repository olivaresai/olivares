#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation self-test for scripts/check-console-func-l9.sh. Every production file
# is restored byte-for-byte after each mutant and again on every exit path.
set -uo pipefail
LC_ALL=C
export LC_ALL

lot9_root="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
lot9_scratch="${TMPDIR:-/tmp}/console-func-l9-mutants.$$"
lot9_files=(
	web/src/features/knowledge/api.ts
	web/src/features/knowledge/knowledge-view.tsx
	web/e2e/console-func-l9.spec.ts
	web/src/features/capabilities/capabilities-view.tsx
	web/src/features/capabilities/server-detail.tsx
	modules/capabilities/toolpins.go
)

mkdir -p "$lot9_scratch" || exit 2
lot9_restore() {
	local lot9_restore_index=0
	local lot9_restore_file
	for lot9_restore_file in "${lot9_files[@]}"; do
		if [ -r "$lot9_scratch/snapshot/$lot9_restore_index" ]; then
			cp -p -- "$lot9_scratch/snapshot/$lot9_restore_index" "$lot9_root/$lot9_restore_file"
		fi
		lot9_restore_index=$((lot9_restore_index + 1))
	done
}
lot9_cleanup() {
	lot9_restore
	rm -rf -- "$lot9_scratch"
}
trap lot9_cleanup EXIT HUP INT TERM

mkdir -p "$lot9_scratch/snapshot" || exit 2
lot9_index=0
for lot9_file in "${lot9_files[@]}"; do
	[ -r "$lot9_root/$lot9_file" ] || {
		echo "console-func-l9-mutants: NO PUDE MIRAR — falta $lot9_file" >&2
		exit 2
	}
	cp -p -- "$lot9_root/$lot9_file" "$lot9_scratch/snapshot/$lot9_index" || exit 2
	lot9_index=$((lot9_index + 1))
done
(
	cd "$lot9_root" || exit 2
	sha256sum "${lot9_files[@]}"
) >"$lot9_scratch/original.sha256" || exit 2

lot9_oracle="$lot9_root/scripts/check-console-func-l9.sh"
TMPDIR="$lot9_scratch" bash "$lot9_oracle" >"$lot9_scratch/control.log" 2>&1 || {
	echo "console-func-l9-mutants: control ROTO" >&2
	sed -n '1,220p' "$lot9_scratch/control.log" >&2
	exit 1
}
echo "console-func-l9-mutants: control FUNCIONA"

lot9_mutant() {
	local lot9_name="$1"
	local lot9_file="$2"
	local lot9_from="$3"
	local lot9_to="$4"
	local lot9_expected="$5"
	local lot9_rc
	local lot9_hits
	lot9_restore
	LOT9_FROM="$lot9_from" LOT9_TO="$lot9_to" \
		perl -0pi -e 's/\Q$ENV{LOT9_FROM}\E/$ENV{LOT9_TO}/g' \
		"$lot9_root/$lot9_file" || exit 2
	TMPDIR="$lot9_scratch" bash "$lot9_oracle" >"$lot9_scratch/$lot9_name.log" 2>&1
	lot9_rc=$?
	lot9_hits="$(rg -F -c -- "console-func-l9: ROTO — $lot9_expected" "$lot9_scratch/$lot9_name.log" 2>/dev/null)"
	if [ "$lot9_rc" -ne 1 ] || [ "${lot9_hits:-0}" -lt 1 ]; then
		echo "console-func-l9-mutants: mutante $lot9_name no discrimino (rc=$lot9_rc)" >&2
		sed -n '1,160p' "$lot9_scratch/$lot9_name.log" >&2
		exit 1
	fi
	echo "console-func-l9-mutants: mutante $lot9_name ROTO — $lot9_expected"
	lot9_restore
	(
		cd "$lot9_root" || exit 2
		sha256sum -c "$lot9_scratch/original.sha256"
	) >"$lot9_scratch/$lot9_name.restore.log" 2>&1 || {
		echo "console-func-l9-mutants: restauracion no byte-exacta tras $lot9_name" >&2
		exit 1
	}
}

lot9_mutant memory-raw-body web/src/features/knowledge/api.ts \
	'rawBody: raw,' 'body: raw,' MEMORY_IMPORT_RAW_BODY
lot9_mutant memory-ndjson web/src/features/knowledge/api.ts \
	"contentType: 'application/x-ndjson'," "contentType: 'application/json'," MEMORY_IMPORT_NDJSON
lot9_mutant memory-export-raw web/src/features/knowledge/knowledge-view.tsx \
	'setPegado(r.raw)' "setPegado('')" MEMORY_EXPORT_PRESERVES_RAW
lot9_mutant memory-import-raw web/src/features/knowledge/knowledge-view.tsx \
	'knowledgeApi.importMemory(pegado)' "knowledgeApi.importMemory('{}')" MEMORY_IMPORT_PRESERVES_RAW
lot9_mutant catalog-mcp-policy web/e2e/console-func-l9.spec.ts \
	"endpoint: '/v1/m/catalog/mcp-admission/policy'," \
	"endpoint: '/v1/m/catalog/admission/policy'," CATALOG_MCP_POLICY_LIVE
lot9_mutant catalog-connector-policy web/e2e/console-func-l9.spec.ts \
	"endpoint: '/v1/m/catalog/connector-admission/policy'," \
	"endpoint: '/v1/m/catalog/admission/policy'," CATALOG_CONNECTOR_POLICY_LIVE
lot9_mutant capabilities-wiring-refetch \
	web/src/features/capabilities/capabilities-view.tsx \
	'onElevated={() => void wiring.refetch()}' \
	'onElevated={() => undefined}' CAPABILITIES_WIRING_REENTRY
lot9_mutant capabilities-server-refetch \
	web/src/features/capabilities/server-detail.tsx \
	'onElevated={() => void query.refetch()}' \
	'onElevated={() => undefined}' CAPABILITIES_SERVER_REENTRY
lot9_mutant toolpins-deny-closed modules/capabilities/toolpins.go \
	'tool pinning is an enterprise add-on (no verifier wired)' \
	'tool pinning unavailable' TOOLPINS_COMMUNITY_DENY_CLOSED

lot9_restore
CONSOLE_FUNC_L9_FORCE_UNAVAILABLE=1 TMPDIR="$lot9_scratch" \
	bash "$lot9_oracle" >"$lot9_scratch/unavailable.log" 2>&1
lot9_rc=$?
lot9_hits="$(rg -F -c -- 'console-func-l9: NO PUDE MIRAR' "$lot9_scratch/unavailable.log" 2>/dev/null)"
if [ "$lot9_rc" -ne 2 ] || [ "${lot9_hits:-0}" -lt 1 ]; then
	echo "console-func-l9-mutants: la tercera respuesta no discrimino (rc=$lot9_rc)" >&2
	exit 1
fi
echo "console-func-l9-mutants: control NO PUDE MIRAR = rc 2"

TMPDIR="$lot9_scratch" bash "$lot9_oracle" >"$lot9_scratch/final.log" 2>&1 || {
	echo "console-func-l9-mutants: final ROTO" >&2
	exit 1
}
(
	cd "$lot9_root" || exit 2
	sha256sum "${lot9_files[@]}"
) >"$lot9_scratch/final.sha256" || exit 2
cmp -s "$lot9_scratch/original.sha256" "$lot9_scratch/final.sha256" || {
	echo "console-func-l9-mutants: manifest original != final" >&2
	exit 1
}
echo "console-func-l9-mutants: manifest original=snapshot=final; 9/9 mutantes discriminados"
