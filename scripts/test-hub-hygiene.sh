#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Regresiones de clasificación y de inmutabilidad de hub-hygiene.sh. Los fixtures son repositorios
# propios bajo un mktemp; el contrato load-bearing exige que cualquier invocación del SUT los deje
# byte a byte idénticos.
set -euo pipefail

# The ambient git environment OUTRANKS `-C`: with GIT_DIR exported — which git does
# from every LINKED worktree, i.e. from every parallel session — this script's throwaway
# repositories would be driven into the LIVE repository instead. Measured 2026-08-06;
# it left the branch of PR #526 pointing at a fixture commit. Fail closed: a missing
# sanitiser is "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

export LC_ALL=C

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPT_UNDER_TEST="${HUB_HYGIENE_SUT:-$SCRIPT_DIR/hub-hygiene.sh}"
SELF="$SCRIPT_DIR/test-hub-hygiene.sh"
REAL_GIT_BIN="$(command -v git)"
CASE_FILTER=""

while [ "$#" -gt 0 ]; do
	case "$1" in
	--case)
		[ "$#" -ge 2 ] || {
			echo "test-hub-hygiene: --case necesita un nombre" >&2
			exit 2
		}
		CASE_FILTER="$2"
		shift 2
		;;
	*)
		printf 'test-hub-hygiene: argumento desconocido: %s\n' "$1" >&2
		exit 2
		;;
	esac
done

[ -r "$SCRIPT_UNDER_TEST" ] || {
	printf 'test-hub-hygiene: SUT no legible: %q\n' "$SCRIPT_UNDER_TEST" >&2
	exit 1
}

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/hub-hygiene-tests.XXXXXXXX")"
BACKGROUND_PIDS=()
FIXTURE_SEQ=0

cleanup() {
	local pid
	for pid in "${BACKGROUND_PIDS[@]}"; do
		kill "$pid" >/dev/null 2>&1 || true
		wait "$pid" >/dev/null 2>&1 || true
	done
	if [ -n "${TEST_ROOT:-}" ] && [ -d "$TEST_ROOT" ] &&
		[ -f "$TEST_ROOT/.hub-hygiene-test-root" ]; then
		chmod -R u+w "$TEST_ROOT" >/dev/null 2>&1 || true
		rm -r -- "$TEST_ROOT"
	fi
}
trap cleanup EXIT INT TERM
: >"$TEST_ROOT/.hub-hygiene-test-root"

CURRENT_TEST="bootstrap"
RUN_OUTPUT=""
RUN_RC=0
RUN_PATH="$PATH"
RUN_GIT_MODE=""
RUN_DU_MODE=""

fail() {
	printf 'not ok - %s: %s\n' "$CURRENT_TEST" "$*" >&2
	if [ -n "${RUN_OUTPUT:-}" ] && [ -f "$RUN_OUTPUT" ]; then
		echo "--- salida del SUT ---" >&2
		sed -n '1,240p' "$RUN_OUTPUT" >&2
		echo "--- fin ---" >&2
	fi
	exit 1
}

assert_rc() {
	local expected="$1"
	[ "$RUN_RC" = "$expected" ] || fail "exit esperado=$expected, observado=$RUN_RC"
}

assert_contains() {
	local needle="$1"
	grep -F -- "$needle" "$RUN_OUTPUT" >/dev/null || fail "falta texto: $needle"
}

assert_not_contains() {
	local needle="$1"
	if grep -F -- "$needle" "$RUN_OUTPUT" >/dev/null; then
		fail "aparece texto prohibido: $needle"
	fi
}

assert_path_exists() {
	[ -e "$1" ] || [ -L "$1" ] || fail "debería existir: $(printf '%q' "$1")"
}

assert_path_absent() {
	if [ -e "$1" ] || [ -L "$1" ]; then
		fail "debería haberse conservado fuera o retirado por completo: $(printf '%q' "$1")"
	fi
}

assert_equal() {
	[ "$1" = "$2" ] || fail "valores distintos: esperado=$(printf '%q' "$1") observado=$(printf '%q' "$2")"
}

git_test() {
	"$REAL_GIT_BIN" -c protocol.file.allow=always "$@"
}

