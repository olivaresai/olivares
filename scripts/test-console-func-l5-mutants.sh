#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation self-test for scripts/check-console-func-l5.sh. Every production file
# is restored byte-for-byte after each mutant and again on every exit path.
set -uo pipefail
LC_ALL=C
export LC_ALL

lot5_root="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
lot5_scratch="${TMPDIR:-/tmp}/console-func-l5-mutants.$$"
lot5_files=(
	web/src/features/health/health-view.tsx
	web/src/features/health/dependency-map.tsx
	web/src/features/alerting/alerting-view.tsx
	web/src/features/backups/backup-inspect-sheet.tsx
	web/src/features/backups/restore-dialog.tsx
	web/src/features/backups/job-progress.tsx
)

mkdir -p "$lot5_scratch" || exit 2
lot5_restore() {
	local lot5_restore_index=0
	local lot5_restore_file
	for lot5_restore_file in "${lot5_files[@]}"; do
		if [ -r "$lot5_scratch/snapshot/$lot5_restore_index" ]; then
			cp -p -- "$lot5_scratch/snapshot/$lot5_restore_index" "$lot5_root/$lot5_restore_file"
		fi
		lot5_restore_index=$((lot5_restore_index + 1))
	done
}
lot5_cleanup() {
	lot5_restore
	rm -rf -- "$lot5_scratch"
}
trap lot5_cleanup EXIT HUP INT TERM

mkdir -p "$lot5_scratch/snapshot" || exit 2
lot5_index=0
for lot5_file in "${lot5_files[@]}"; do
	[ -r "$lot5_root/$lot5_file" ] || {
		echo "console-func-l5-mutants: NO PUDE MIRAR — falta $lot5_file" >&2
		exit 2
	}
	cp -p -- "$lot5_root/$lot5_file" "$lot5_scratch/snapshot/$lot5_index" || exit 2
	lot5_index=$((lot5_index + 1))
done
(
	cd "$lot5_root" || exit 2
	sha256sum "${lot5_files[@]}"
) >"$lot5_scratch/original.sha256" || exit 2

lot5_oracle="$lot5_root/scripts/check-console-func-l5.sh"
TMPDIR="$lot5_scratch" bash "$lot5_oracle" >"$lot5_scratch/control.log" 2>&1 || {
	echo "console-func-l5-mutants: control ROTO" >&2
	sed -n '1,160p' "$lot5_scratch/control.log" >&2
	exit 1
}
echo "console-func-l5-mutants: control FUNCIONA"

lot5_mutant() {
	local lot5_name="$1"
	local lot5_file="$2"
	local lot5_from="$3"
	local lot5_to="$4"
	local lot5_expected="$5"
	local lot5_rc
	local lot5_hits
	lot5_restore
	LOT5_FROM="$lot5_from" LOT5_TO="$lot5_to" \
		perl -0pi -e 's/\Q$ENV{LOT5_FROM}\E/$ENV{LOT5_TO}/g' \
		"$lot5_root/$lot5_file" || exit 2
	TMPDIR="$lot5_scratch" bash "$lot5_oracle" >"$lot5_scratch/$lot5_name.log" 2>&1
	lot5_rc=$?
	lot5_hits="$(rg -F -c -- "console-func-l5: ROTO — $lot5_expected" "$lot5_scratch/$lot5_name.log" 2>/dev/null)"
	if [ "$lot5_rc" -ne 1 ] || [ "${lot5_hits:-0}" -lt 1 ]; then
		echo "console-func-l5-mutants: mutante $lot5_name no discrimino (rc=$lot5_rc)" >&2
		sed -n '1,120p' "$lot5_scratch/$lot5_name.log" >&2
		exit 1
	fi
	echo "console-func-l5-mutants: mutante $lot5_name ROTO — $lot5_expected"
	lot5_restore
	(
		cd "$lot5_root" || exit 2
		sha256sum -c "$lot5_scratch/original.sha256"
	) >"$lot5_scratch/$lot5_name.restore.log" 2>&1 || {
		echo "console-func-l5-mutants: restauracion no byte-exacta tras $lot5_name" >&2
		exit 1
	}
}

lot5_mutant health-sla web/src/features/health/health-view.tsx \
	"setActiveTab('sla')" "setActiveTab('status')" HEALTH_DETAIL_SLA_DESTINATION
lot5_mutant health-dependency-controls web/src/features/health/dependency-map.tsx \
	'<Panel position="top-right">' '<Panel position="bottom-left">' HEALTH_DEPENDENCY_CONTROLS_REACHABLE
lot5_mutant alert-viewport web/src/features/alerting/alerting-view.tsx \
	'max-h-[calc(100vh-2rem)] max-w-lg overflow-y-auto' 'max-w-lg' ALERT_ROUTE_VIEWPORT
lot5_mutant alert-name web/src/features/alerting/alerting-view.tsx \
	'disabled={isEdit}' 'disabled={false}' ALERT_ROUTE_IMMUTABLE_NAME
lot5_mutant backup-manifest web/src/features/backups/backup-inspect-sheet.tsx \
	'{tenant.tenant}' '{tenant}' BACKUP_MANIFEST_TENANT_OBJECT
lot5_mutant restore-viewport web/src/features/backups/restore-dialog.tsx \
	'max-h-[calc(100vh-2rem)] max-w-lg overflow-y-auto' 'max-w-lg' RESTORE_FAILURE_REACHABLE

lot5_restore
CONSOLE_FUNC_L5_FORCE_UNAVAILABLE=1 TMPDIR="$lot5_scratch" \
	bash "$lot5_oracle" >"$lot5_scratch/unavailable.log" 2>&1
lot5_rc=$?
lot5_hits="$(rg -F -c -- 'console-func-l5: NO PUDE MIRAR' "$lot5_scratch/unavailable.log" 2>/dev/null)"
if [ "$lot5_rc" -ne 2 ] || [ "${lot5_hits:-0}" -lt 1 ]; then
	echo "console-func-l5-mutants: la tercera respuesta no discrimino (rc=$lot5_rc)" >&2
	exit 1
fi
echo "console-func-l5-mutants: control NO PUDE MIRAR = rc 2"

TMPDIR="$lot5_scratch" bash "$lot5_oracle" >"$lot5_scratch/final.log" 2>&1 || {
	echo "console-func-l5-mutants: final ROTO" >&2
	exit 1
}
(
	cd "$lot5_root" || exit 2
	sha256sum "${lot5_files[@]}"
) >"$lot5_scratch/final.sha256" || exit 2
cmp -s "$lot5_scratch/original.sha256" "$lot5_scratch/final.sha256" || {
	echo "console-func-l5-mutants: manifest original != final" >&2
	exit 1
}
echo "console-func-l5-mutants: manifest original=snapshot=final; 6/6 mutantes discriminados"
