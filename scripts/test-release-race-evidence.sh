#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-race-evidence.sh — battery for the phase-1 step
# "preflight — a green race-full ran on the EXACT tagged SHA" of .github/workflows/release.yml.
#
# WHY IT EXISTS. The step decides whether a tag may build on race evidence. On 2026-09-17 the
# v26.9 release added one admitted non-equality directly on olivaresai/olivares (7263d082e8,
# 37da5b582a): a green race-full on a commit whose diff to the tag is workflow-only. Ported to
# this repository, that first form carried two defects, both measured on the real commits (a local
# clone of 1d84bd1788 with the v26.8.0 green f443e0844d as the only candidate):
#
#   · `printf | grep -qvE` under pipefail ADMITTED a 2,986-file product diff (131 KB of names)
#     as "workflow-only". grep -q exits at the first non-workflow name, printf dies of SIGPIPE
#     above the pipe buffer, the pipeline answers 141, and `!` reads 141 as "no other file".
#   · `git fetch --depth=1` into the fetch-depth: 0 checkout made it SHALLOW. Run 35237680006
#     then logged "running against a shallow clone" and GoReleaser wrote previous_tag "".
#
# The battery runs the step's OWN `run:` block, extracted from the workflow, in a real git
# clone of a local origin. `gh` is a stub that answers the run history; git is real. Each
# defect has a mutant that restores it in a COPY of the block, and the matching case must go
# red: a case that cannot be shown red against the defect it names is not a witness.
#
# Hermetic: no network, no GitHub, nothing outside this run's temporary directory.
#
# NO `set -e` (battery reports through check()).
# SC2319: `check … $?` reads the status of the assertion just above it, on purpose.
# SC2016: the mutant literals are shell text of the workflow step; they must not expand here.
# shellcheck disable=SC2319,SC2016
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
_olivares_git_env="${ROOT}/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env
# The fixture must not read this user's git configuration (signing, hooks, url rewrites).
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

WF_REAL="${OLIVARES_RACE_EVIDENCE_WORKFLOW:-${ROOT}/.github/workflows/release.yml}"
STEP_NAME="preflight — a green race-full ran on the EXACT tagged SHA"

blind() {
	echo "test-release-race-evidence: NO HE PODIDO MIRAR: $*" >&2
	exit 2
}
for tool in git awk sed; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
[ -r "$WF_REAL" ] || blind "cannot read $WF_REAL"

# shellcheck source=/dev/null
. "$ROOT/scripts/lib/exec-workdir.sh" || blind "missing scripts/lib/exec-workdir.sh"
WORK="$(olivares_pick_exec_workdir olivares-race-evidence)" || blind "no candidate directory can create and EXECUTE a binary"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM
started="$(date +%s)"

pass=0
fail=0
failed_names=()
# FAIL starts at column 0, like the sibling batteries.
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok  %-66s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		failed_names+=("$1")
		printf 'FAIL  %-66s %s\n' "$1" "$2"
		printf '      last step: rc=%s\n' "${rc:-<none>}"
		[ -f "$WORK/out" ] && tail -n 6 "$WORK/out" | sed 's/^/      | /'
	fi
}
# Output assertions decide in-process: a `grep -q` behind a pipe is the defect this battery
# exists for, and it would make the oracle itself racy.
out_has() { # out_has <literal>
	local text
	text="$(cat "$WORK/out")"
	case "$text" in
	*"$1"*) return 0 ;;
	*) return 1 ;;
	esac
}

echo "release race-full evidence — the workflow's own step, a real git clone, a stub gh"

extract() { # extract <workflow> <step name>
	awk -v step="      - name: $2" '
		$0 == step { instep = 1; next }
		instep && /^      - name: / { instep = 0 }
		instep && $0 == "        run: |" { inrun = 1; next }
		inrun {
			if ($0 == "") { print ""; next }
			if ($0 !~ /^          /) { inrun = 0; instep = 0; next }
			print substr($0, 11)
		}
	' "$1"
}
extract "$WF_REAL" "$STEP_NAME" >"$WORK/step.sh"
[ -s "$WORK/step.sh" ] && [ "$(head -n 1 "$WORK/step.sh")" = "set -euo pipefail" ]
check "the step is extracted from the workflow and starts with set -euo pipefail" "real block" $?