init_fixture() {
	local label="$1"
	FIXTURE_SEQ=$((FIXTURE_SEQ + 1))
	CASE_DIR="$TEST_ROOT/${FIXTURE_SEQ}-${label}"
	REMOTE="$CASE_DIR/remote.git"
	HUB="$CASE_DIR/hub"
	CASE_HOME="$CASE_DIR/home"
	CASE_TMP="$CASE_DIR/tmp"
	mkdir -p "$CASE_DIR" "$CASE_HOME" "$CASE_TMP"
	git_test init -q --bare "$REMOTE"
	git_test init -q "$HUB"
	git_test -C "$HUB" config user.name "Hub Hygiene Test"
	git_test -C "$HUB" config user.email "hub-hygiene@example.invalid"
	printf 'base\n' >"$HUB/base.txt"
	git_test -C "$HUB" add base.txt
	git_test -C "$HUB" commit -qm "base"
	git_test -C "$HUB" branch -M main
	git_test -C "$HUB" remote add origin "$REMOTE"
	git_test -C "$HUB" push -qu origin main
	git_test -C "$REMOTE" symbolic-ref HEAD refs/heads/main
	RUN_PATH="$PATH"
	RUN_GIT_MODE=""
	RUN_DU_MODE=""
}

add_clean_worktree() {
	local path="$1" branch="$2"
	git_test -C "$HUB" worktree add -qb "$branch" "$path" main
}

commit_main_and_push() {
	git_test -C "$HUB" add -A
	git_test -C "$HUB" commit -qm "$1"
	git_test -C "$HUB" push -q origin main
}

run_sut() {
	RUN_OUTPUT="$TEST_ROOT/sut-output-${FIXTURE_SEQ}-${RANDOM}.txt"
	RUN_RC=0
	(
		cd "$CASE_DIR"
		if [ -n "$RUN_GIT_MODE" ]; then
			git() {
				local saw_fetch=0 saw_prune=0 saw_ls_remote=0 arg
				for arg in "$@"; do
					[ "$arg" = "fetch" ] && saw_fetch=1
					[ "$arg" = "--prune" ] && saw_prune=1
					[ "$arg" = "ls-remote" ] && saw_ls_remote=1
				done
				if [ "$RUN_GIT_MODE" = "fetch" ] && [ "$saw_fetch" = "1" ] &&
					[ "$saw_prune" = "1" ]; then
					return 42
				fi
				if [ "$RUN_GIT_MODE" = "ls-remote" ] && [ "$saw_ls_remote" = "1" ]; then
					return 43
				fi
				"$REAL_GIT_BIN" "$@"
			}
			export -f git
		fi
		if [ "$RUN_DU_MODE" = "invalid" ]; then
			du() {
				printf '123\n0\t%s\0' "${*: -1}"
			}
			export -f du
		fi
		HOME="$CASE_HOME" \
			TMPDIR="$CASE_TMP" \
			PATH="$RUN_PATH" \
			REAL_GIT_BIN="$REAL_GIT_BIN" \
			RUN_GIT_MODE="$RUN_GIT_MODE" \
			RUN_DU_MODE="$RUN_DU_MODE" \
			HUB_ROOT="$HUB" \
			bash "$SCRIPT_UNDER_TEST" "$@"
	) >"$RUN_OUTPUT" 2>&1 || RUN_RC=$?
}

ref_snapshot() {
	local repo="$1" fetch_head="MISSING"
	if [ -f "$repo/.git/FETCH_HEAD" ]; then
		fetch_head="$(sha256sum "$repo/.git/FETCH_HEAD" | awk '{print $1}')"
	fi
	{
		git_test -C "$repo" for-each-ref --format='%(refname)%09%(objectname)'
		printf 'FETCH_HEAD\t%s\n' "$fetch_head"
	} | sha256sum | awk '{print $1}'
}

