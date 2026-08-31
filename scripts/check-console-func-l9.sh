#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic oracle for CONSOLE-FUNC-01 lot 9.
# Exit 0 = the measured contracts work; 1 = a named contract is broken;
# 2 = the oracle could not inspect its subject.
set -uo pipefail
LC_ALL=C
export LC_ALL

lot9_root="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$lot9_root" 2>/dev/null || {
	echo "console-func-l9: NO PUDE MIRAR — no puedo entrar en $lot9_root" >&2
	exit 2
}

if [ "${CONSOLE_FUNC_L9_FORCE_UNAVAILABLE:-0}" = "1" ]; then
	echo "console-func-l9: NO PUDE MIRAR — indisponibilidad de control solicitada" >&2
	exit 2
fi

for lot9_tool in rg pnpm go mktemp; do
	command -v "$lot9_tool" >/dev/null 2>&1 || {
		echo "console-func-l9: NO PUDE MIRAR — falta $lot9_tool" >&2
		exit 2
	}
done

lot9_broken=0
lot9_require() {
	local lot9_name="$1"
	local lot9_file="$2"
	local lot9_needle="$3"
	local lot9_count
	local lot9_rc
	if [ ! -r "$lot9_file" ]; then
		echo "console-func-l9: NO PUDE MIRAR — no puedo leer $lot9_file" >&2
		exit 2
	fi
	lot9_count="$(rg -F -c -- "$lot9_needle" "$lot9_file" 2>/dev/null)"
	lot9_rc=$?
	if [ "$lot9_rc" -gt 1 ]; then
		echo "console-func-l9: NO PUDE MIRAR — rg fallo sobre $lot9_file" >&2
		exit 2
	fi
	if [ "${lot9_count:-0}" -lt 1 ]; then
		echo "console-func-l9: ROTO — $lot9_name" >&2
		lot9_broken=1
	else
		echo "console-func-l9: FUNCIONA — $lot9_name"
	fi
}

lot9_forbid() {
	local lot9_name="$1"
	local lot9_file="$2"
	local lot9_needle="$3"
	local lot9_rc
	if [ ! -r "$lot9_file" ]; then
		echo "console-func-l9: NO PUDE MIRAR — no puedo leer $lot9_file" >&2
		exit 2
	fi
	rg -F -q -- "$lot9_needle" "$lot9_file" 2>/dev/null
	lot9_rc=$?
	if [ "$lot9_rc" -eq 0 ]; then
		echo "console-func-l9: ROTO — $lot9_name" >&2
		lot9_broken=1
	elif [ "$lot9_rc" -gt 1 ]; then
		echo "console-func-l9: NO PUDE MIRAR — rg fallo sobre $lot9_file" >&2
		exit 2
	else
		echo "console-func-l9: FUNCIONA — $lot9_name"
	fi
}

lot9_require MEMORY_IMPORT_RAW_BODY \
	web/src/features/knowledge/api.ts 'rawBody: raw,'
lot9_require MEMORY_IMPORT_NDJSON \
	web/src/features/knowledge/api.ts "contentType: 'application/x-ndjson',"
lot9_require MEMORY_EXPORT_PRESERVES_RAW \
	web/src/features/knowledge/knowledge-view.tsx 'setPegado(r.raw)'
lot9_require MEMORY_IMPORT_PRESERVES_RAW \
	web/src/features/knowledge/knowledge-view.tsx 'knowledgeApi.importMemory(pegado)'
lot9_forbid MEMORY_IMPORT_REPARSES_BUNDLE \
	web/src/features/knowledge/knowledge-view.tsx 'JSON.parse(pegado'

lot9_require CATALOG_MCP_POLICY_LIVE \
	web/e2e/console-func-l9.spec.ts "endpoint: '/v1/m/catalog/mcp-admission/policy',"
lot9_require CATALOG_CONNECTOR_POLICY_LIVE \
	web/e2e/console-func-l9.spec.ts "endpoint: '/v1/m/catalog/connector-admission/policy',"
lot9_forbid E2E_HAS_NO_ROUTE_INTERCEPTION \
	web/e2e/console-func-l9.spec.ts 'page.route('

lot9_require CAPABILITIES_WIRING_REENTRY \
	web/src/features/capabilities/capabilities-view.tsx \
	'onElevated={() => void wiring.refetch()}'
lot9_require CAPABILITIES_SERVER_REENTRY \
	web/src/features/capabilities/server-detail.tsx \
	'onElevated={() => void query.refetch()}'
lot9_require CAPABILITIES_REVISIONS_REENTRY \
	web/src/features/capabilities/revisions.tsx \
	'onElevated={() => void query.refetch()}'
lot9_require CAPABILITIES_TOOLPINS_REENTRY \
	web/src/features/capabilities/tool-pins.tsx \
	'onElevated={() => void query.refetch()}'
