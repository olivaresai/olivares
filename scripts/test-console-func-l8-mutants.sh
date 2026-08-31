#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Causal mutation battery for check-console-func-l8.sh. Each mutant changes one
# production contract, must be classified ROTO, and is restored byte-for-byte.
set -u -o pipefail

NAME='test-console-func-l8-mutants'
cannot() {
	printf '%s: NO PUDE MIRAR — %s\n' "$NAME" "$*" >&2
	exit 2
}

_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || cannot "no puedo cargar $_olivares_git_env (aislamiento git-env)"
unset _olivares_git_env

for command_name in git bash perl mktemp cp cmp tail tr; do
	command -v "$command_name" >/dev/null 2>&1 || cannot "$command_name no está disponible"
done

ROOT=$(git rev-parse --show-toplevel 2>/dev/null) || cannot 'no resuelvo la raíz del repositorio'
ORACLE="$ROOT/scripts/check-console-func-l8.sh"
API_GO="$ROOT/modules/inferenceproxy/api.go"
DEVICE_GO="$ROOT/modules/inferenceproxy/devicegrant.go"
INFERENCE="$ROOT/web/src/features/inference-proxy/inference-proxy-view.tsx"
GPAI="$ROOT/web/src/features/models/gpai.tsx"
MODELS="$ROOT/web/src/features/models/models-view.tsx"
SANDBOX="$ROOT/web/src/features/sandbox/sandbox-view.tsx"
EVALS="$ROOT/web/src/features/evals/evals-view.tsx"
DOCUMENTS="$ROOT/web/src/features/model-ops/documents.tsx"
ADMISSION="$ROOT/web/src/features/model-ops/admission.tsx"
FILES=("$API_GO" "$DEVICE_GO" "$INFERENCE" "$GPAI" "$MODELS" "$SANDBOX" "$EVALS" "$DOCUMENTS" "$ADMISSION")

for required in "$ORACLE" "${FILES[@]}"; do
	[ -r "$required" ] || cannot "no leo $required"
done

TMP=$(mktemp -d "${TMPDIR:-/tmp}/console-func-l8-mutants.XXXXXX") || cannot 'mktemp falló'
[ -d "$TMP" ] || cannot 'mktemp no devolvió un directorio'

snapshot_name() {
	printf '%s' "$1" | tr '/.' '__'
}
restore_all() {
	local file snapshot
	for file in "${FILES[@]}"; do
		snapshot="$TMP/$(snapshot_name "$file")"
		cp "$snapshot" "$file" || return 1
	done
}
assert_restored() {
	local file snapshot
	for file in "${FILES[@]}"; do
		snapshot="$TMP/$(snapshot_name "$file")"
		cmp -s "$snapshot" "$file" || return 1
	done
}
cleanup_on_exit() {
	local original_rc=$?
	trap - EXIT HUP INT TERM
	if ! restore_all || ! assert_restored; then
		printf '%s: NO PUDE MIRAR — cleanup no restauró bytes exactos; temporal %s\n' \
			"$NAME" "$TMP" >&2
		exit 2
	fi
	if ! rm -rf -- "$TMP"; then
		printf '%s: NO PUDE MIRAR — no retiro el temporal restaurado %s\n' "$NAME" "$TMP" >&2
		exit 2
	fi
	exit "$original_rc"
}
trap cleanup_on_exit EXIT
trap 'exit 2' HUP INT TERM

for file in "${FILES[@]}"; do
	cp "$file" "$TMP/$(snapshot_name "$file")" || cannot 'no creo el snapshot completo'
done

replace_once() {
	local file=$1 old=$2 new=$3
	OLD=$old NEW=$new perl -0pi -e '
		$count = s/\Q$ENV{OLD}\E/$ENV{NEW}/g;
		END { exit((defined($count) && $count == 1) ? 0 : 3) }
	' "$file"
}

run_oracle() {
	local log=$1
	TMPDIR="${TMPDIR:-/tmp}" NO_COLOR=1 bash "$ORACLE" >"$log" 2>&1
}

run_mutant() {
	local label=$1 file=$2 old=$3 new=$4 rc actual
	restore_all || cannot "no restauro antes del mutante $label"
	if ! replace_once "$file" "$old" "$new"; then
		cannot "el mutante $label no aplicó exactamente una vez"
	fi
	if run_oracle "$TMP/$label.log"; then rc=0; else rc=$?; fi
	actual=$(tail -n 1 "$TMP/$label.log")
	if [ "$rc" -ne 1 ] || [ "$actual" != 'console-func-l8: ROTO — L8_FOCAL_CONTRACT: falló una aserción funcional focal' ]; then
		printf '%s: NO PUDE MIRAR — mutante %s: rc=%s, cierre inesperado\n' "$NAME" "$label" "$rc" >&2
		cat "$TMP/$label.log" >&2
		exit 2
	fi
	restore_all || cannot "no restauro después del mutante $label"
	assert_restored || cannot "el mutante $label no restauró bytes exactos"
	printf '%s: MUERDE %s\n' "$NAME" "$label"
}