test_source_surface() {
	local bad
	bash -n "$SCRIPT_UNDER_TEST" || fail "bash -n ha fallado"
	bad="$(awk '
		/^[[:space:]]*#/ { next }
		/^[[:space:]]*(eval|find|rm|rmdir|unlink|truncate|go[[:space:]]+clean|pnpm[[:space:]]+store[[:space:]]+prune)([[:space:]]|$)/ { print NR ":" $0 }
		/find[[:space:]].*-exec/ { print NR ":" $0 }
		/worktree[[:space:]]+(remove|prune)[[:space:]]/ { print NR ":" $0 }
		/git[[:space:]].*(update-ref|fetch|reset|clean|gc|config[[:space:]].*--(add|replace-all|unset|remove-section))/ { print NR ":" $0 }
	' "$SCRIPT_UNDER_TEST")"
	[ -z "$bad" ] || fail "superficie destructiva no permitida: $bad"
	grep -F 'worktree list --porcelain -z' "$SCRIPT_UNDER_TEST" >/dev/null ||
		fail "falta el protocolo NUL de worktree list"
}

fixture_tree_digest() {
	local tree="$1" path target
	(
		cd "$tree"
		while IFS= read -r -d '' path; do
			if [ -L "$path" ]; then
				target="$(readlink -- "$path")" || exit 1
				printf 'symlink %s -> %s\n' "$path" "$target"
			elif [ -f "$path" ]; then
				printf 'file %s ' "$path"
				sha256sum -- "$path"
			elif [ -d "$path" ]; then
				printf 'directory %s\n' "$path"
			else
				printf 'other %s\n' "$path"
			fi
		done < <(find . -print0 | LC_ALL=C sort -z)
	) | sha256sum | awk '{print $1}'
}

test_all_arguments_no_mutation() {
	local wt before after
	init_fixture "all-arguments-no-mutation"
	wt="$CASE_DIR/wt-immutable"
	add_clean_worktree "$wt" "feature/immutable"

	before="$(fixture_tree_digest "$CASE_DIR")"
	run_sut
	assert_rc 0
	after="$(fixture_tree_digest "$CASE_DIR")"
	assert_equal "$before" "$after"

	before="$after"
	run_sut --dry-run
	assert_rc 0
	after="$(fixture_tree_digest "$CASE_DIR")"
	assert_equal "$before" "$after"

	before="$after"
	run_sut --apply
	assert_rc 2
	assert_contains "--apply se ha retirado"
	assert_contains "2026-08-04-codex-hub-hygiene-recontrast.md"
	after="$(fixture_tree_digest "$CASE_DIR")"
	assert_equal "$before" "$after"

	before="$after"
	run_sut --argumento-invalido
	assert_rc 2
	after="$(fixture_tree_digest "$CASE_DIR")"
	assert_equal "$before" "$after"
}

test_dry_run_no_mutation() {
	local wt updater before after old_main stale_oid new_remote_oid expected_kib
	init_fixture "dry-run"
	wt="$CASE_DIR/wt-dry"
	add_clean_worktree "$wt" "feature/dry"
	old_main="$(git_test -C "$HUB" rev-parse refs/remotes/origin/main)"
	stale_oid="$old_main"
	git_test -C "$HUB" update-ref refs/remotes/origin/stale "$stale_oid"
	updater="$CASE_DIR/updater"
	git_test clone -qb main "$REMOTE" "$updater"
	git_test -C "$updater" config user.name "Remote Updater"
	git_test -C "$updater" config user.email "updater@example.invalid"
	printf 'advance\n' >>"$updater/base.txt"
	git_test -C "$updater" add base.txt
	git_test -C "$updater" commit -qm "advance remote"
	new_remote_oid="$(git_test -C "$updater" rev-parse HEAD)"
	git_test -C "$updater" push -q origin main
	if git_test -C "$HUB" cat-file -e "${new_remote_oid}^{commit}" 2>/dev/null; then
		fail "el fixture ya tenía el objeto remoto nuevo antes del dry-run"
	fi
	expected_kib="$(du -sk -- "$wt" | awk '{print $1}')"
	before="$(ref_snapshot "$HUB")"
	run_sut --dry-run
	after="$(ref_snapshot "$HUB")"
	assert_rc 0
	assert_equal "$before" "$after"
	assert_equal "$old_main" "$(git_test -C "$HUB" rev-parse refs/remotes/origin/main)"
	if git_test -C "$HUB" cat-file -e "${new_remote_oid}^{commit}" 2>/dev/null; then
		fail "el dry-run ha descargado objetos al almacén local"
	fi
	if find "$CASE_TMP" -mindepth 1 -print -quit | grep -q .; then
		fail "el dry-run ha dejado su almacén remoto temporal sin retirar"
	fi
	git_test -C "$HUB" show-ref --verify --quiet refs/remotes/origin/stale ||
		fail "el dry-run ha podado una tracking ref"
	assert_path_exists "$wt"
	assert_contains "tamaño=$expected_kib KiB"
	assert_contains "INFORME solamente; no se ha cambiado ninguna ref"
}

