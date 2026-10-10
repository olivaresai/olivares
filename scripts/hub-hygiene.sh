#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# hub-hygiene.sh — informa sobre worktrees candidatos a retirada manual; nunca los borra.
# La recuperación de disco nunca justifica arriesgar trabajo no publicado.
#
# DOCTRINA. Hay cuatro respuestas: CANDIDATO, CONSERVAR, EVIDENCIA y NO HE PODIDO MIRAR. La
# clasificación sigue siendo útil para que un operador compruebe una retirada manual, pero la
# ejecución desatendida se retiró tras dos contrastes NO APTO: siguen abiertas cuatro clases de
# pérdida (contenido manual ignorado, estado Git privado, descriptores/procesos no visibles y
# hooks que seguían symlinks). Recuperar 740 MB no justifica ese riesgo frente a ~60 GB de cachés
# que un operador puede vaciar por separado con un comando trivial.
#
# Este programa es estrictamente de sólo lectura: no crea temporales, no actualiza refs, no borra
# worktrees, cachés ni procesos. La comprobación de origin usa `git ls-remote` y falla cerrada.
#
# ALCANCE ENTRE CONTENEDORES. /workspace, GOCACHE, uv y pnpm pueden ser compartidos por varios
# contenedores cuyos procesos no son visibles entre sí. Este script no los limpia: hacerlo exigiría
# adquirir refs/gate-locks/heavy como .githooks/pre-push y, además, disponer de leases que distingan
# temporales activos de restos. Tampoco mata procesos ni poda metadatos de Git a escondidas.
#
# USO
#   scripts/hub-hygiene.sh             # informe de sólo lectura
#   scripts/hub-hygiene.sh --dry-run   # equivalente explícito
set -euo pipefail
shopt -s lastpipe

export LC_ALL=C
# Evita que las consultas de estado refresquen opcionalmente el índice durante el informe.
export GIT_OPTIONAL_LOCKS=0

for arg in "$@"; do
	case "$arg" in
	--apply)
		printf '%s\n' \
			'hub-hygiene: --apply was removed (exit 2): two failed rechecks left four classes of data loss unresolved; see design/audits/2026-08-04-codex-hub-hygiene-recontrast.md.' >&2
		exit 2
		;;
	--dry-run) ;;
	*)
		printf 'hub-hygiene: unknown argument: %s\n' "$arg" >&2
		exit 2
		;;
	esac
done

# La raíz se DERIVA del repositorio desde el que se invoca; no se fija ninguna ruta en el
# fuente. Además de no atar la herramienta a un despliegue concreto, evita que el nombre de un
# directorio privado viaje al árbol publicado (`lint:export` lo rechaza, y con razón).
if [ -n "${HUB_ROOT:-}" ]; then
	HUB="$HUB_ROOT"
elif HUB="$(git rev-parse --show-toplevel 2>/dev/null)" && [ -n "$HUB" ]; then
	: # raíz del repositorio que invoca
else
	printf 'hub-hygiene: not inside a Git repository and HUB_ROOT is not set.\n' >&2
	exit 2
fi

declare -a REMOTE_OIDS=()
declare -a REMOTE_REFS=()
REMOTE_ERROR=""

declare -a WT_BR_PATHS=()
declare -a WT_BR_REFS=()

declare -a WORKTREE_PATHS=()
declare -a WORKTREE_LOCKED=()

declare -a CANDIDATE_PATHS=()
declare -a CANDIDATE_KIB=()
declare -a CANDIDATE_HEADS=()
declare -a CANDIDATE_ARTIFACTS=()
declare -a CANDIDATE_EVIDENCE=()
declare -a CANDIDATE_REMOTE_REFS=()

declare -a NUL_RECORDS=()