run_mutant config-server-aal "$API_GO" \
	$'func (m *Module) handlePutConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n\tif !requireAAL3(w, mc) {\n\t\treturn\n\t}\n' \
	$'func (m *Module) handlePutConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n'
run_mutant dlp-put-server-aal "$API_GO" \
	$'func (m *Module) handlePutDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n\tif !requireAAL3(w, mc) {\n\t\treturn\n\t}\n' \
	$'func (m *Module) handlePutDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n'
run_mutant dlp-delete-server-aal "$API_GO" \
	$'func (m *Module) handleDeleteDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n\tif !requireAAL3(w, mc) {\n\t\treturn\n\t}\n' \
	$'func (m *Module) handleDeleteDLPRule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n'
run_mutant device-server-aal "$DEVICE_GO" \
	$'func (m *Module) handleApproveDeviceGrant(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n\tif !requireAAL3(w, mc) {\n\t\treturn\n\t}\n' \
	$'func (m *Module) handleApproveDeviceGrant(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {\n'
run_mutant config-ui-aal "$INFERENCE" \
	'const stepUpNeeded = canWrite && aal < AAL.HARDWARE' \
	'const stepUpNeeded = false'
run_mutant dlp-ui-aal "$INFERENCE" \
	'const canEdit = canAdmin && aal >= AAL.HARDWARE' \
	'const canEdit = canAdmin'
run_mutant device-ui-aal "$INFERENCE" \
	'const stepUpNeeded = canApprove && aal < AAL.HARDWARE' \
	'const stepUpNeeded = false'
run_mutant gpai-seven-claims "$GPAI" \
	'copyright_policy: values.copyright_policy,' \
	'copyright_policy: false,'
run_mutant model-execute-admin "$MODELS" \
	"if (!can('models:routing:admin')) return null" \
	"if (!can('models:routing:write')) return null"
run_mutant sandbox-compare-admin "$SANDBOX" \
	"const puedeComparar = can('sandbox:run:admin') && !confined" \
	"const puedeComparar = can('sandbox:run:write') && !confined"
run_mutant eval-gate-admin "$EVALS" \
	"canOverride={can('evals:run:admin')}" \
	"canOverride={can('evals:run:write')}"
run_mutant eval-baseline-admin "$EVALS" \
	"if (!can('evals:run:admin')) return null" \
	"if (!can('evals:run:write')) return null"
run_mutant documents-read-only-seal "$DOCUMENTS" \
	'{canWrite && (' \
	'{true && ('
run_mutant admission-tightening-confirm "$ADMISSION" \
	'if (needsConfirm) {' \
	'if (false) {'

restore_all || cannot 'no restauro antes del control final'
if ! run_oracle "$TMP/final-green.log"; then
	printf '%s: NO PUDE MIRAR — el control final limpio no quedó verde\n' "$NAME" >&2
	cat "$TMP/final-green.log" >&2
	exit 2
fi
expected_green='console-func-l8: FUNCIONA — AAL3, GPAI, modelos, sandbox, evals y documentos verificados'
[ "$(tail -n 1 "$TMP/final-green.log")" = "$expected_green" ] || cannot 'mensaje final verde inesperado'

MISSING="$TMP/no-web"
if OLIVARES_L8_WEB_DIR="$MISSING" bash "$ORACLE" >"$TMP/cannot.log" 2>&1; then rc=0; else rc=$?; fi
expected_cannot="console-func-l8: NO PUDE MIRAR — no leo $MISSING/package.json"
if [ "$rc" -ne 2 ] || [ "$(tail -n 1 "$TMP/cannot.log")" != "$expected_cannot" ]; then
	printf '%s: NO PUDE MIRAR — el control rc2 no conservó código y mensaje exactos\n' "$NAME" >&2
	cat "$TMP/cannot.log" >&2
	exit 2
fi

assert_restored || cannot 'la batería terminó con bytes distintos'
printf '%s: FUNCIONA — 14/14 mutantes mordieron, rc0/rc1/rc2 y restauración byte-exacta\n' "$NAME"