test_reproducible_ignored_artifacts() {
	local wt
	init_fixture "reproducible-ignored"
	printf '/node_modules/\n/.export-tmp/\n/clients/generator/generator\n/web/test-results/\n' \
		>"$HUB/.gitignore"
	commit_main_and_push "ignore reproducible outputs"
	wt="$CASE_DIR/wt-reproducible-ignored"
	add_clean_worktree "$wt" "feature/reproducible-ignored"
	mkdir -p "$wt/node_modules/pkg" "$wt/.export-tmp" "$wt/clients/generator" \
		"$wt/web/test-results"
	printf 'installed dependency\n' >"$wt/node_modules/pkg/index.js"
	printf 'throwaway export\n' >"$wt/.export-tmp/export.txt"
	printf 'compiled generator\n' >"$wt/clients/generator/generator"
	printf 'playwright output\n' >"$wt/web/test-results/result.json"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "CANDIDATO[1]"
	assert_contains "comprobación manual obligatoria"
	assert_contains "comando manual (no ejecutado): git -C"
	assert_contains "EVIDENCIA=git status NUL: 4 registro(s), 0 cambios y 4 artefacto(s)"
	assert_contains ".export-tmp/"
	assert_contains "clients/generator/generator"
	assert_contains "node_modules/"
	assert_contains "web/test-results/"
	assert_contains "git status NUL: 4 registro(s), 0 cambios y 4 artefacto(s)"
	assert_contains "1 candidato(s)"
}

test_ignored_env() {
	local wt
	init_fixture "ignored-env"
	printf '.env\n' >"$HUB/.gitignore"
	commit_main_and_push "ignore env"
	wt="$CASE_DIR/wt-ignored"
	add_clean_worktree "$wt" "feature/ignored"
	printf 'ONLY_COPY=secret\n' >"$wt/.env"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "ignorado posible trabajo único fichero=.env"
	assert_contains "detalle de git status=1 registro(s) NUL"
	assert_not_contains "CANDIDATO[1]"
}

test_reproducible_artifacts_with_env() {
	local wt
	init_fixture "reproducible-with-env"
	printf '.env\n/node_modules/\n/.export-tmp/\n' >"$HUB/.gitignore"
	commit_main_and_push "ignore env and reproducible outputs"
	wt="$CASE_DIR/wt-reproducible-with-env"
	add_clean_worktree "$wt" "feature/reproducible-with-env"
	mkdir -p "$wt/node_modules/pkg" "$wt/.export-tmp"
	printf 'installed dependency\n' >"$wt/node_modules/pkg/index.js"
	printf 'throwaway export\n' >"$wt/.export-tmp/export.txt"
	printf 'ONLY_COPY=secret\n' >"$wt/.env"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "ignorado posible trabajo único fichero=.env"
	assert_contains "detalle de git status=3 registro(s) NUL"
	assert_not_contains "CANDIDATO[1]"
}

test_unknown_ignored_file() {
	local wt
	init_fixture "unknown-ignored"
	printf '/.local/\n' >"$HUB/.gitignore"
	commit_main_and_push "ignore unknown local directory"
	wt="$CASE_DIR/wt-unknown-ignored"
	add_clean_worktree "$wt" "feature/unknown-ignored"
	mkdir -p "$wt/.local"
	printf 'operator-only setting\n' >"$wt/.local/operator.yaml"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_path_exists "$wt/.local/operator.yaml"
	assert_contains "ignorado posible trabajo único fichero=.local/operator.yaml"
	assert_not_contains "CANDIDATO[1]"
}