# ARTEFACTOS REPRODUCIBLES — lista cerrada. Sólo estos paths ignorados pueden acompañar una
# acción. La comparación usa patrones Bash literales enumerados aquí; no consulta el nombre para
# adivinar si "parece" caché o build. Cualquier ignorado que no coincida conserva el worktree.
readonly -a REPRODUCIBLE_IGNORED_PATTERNS=(
	# 1. Dependencias instaladas y metadatos de lenguajes. `task setup`, los tests de los SDK o
	#    sus package managers los reconstruyen desde manifests/locks versionados.
	'node_modules/'
	'node_modules/*'
	'*/node_modules/'
	'*/node_modules/*'
	'__pycache__/'
	'__pycache__/*'
	'*/__pycache__/'
	'*/__pycache__/*'
	'*.pyc'
	'*.pyo'
	'*.pyd'
	'*.egg-info/'
	'*.egg-info/*'

	# 2. Temporales propios de gates y smoke tests. Los scripts que los crean los regeneran y
	#    normalmente los retiran con trap; sólo sobreviven a una interrupción abrupta.
	'.buftmp/'
	'.buftmp/*'
	'.examples-tmp/'
	'.examples-tmp/*'
	'.schema-parity.*/'
	'.schema-parity.*/*'
	'.export-tmp/'
	'.export-tmp/*'
	'.commerce-tmp/'
	'.commerce-tmp/*'
	'.prepush-refclass-tmp/'
	'.prepush-refclass-tmp/*'
	'.release-smoke-tmp/'
	'.release-smoke-tmp/*'
	'.tmpexec.*/'
	'.tmpexec.*/*'
	'terraform-provider-olivares/.docs-check-tmp/'
	'terraform-provider-olivares/.docs-check-tmp/*'

	# 3. Binarios y salidas de compilación. `task build`, `task build:connectors`, los builds de
	#    SDK/docs o el build del módulo indicado los producen a partir de fuentes versionadas.
	'olivares'
	'cmd/olivares/olivares'
	'dist/'
	'dist/*'
	'bin/'
	'bin/*'
	'clients/generator/generator'
	'clients/typescript/dist/'
	'clients/typescript/dist/*'
	'clients/java/target/'
	'clients/java/target/*'
	'cmd/olivares/firstparty/bins/*'
	'docs-site/dist/'
	'docs-site/dist/*'
	'commercial/license-worker/dist/'
	'commercial/license-worker/dist/*'
	'commercial/commerce-lint/olivares-commerce-lint'
	'cloud/control-plane/cloud-cp'
	'cloud/control-plane/*.test'
	'connectors/backstage/plugin-backend/dist/'
	'connectors/backstage/plugin-backend/dist/*'
	'connectors/backstage/plugin-backend/*.tsbuildinfo'
	'connectors/backstage/plugin-frontend/dist/'
	'connectors/backstage/plugin-frontend/dist/*'
	'connectors/backstage/plugin-frontend/*.tsbuildinfo'
	'examples/bring-your-own-protocol/fabworks-connector/bin/'
	'examples/bring-your-own-protocol/fabworks-connector/bin/*'
	'examples/bring-your-own-protocol/fabworks-connector/dist/'
	'examples/bring-your-own-protocol/fabworks-connector/dist/*'
	'examples/bring-your-own-protocol/fabworks-connector/acme-fabworks-erp'
	'scripts/export-scrub/export-scrub'

	# 4. Cachés e informes de herramientas. Astro, Playwright y los harnesses los recrean; los
	#    frames son una salida intermedia, no los vídeos/subtítulos curados del lanzamiento.
	'docs-site/.astro/'
	'docs-site/.astro/*'
	'web/playwright-report/'
	'web/playwright-report/*'
	'web/test-results/'
	'web/test-results/*'
	'web/e2e-visual/__at__/'
	'web/e2e-visual/__at__/*'
	'connectors/backstage/plugin-backend/.test-out/'
	'connectors/backstage/plugin-backend/.test-out/*'
	'connectors/backstage/plugin-frontend/.test-out/'
	'connectors/backstage/plugin-frontend/.test-out/*'
	'design/launch-video/out/frames/'
	'design/launch-video/out/frames/*'

	# 5. Metadatos de sistema operativo sin contenido de usuario. No se admite ninguna ruta
	#    `.claude/`: sus locks, worktrees, checkpoints, mailbox y JSON pueden señalar actividad,
	#    configuración local, evidencia o trabajo único.
	'.DS_Store'
	'*/.DS_Store'
)

say() { printf '%s\n' "$*"; }
hdr() { printf '\n==> %s\n' "$*"; }

path_exists() {
	[ -e "$1" ] || [ -L "$1" ]
}

is_reproducible_ignored_path() {
	local path="$1" pattern
	for pattern in "${REPRODUCIBLE_IGNORED_PATTERNS[@]}"; do
		[[ "$path" == $pattern ]] && return 0
	done
	return 1
}

resolve_ignored_leaf() {
	local wt="$1" ignored_path="$2" candidate
	IGNORED_LEAF=""
	case "$ignored_path" in
	*/)
		# git status colapsa directorios ignorados. Sólo para un patrón NO autorizado se abre el
		# directorio con otro protocolo NUL y se nombra el primer fichero concreto que lo bloquea.
		if ! run_nul_command git -C "$wt" ls-files --others --ignored --exclude-standard -z -- \
			":(literal)$ignored_path"; then
			return 1
		fi
		for candidate in "${NUL_RECORDS[@]}"; do
			[ -n "$candidate" ] || continue
			IGNORED_LEAF="$candidate"
			return 0
		done
		return 1
		;;
	*)
		IGNORED_LEAF="$ignored_path"
		return 0
		;;
	esac
}

# Ejecuta un productor NUL, conserva cada registro como un elemento de array y comprueba tanto
# su estado de salida como un posible registro final sin NUL. No se convierte ninguna ruta en una
# línea ni en código de shell.
run_nul_command() {
	local record partial=0
	local -a pipe_status=()
	NUL_RECORDS=()

	# lastpipe mantiene el consumidor en este shell: así el array conserva los NUL sin un fichero
	# temporal y PIPESTATUS sigue distinguiendo fallo del productor y fin normal del lector.
	if "$@" 2>/dev/null | while :; do
		record=""
		if IFS= read -r -d '' record; then
			NUL_RECORDS+=("$record")
			continue
		fi
		[ -z "$record" ] || partial=1
		break
	done; then
		pipe_status=("${PIPESTATUS[@]}")
	else
		pipe_status=("${PIPESTATUS[@]}")
	fi
	[ "${pipe_status[0]:-1}" = "0" ] && [ "${pipe_status[1]:-1}" = "0" ] &&
		[ "$partial" = "0" ]
}

measure_kib() {
	local path="$1" output value measured_path
	MEASURED_KIB=""
	if ! run_nul_command du -sk --null -- "$path" || [ "${#NUL_RECORDS[@]}" != "1" ]; then
		return 1
	fi
	output="${NUL_RECORDS[0]}"
	case "$output" in
	*$'\t'*)
		value="${output%%$'\t'*}"
		measured_path="${output#*$'\t'}"
		;;
	*) return 1 ;;
	esac
	[ "$measured_path" = "$path" ] || return 1
	# MUTATION-ANCHOR: numeric-size-validation
	[[ "$value" =~ ^[0-9]+$ ]] || return 1
	MEASURED_KIB="$value"
}