# THE PREMISE OF THE SHALLOW REFUSAL, asserted on the YAML because a block cannot see the
# `uses:` above it: the job that runs this step checks out FULL history, which is what makes
# "the clone is shallow" a defect rather than the configured state. If someone sets a depth on
# that checkout, the refusal below starts firing on every release and this row says why.
_ck_line="$(command grep -n '      - uses: actions/checkout@' "$WF_REAL" | head -2 | tail -1 | cut -d: -f1)"
[ -n "$_ck_line" ] && _ck_with="$(command sed -n "$((_ck_line + 1)),$((_ck_line + 3))p" "$WF_REAL")" &&
	command grep -q 'fetch-depth: 0' <<<"$_ck_with"
check "the job that runs this step checks out full history" "fetch-depth: 0" $?

# --- stub gh ------------------------------------------------------------------------------------
# Answers the three queries the step makes, from files this battery writes per case.
mkdir -p "$WORK/bin"
cat >"$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
case "$*" in
*'select(.head_sha =='*) cat "$STUB_DIR/count" ;;
*'.workflow_runs[].head_sha'*) cat "$STUB_DIR/greens" ;;
*'.workflow_runs[0].head_sha'*) cat "$STUB_DIR/newest" ;;
*) echo "stub gh: unexpected call: $*" >&2; exit 9 ;;
esac
EOF
chmod +x "$WORK/bin/gh"

# --- fixture history ----------------------------------------------------------------------------
#   B  base: main.go, README, .github/workflows/release.yml
#   W  B + a release.yml change                (workflow-only)
#   P  W + a main.go change                    (product)
#   L  W + 3,000 product files, names > 64 KiB (product, above the pipe buffer)
#   S  B + .github/workflows/extra.yml         (workflow-only; only under refs/keep/, NOT cloned)
SRC="$WORK/src"
ORIGIN="$WORK/origin.git"
g() { git -C "$SRC" -c user.name=battery -c user.email=battery@example.invalid -c commit.gpgsign=false "$@"; }
{
	git init -q "$SRC" &&
		mkdir -p "$SRC/.github/workflows" &&
		printf 'package main\n' >"$SRC/main.go" &&
		printf 'readme\n' >"$SRC/README" &&
		printf 'name: release\n' >"$SRC/.github/workflows/release.yml" &&
		g add -- main.go README .github/workflows/release.yml &&
		g commit -q -m base &&
		B="$(g rev-parse HEAD)" &&
		printf 'name: release\n# runs-on changed\n' >"$SRC/.github/workflows/release.yml" &&
		g add -- .github/workflows/release.yml &&
		g commit -q -m workflow-only &&
		W="$(g rev-parse HEAD)" &&
		printf 'package main\n\nfunc main() {}\n' >"$SRC/main.go" &&
		g add -- main.go &&
		g commit -q -m product &&
		P="$(g rev-parse HEAD)"
} || blind "could not build the fixture history"
# L through plumbing: one blob, 3,000 index entries, no 3,000 file writes.
{
	blob="$(printf 'package padding\n' | g hash-object -w --stdin)" &&
		g read-tree "$W" &&
		for i in $(seq -w 1 3000); do
			printf '100644 %s\tinternal/padding/a-product-file-with-a-long-name-%s.go\n' "$blob" "$i"
		done | g update-index --index-info &&
		L_TREE="$(g write-tree)" &&
		L="$(g -c user.name=battery commit-tree "$L_TREE" -p "$W" -m large-product)" &&
		g read-tree "$B" &&
		printf 'name: extra\n' | g hash-object -w --stdin >"$WORK/extra.blob" &&
		printf '100644 %s\t.github/workflows/extra.yml\n' "$(cat "$WORK/extra.blob")" | g update-index --index-info &&
		S_TREE="$(g write-tree)" &&
		S="$(g commit-tree "$S_TREE" -p "$B" -m side-workflow-only)" &&
		g read-tree "$P"
} || blind "could not build the large and side commits"
names_bytes="$(g diff --name-only "$W" "$L" | wc -c)"
[ "$names_bytes" -gt 65536 ]
check "the large diff's name list is above the pipe buffer ($names_bytes bytes)" "the SIGPIPE shape" $?
{
	git init -q --bare "$ORIGIN" &&
		git -C "$ORIGIN" config uploadpack.allowAnySHA1InWant true &&
		git -C "$ORIGIN" fetch -q "$SRC" "$P:refs/heads/main" "$L:refs/heads/large" \
			"$B:refs/tags/v0" "$S:refs/keep/side"
} || blind "could not build the origin"

