#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Hermetic oracle for CONSOLE-FUNC-01 lot 5.
# Exit 0 = the measured contracts work; 1 = a named contract is broken;
# 2 = the oracle could not inspect its subject.
set -uo pipefail
LC_ALL=C
export LC_ALL

lot5_root="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
cd "$lot5_root" 2>/dev/null || {
	echo "console-func-l5: NO PUDE MIRAR — no puedo entrar en $lot5_root" >&2
	exit 2
}

if [ "${CONSOLE_FUNC_L5_FORCE_UNAVAILABLE:-0}" = "1" ]; then
	echo "console-func-l5: NO PUDE MIRAR — indisponibilidad de control solicitada" >&2
	exit 2
fi

command -v rg >/dev/null 2>&1 || {
	echo "console-func-l5: NO PUDE MIRAR — falta rg" >&2
	exit 2
}
command -v pnpm >/dev/null 2>&1 || {
	echo "console-func-l5: NO PUDE MIRAR — falta pnpm" >&2
	exit 2
}

lot5_broken=0
lot5_require() {
	lot5_name="$1"
	lot5_file="$2"
	lot5_needle="$3"
	if [ ! -r "$lot5_file" ]; then
		echo "console-func-l5: NO PUDE MIRAR — no puedo leer $lot5_file" >&2
		exit 2
	fi
	lot5_count="$(rg -F -c -- "$lot5_needle" "$lot5_file" 2>/dev/null)"
	lot5_rc=$?
	if [ "$lot5_rc" -gt 1 ]; then
		echo "console-func-l5: NO PUDE MIRAR — rg fallo sobre $lot5_file" >&2
		exit 2
	fi
	if [ "${lot5_count:-0}" -lt 1 ]; then
		echo "console-func-l5: ROTO — $lot5_name" >&2
		lot5_broken=1
	else
		echo "console-func-l5: FUNCIONA — $lot5_name"
	fi
}

lot5_require HEALTH_DETAIL_SLA_DESTINATION \
	web/src/features/health/health-view.tsx "setActiveTab('sla')"
lot5_require HEALTH_DETAIL_TIMELINE_DESTINATION \
	web/src/features/health/health-view.tsx "setActiveTab('timeline')"
lot5_require HEALTH_DEPENDENCY_CONTROLS_REACHABLE \
	web/src/features/health/dependency-map.tsx '<Panel position="top-right">'
lot5_require ALERT_ROUTE_VIEWPORT \
	web/src/features/alerting/alerting-view.tsx 'max-h-[calc(100vh-2rem)] max-w-lg overflow-y-auto'
lot5_require ALERT_ROUTE_IMMUTABLE_NAME \
	web/src/features/alerting/alerting-view.tsx 'disabled={isEdit}'
lot5_require BACKUP_MANIFEST_TENANT_OBJECT \
	web/src/features/backups/backup-inspect-sheet.tsx '{tenant.tenant}'
lot5_require BACKUP_MANIFEST_KEY_OBJECT \
	web/src/features/backups/backup-inspect-sheet.tsx '{key.name}'
lot5_require RESTORE_FAILURE_REACHABLE \
	web/src/features/backups/restore-dialog.tsx 'max-h-[calc(100vh-2rem)] max-w-lg overflow-y-auto'
lot5_require RESTORE_FAILURE_WRAPS \
	web/src/features/backups/job-progress.tsx 'break-words text-sm text-danger'

if [ "$lot5_broken" -ne 0 ]; then
	exit 1
fi

lot5_log="$(mktemp "${TMPDIR:-/tmp}/console-func-l5.XXXXXX")" || {
	echo "console-func-l5: NO PUDE MIRAR — mktemp fallo" >&2
	exit 2
}
trap 'rm -f -- "$lot5_log"' EXIT
if pnpm -C web exec vitest run \
	src/features/health/health-view.test.tsx \
	src/features/alerting/alerting-view.test.tsx \
	src/features/backups/backup-inspect.test.tsx >"$lot5_log" 2>&1; then
	echo "console-func-l5: FUNCIONA — FOCUSED_COMPONENT_TESTS"
	exit 0
fi
lot5_rc=$?
echo "console-func-l5: ROTO — FOCUSED_COMPONENT_TESTS (rc=$lot5_rc)" >&2
sed -n '1,160p' "$lot5_log" >&2
exit 1