refresh_remote_snapshot() {
	local origin_url output line oid ref rest
	REMOTE_OIDS=()
	REMOTE_REFS=()
	REMOTE_ERROR=""

	if ! run_nul_command git -C "$HUB" config --null --get-all remote.origin.url ||
		[ "${#NUL_RECORDS[@]}" != "1" ] || [ -z "${NUL_RECORDS[0]}" ]; then
		REMOTE_ERROR="cannot resolve the origin URL"
		return 1
	fi
	origin_url="${NUL_RECORDS[0]}"
	# Sólo las refs anunciadas ahora por origin autorizan un candidato; las tracking refs locales
	# no deciden nada. `ls-remote` no actualiza el almacén ni las refs locales.
	if ! output="$(git -C "$HUB" ls-remote --heads --refs origin 2>/dev/null)"; then
		REMOTE_ERROR="ls-remote could not query origin without writing"
		return 1
	fi

	while IFS= read -r line; do
		[ -n "$line" ] || continue
		case "$line" in
		*$'\t'*)
			oid="${line%%$'\t'*}"
			rest="${line#*$'\t'}"
			;;
		*)
			REMOTE_ERROR="origin returned a line without a separator"
			return 1
			;;
		esac
		case "$rest" in
		*$'\t'*)
			REMOTE_ERROR="origin returned a line with extra fields"
			return 1
			;;
		esac
		ref="$rest"
		if [[ ! "$oid" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]] ||
			[[ "$ref" != refs/heads/* ]] ||
			! git check-ref-format "$ref" >/dev/null 2>&1; then
			REMOTE_ERROR="origin returned an invalid ref"
			return 1
		fi
		REMOTE_OIDS+=("$oid")
		REMOTE_REFS+=("$ref")
	done <<<"$output"
}

remote_contains_oid() {
	local oid="$1" i tip
	REMOTE_MATCH_REF=""
	for i in "${!REMOTE_OIDS[@]}"; do
		tip="${REMOTE_OIDS[$i]}"
		if [ "$oid" = "$tip" ]; then
			REMOTE_MATCH_REF="${REMOTE_REFS[$i]}"
			return 0
		fi
		# Sólo se acepta ascendencia si los dos objetos ya están en el almacén local. No se descarga
		# nada para completar la prueba: si falta un objeto, se conserva el worktree.
		if git -C "$HUB" cat-file -e "${oid}^{commit}" 2>/dev/null &&
			git -C "$HUB" cat-file -e "${tip}^{commit}" 2>/dev/null &&
			git -C "$HUB" merge-base --is-ancestor "$oid" "$tip" 2>/dev/null; then
			REMOTE_MATCH_REF="${REMOTE_REFS[$i]}"
			return 0
		fi
	done
	# MUTATION-ANCHOR: live-remote-membership
	return 1
}

# Enumera path + rama de cada worktree. Git lista el ÁRBOL PRINCIPAL el primero, y de eso depende
# la fase 4: el primer registro identifica el clon compartido sin fijar ninguna ruta en el fuente.
enumerate_worktree_branches() {
	local field current_path="" current_ref="" have_current=0
	WT_BR_PATHS=()
	WT_BR_REFS=()
	# MUTATION-ANCHOR: worktree-branch-nul-protocol
	if ! run_nul_command git -C "$HUB" worktree list --porcelain -z; then
		return 1
	fi
	for field in "${NUL_RECORDS[@]}"; do
		if [ -z "$field" ]; then
			if [ "$have_current" = "1" ]; then
				WT_BR_PATHS+=("$current_path")
				WT_BR_REFS+=("$current_ref")
			fi
			current_path=""
			current_ref=""
			have_current=0
			continue
		fi
		case "$field" in
		worktree\ *)
			[ "$have_current" = "0" ] || return 1
			current_path="${field#worktree }"
			[ -n "$current_path" ] || return 1
			have_current=1
			;;
		branch\ *) current_ref="${field#branch }" ;;
		esac
	done
	if [ "$have_current" = "1" ]; then
		WT_BR_PATHS+=("$current_path")
		WT_BR_REFS+=("$current_ref")
	fi
	[ "${#WT_BR_PATHS[@]}" -gt 0 ]
}

enumerate_worktrees() {
	local field current_path="" current_locked=0 have_current=0
	WORKTREE_PATHS=()
	WORKTREE_LOCKED=()
	# MUTATION-ANCHOR: worktree-nul-protocol
	if ! run_nul_command git -C "$HUB" worktree list --porcelain -z; then
		return 1
	fi
	for field in "${NUL_RECORDS[@]}"; do
		if [ -z "$field" ]; then
			if [ "$have_current" = "1" ]; then
				WORKTREE_PATHS+=("$current_path")
				WORKTREE_LOCKED+=("$current_locked")
			fi
			current_path=""
			current_locked=0
			have_current=0
			continue
		fi
		case "$field" in
		worktree\ *)
			# Dos cabeceras sin el registro NUL vacío intermedio son salida malformada.
			[ "$have_current" = "0" ] || return 1
			current_path="${field#worktree }"
			[ -n "$current_path" ] || return 1
			have_current=1
			;;
		locked | locked\ *) current_locked=1 ;;
		esac
	done
	if [ "$have_current" = "1" ]; then
		WORKTREE_PATHS+=("$current_path")
		WORKTREE_LOCKED+=("$current_locked")
	fi
	[ "${#WORKTREE_PATHS[@]}" -gt 0 ]
}

process_uses_worktree() {
	local worktree_real="$1" proc_dir cwd_link cwd pid unknown=0
	PROCESS_DETAIL=""
	[ -d /proc ] || return 2
	for proc_dir in /proc/[0-9]*; do
		[ -d "$proc_dir" ] || continue
		pid="${proc_dir##*/}"
		cwd_link="$proc_dir/cwd"
		[ -L "$cwd_link" ] || continue
		if ! run_nul_command readlink --canonicalize-existing --zero -- "$cwd_link"; then
			# `--canonicalize-existing` tambien falla cuando el cwd del proceso EXISTIO y se
			# borro, que aqui es lo normal: medido el 2026-08-19, de 87 procesos con cwd
			# habia 83 legibles, 4 con el cwd borrado y CERO realmente ilegibles. Con la
			# version anterior esos 4 ponian unknown=1 y la herramienta contestaba «NO SÉ»
			# de forma PERMANENTE: no podia proponer un solo candidato en este contenedor,
			# y su bateria de regresion llevaba roja desde entonces por el caso
			# `reproducible_ignored_artifacts`.
			#
			# Un cwd BORRADO no puede estar dentro de un worktree que SI existe, que es lo
			# unico que se esta preguntando, asi que no informa y no debe bloquear. Se
			# distingue con un readlink normal: si ese lee el enlace, el destino existio y
			# ya no esta; si tampoco lee, entonces si es un desconocido de verdad.
			#
			# Esto NO afloja la guarda: el caso que la justifica —un proceso vivo cuyo cwd
			# no se puede mirar— sigue poniendo unknown=1 y sigue impidiendo el borrado.
			if [ ! -d "$proc_dir" ]; then
				continue
			fi
			if run_nul_command readlink --zero -- "$cwd_link"; then
				continue
			fi
			unknown=1
			continue
		fi
		if [ "${#NUL_RECORDS[@]}" != "1" ]; then
			unknown=1
			continue
		fi
		cwd="${NUL_RECORDS[0]}"
		if [ "$cwd" = "$worktree_real" ] || [[ "$cwd" == "$worktree_real/"* ]]; then
			PROCESS_DETAIL="PID $pid has its working directory inside the worktree (scope: this container)"
			return 0
		fi
	done
	[ "$unknown" = "0" ] || return 2
	return 1
}