test_selftest_evidence_head() {
	local wt
	init_fixture "selftest-evidence"
	wt="$CASE_DIR/wt-selftest-evidence"
	add_clean_worktree "$wt" "feature/selftest-evidence"
	printf 'evidence\n' >"$wt/evidence.txt"
	git_test -C "$wt" add evidence.txt
	git_test -C "$wt" -c user.name="Selftest Evidence" \
		-c user.email="selftest@olivares.invalid" commit -qm "selftest evidence"
	git_test -C "$wt" push -qu origin HEAD:refs/heads/feature/selftest-evidence
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "HEAD es evidencia creada por un selftest"
	assert_contains "EVIDENCIA"
	assert_not_contains "CANDIDATO[1]"
}

test_hidden_untracked_config() {
	local wt
	init_fixture "hidden-config"
	wt="$CASE_DIR/wt-hidden-config"
	add_clean_worktree "$wt" "feature/hidden-config"
	git_test -C "$wt" config status.showUntrackedFiles no
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "status.showUntrackedFiles=no oculta estado"
}

test_submodule_unique_ref() {
	local wt sub_remote sub_seed sub_path pinned unique_oid advertised
	init_fixture "submodule"
	sub_remote="$CASE_DIR/sub-remote.git"
	sub_seed="$CASE_DIR/sub-seed"
	git_test init -q --bare "$sub_remote"
	git_test init -q "$sub_seed"
	git_test -C "$sub_seed" config user.name "Submodule Test"
	git_test -C "$sub_seed" config user.email "submodule@example.invalid"
	printf 'sub-base\n' >"$sub_seed/sub.txt"
	git_test -C "$sub_seed" add sub.txt
	git_test -C "$sub_seed" commit -qm "sub base"
	git_test -C "$sub_seed" branch -M main
	git_test -C "$sub_seed" remote add origin "$sub_remote"
	git_test -C "$sub_seed" push -qu origin main
	git_test -C "$sub_remote" symbolic-ref HEAD refs/heads/main
	git_test -C "$HUB" submodule add -q "$sub_remote" modules/sub
	commit_main_and_push "add submodule"
	wt="$CASE_DIR/wt-submodule"
	add_clean_worktree "$wt" "feature/submodule"
	git_test -C "$wt" submodule update -q --init
	sub_path="$wt/modules/sub"
	git_test -C "$sub_path" config user.name "Unique Submodule"
	git_test -C "$sub_path" config user.email "unique-sub@example.invalid"
	pinned="$(git_test -C "$sub_path" rev-parse HEAD)"
	git_test -C "$sub_path" checkout -qb only-in-this-submodule-worktree
	printf 'unique\n' >"$sub_path/unique.txt"
	git_test -C "$sub_path" add unique.txt
	git_test -C "$sub_path" commit -qm "unique submodule ref"
	unique_oid="$(git_test -C "$sub_path" rev-parse HEAD)"
	git_test -C "$sub_path" checkout -q --detach "$pinned"
	git_test -C "$sub_path" show-ref --verify --quiet refs/heads/only-in-this-submodule-worktree ||
		fail "el fixture no contiene la ref única del submódulo"
	advertised="$(git_test ls-remote --heads "$sub_remote" refs/heads/only-in-this-submodule-worktree)"
	[ -z "$advertised" ] || fail "la ref única apareció en el remoto del fixture"
	git_test -C "$sub_path" cat-file -e "${unique_oid}^{commit}" || fail "falta el objeto único"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "contiene al menos un submódulo"
}

test_reflog_detached() {
	local wt unique_oid containing
	init_fixture "reflog"
	wt="$CASE_DIR/wt-reflog"
	add_clean_worktree "$wt" "feature/reflog"
	git_test -C "$wt" checkout -q --detach HEAD
	printf 'reflog-only\n' >"$wt/reflog-only.txt"
	git_test -C "$wt" add reflog-only.txt
	git_test -C "$wt" commit -qm "reflog only"
	unique_oid="$(git_test -C "$wt" rev-parse HEAD)"
	git_test -C "$wt" checkout -q feature/reflog
	containing="$(git_test -C "$HUB" for-each-ref --contains "$unique_oid" --format='%(refname)')"
	[ -z "$containing" ] || fail "el commit del fixture todavía tiene una ref"
	git_test -C "$wt" reflog show --format=%H HEAD | grep -Fx "$unique_oid" >/dev/null ||
		fail "el commit único no está en el reflog del worktree"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "el reflog de HEAD conserva $unique_oid"
}

