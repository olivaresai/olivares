#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Mutation self-test for scripts/check-console-func-l6.sh. Every production file
# is restored byte-for-byte after each mutant and again on every exit path.
set -uo pipefail
LC_ALL=C
export LC_ALL

lot6_root="${OLIVARES_CLONE:-$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd -P)}"
lot6_scratch="${TMPDIR:-/tmp}/console-func-l6-mutants.$$"
lot6_files=(
	web/src/features/deploy/definition-detail.tsx
	modules/sessions/communication_binding_spec_api.go
	web/src/features/eventing/eventing-view.tsx
	modules/deploy/lifecycle.go
	web/src/features/orchestration/orchestration-view.tsx
)

mkdir -p "$lot6_scratch" || exit 2
lot6_restore() {
	local lot6_restore_index=0
	local lot6_restore_file
	for lot6_restore_file in "${lot6_files[@]}"; do
		if [ -r "$lot6_scratch/snapshot/$lot6_restore_index" ]; then
			cp -p -- "$lot6_scratch/snapshot/$lot6_restore_index" "$lot6_root/$lot6_restore_file"
		fi
		lot6_restore_index=$((lot6_restore_index + 1))
	done
}
lot6_cleanup() {
	lot6_restore
	rm -rf -- "$lot6_scratch"
}
trap lot6_cleanup EXIT HUP INT TERM

mkdir -p "$lot6_scratch/snapshot" || exit 2
lot6_index=0
for lot6_file in "${lot6_files[@]}"; do
	[ -r "$lot6_root/$lot6_file" ] || {
		echo "console-func-l6-mutants: NO PUDE MIRAR — falta $lot6_file" >&2
		exit 2
	}
	cp -p -- "$lot6_root/$lot6_file" "$lot6_scratch/snapshot/$lot6_index" || exit 2
	lot6_index=$((lot6_index + 1))
done
(
	cd "$lot6_root" || exit 2
	sha256sum "${lot6_files[@]}"
) >"$lot6_scratch/original.sha256" || exit 2

lot6_oracle="$lot6_root/scripts/check-console-func-l6.sh"
TMPDIR="$lot6_scratch" bash "$lot6_oracle" >"$lot6_scratch/control.log" 2>&1 || {
	echo "console-func-l6-mutants: control ROTO" >&2
	sed -n '1,160p' "$lot6_scratch/control.log" >&2
	exit 1
}
echo "console-func-l6-mutants: control FUNCIONA"

lot6_mutant() {
	local lot6_name="$1"
	local lot6_file="$2"
	local lot6_from="$3"
	local lot6_to="$4"
	local lot6_expected="$5"
	local lot6_rc
	local lot6_hits
	lot6_restore
	LOT6_FROM="$lot6_from" LOT6_TO="$lot6_to" \
		perl -0pi -e 's/\Q$ENV{LOT6_FROM}\E/$ENV{LOT6_TO}/g' \
		"$lot6_root/$lot6_file" || exit 2
	TMPDIR="$lot6_scratch" bash "$lot6_oracle" >"$lot6_scratch/$lot6_name.log" 2>&1
	lot6_rc=$?
	lot6_hits="$(rg -F -c -- "console-func-l6: ROTO — $lot6_expected" "$lot6_scratch/$lot6_name.log" 2>/dev/null)"
	if [ "$lot6_rc" -ne 1 ] || [ "${lot6_hits:-0}" -lt 1 ]; then
		echo "console-func-l6-mutants: mutante $lot6_name no discrimino (rc=$lot6_rc)" >&2
		sed -n '1,120p' "$lot6_scratch/$lot6_name.log" >&2
		exit 1
	fi
	echo "console-func-l6-mutants: mutante $lot6_name ROTO — $lot6_expected"
	lot6_restore
	(
		cd "$lot6_root" || exit 2
		sha256sum -c "$lot6_scratch/original.sha256"
	) >"$lot6_scratch/$lot6_name.restore.log" 2>&1 || {
		echo "console-func-l6-mutants: restauracion no byte-exacta tras $lot6_name" >&2
		exit 1
	}
}

lot6_mutant deploy-apply-phrase web/src/features/deploy/definition-detail.tsx \
	'confirmPhrase="APPLY"' 'confirmPhrase="DEPLOY"' DEPLOY_APPLY_TYPED
lot6_mutant protocol-loss-array modules/sessions/communication_binding_spec_api.go \
	'if spec.KnownLosses == nil {' 'if false {' PROTOCOL_LOSSES_ARRAY
lot6_mutant eventing-bulk-fresh web/src/features/eventing/eventing-view.tsx \
	'const current = await eventingApi.subscription(id)' 'const current = subsQ.data!.items[0]!' EVENTING_BULK_FRESH_GET
lot6_mutant eventing-edit-fresh web/src/features/eventing/eventing-view.tsx \
	'const current = await eventingApi.subscription(existingId)' 'const current = existing!' EVENTING_EDIT_FRESH_GET
lot6_mutant deploy-plan-array modules/deploy/lifecycle.go \
	'if changes == nil {' 'if false {' DEPLOY_PLAN_ARRAY
lot6_mutant deploy-verify-array modules/deploy/lifecycle.go \
	'if result.Changes == nil {' 'if false {' DEPLOY_VERIFY_ARRAY
lot6_mutant orchestration-fire-outcome web/src/features/orchestration/orchestration-view.tsx \
	"'declared_not_fired'," "'dispatched'," ORCHESTRATION_DECLARED_NOT_FIRED

lot6_restore
CONSOLE_FUNC_L6_FORCE_UNAVAILABLE=1 TMPDIR="$lot6_scratch" \
	bash "$lot6_oracle" >"$lot6_scratch/unavailable.log" 2>&1
lot6_rc=$?
lot6_hits="$(rg -F -c -- 'console-func-l6: NO PUDE MIRAR' "$lot6_scratch/unavailable.log" 2>/dev/null)"
if [ "$lot6_rc" -ne 2 ] || [ "${lot6_hits:-0}" -lt 1 ]; then
	echo "console-func-l6-mutants: la tercera respuesta no discrimino (rc=$lot6_rc)" >&2
	exit 1
fi
echo "console-func-l6-mutants: control NO PUDE MIRAR = rc 2"

TMPDIR="$lot6_scratch" bash "$lot6_oracle" >"$lot6_scratch/final.log" 2>&1 || {
	echo "console-func-l6-mutants: final ROTO" >&2
	exit 1
}
(
	cd "$lot6_root" || exit 2
	sha256sum "${lot6_files[@]}"
) >"$lot6_scratch/final.sha256" || exit 2
cmp -s "$lot6_scratch/original.sha256" "$lot6_scratch/final.sha256" || {
	echo "console-func-l6-mutants: manifest original != final" >&2
	exit 1
}
echo "console-func-l6-mutants: manifest original=snapshot=final; 7/7 mutantes discriminados"