worktree_registration_state() {
	local wt="$1" i
	REGISTRATION_FOUND=0
	REGISTRATION_LOCKED=0
	if ! enumerate_worktrees; then
		return 2
	fi
	for i in "${!WORKTREE_PATHS[@]}"; do
		if [ "${WORKTREE_PATHS[$i]}" = "$wt" ]; then
			REGISTRATION_FOUND=1
			REGISTRATION_LOCKED="${WORKTREE_LOCKED[$i]}"
			return 0
		fi
	done
	return 1
}

inspect_worktree() {
	local wt="$1" config_rc=0 config_value="" lowered="" record status_count
	local status_path status_code status_key="" blocker="" quoted_path quoted_record quoted_status
	local artifacts="" artifact_count=0 hidden_untracked=0 has_gitlink=0 stash_rc=0
	local oid reflog_key="" process_rc=0 registration_rc=0 admin_stat admin_hash
	INSPECT_RESULT="unknown"
	INSPECT_REASON="incomplete inspection"
	INSPECT_KIB=""
	INSPECT_HEAD=""
	INSPECT_ADMIN_ID=""
	INSPECT_REALPATH=""
	INSPECT_CONFIG=""
	INSPECT_REFLOG_KEY=""
	INSPECT_STATUS_KEY=""
	INSPECT_ARTIFACTS=""
	INSPECT_EVIDENCE=""
	INSPECT_REMOTE_REF=""

	if ! path_exists "$wt" || [ ! -d "$wt" ]; then
		INSPECT_REASON="the path is no longer an existing directory"
		return 0
	fi
	if ! run_nul_command readlink --canonicalize-existing --zero -- "$wt" ||
		[ "${#NUL_RECORDS[@]}" != "1" ]; then
		INSPECT_REASON="cannot resolve the physical identity of the path"
		return 0
	fi
	INSPECT_REALPATH="${NUL_RECORDS[0]}"
	if worktree_registration_state "$wt"; then
		registration_rc=0
	else
		registration_rc=$?
	fi
	if [ "$registration_rc" = "2" ]; then
		INSPECT_REASON="cannot revalidate the NUL-delimited worktree record"
		return 0
	fi
	if [ "$registration_rc" = "1" ] || [ "$REGISTRATION_FOUND" != "1" ]; then
		INSPECT_REASON="the path is no longer listed in the worktree registry"
		return 0
	fi
	if [ "$REGISTRATION_LOCKED" = "1" ]; then
		INSPECT_RESULT="keep"
		INSPECT_REASON="Git marks it as a locked worktree"
		return 0
	fi
	if ! measure_kib "$wt"; then
		INSPECT_REASON="cannot measure its total size"
		return 0
	fi
	INSPECT_KIB="$MEASURED_KIB"
	# La identidad administrativa se compara sin sacar la ruta guardada en .git de su fichero:
	# inode/metadatos más hash del contenido, ambos escalares sin protocolo de rutas por LF.
	if [ ! -f "$wt/.git" ] ||
		! admin_stat="$(stat -c '%d:%i:%s' -- "$wt/.git" 2>/dev/null)" ||
		! admin_hash="$(git hash-object -- "$wt/.git" 2>/dev/null)" ||
		[ -z "$admin_stat" ] || [ -z "$admin_hash" ]; then
		INSPECT_REASON="cannot inspect the administrative identity of the worktree"
		return 0
	fi
	INSPECT_ADMIN_ID="$admin_stat:$admin_hash"

	config_value="$(git -C "$wt" config --get status.showUntrackedFiles 2>/dev/null)" || config_rc=$?
	if [ "$config_rc" -gt 1 ]; then
		INSPECT_REASON="cannot read status.showUntrackedFiles"
		return 0
	fi
	if [ "$config_rc" = "1" ]; then
		INSPECT_CONFIG="<unset>"
	else
		INSPECT_CONFIG="$config_value"
		lowered="${config_value,,}"
		case "$lowered" in
		no | false | off | 0) hidden_untracked=1 ;;
		esac
		# MUTATION-ANCHOR: hidden-untracked-config
		if [ "$hidden_untracked" = "1" ]; then
			INSPECT_RESULT="keep"
			INSPECT_REASON="status.showUntrackedFiles=$config_value hides state"
			return 0
		fi
	fi

	if ! run_nul_command git -c status.showUntrackedFiles=all -C "$wt" status \
		--porcelain=v1 -z --untracked-files=all --ignored=matching --ignore-submodules=none; then
		INSPECT_REASON="git status could not enumerate the complete state"
		return 0
	fi
	status_count="${#NUL_RECORDS[@]}"
	for record in "${NUL_RECORDS[@]}"; do
		status_key+="${#record}:$record|"
		if [[ "$record" == '!! '* ]]; then
			status_path="${record#?? }"
			# MUTATION-ANCHOR: ignored-artifact-classification
			if is_reproducible_ignored_path "$status_path"; then
				artifact_count=$((artifact_count + 1))
				printf -v quoted_path '%q' "$status_path"
				[ -z "$artifacts" ] || artifacts+=", "
				artifacts+="$quoted_path"
			elif [ -z "$blocker" ]; then
				if resolve_ignored_leaf "$wt" "$status_path"; then
					printf -v quoted_path '%q' "$IGNORED_LEAF"
					# MUTATION-ANCHOR: ignored-blocker-detail
					blocker="ignored file may contain unique work=$quoted_path; git status detail=$status_count NUL-delimited record(s)"
				else
					printf -v quoted_path '%q' "$status_path"
					blocker="ignored path not enumerated=$quoted_path; COULD NOT identify its contents; git status detail=$status_count NUL-delimited record(s)"
				fi
			fi
		elif [ -z "$blocker" ]; then
			if [[ "$record" == ??\ * ]]; then
				status_code="${record:0:2}"
				status_path="${record:3}"
				printf -v quoted_status '%q' "$status_code"
				printf -v quoted_path '%q' "$status_path"
				blocker="local change status=$quoted_status file=$quoted_path; git status detail=$status_count NUL record(s)"
			else
				printf -v quoted_record '%q' "$record"
				blocker="unrecognized git status record=$quoted_record; detail=$status_count NUL-delimited record(s)"
			fi
		fi
	done
	INSPECT_STATUS_KEY="$status_key"
	INSPECT_ARTIFACTS="${artifacts:-none}"
	# MUTATION-ANCHOR: complete-status-guard
	if [ -n "$blocker" ]; then
		INSPECT_RESULT="keep"
		INSPECT_REASON="$blocker"
		return 0
	fi

	# Se conserva cualquier gitlink. Una ruta que parece un artefacto no demuestra que las refs,
	# stashes y objetos de su repositorio inicializado sean reproducibles fuera de este worktree.
	if ! run_nul_command git -C "$wt" ls-files --stage -z; then
		INSPECT_REASON="cannot inventory the index to detect submodules"
		return 0
	fi
	for record in "${NUL_RECORDS[@]}"; do
		[[ "$record" == 160000\ * ]] && has_gitlink=1
	done
	# MUTATION-ANCHOR: submodule-gitlink-guard
	if [ "$has_gitlink" = "1" ]; then
		INSPECT_RESULT="keep"
		INSPECT_REASON="contains at least one submodule; preserving its refs, stash, and objects"
		return 0
	fi

	if git -C "$wt" show-ref --verify --quiet refs/stash 2>/dev/null; then
		stash_rc=0
	else
		stash_rc=$?
	fi
	# MUTATION-ANCHOR: stash-guard
	if [ "$stash_rc" = "0" ]; then
		INSPECT_RESULT="keep"
		INSPECT_REASON="the repository has refs/stash"
		return 0
	fi
	if [ "$stash_rc" != "1" ]; then
		INSPECT_REASON="cannot check refs/stash"
		return 0
	fi

	if ! INSPECT_HEAD="$(git -C "$wt" rev-parse --verify HEAD 2>/dev/null)" ||
		[[ ! "$INSPECT_HEAD" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]]; then
		INSPECT_REASON="cannot read a valid HEAD"
		return 0
	fi
	local author
	author="$(git -C "$wt" log -1 --format=%ae HEAD 2>/dev/null)" || {
		INSPECT_REASON="cannot read the HEAD author"
		return 0
	}
	# MUTATION-ANCHOR: selftest-evidence-guard
	if [ "$author" = "selftest@olivares.invalid" ]; then
		INSPECT_RESULT="evidence"
		INSPECT_REASON="HEAD is evidence created by a self-test"
		return 0
	fi

	if ! run_nul_command git -C "$wt" reflog show --format=%H -z HEAD; then
		INSPECT_REASON="cannot enumerate the HEAD reflog"
		return 0
	fi
	for oid in "${NUL_RECORDS[@]}"; do
		if [[ ! "$oid" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]]; then
			INSPECT_REASON="the HEAD reflog contains an invalid OID"
			return 0
		fi
		reflog_key+="${#oid}:$oid|"
		# MUTATION-ANCHOR: reflog-publication-guard
		if ! remote_contains_oid "$oid"; then
			INSPECT_RESULT="keep"
			INSPECT_REASON="the HEAD reflog retains $oid, unreachable from a live origin branch"
			return 0
		fi
	done
	INSPECT_REFLOG_KEY="$reflog_key"

	if ! remote_contains_oid "$INSPECT_HEAD"; then
		INSPECT_RESULT="keep"
		INSPECT_REASON="HEAD $INSPECT_HEAD is unreachable from a live origin branch"
		return 0
	fi
	INSPECT_REMOTE_REF="$REMOTE_MATCH_REF"

	if process_uses_worktree "$INSPECT_REALPATH"; then
		process_rc=0
	else
		process_rc=$?
	fi
	# MUTATION-ANCHOR: local-process-cwd-guard
	if [ "$process_rc" = "0" ]; then
		INSPECT_RESULT="keep"
		INSPECT_REASON="$PROCESS_DETAIL"
		return 0
	fi
	if [ "$process_rc" = "2" ]; then
		INSPECT_REASON="cannot inspect the working directories of all processes in this container"
		return 0
	fi

	INSPECT_RESULT="safe"
	INSPECT_REASON="no tracked/untracked changes; $artifact_count ignored item(s) in the explicit allowlist"
	INSPECT_EVIDENCE="git status NUL: $status_count record(s), 0 changes, and $artifact_count artifact(s); no gitlinks or refs/stash; HEAD and reflog published at $INSPECT_REMOTE_REF; no local working directory inside"
}

