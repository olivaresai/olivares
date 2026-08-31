#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic oracle for CONSOLE-FUNC-01 lot 6.
# Exit 0 = the measured contracts work; 1 = a named contract is broken;
# 2 = the oracle could not inspect its subject.
set -uo pipefail
LC_ALL=C
export LC_ALL

lot6_root="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$lot6_root" 2>/dev/null || {
	echo "console-func-l6: NO PUDE MIRAR — no puedo entrar en $lot6_root" >&2
	exit 2
}

if [ "${CONSOLE_FUNC_L6_FORCE_UNAVAILABLE:-0}" = "1" ]; then
	echo "console-func-l6: NO PUDE MIRAR — indisponibilidad de control solicitada" >&2
	exit 2
fi

for lot6_tool in rg pnpm go mktemp; do
	command -v "$lot6_tool" >/dev/null 2>&1 || {
		echo "console-func-l6: NO PUDE MIRAR — falta $lot6_tool" >&2
		exit 2
	}
done

lot6_broken=0
lot6_require() {
	local lot6_name="$1"
	local lot6_file="$2"
	local lot6_needle="$3"
	local lot6_count
	local lot6_rc
	if [ ! -r "$lot6_file" ]; then
		echo "console-func-l6: NO PUDE MIRAR — no puedo leer $lot6_file" >&2
		exit 2
	fi
	lot6_count="$(rg -F -c -- "$lot6_needle" "$lot6_file" 2>/dev/null)"
	lot6_rc=$?
	if [ "$lot6_rc" -gt 1 ]; then
		echo "console-func-l6: NO PUDE MIRAR — rg fallo sobre $lot6_file" >&2
		exit 2
	fi
	if [ "${lot6_count:-0}" -lt 1 ]; then
		echo "console-func-l6: ROTO — $lot6_name" >&2
		lot6_broken=1
	else
		echo "console-func-l6: FUNCIONA — $lot6_name"
	fi
}

lot6_forbid() {
	local lot6_name="$1"
	local lot6_file="$2"
	local lot6_needle="$3"
	local lot6_rc
	if [ ! -r "$lot6_file" ]; then
		echo "console-func-l6: NO PUDE MIRAR — no puedo leer $lot6_file" >&2
		exit 2
	fi
	rg -F -q -- "$lot6_needle" "$lot6_file" 2>/dev/null
	lot6_rc=$?
	if [ "$lot6_rc" -eq 0 ]; then
		echo "console-func-l6: ROTO — $lot6_name" >&2
		lot6_broken=1
	elif [ "$lot6_rc" -gt 1 ]; then
		echo "console-func-l6: NO PUDE MIRAR — rg fallo sobre $lot6_file" >&2
		exit 2
	else
		echo "console-func-l6: FUNCIONA — $lot6_name"
	fi
}

lot6_require DEPLOY_APPLY_TYPED \
	web/src/features/deploy/definition-detail.tsx 'confirmPhrase="APPLY"'
lot6_require PROTOCOL_LOSSES_ARRAY \
	modules/sessions/communication_binding_spec_api.go 'if spec.KnownLosses == nil {'
lot6_require EVENTING_BULK_FRESH_GET \
	web/src/features/eventing/eventing-view.tsx 'const current = await eventingApi.subscription(id)'
lot6_require EVENTING_EDIT_FRESH_GET \
	web/src/features/eventing/eventing-view.tsx 'const current = await eventingApi.subscription(existingId)'
lot6_require DEPLOY_PLAN_ARRAY \
	modules/deploy/lifecycle.go 'if changes == nil {'
lot6_require DEPLOY_VERIFY_ARRAY \
	modules/deploy/lifecycle.go 'if result.Changes == nil {'
lot6_require ORCHESTRATION_DECLARED_NOT_FIRED \
	web/src/features/orchestration/orchestration-view.tsx "'declared_not_fired',"
lot6_require VIEWER_PERMISSION_CONTEXT_ISOLATED \
	web/e2e/console-func-l6.spec.ts 'const viewerContext = await browser.newContext()'
lot6_forbid ORCHESTRATION_NEIGHBORS_UNREACHABLE \
	web/src/features/orchestration/orchestration-view.tsx 'orchestrationApi.neighbors('
lot6_forbid ORCHESTRATION_TIMELINE_UNREACHABLE \
	web/src/features/orchestration/orchestration-view.tsx 'orchestrationApi.timeline('
lot6_forbid ORCHESTRATION_SCHEDULE_DETAIL_UNREACHABLE \
	web/src/features/orchestration/orchestration-view.tsx 'orchestrationApi.schedule('

if [ "$lot6_broken" -ne 0 ]; then
	exit 1
fi

lot6_log="$(mktemp "${TMPDIR:-/tmp}/console-func-l6.XXXXXX")" || {
	echo "console-func-l6: NO PUDE MIRAR — mktemp fallo" >&2
	exit 2
}
trap 'rm -f -- "$lot6_log"' EXIT

if pnpm -C web exec vitest run \
	src/features/protocol-bindings/api.test.ts \
	src/features/protocol-bindings/catalog.test.ts \
	src/features/protocol-bindings/flow.test.tsx \
	src/features/protocol-bindings/model.test.ts \
	src/features/protocol-bindings/protocol-bindings-view.test.tsx \
	src/features/eventing/api.test.ts \
	src/features/eventing/confirm-delete.test.tsx \
	src/features/eventing/egress-policy-fixtures.test.ts \
	src/features/eventing/egress-policy-panel.test.tsx \
	src/features/eventing/eventing-view.test.tsx \
	src/features/eventing/sink-format.test.ts \
	src/features/deploy/deploy.test.tsx \
	src/features/orchestration/api.test.ts \
	src/features/orchestration/decisions-estate.test.tsx \
	src/features/orchestration/orchestration.test.tsx >"$lot6_log" 2>&1; then
	echo "console-func-l6: FUNCIONA — FOCUSED_COMPONENT_TESTS"
else
	lot6_rc=$?
	echo "console-func-l6: ROTO — FOCUSED_COMPONENT_TESTS (rc=$lot6_rc)" >&2
	sed -n '1,180p' "$lot6_log" >&2
	exit 1
fi

if go test ./modules/sessions \
	-run '^TestProtocolBindingSpecAPIDraftActivateDisableAndRead$' -count=1 \
	>>"$lot6_log" 2>&1; then
	echo "console-func-l6: FUNCIONA — PROTOCOL_HANDLER_TEST"
else
	lot6_rc=$?
	echo "console-func-l6: ROTO — PROTOCOL_HANDLER_TEST (rc=$lot6_rc)" >&2
	sed -n '1,180p' "$lot6_log" >&2
	exit 1
fi

if go test ./modules/deploy -run '^TestApplyIsIdempotent$' -count=1 \
	>>"$lot6_log" 2>&1; then
	echo "console-func-l6: FUNCIONA — DEPLOY_HANDLER_TEST"
else
	lot6_rc=$?
	echo "console-func-l6: ROTO — DEPLOY_HANDLER_TEST (rc=$lot6_rc)" >&2
	sed -n '1,180p' "$lot6_log" >&2
	exit 1
fi