test_ls_remote_failure() {
	local wt
	init_fixture "ls-remote-failure"
	wt="$CASE_DIR/wt-ls-remote-failure"
	add_clean_worktree "$wt" "feature/ls-remote-failure"
	RUN_GIT_MODE="ls-remote"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "NO HE PODIDO MIRAR origin — ls-remote no ha podido consultar origin sin escribir"
}

test_deleted_remote_ref() {
	local wt head_oid stale_after
	init_fixture "deleted-ref"
	wt="$CASE_DIR/wt-deleted-ref"
	add_clean_worktree "$wt" "feature/deleted-ref"
	printf 'only-feature\n' >"$wt/only-feature.txt"
	git_test -C "$wt" add only-feature.txt
	git_test -C "$wt" commit -qm "only feature"
	head_oid="$(git_test -C "$wt" rev-parse HEAD)"
	git_test -C "$wt" push -qu origin HEAD:refs/heads/feature/deleted-ref
	git_test -C "$wt" push -q origin :refs/heads/feature/deleted-ref
	git_test -C "$HUB" update-ref refs/remotes/origin/feature/deleted-ref "$head_oid"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "no alcanzable desde una rama viva de origin"
	stale_after="$(git_test -C "$HUB" rev-parse refs/remotes/origin/feature/deleted-ref)"
	assert_equal "$head_oid" "$stale_after"
}

test_special_paths() {
	local paths candidate_lines i=0 path
	init_fixture "special-paths"
	paths=(
		"$CASE_DIR/wt space"
		"$CASE_DIR/"$'wt\ttab'
		"$CASE_DIR/wt'quote"
		"$CASE_DIR/"$'wt\nline'
		"$CASE_DIR/wt;touch MUTANT_SEMI"
		"$CASE_DIR/"'wt$(touch MUTANT_INJECTED)'
	)
	for path in "${paths[@]}"; do
		i=$((i + 1))
		add_clean_worktree "$path" "feature/special-$i"
	done
	run_sut --dry-run
	assert_rc 0
	for path in "${paths[@]}"; do
		assert_path_exists "$path"
	done
	assert_path_absent "$CASE_DIR/MUTANT_SEMI"
	assert_path_absent "$CASE_DIR/MUTANT_INJECTED"
	candidate_lines="$(grep -c 'CANDIDATO\[' "$RUN_OUTPUT" || true)"
	assert_equal "6" "$candidate_lines"
}

test_stash_guard() {
	local wt
	init_fixture "stash"
	wt="$CASE_DIR/wt-stash"
	add_clean_worktree "$wt" "feature/stash"
	printf 'stashed-change\n' >>"$HUB/base.txt"
	git_test -C "$HUB" stash push -qm "unique stash"
	git_test -C "$HUB" show-ref --verify --quiet refs/stash || fail "no se creó el stash"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "el repositorio tiene refs/stash"
}

test_local_process_cwd() {
	local wt ready pid attempt
	init_fixture "process-cwd"
	wt="$CASE_DIR/wt-process-cwd"
	add_clean_worktree "$wt" "feature/process-cwd"
	ready="$CASE_DIR/process-ready"
	(
		cd "$wt"
		: >"$ready"
		exec sleep 30
	) &
	pid=$!
	BACKGROUND_PIDS+=("$pid")
	for attempt in $(seq 1 100); do
		[ -f "$ready" ] && break
		sleep 0.01
	done
	[ -f "$ready" ] || fail "el proceso con cwd no llegó a arrancar"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "PID $pid tiene su cwd dentro del worktree"
	kill "$pid" >/dev/null 2>&1 || true
	wait "$pid" >/dev/null 2>&1 || true
}