n=0
# run_case <head> <count> <greens...> — fresh full clone at <head>, the step run in it.
run_case() {
	local head="$1" count="$2"
	shift 2
	n=$((n + 1))
	CLONE="$WORK/clone-$n"
	STUB_DIR="$WORK/stub-$n"
	mkdir -p "$STUB_DIR"
	printf '%s\n' "$count" >"$STUB_DIR/count"
	printf '%s\n' "$@" >"$STUB_DIR/greens"
	printf '%s\n' "${1:-none}" >"$STUB_DIR/newest"
	if ! {
		git clone -q ${CLONE_DEPTH:+--depth "$CLONE_DEPTH"} "file://$ORIGIN" "$CLONE" 2>/dev/null &&
			git -C "$CLONE" fetch -q ${CLONE_DEPTH:+--depth "$CLONE_DEPTH"} origin "$head" 2>/dev/null &&
			git -C "$CLONE" -c advice.detachedHead=false checkout -q --detach "$head"
	}; then
		blind "could not prepare clone $n at $head"
	fi
	commits_before="$(git -C "$CLONE" rev-list --count HEAD)"
	(
		cd "$CLONE/${STEP_SUBDIR:-.}" &&
			PATH="$WORK/bin:$PATH" STUB_DIR="$STUB_DIR" GITHUB_REPOSITORY=olivaresai/olivares \
				GITHUB_SHA="$head" bash "${STEP_UNDER_TEST:-$WORK/step.sh}"
	) >"$WORK/out" 2>&1
	rc=$?
	shallow="$(git -C "$CLONE" rev-parse --is-shallow-repository)"
	commits_after="$(git -C "$CLONE" rev-list --count HEAD)"
}
full_clone_intact() { [ "$shallow" = "false" ] && [ "$commits_after" -eq "$commits_before" ]; }

# --- A: the equality rule --------------------------------------------------------------------------
run_case "$W" 1
[ "$rc" -eq 0 ] && out_has "race-full evidence exists for the exact tagged SHA $W"
check "A1 a green race-full on the exact tagged SHA admits the tag" "equality" $?
run_case "$P" 0 "$B"
[ "$rc" -ne 0 ] && out_has "::error::no successful race-full run exists on the tagged SHA $P"
check "A2 no exact green and only a product diff refuses" "equality stays the rule" $?

# --- B: the workflow-only admission -------------------------------------------------------------
run_case "$W" 0 "$B"
[ "$rc" -eq 0 ] && out_has "race-full evidence: green run on $B; diff to $W is workflow-only:" &&
	out_has "  .github/workflows/release.yml"