# ⛔ GUARDA DE FRESCURA — sólo CONSERVA, nunca retira de más. Añadida 2026-08-24 por
#    el carril del hub tras medir que este censo marcaba como candidatos SUS PROPIOS worktrees
#    vivos (.hub-r25-889 y .hub-r25-gate2, en uso mientras se escribía esto).
#
#    POR QUÉ PASABA, y es el hallazgo: las tres señales que este censo usa para declarar un
#    worktree abandonado son EXACTAMENTE las que produce la buena disciplina.
#      1. árbol sin cambios      -> se cumple PORQUE SE COMMITEA
#      2. HEAD publicado         -> se cumple PORQUE SE EMPUJA AL NACER
#      3. ningún cwd dentro      -> se cumple PORQUE EL ARNÉS DE AGENTES RESETEA EL CWD tras
#                                   cada mandato; la actividad de un agente NO se ve en /proc.
#    Cuanto mejor se porta un carril, más desechable parece. La señal 3 no mide lo que cree medir.
#
#    EL UMBRAL NO ESTÁ ELEGIDO: LO ENSEÑA EL DATO. Edades del último commit de los 34 worktrees
#    de esta caja, en horas:
#      0×10 · 8 · 9 · 9 · 10 · 11 · 11 · 13 · 17 · 40 · 46 · 71 || 266 · 266 · 277 · 365 · 483 ·
#      484 · 484 · 490 · 507 · 508 · 508 · 508 · 560
#    El hueco MAYOR mide 195 h, entre 71 h y 266 h. Cualquier corte dentro de ese hueco separa
#    vivos de muertos sin tocar un solo caso ambiguo. 168 h (7 días) queda holgadamente dentro.
#
#    ⚠ Y se DESCARTÓ, también por medida, la señal «el ref está en una PR abierta»: sobre cuatro
#    worktrees —dos vivos y dos muertos— dio 0 en LOS CUATRO, porque casi todos están en HEAD
#    separado o en ramas sin PR. No discrimina; añadirla habría conservado todo.
#
#    Sólo conserva: un worktree por debajo del umbral NUNCA llega a candidato. Nada que este
#    censo retirase antes deja de retirarse por esta guarda.
readonly WORKTREE_FRESH_HOURS="${OLIVARES_HYGIENE_FRESH_HOURS:-168}"