test_invalid_size_output() {
	local wt
	init_fixture "invalid-size"
	wt="$CASE_DIR/wt-invalid-size"
	add_clean_worktree "$wt" "feature/invalid-size"
	RUN_DU_MODE="invalid"
	run_sut --dry-run
	assert_rc 0
	assert_path_exists "$wt"
	assert_contains "no se puede medir su tamaño completo"
}

# FASE 4 — el clon compartido y la rama por defecto. Cuatro casos porque la clase tiene cuatro
# estados distinguibles, y el que importa es el SEGUNDO: es el que ocurrió de verdad el
# 2026-08-21 y el que ninguna señal de las que se miran a diario cubre.
test_default_branch_held_by_clone() {
	init_fixture "rama-en-el-clon"
	run_sut --dry-run
	assert_rc 0
	assert_contains "CONSERVAR — el clon compartido tiene"
	assert_contains "falla con un fatal VISIBLE"
	assert_not_contains "EVIDENCIA — el clon compartido"
}

test_default_branch_held_by_worktree() {
	local wt
	init_fixture "rama-en-un-worktree"
	wt="$CASE_DIR/wt-tiene-main"
	# El orden ES el mecanismo: para que un worktree pueda tomar `main`, el clon tiene que
	# soltarla antes. Ese es exactamente el estado en que queda el clon compartido después.
	git_test -C "$HUB" checkout -q --detach HEAD
	git_test -C "$HUB" worktree add -q "$wt" main
	run_sut --dry-run
	assert_rc 0
	assert_contains "EVIDENCIA — el clon compartido NO puede estar en"
	assert_contains "$wt"
	assert_contains "no sostiene nada propio"
	assert_not_contains "CONSERVAR — el clon compartido tiene"
}

test_detached_clone_nobody_holds_default() {
	init_fixture "separado-sin-poseedor"
	git_test -C "$HUB" checkout -q --detach HEAD
	run_sut --dry-run
	assert_rc 0
	assert_contains "está en HEAD SEPARADO y nadie tiene"
	assert_contains "alguien lo separó a mano"
}

test_detached_clone_holds_unpublished_work() {
	init_fixture "separado-con-trabajo"
	git_test -C "$HUB" checkout -q --detach HEAD
	printf 'sin publicar\n' >"$HUB/sin-publicar.txt"
	git_test -C "$HUB" add sin-publicar.txt
	git_test -C "$HUB" commit -qm "trabajo que no esta en origin"
	run_sut --dry-run
	assert_rc 0
	assert_contains "SOSTIENE trabajo que no está publicado"
	assert_contains "No lo muevas"
	# La otra mitad del hallazgo: `git status` limpio NO habría enseñado esto.
	assert_equal "" "$(git_test -C "$HUB" status --porcelain)"
	assert_not_contains "no sostiene nada propio"
}

declare -a TEST_CASES=(
	source_surface
	all_arguments_no_mutation
	dry_run_no_mutation
	reproducible_ignored_artifacts
	ignored_env
	reproducible_artifacts_with_env
	unknown_ignored_file
	selftest_evidence_head
	hidden_untracked_config
	submodule_unique_ref
	reflog_detached
	ls_remote_failure
	deleted_remote_ref
	special_paths
	stash_guard
	local_process_cwd
	invalid_size_output
	default_branch_held_by_clone
	default_branch_held_by_worktree
	detached_clone_nobody_holds_default
	detached_clone_holds_unpublished_work
)

run_case() {
	local name="$1"
	CURRENT_TEST="$name"
	RUN_OUTPUT=""
	"test_$name"
	printf 'ok - %s\n' "$name"
}

if [ -n "$CASE_FILTER" ]; then
	known=0
	for test_name in "${TEST_CASES[@]}"; do
		if [ "$test_name" = "$CASE_FILTER" ]; then
			known=1
			run_case "$test_name"
			break
		fi
	done
	[ "$known" = "1" ] || fail "caso desconocido: $CASE_FILTER"
	exit 0
fi

for test_name in "${TEST_CASES[@]}"; do
	run_case "$test_name"
done

echo "hub-hygiene: ${#TEST_CASES[@]} regresiones superadas."