check "B1 a green ancestor whose diff is workflow-only admits, with SHA and file list" "7263d082e8" $?
! out_has "exists for the exact tagged SHA"
check "B2 a workflow-only admission does not claim exact-SHA evidence" "honest label" $?
full_clone_intact
check "B3 the clone stays full after an admission from a local object" "no --depth" $?
run_case "$W" 0 "$S"
[ "$rc" -eq 0 ] && out_has "race-full evidence: green run on $S;" && full_clone_intact
check "B4 a green commit missing locally is fetched and the clone stays full" "fetch, not shallow" $?
run_case "$P" 0 "$W"
[ "$rc" -ne 0 ] && ! out_has "is workflow-only"
check "B5 a small product diff is not admitted" "grep-free filter" $?
run_case "$L" 0 "$W"
[ "$rc" -ne 0 ] && ! out_has "is workflow-only"
check "B6 a product diff above the pipe buffer is not admitted" "the measured false admission" $?
STEP_SUBDIR=.github/workflows run_case "$L" 0 "$W"
[ "$rc" -ne 0 ] && ! out_has "is workflow-only"
check "B8 a product diff is refused even when the step runs from a subdirectory" "root-anchored filter" $?
run_case "$W" 0 "0123456789abcdef0123456789abcdef01234567" "$P"
[ "$rc" -ne 0 ]
check "B7 an unfetchable green and a product diff refuse" "could not look is not evidence" $?

# --- C: answers that are not evidence ------------------------------------------------------------
run_case "$W" ""
[ "$rc" -ne 0 ] && out_has "not a count"
check "C1 an empty count from the run history refuses" "not a silent pass" $?
run_case "$W" "null"
[ "$rc" -ne 0 ] && out_has "not a count"
check "C2 a non-numeric count refuses" "not a silent pass" $?
printf '#!/bin/sh\ntouch "%s/upload-pack-ran"\nexit 1\n' "$WORK" >"$WORK/bin/fake-upload-pack"
chmod +x "$WORK/bin/fake-upload-pack"
run_case "$W" 0 "--upload-pack=$WORK/bin/fake-upload-pack" "$P"
[ "$rc" -ne 0 ] && [ ! -e "$WORK/upload-pack-ran" ]
check "C3 a run-history value that is not a 40-hex SHA never reaches git" "no option injection" $?
rm -f "$WORK/upload-pack-ran"
CLONE_DEPTH=1 run_case "$W" 1
[ "$rc" -ne 0 ] && out_has "the release checkout is shallow"
check "C4 a shallow checkout refuses before GoReleaser reads its history" "previous_tag measured empty" $?

# --- M: mutants restore each defect in a COPY; the matching case must go red ------------------------
# mutate <name> <old literal> <new literal> [base]: writes $WORK/mutant-<name>.sh from the step
# (or from an earlier mutant), and fails if the literal is absent, so no mutant is vacuous.
mutate() {
	local src new
	src="$(cat "${4:-$WORK/step.sh}")"
	new="${src/"$2"/"$3"}"
	[ "$new" != "$src" ] || return 1
	printf '%s\n' "$new" >"$WORK/mutant-$1.sh"
}
NL=$'\n'
# The literals are the extracted block's text: the YAML indent (10 columns) is already removed.
FILTER_NEW='    outside="$(git diff --name-only "${green}" HEAD -- '"':/' ':(exclude,top).github/workflows/'"')"'"$NL"'    if [ -z "${outside}" ]; then'
FILTER_CWD='    outside="$(git diff --name-only "${green}" HEAD -- . '"':(exclude).github/workflows/'"')"'"$NL"'    if [ -z "${outside}" ]; then'
# FILTER_OLD is the measured defect itself, restored as TEXT into a copy of the step: it has to
# stay a pipe, because the pipe is what M1 proves causal. It is assembled from two halves so the
# line census of check-sigpipe-booleans.sh, which reads lines and not quotes, does not count a
# fixture string as a pipe this battery reads the rc of. The assembled value is byte-identical.
FILTER_OLD_PRODUCER="    if ! printf '%s\\n' \"\${changed}\" |"
FILTER_OLD="$FILTER_OLD_PRODUCER grep -qvE '^\\.github/workflows/'; then"
FETCH_NEW='    if ! git cat-file -e "${green}^{commit}" 2>/dev/null; then'"$NL"'      git fetch -q origin "${green}" 2>/dev/null || continue'"$NL"'    fi'
FETCH_OLD='    git fetch -q --depth=1 origin "${green}" 2>/dev/null || continue'
COUNT_GUARD='if ! [[ "${match}" =~ ^[0-9]+$ ]]; then'
SHALLOW_GUARD='if [ "$(git rev-parse --is-shallow-repository)" != "false" ]; then'
SHA_GUARD='    [[ "${green}" =~ ^[0-9a-f]{40}$ ]] || continue'