worktree_demasiado_fresco() {
	local wt="$1" head_ts ahora horas
	head_ts="$(git -C "$wt" log -1 --format=%ct HEAD 2>/dev/null)" || return 1
	case "$head_ts" in '' | *[!0-9]*) return 1 ;; esac
	ahora="$(date +%s)"
	horas=$(( (ahora - head_ts) / 3600 ))
	[ "$horas" -lt "$WORKTREE_FRESH_HOURS" ]
}

add_worktree_candidate() {
	local wt="$1"
	if worktree_demasiado_fresco "$wt"; then
		print_classification "KEEP" "$wt" "$INSPECT_KIB" \
			"its latest commit is less than ${WORKTREE_FRESH_HOURS} h old: it is ACTIVE even if clean and published"
		return 0
	fi
	CANDIDATE_PATHS+=("$wt")
	CANDIDATE_KIB+=("$INSPECT_KIB")
	CANDIDATE_HEADS+=("$INSPECT_HEAD")
	CANDIDATE_ARTIFACTS+=("$INSPECT_ARTIFACTS")
	CANDIDATE_EVIDENCE+=("$INSPECT_EVIDENCE")
	CANDIDATE_REMOTE_REFS+=("$INSPECT_REMOTE_REF")
}

print_candidate() {
	local i="$1"
	printf '    CANDIDATE[%s]  path=%q  size=%s KiB  HEAD=%s  remote=%s\n' \
		"$((i + 1))" "${CANDIDATE_PATHS[$i]}" "${CANDIDATE_KIB[$i]}" \
		"${CANDIDATE_HEADS[$i]:0:12}" "${CANDIDATE_REMOTE_REFS[$i]}"
	printf '        reason=%s; classified artifacts=%s\n' \
		"$INSPECT_REASON" "${CANDIDATE_ARTIFACTS[$i]}"
	printf '        EVIDENCE=%s\n' "${CANDIDATE_EVIDENCE[$i]}"
	say "        required manual check: confirm there are no ignored contents, private refs/configuration, or active processes/file descriptors in any container."
	printf '        manual command (not run): git -C %q worktree %s -- %q\n' \
		"$HUB" "remove" "${CANDIDATE_PATHS[$i]}"
}

print_classification() {
	local label="$1" wt="$2" kib="$3" reason="$4"
	printf '    %-10s path=%q' "$label" "$wt"
	[ -z "$kib" ] || printf '  size=%s KiB' "$kib"
	printf '  — %s\n' "$reason"
}

# --------------------------------------------------------------------------------------
# FASE 0 — declarar el alcance real de la exclusión.
# --------------------------------------------------------------------------------------
hdr "phase 0 — concurrency scope"
say "    Processes and working directories: this container only (local /proc); used to preserve worktrees."
say "    Resources shared across containers: out of scope; this run does not clean them."
say "    A local pgrep does not authorize any action on /workspace, GOCACHE, uv, or pnpm."