lot9_require TOOLPINS_COMMUNITY_DENY_CLOSED \
	modules/capabilities/toolpins.go \
	'tool pinning is an enterprise add-on (no verifier wired)'

lot9_elevation_count=0
for lot9_file in \
	web/src/features/capabilities/capabilities-view.tsx \
	web/src/features/capabilities/server-detail.tsx \
	web/src/features/capabilities/revisions.tsx \
	web/src/features/capabilities/tool-pins.tsx \
	web/src/features/capabilities/config-editor.tsx \
	web/src/features/capabilities/wiring-graph.tsx; do
	lot9_count="$(rg -F -c -- 'onElevated=' "$lot9_file" 2>/dev/null)"
	lot9_rc=$?
	if [ "$lot9_rc" -gt 1 ]; then
		echo "console-func-l9: NO PUDE MIRAR — rg fallo sobre $lot9_file" >&2
		exit 2
	fi
	lot9_elevation_count=$((lot9_elevation_count + ${lot9_count:-0}))
done
if [ "$lot9_elevation_count" -ne 4 ]; then
	echo "console-func-l9: ROTO — CAPABILITIES_EXACTLY_FOUR_REENTRY_SITES ($lot9_elevation_count)" >&2
	lot9_broken=1
else
	echo "console-func-l9: FUNCIONA — CAPABILITIES_EXACTLY_FOUR_REENTRY_SITES"
fi

if [ "$lot9_broken" -ne 0 ]; then
	exit 1
fi

lot9_log="$(mktemp "${TMPDIR:-/tmp}/console-func-l9.XXXXXX")" || {
	echo "console-func-l9: NO PUDE MIRAR — mktemp fallo" >&2
	exit 2
}
trap 'rm -f -- "$lot9_log"' EXIT

if pnpm -C web exec vitest run \
	src/features/knowledge/dlp-ceiling.test.ts \
	src/features/knowledge/dlp-view.test.tsx \
	src/features/knowledge/governance-contract.test.ts \
	src/features/knowledge/integrity-view.test.tsx \
	src/features/knowledge/knowledge.test.tsx \
	src/features/knowledge/lineage-step-up.test.tsx \
	src/features/knowledge/portability-view.test.tsx \
	src/features/knowledge/scans-view.test.tsx \
	src/features/knowledge/step-up-policy.test.tsx \
	src/features/catalog/admission-panel.test.tsx \
	src/features/catalog/admission-policy.test.tsx \
	src/features/catalog/api.test.ts \
	src/features/catalog/catalog.test.tsx \
	src/features/capabilities/capabilities.test.tsx \
	src/features/capabilities/step-up-detail-sheets.test.ts \
	src/features/capabilities/step-up-refetch.test.tsx \
	src/features/capabilities/tool-pins-step-up.test.tsx \
	src/features/capabilities/toolpin-wire.contract.test.ts >"$lot9_log" 2>&1; then
	echo "console-func-l9: FUNCIONA — FOCUSED_COMPONENT_TESTS"
else
	lot9_rc=$?
	echo "console-func-l9: ROTO — FOCUSED_COMPONENT_TESTS (rc=$lot9_rc)" >&2
	sed -n '1,220p' "$lot9_log" >&2
	exit 1
fi

if go test ./modules/knowledge \
	-run '^(TestMemoryPortability_RoundTripPreservesClassificationAndScope|TestDLPRuleCRUDAndValidation|TestDataProductCRUDLifecycleAndContracts)$' \
	-count=1 >>"$lot9_log" 2>&1; then
	echo "console-func-l9: FUNCIONA — KNOWLEDGE_HANDLER_TESTS"
else
	lot9_rc=$?
	echo "console-func-l9: ROTO — KNOWLEDGE_HANDLER_TESTS (rc=$lot9_rc)" >&2
	sed -n '1,220p' "$lot9_log" >&2
	exit 1
fi

if go test ./modules/catalog \
	-run '^(TestEntryApproveSignVerify|TestInstantiateGoverned)$' -count=1 \
	>>"$lot9_log" 2>&1; then
	echo "console-func-l9: FUNCIONA — CATALOG_HANDLER_TESTS"
else
	lot9_rc=$?
	echo "console-func-l9: ROTO — CATALOG_HANDLER_TESTS (rc=$lot9_rc)" >&2
	sed -n '1,220p' "$lot9_log" >&2
	exit 1
fi

if go test ./modules/capabilities \
	-run '^(TestConfigLifecycleAndVersioning|TestToolPinsRoutesAreEnterprisePending)$' \
	-count=1 >>"$lot9_log" 2>&1; then
	echo "console-func-l9: FUNCIONA — CAPABILITIES_HANDLER_TESTS"
else
	lot9_rc=$?
	echo "console-func-l9: ROTO — CAPABILITIES_HANDLER_TESTS (rc=$lot9_rc)" >&2
	sed -n '1,220p' "$lot9_log" >&2
	exit 1
fi