if mutate pipe "$FILTER_NEW" "$FILTER_OLD"; then
	STEP_UNDER_TEST="$WORK/mutant-pipe.sh" run_case "$L" 0 "$W"
	[ "$rc" -eq 0 ] && out_has "is workflow-only"
	check "M1 [mutant] the printf-into-grep -qv filter admits the large product diff" "B6 is causal" $?
else
	check "M1 [mutant] the git pathspec filter text is present" "mutant applied" 1
fi
if mutate depth "$FETCH_NEW" "$FETCH_OLD"; then
	STEP_UNDER_TEST="$WORK/mutant-depth.sh" run_case "$W" 0 "$B"
	! { [ "$rc" -eq 0 ] && full_clone_intact; }
	check "M2 [mutant] fetch --depth=1 cannot pass B1+B3" "B3 is causal" $?
	# and with the shallow refusal also removed, the mutant shows the clone itself going shallow
	if mutate depth-noguard "$SHALLOW_GUARD" 'if false; then' "$WORK/mutant-depth.sh"; then
		STEP_UNDER_TEST="$WORK/mutant-depth-noguard.sh" run_case "$W" 0 "$B"
		[ "$shallow" = "true" ]
		check "M3 [mutant] without both fixes the full clone really turns shallow" "the defect is real" $?
	else
		check "M3 [mutant] the shallow refusal text is present" "mutant applied" 1
	fi
else
	check "M2 [mutant] the local-object fetch text is present" "mutant applied" 1
fi
if mutate count "$COUNT_GUARD" 'if false; then'; then
	STEP_UNDER_TEST="$WORK/mutant-count.sh" run_case "$W" "null"
	[ "$rc" -eq 0 ]
	check "M4 [mutant] without the count guard a non-numeric answer passes" "C2 is causal" $?
else
	check "M4 [mutant] the count guard text is present" "mutant applied" 1
fi
if mutate shallow "$SHALLOW_GUARD" 'if false; then'; then
	STEP_UNDER_TEST="$WORK/mutant-shallow.sh" CLONE_DEPTH=1 run_case "$W" 1
	[ "$rc" -eq 0 ]
	check "M5 [mutant] without the shallow refusal a shallow checkout passes" "C4 is causal" $?
else
	check "M5 [mutant] the shallow refusal text is present" "mutant applied" 1
fi
if mutate cwd "$FILTER_NEW" "$FILTER_CWD"; then
	STEP_UNDER_TEST="$WORK/mutant-cwd.sh" STEP_SUBDIR=.github/workflows run_case "$L" 0 "$W"
	[ "$rc" -eq 0 ] && out_has "is workflow-only"
	check "M7 [mutant] a working-directory pathspec admits from a subdirectory" "B8 is causal" $?
else
	check "M7 [mutant] the root-anchored pathspec text is present" "mutant applied" 1
fi
if mutate sha "$SHA_GUARD" ':'; then
	STEP_UNDER_TEST="$WORK/mutant-sha.sh" run_case "$W" 0 "--upload-pack=$WORK/bin/fake-upload-pack" "$P"
	[ -e "$WORK/upload-pack-ran" ]
	check "M6 [mutant] without the SHA shape guard the value reaches git fetch" "C3 is causal" $?
else
	check "M6 [mutant] the SHA shape guard text is present" "mutant applied" 1
fi

echo ""
echo "== summary =="
printf 'pass=%d fail=%d duration=%ss\n' "$pass" "$fail" "$(($(date +%s) - started))"
if [ "$fail" -ne 0 ]; then
	printf 'failed:'
	for f in "${failed_names[@]}"; do printf ' %s' "$f"; done
	printf '\n'
	echo "test-release-race-evidence: RED"
	exit 1
fi
echo "test-release-race-evidence: OK — $pass cases, the workflow's own step in a real clone"