# --------------------------------------------------------------------------------------
# FASE 1 — candidatos reparentados. Sólo información: PPID 1 no demuestra abandono.
# --------------------------------------------------------------------------------------
hdr "phase 1 — reparented processes (report only)"
process_rows=""
if process_rows="$(ps -eo pid=,ppid=,sid=,tty=,etimes=,comm=,args= 2>/dev/null)"; then
	reparented=0
	while read -r pid ppid sid tty etimes comm args; do
		[ -n "${pid:-}" ] || continue
		[ "$ppid" = "1" ] || continue
		[ "$etimes" -gt 3600 ] 2>/dev/null || continue
		criterion=""
		case "$comm $args" in
		*claude*) criterion="texto claude" ;;
		*codex*) criterion="texto codex" ;;
		*'go test'*) criterion="go test command text" ;;
		*vitest*) criterion="vitest command text" ;;
		*wrangler*) criterion="wrangler command text" ;;
		*) continue ;;
		esac
		reparented=$((reparented + 1))
		cwd="UNAVAILABLE"
		if run_nul_command readlink --canonicalize-existing --zero -- "/proc/$pid/cwd" &&
			[ "${#NUL_RECORDS[@]}" = "1" ]; then
			cwd="${NUL_RECORDS[0]}"
		fi
		cgroup="UNAVAILABLE"
		if [ -r "/proc/$pid/cgroup" ]; then
			cgroup="$(tr '\n' '|' <"/proc/$pid/cgroup" 2>/dev/null)" || cgroup="UNAVAILABLE"
		fi
		printf '    REPARENTED CANDIDATE pid=%s ppid=%s sid=%s tty=%s age=%ss criterion=%q cwd=%q cgroup=%q cmd=%q\n' \
			"$pid" "$ppid" "$sid" "$tty" "$etimes" "$criterion" "$cwd" \
			"${cgroup:0:160}" "${args:0:160}"
	done <<<"$process_rows"
	[ "$reparented" = "0" ] && say "    none according to the reporting criteria"
	[ "$reparented" = "0" ] || say "    PPID 1 only proves reparenting; no process is killed or assumed abandoned."
else
	say "    COULD NOT LOOK — ps failed; absence of candidates cannot be inferred."
fi

# --------------------------------------------------------------------------------------
# FASE 2 — worktrees con prueba remota viva y estado local completo.
# --------------------------------------------------------------------------------------
hdr "fase 2 — worktrees"
hub_real=""
if ! run_nul_command readlink --canonicalize-existing --zero -- "$HUB" ||
	[ "${#NUL_RECORDS[@]}" != "1" ] ||
	! git -C "$HUB" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	say "    COULD NOT LOOK — HUB_ROOT does not identify a readable Git worktree."
else
	hub_real="${NUL_RECORDS[0]}"
	remote_ready=1
	if ! refresh_remote_snapshot; then
		remote_ready=0
		printf '    COULD NOT LOOK origin — %s; no worktree is marked as a candidate.\n' \
			"$REMOTE_ERROR"
	else
		say "    origin verified using read-only ls-remote; unverified relationships are preserved."
	fi
	if ! enumerate_worktrees; then
		say "    COULD NOT LOOK — git worktree list did not produce a valid NUL-delimited inventory."
	else
		# La inspección individual vuelve a enumerar el registro. Se itera sobre esta foto separada
		# para que una carrera no desplace índices del bucle exterior.
		plan_worktree_paths=("${WORKTREE_PATHS[@]}")
		plan_worktree_locked=("${WORKTREE_LOCKED[@]}")
		for i in "${!plan_worktree_paths[@]}"; do
			wt="${plan_worktree_paths[$i]}"
			if run_nul_command readlink --canonicalize-existing --zero -- "$wt" &&
				[ "${#NUL_RECORDS[@]}" = "1" ] && [ "${NUL_RECORDS[0]}" = "$hub_real" ]; then
				continue
			fi
			if [ "${plan_worktree_locked[$i]}" = "1" ]; then
				print_classification "KEEP" "$wt" "" "Git lo anuncia como bloqueado"
				continue
			fi
			if [ "$remote_ready" != "1" ]; then
				print_classification "UNKNOWN" "$wt" "" "remote publication could not be verified"
				continue
			fi
			inspect_worktree "$wt"
			case "$INSPECT_RESULT" in
			safe)
				add_worktree_candidate "$wt"
				print_candidate "$((${#CANDIDATE_PATHS[@]} - 1))"
				;;
			keep) print_classification "KEEP" "$wt" "$INSPECT_KIB" "$INSPECT_REASON" ;;
			evidence) print_classification "EVIDENCE" "$wt" "$INSPECT_KIB" "$INSPECT_REASON" ;;
			*) print_classification "UNKNOWN" "$wt" "$INSPECT_KIB" "$INSPECT_REASON" ;;
			esac
		done
	fi
fi

# --------------------------------------------------------------------------------------
# FASE 3 — recursos compartidos: abstención explícita.
# --------------------------------------------------------------------------------------
hdr "phase 3 — shared caches and temporary directories"
# MUTATION-ANCHOR: shared-resource-abstention
say "    KEEP — /workspace/.gobuildtmp and /workspace/.olivares-tmptest have no reliable leases."
say "    KEEP — GOCACHE, uv, and pnpm are left untouched; offline reproducibility is not assumed."
say "    refs/gate-locks/heavy is not acquired because this run performs no shared action."

# --------------------------------------------------------------------------------------
# FASE 4 — el clon compartido y la rama por defecto.
#
# ⛔ EL PRIMERO QUE TOMA LA RAMA POR DEFECTO GANA, y el otro queda SEPARADO. Git no permite la
# misma rama en dos árboles de trabajo, así que un `git worktree add … main` —un comando que la
# REGLA CERO del repositorio permite explícitamente— deja el clon compartido en HEAD separado
# POR CONSTRUCCIÓN, sin ruido y para siempre; desde ahí sólo envejece, y quien mida en él mide
# un árbol viejo y NO FALLA.
#
# Medido el 2026-08-21 en dos contenedores: dos carriles publicaron el clon compartido como
# «589 commits detrás y separado» mientras un tercero lo medía en `main`, limpio y al día. Eran
# dos clones distintos en la MISMA RUTA (mismo dispositivo, inodo del .git distinto), y la causa
# del separado era un worktree de medición que tenía `main` tomada.
#
# La asimetría es lo aprovechable: si la rama la tiene EL CLON, un `worktree add` sobre ella
# falla con un `fatal` que se ve; si la tiene un worktree, no se entera nadie. Por eso esta fase
# informa de CUÁL de los dos casos es, y NOMBRA AL POSEEDOR, que es la causa y no el síntoma.
#
# Es un INFORME, no un gate, y por la misma razón que el resto del programa: la condición vive
# en el contenedor, no en el árbol publicado, así que un rojo aquí enseñaría a ignorarlo. Y NO
# hace fetch: mide contra la ref local de origin, que puede estar tan atrasada como el clon, y
# lo dice.
# --------------------------------------------------------------------------------------
hdr "phase 4 — shared clone and default branch"
# MUTATION-ANCHOR: shared-clone-default-branch
default_ref="$(git -C "$HUB" symbolic-ref -q refs/remotes/origin/HEAD 2>/dev/null || true)"
if [ -n "$default_ref" ]; then
	default_branch="${default_ref#refs/remotes/origin/}"
else
	default_branch="main"
	say "    origin/HEAD is not set in this clone; assuming ${default_branch} explicitly."
fi
if ! enumerate_worktree_branches; then
	say "    COULD NOT LOOK — git worktree list did not produce a valid NUL-delimited inventory."
else
	clone_root="${WT_BR_PATHS[0]}"
	holder=""
	for idx in "${!WT_BR_PATHS[@]}"; do
		if [ "${WT_BR_REFS[$idx]}" = "refs/heads/${default_branch}" ]; then
			holder="${WT_BR_PATHS[$idx]}"
			break
		fi
	done
	# `symbolic-ref -q HEAD` FALLA si el HEAD está separado. `git branch --show-current` sale 0
	# con salida VACÍA, así que un `||` detrás no dispara nunca: no es una medida.
	if clone_ref="$(git -C "$clone_root" symbolic-ref -q HEAD 2>/dev/null)"; then
		clone_branch="${clone_ref#refs/heads/}"
	else
		clone_branch=""
	fi
	if [ "$clone_branch" = "$default_branch" ]; then
		printf '    KEEP — the shared clone has %q checked out (path=%q).\n' \
			"$default_branch" "$clone_root"
		say "    In this state, 'git worktree add … ${default_branch}' fails with a VISIBLE fatal error, as intended."
	elif [ -n "$holder" ] && [ "$holder" != "$clone_root" ]; then
		printf '    EVIDENCE — the shared clone CANNOT be on %q: worktree %q has it checked out.\n' \
			"$default_branch" "$holder"
		say "    That is the CAUSE: 'git checkout ${default_branch}' in the clone fails while it remains checked out elsewhere."
	elif [ -z "$clone_branch" ]; then
		printf '    EVIDENCE — the shared clone has a DETACHED HEAD and nobody has %q checked out.\n' \
			"$default_branch"
		say "    No worktree accounts for this: HEAD was detached manually, or a rebase/bisect remains incomplete."
	else
		printf '    KEEP — the shared clone is on branch %q, which is not the default branch.\n' \
			"$clone_branch"
	fi
	if [ -z "$clone_branch" ]; then
		if ! head_oid="$(git -C "$clone_root" rev-parse -q --verify HEAD 2>/dev/null)"; then
			say "    COULD NOT LOOK — the shared clone cannot resolve HEAD; nothing is inferred about its contents."
		elif ! git -C "$clone_root" rev-parse -q --verify "refs/remotes/origin/${default_branch}" >/dev/null 2>&1; then
			say "    COULD NOT LOOK — no local refs/remotes/origin/${default_branch} for comparison (fetch is not performed)."
		elif git -C "$clone_root" merge-base --is-ancestor "$head_oid" "refs/remotes/origin/${default_branch}" 2>/dev/null; then
			behind="$(git -C "$clone_root" rev-list --count "${head_oid}..refs/remotes/origin/${default_branch}" 2>/dev/null || echo '?')"
			printf '    Its HEAD is already in origin/%s and is %s commit(s) behind: it retains no unique work.\n' \
				"$default_branch" "$behind"
			say "    Measured against the LOCAL origin ref, which may be as stale as the clone."
		else
			say "    ⛔ Its HEAD is NOT an ancestor of origin/${default_branch}: it RETAINS unpublished work."
			say "    Leave it in place. Publish it or hand it to its owner; a clean 'git status' would not reveal this."
		fi
	fi
fi

# --------------------------------------------------------------------------------------
# RESUMEN. No hay camino de ejecución en este script.
# --------------------------------------------------------------------------------------
hdr "resumen"
candidate_kib=0
for kib in "${CANDIDATE_KIB[@]}"; do
	candidate_kib=$((candidate_kib + kib))
done
printf '    %s candidate(s); %s KiB measured inside candidate trees.\n' \
	"${#CANDIDATE_PATHS[@]}" "$candidate_kib"
say "    That measurement does not promise recovered physical bytes: hardlinks, sparse files, and concurrency affect df."
say "    REPORT only; no ref, worktree, cache, or process was changed."
