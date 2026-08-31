#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-security-sig-guard.sh — battery for the PHASE-2 guard that refuses to finish
# the signing ceremony green while an UNSIGNED security manifest sits on the draft
# (`.github/workflows/release.yml`, step "attach verified OTA signature to the draft
# release").
#
# WHY IT RUNS THE REAL BLOCK. `scripts/test-release-wiring.sh` pins workflow SHAPE by
# grepping the YAML text, and it says so: a determined edit satisfies the string and breaks
# the semantics. The defect this battery exists for is invisible to that class of lint,
# because the YAML text was *correct-looking*: the guard's first `gh release view` sat in
# an `if` CONDITION, where `set -e` is suspended by POSIX rule. So this battery EXTRACTS the
# step's real `run:` script out of the workflow and EXECUTES it against a hermetic `gh`.
#
# THE ESCAPE IT WAS WRITTEN AGAINST, reproduced by case D below:
#
#     if gh release view … --jq '.assets[].name' | command grep -qx 'security-manifest.json'
#
#   `gh` fails (API 5xx, expired token, DNS) -> pipefail makes the pipeline non-zero -> the
#   `if` is false -> the block ends rc=0. A ceremony that COULD NOT LOOK reports the same
#   green as a ceremony that looked and found no security manifest: rule 5 of the canon,
#   "limpio / roto / NO HE PODIDO MIRAR", with the third collapsed into the first.
#
# BOTH DIRECTIONS ARE HERE ON PURPOSE. A guard that reddens on everything passes every
# "it refuses" test: cases A and G assert the ceremony still finishes GREEN when it should —
# an ordinary release, and a draft whose asset names merely RESEMBLE the security ones — and
# case F asserts the inventory is read exactly ONCE, since two `gh` reads of a mutable draft
# are two snapshots that can disagree, with the disagreement settled in favour of whichever
# call the code happened to trust.
#
# Case B is NOT in that list any more. Since 2026-08-09 a nominal `security-manifest.json.sig`
# does not authorize anything: any draft carrying security-manifest.json ends this ceremony
# red until #644 verifies the bytes under the OTA key. The green direction is carried by the
# releases that declare no security channel at all.
#
# Hermetic: a stub `gh` on PATH that never touches the network, a temp tree, no repository
# writes, nothing signed, no draft mutated.
#
# OLIVARES_SIGGUARD_WORKFLOW overrides the workflow read; the red-first proof points it at
# the pre-fix tree.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# GIT ISOLATION — same reason as test-release-ota-channel.sh: this battery builds a
# throwaway tree inside what may be a LINKED WORKTREE, where git exports GIT_DIR and GIT_DIR
# outranks `-C`. Inert while no git call exists here, correct the moment one is added.
_olivares_git_env="${ROOT}/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

WF="${OLIVARES_SIGGUARD_WORKFLOW:-$ROOT/.github/workflows/release.yml}"
STEP="attach verified OTA signature to the draft release"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-sigguard.XXXXXX")" || exit 1
# ${TMPDIR:-/tmp} may be mounted noexec (this dev container's /tmp is) and this battery runs
# a PATH-stubbed `gh`: there, execve returns EACCES and every case that reaches the stub
# fails while the rest pass — the exact half-green that made test-cosign-verified-mode.sh
# unrunnable on this host until it grew the same probe.
printf '#!/bin/sh\nexit 0\n' >"$WORK/.execprobe" && chmod +x "$WORK/.execprobe"
if ! "$WORK/.execprobe" >/dev/null 2>&1; then
	rm -rf "$WORK"
	WORK="$(mktemp -d "$ROOT/.tmpexec.XXXXXX")" || exit 1
fi
rm -f "$WORK/.execprobe"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT HUP INT TERM

pass=0
fail=0
failed_names=()
# The FAIL line starts at COLUMN 0, like test-release-ota-channel.sh and unlike the
# wiring battery: the mutation round attributes a kill by matching `^FAIL  *<witness>` in
# this output (an internal design note (not shipped)). Indent the word and every
# mutant here is reported MISATRIBUIDO — a battery that reddens correctly while the harness
# reads it as never reddening the case it named.
check() {
	if [ "$3" -eq 0 ]; then
		pass=$((pass + 1))
		printf '  ok  %-58s %s\n' "$1" "$2"
	else
		fail=$((fail + 1))
		failed_names+=("$1")
		printf 'FAIL  %-58s %s\n' "$1" "$2"
	fi
}

echo "phase-2 security-signature guard — the REAL run: block against a hermetic gh"

# --- extract the step's run: script ------------------------------------------------------
# The block is the payload; everything below asserts on ITS behaviour, so an extraction that
# silently yields nothing would make every case pass against an empty file. That is the
# "a proxy that is always true is not a probe" trap, so the extraction is asserted first.
BLOCK="$WORK/step.sh"
awk -v step="      - name: ${STEP}" '
	$0 == step { instep = 1; next }
	instep && /^      - name: / { instep = 0 }
	instep && $0 == "        run: |" { inrun = 1; next }
	inrun {
		if ($0 == "") { print ""; next }
		if ($0 !~ /^          /) { inrun = 0; instep = 0; next }
		print substr($0, 11)
	}
' "$WF" >"$BLOCK"

[ -s "$BLOCK" ] && [ "$(wc -l <"$BLOCK")" -ge 10 ]
check "the step's run: block was extracted from the workflow" "not an empty payload" $?
# THE PAYLOAD IS THE BLOCK PLUS THE SCRIPT IT CALLS. The custody half of this ceremony — the
# single draft read, the refusals, the staging, the upload and the rollback — moved into
# scripts/release-attach-stable-pair.sh because the `run:` had grown to 21,600 characters
# against GitHub's documented 21,000 limit (the model, 2026-08-15, P0-B). The cases below
# still EXECUTE the block, which invokes it; only the text assertions have to look in both
# files, and asserting them against the block alone would silently stop covering the code that
# moved.
CUSTODY="$ROOT/scripts/release-attach-stable-pair.sh"
[ -f "$CUSTODY" ]
check "the custody script the block invokes exists" "the payload is complete" $?
command grep -q 'bash scripts/release-attach-stable-pair.sh' "$BLOCK"
check "the block INVOKES the custody script" "same run:, same window" $?
PAYLOAD="$WORK/payload.sh"
cat "$BLOCK" "$CUSTODY" >"$PAYLOAD" 2>/dev/null
# EACH HALF IS IDENTIFIED IN ITS OWN FILE, never in the concatenation. Asserting "the payload
# mentions gh release upload" would pass on an EMPTY block, because the script alone satisfies
# it — measured against main's workflow, where this step does not exist at all. So the block is
# asked for the guard half and the script for the upload half.
command grep -q 'status --porcelain' "$BLOCK" && command grep -q 'release-ota-channel.sh' "$BLOCK"
check "the extracted block is the ceremony's guard half" "real block, not a fragment" $?
command grep -q 'gh release upload' "$CUSTODY" && command grep -q 'security-manifest.json' "$CUSTODY"
check "the custody script is the ceremony's upload half" "the moved code, not a stub" $?
bash -n "$BLOCK" 2>/dev/null && bash -n "$CUSTODY" 2>/dev/null
check "the extracted block is syntactically runnable bash" "bash -n" $?
# --- STRUCTURAL: nothing runs between the tree check and the first sensitive read ----------
# The check proves the workspace matches its commit AT THE MOMENT IT RUNS. If a step boundary
# fell between it and the read of the release rule, everything it proved would be stale by the
# time it mattered: a `uses:` there is arbitrary code, in this workspace, after the check and
# before the rule is read.
#
# A `run:` scalar CANNOT contain a step. So proving both anchors live in the SAME extracted
# block, in that order, is proving there is no `uses:` and no other step between them — and it
# is checked here, before the early-exit gate, so a mutant that interposes a step reddens THIS
# rather than truncating the block and skipping the assertion.
# Positions are taken on the EXECUTABLE lines only. Both anchors are named in the comments
# that explain them, and the comment for the check mentions the script it protects — so a
# naive grep put the "read" before the check and the order assertion failed on prose.
_exec_only="$(command grep -vE '^[[:space:]]*#' "$BLOCK")"
chk_line="$(printf '%s\n' "$_exec_only" | command grep -n 'status --porcelain' | head -1 | cut -d: -f1)"
read_line="$(printf '%s\n' "$_exec_only" | command grep -n 'release-ota-channel.sh' | head -1 | cut -d: -f1)"
[ -n "$chk_line" ] && [ -n "$read_line" ]
check "the tree check and the rule read are in the SAME step" "no step boundary between them" $?
[ -n "$chk_line" ] && [ -n "$read_line" ] && [ "$chk_line" -lt "$read_line" ]
check "the tree is checked BEFORE the rule is read" "order, not mere presence" $?
# AND THE CUSTODY SCRIPT IS INSIDE THAT SAME WINDOW. Reducing the scalar under the platform
# limit could have been done by cutting the step in two — and cutting it anywhere between the
# `git status` and the tracked code it protects would have destroyed the only thing the check
# proves, since a `uses:` can be scheduled at a step boundary but not inside a scalar. So the
# invocation is asserted to live in THIS block, after the check: a split that moves it into a
# second step reddens here.
custody_line="$(printf '%s\n' "$_exec_only" | command grep -n 'release-attach-stable-pair.sh' | head -1 | cut -d: -f1)"
[ -n "$custody_line" ] && [ -n "$chk_line" ] && [ "$chk_line" -lt "$custody_line" ]
check "the custody script runs INSIDE the checked window" "no step boundary before it" $?

if [ "$fail" -ne 0 ]; then
	echo "extraction failed — every case below would assert against nothing" >&2
	printf 'pass=%d fail=%d\n' "$pass" "$fail"
	exit 1
fi

# --- the hermetic gh ---------------------------------------------------------------------
# It records every invocation (so "how many times was the inventory read" is measurable),
# answers `release view` from GH_STUB_ASSETS, and can fail like the real one does.
mkdir -p "$WORK/bin" || exit 1
cat >"$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
# ARGV IS RECORDED BY BOUNDARY, never as "$*". Joining with spaces erases where one argument
# ends and the next begins, so `gh "v26.8.0 --json" assets` and `gh v26.8.0 --json assets`
# produce the SAME "$*" — the stub would accept an argv the real CLI never receives, and the
# check reading that joined line would call it exact (the model re-audit, P3-04). Each
# argument is logged on its own line under a header, and compared one by one.
{
	printf 'ARGV %s\n' "$#"
	for _a in "$@"; do printf 'ARG %s\n' "$_a"; done
} >>"${GH_STUB_LOG}"
case "${1:-} ${2:-}" in
"release view")
	# THE QUERY ITSELF IS PART OF THE CONTRACT. A stub that answers whatever it is asked
	# cannot see a guard that reads the WRONG RELEASE or asks for the wrong field: the
	# inventory would come back plausible and the guard would rule on another tag's assets.
	# Anything but the exact expected query answers EMPTY — which is precisely the fail-open
	# shape (no manifest -> green) the cases below then catch.
	# The selector is checked by PROPERTY, not by equality with itself. Extracting the whole
	# expression from the block and comparing it back is an always-true probe: a mutant that
	# changes `.assets[].name` to `.assets[].url` moves BOTH sides and the stub answers
	# happily — measured, the mutant escaped. So the stub demands the three things the guard
	# cannot work without, and tolerates harmless reformatting of the rest.
	# TWO DIFFERENT QUERIES. The inventory read asks for assets AND the draft/immutable
	# posture; the rollback asks only for asset names, to see what the failed upload left
	# behind. Applying the inventory's selector contract to both made the stub answer the
	# rollback with nothing, and an empty answer reads as "the asset is not there" — the
	# stub inventing the very fail-open under test.
	_jsonarg=""
	_prev=""
	for _a in "$@"; do
		[ "$_prev" = "--json" ] && _jsonarg="$_a"
		_prev="$_a"
	done
	if [ "$_jsonarg" = "assets" ]; then
		if [ -n "${GH_STUB_REMOTE:-}" ]; then
			for _f in "${GH_STUB_REMOTE}"/*; do
				[ -e "$_f" ] || continue
				printf '%s\n' "${_f##*/}"
			done
		fi
		exit "${GH_STUB_POSTVIEW_RC:-0}"
	fi
	_jq="${!#}"
	case "$_jq" in
	*".assets[].name"*) ;;
	*) echo "gh stub: selector does not ask for asset NAMES: $_jq" >&2; exit 0 ;;
	esac
	case "$_jq" in *isDraft*) ;; *) echo "gh stub: selector omits isDraft" >&2; exit 0 ;; esac
	case "$_jq" in *isImmutable*) ;; *) echo "gh stub: selector omits isImmutable" >&2; exit 0 ;; esac
	_want=(release view "${GH_STUB_EXPECT_TAG:-v26.8.0}" --json assets,isDraft,isImmutable --jq "$_jq")
	_ok=1
	[ "$#" -eq "${#_want[@]}" ] || _ok=0
	if [ "$_ok" -eq 1 ]; then
		_i=0
		for _a in "$@"; do
			[ "$_a" = "${_want[$_i]}" ] || _ok=0
			_i=$((_i + 1))
		done
	fi
	if [ "$_ok" -ne 1 ]; then
		echo "gh stub: WRONG ARGV ($# args)" >&2
		exit 0
	fi
	if [ "${GH_STUB_VIEW_RC:-0}" -ne 0 ]; then
		# Deliberately worded so it shares NO substring with the block's own diagnosis:
		# the first draft of this battery asserted on "could not read" and PASSED against
		# the unfixed block, because it was matching THIS line on stderr rather than
		# anything the guard printed. An always-true probe is not a probe.
		echo "gh: HTTP 503 Service Unavailable (stub, rc ${GH_STUB_VIEW_RC})" >&2
		exit "${GH_STUB_VIEW_RC}"
	fi
	# The listing comes from a FILE when one is named: an inventory big enough to make the
	# SIGPIPE case deterministic (megabytes) does not fit in the environment — execve
	# returns E2BIG long before that — and `cat` is an EXTERNAL producer, which is what
	# `gh` is. A shell builtin here would not reproduce the class: measured, a builtin
	# producer took the broken pipe 1 time in 5 while an external one is decided by
	# payload size.
	printf 'DRAFT %s\n' "${GH_STUB_IS_DRAFT:-true}"
	printf 'IMMUTABLE %s\n' "${GH_STUB_IS_IMMUTABLE:-false}"
	if [ -n "${GH_STUB_REMOTE:-}" ]; then
		for _f in "${GH_STUB_REMOTE}"/*; do
			[ -e "$_f" ] || continue
			printf 'ASSET %s\n' "${_f##*/}"
		done
		exit 0
	fi
	if [ -n "${GH_STUB_ASSETS_FILE:-}" ]; then
		exec sed 's/^/ASSET /' -- "${GH_STUB_ASSETS_FILE}"
	fi
	if [ -n "${GH_STUB_ASSETS:-}" ]; then printf '%s\n' "${GH_STUB_ASSETS}" | sed 's/^/ASSET /'; fi
	exit 0
	;;
"release delete-asset")
	# The draft is a STATE, not a call log. Without this the battery could only count
	# invocations, so a rollback that left an asset it had created behind looked identical to
	# one that removed it (the model, exact audit of 533563e48, second P1).
	# argv is: release delete-asset <tag> <asset> --yes — the ASSET is $4, and deleting $3
	# quietly removed nothing while the case still read "rollback done".
	rm -f "${GH_STUB_REMOTE}/${4:-}"
	exit "${GH_STUB_DELETE_RC:-0}"
	;;
"release download")
	# models `gh release download --dir <d> --pattern …`: writes the files it is asked for so
	# the staging copy exists, unless told to fail.
	if [ "${GH_STUB_DOWNLOAD_RC:-0}" -ne 0 ]; then exit "${GH_STUB_DOWNLOAD_RC}"; fi
	_dir=""
	_prev=""
	for _a in "$@"; do
		[ "$_prev" = "--dir" ] && _dir="$_a"
		_prev="$_a"
	done
	if [ -n "$_dir" ]; then
		mkdir -p "$_dir"
		printf 'PREVIOUS-MANIFEST\n' >"$_dir/stable-manifest.json"
		printf 'PREVIOUS-SIG\n' >"$_dir/stable-manifest.json.sig"
	fi
	exit 0
	;;
"release upload")
	# gh uploads assets CONCURRENTLY, so a failed call can still have created some of them.
	# GH_STUB_UPLOAD_PARTIAL=n means "the first n succeed, then the call fails" — which is the
	# state the exact-snapshot rollback has to undo.
	if [ -n "${GH_STUB_REMOTE:-}" ]; then
		mkdir -p "${GH_STUB_REMOTE}"
		_i=0
		for _a in "$@"; do
			case "$_a" in release | upload | --clobber | -*) continue ;; esac
			case "$_a" in v*.*.*) continue ;; esac
			[ -f "$_a" ] || [ -e "$_a" ] || continue
			if [ -n "${GH_STUB_UPLOAD_PARTIAL:-}" ] && [ "$_i" -ge "${GH_STUB_UPLOAD_PARTIAL}" ]; then
				break
			fi
			cp -- "$_a" "${GH_STUB_REMOTE}/${_a##*/}" 2>/dev/null || :
			_i=$((_i + 1))
		done
	fi
	[ -n "${GH_STUB_UPLOAD_PARTIAL:-}" ] && exit 1
	exit "${GH_STUB_UPLOAD_RC:-0}"
	;;
esac
echo "gh stub: unexpected invocation: $*" >&2
exit 97
EOF
chmod +x "$WORK/bin/gh" || exit 1

# --- the fixture CHECKOUT ------------------------------------------------------------------
# The block now authorizes on `release/advisories/<version>.txt` from the tagged checkout, so
# the battery owns a tree where that declaration can be present or absent. The classifier is
# the REAL one, copied in: a stubbed classifier here would test the battery's opinion of the
# rule instead of the rule.
mkdir -p "$WORK/root/scripts" "$WORK/root/release/advisories" || exit 1
cp "$ROOT/scripts/release-ota-channel.sh" "$WORK/root/scripts/" || exit 1
# The custody script the block invokes, for the same reason and copied the same way: the block
# runs it from the CHECKOUT, so the fixture has to be a checkout that carries it. It is
# committed by _restamp() below (`git add -A -- scripts …`), which is also what keeps the
# fixture tree clean — an uncommitted copy here would make every integrity check refuse.
cp "$ROOT/scripts/release-attach-stable-pair.sh" "$WORK/root/scripts/" || exit 1
# The declaration is COMMITTED, because that is what it is in the real repository: a file
# reviewed in the tagged commit. Leaving it untracked would make every fixture tree dirty and
# the integrity check would refuse every case — a battery red for a reason that has nothing to
# do with what it tests. Amending keeps one commit, so HEAD stays the object the tag names.
# PHASE-1 EVIDENCE, as the ceremony now demands it: release-commit.txt on the draft, and its
# digest inside the checksums.txt the job cosign-verified earlier. Written from the fixture's
# real HEAD so the happy path is genuinely consistent, and rewritten whenever the fixture
# commit changes — stale evidence would refuse every case for a reason unrelated to the test.
_write_evidence() { # _write_evidence [oid-to-claim]
	local oid="${1:-$FIXTURE_OID}" sum
	mkdir -p "$WORK/root/ota-dist"
	printf '%s\n' "$oid" >"$WORK/root/ota-dist/release-commit.txt"
	sum="$(sha256sum "$WORK/root/ota-dist/release-commit.txt" | cut -d' ' -f1)"
	printf '%s  release-commit.txt\n' "$sum" >"$WORK/root/ota-dist/checksums.txt"
}
_restamp() {
	(
		cd "$WORK/root" || exit 1
		# ONLY THE SOURCE PATHS. `git add -A` would commit ota-dist/ now that the fixture
		# uses the repository's real ignore rules, and then the generated artifact would be
		# TRACKED here while it is untracked in CI — the fixture drifting from production in
		# the opposite direction to the invented .gitignore it replaced.
		git add -A -- scripts release .gitignore >/dev/null 2>&1
		git commit -q --amend --no-edit >/dev/null 2>&1
		git tag -f v26.8.0 >/dev/null 2>&1
	)
	FIXTURE_OID="$(git -C "$WORK/root" rev-parse HEAD)"
	_write_evidence
}
declare_security() {
	printf 'CVE-2026-0001\n' >"$WORK/root/release/advisories/26.8.0.txt"
	_restamp
}
declare_none() {
	rm -f "$WORK/root/release/advisories/26.8.0.txt"
	_restamp
}

# A REAL repository, because the block now binds itself to an OID: it asks git what
# `refs/tags/<tag>` resolves to and what HEAD is. A fixture that is not a repo would make
# every case fail identically at `git rev-parse`, which is a battery that tests nothing.
# git-env.sh above is what keeps `git init` here from acting on the LIVE repository through an
# inherited GIT_DIR.
(
	cd "$WORK/root" || exit 1
	git init -q . >/dev/null 2>&1
	# THE REPOSITORY'S OWN .gitignore, copied — never one invented here. The line this
	# replaces wrote `ota-staging/ ota-dist/ dist/` and claimed it mirrored the real file;
	# the real file has one matching rule, `/dist/`. That invention made this battery green
	# over a workflow that refused every release, because the guards saw the very paths the
	# positive flow creates. A fixture more forgiving than production is not a fixture.
	cp "$ROOT/.gitignore" .gitignore 2>/dev/null || :
	git config user.email fixture@example.invalid
	git config user.name fixture
	git config commit.gpgsign false
	git add -A >/dev/null 2>&1
	git commit -q -m "fixture release commit" >/dev/null 2>&1
	git tag v26.8.0
) || exit 1
declare_none
[ -n "$FIXTURE_OID" ] && [ "${#FIXTURE_OID}" -eq 40 ]
check "the fixture checkout is a real commit the tag resolves to" "OID binding is testable" $?
# a SECOND commit the tag can be moved to, for the moved-tag direction
# The OID is written out by the subshell that creates it: deriving it afterwards from a branch
# NAME assumes what `git init` called the default branch, and when that guess missed, the
# override fell back to the fixture commit and the moved-tag case passed while testing nothing.
(
	cd "$WORK/root" || exit 1
	printf '# later\n' >>scripts/release-ota-channel.sh
	# source paths only, for the same reason as _restamp: an `-A` here tracked ota-dist/ in
	# this second commit, and checking the first one back out then DELETED the evidence the
	# ceremony reads — every case afterwards refused for a missing file the fixture had made.
	git add -A -- scripts release .gitignore >/dev/null 2>&1
	git commit -q -m "a later commit" >/dev/null 2>&1
	git rev-parse HEAD >"$WORK/other-oid"
	git checkout -q "$FIXTURE_OID" 2>/dev/null
) || exit 1
OTHER_OID="$(cat "$WORK/other-oid" 2>/dev/null)"
[ -n "$OTHER_OID" ] && [ "$OTHER_OID" != "$FIXTURE_OID" ]
check "the fixture has a SECOND distinct commit" "the moved-tag case is real" $?

n=0
rc=0
out=""
log=""
# The PATH the block runs under. Overridden by the cases that prove the guard does not
# depend on any external text tool — see "no external parser" below.
BLOCK_PATH="$WORK/bin:/usr/bin:/bin"
RELEASE_COMMIT_OVERRIDE=""
REMOTE_DIR=""
# The jq the block sends is EXTRACTED from the payload, not retyped here: a hand-copied
# expectation drifts silently the first time the query changes, and then the stub answers a
# query nobody sends while the assertion still passes. It reads the BLOCK PLUS the custody
# script, because the inventory read moved into the latter — pointed at the block alone this
# would come back empty and the check below is what says so.
EXPECT_JQ="$(sed -n "s/.*--jq '\(.*\)')\"\$/\1/p" "$PAYLOAD" | head -1)"
if [ -z "$EXPECT_JQ" ]; then
	EXPECT_JQ="$(command grep -o -- "--jq '[^']*'" "$PAYLOAD" | head -1 | sed "s/^--jq '//; s/'$//")"
fi
[ -n "$EXPECT_JQ" ]
check "the jq the block sends was extracted from it" "expectation cannot drift" $?
# run_block <assets-listing> [VAR=VAL …]
run_block() {
	n=$((n + 1))
	local assets="$1"
	shift
	log="$WORK/ghlog.$n"
	: >"$log"
	(cd "$WORK/root" && env -i PATH="$BLOCK_PATH" HOME="$WORK" \
		RELEASE_TAG="v26.8.0" RELEASE_COMMIT="${RELEASE_COMMIT_OVERRIDE:-$FIXTURE_OID}" \
		GH_TOKEN="stub-token" GH_STUB_EXPECT_JQ="$EXPECT_JQ" \
		GH_STUB_LOG="$log" GH_STUB_ASSETS="$assets" GH_STUB_REMOTE="${REMOTE_DIR:-}" "$@" \
		bash "$BLOCK") >"$WORK/out.$n" 2>&1
	rc=$?
	out="$(cat "$WORK/out.$n")"
}
# run_block_file <assets-file> [VAR=VAL …] — for inventories too large for the environment.
run_block_file() {
	n=$((n + 1))
	local file="$1"
	shift
	log="$WORK/ghlog.$n"
	: >"$log"
	(cd "$WORK/root" && env -i PATH="$BLOCK_PATH" HOME="$WORK" \
		RELEASE_TAG="v26.8.0" RELEASE_COMMIT="${RELEASE_COMMIT_OVERRIDE:-$FIXTURE_OID}" \
		GH_TOKEN="stub-token" GH_STUB_EXPECT_JQ="$EXPECT_JQ" \
		GH_STUB_LOG="$log" GH_STUB_ASSETS_FILE="$file" GH_STUB_REMOTE="${REMOTE_DIR:-}" "$@" \
		bash "$BLOCK") >"$WORK/out.$n" 2>&1
	rc=$?
	out="$(cat "$WORK/out.$n")"
}
# One `release view` invocation logs one `ARG view` boundary record; `release upload` logs
# `ARG upload`. Counting the record, not a joined line, keeps this honest after the argv
# format changed underneath it — which it did, and these two assertions caught it.
views() { command grep -c '^ARG view$' "$log"; }
# argv_record <verb> — print the boundary record whose second argument is <verb>. Reading by
# OFFSET broke the moment a read moved ahead of the upload; reading by verb does not care.
argv_record() {
	awk -v verb="$1" '
		/^ARGV / {
			# `exit` runs END, so without the printed flag the record comes out TWICE and the
			# comparison fails against a value that is right, doubled.
			if (found) { print rec; printed = 1; exit }
			rec = $0; n = 0; found = 0; next
		}
		/^ARG / {
			n++; rec = rec "\n" $0
			if (n == 2 && $2 == verb) found = 1
			next
		}
		END { if (found && !printed) print rec }
	' "$log"
}

# --- A · no security manifest on the draft: the ordinary release, GREEN ------------------
run_block $'olivares_26.8.0_linux_amd64.tar.gz\nchecksums.txt\nstable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -eq 0 ]
check "no security manifest -> the ceremony finishes green" "ordinary release" $?

# --- B · the NOMINAL pair: RED ------------------------------------------------------------
# This section said "the signed pair: GREEN" and argued the guard must not block #644's
# landing. That prose outlived the decision it described and contradicted the assertion two
# lines below it — the most expensive kind of stale comment, because it explains the opposite
# of what the code enforces. A `.sig` FILENAME is not a signed pair: nothing here reads those
# bytes. Red until #644 integrates real verification.
# ⛔ DENY-CLOSED (the commerce lane decision, 2026-08-09). A NOMINAL SIGNATURE DOES NOT
# AUTHORIZE: an asset called security-manifest.json.sig proves a filename, not that anything
# verified those bytes. Phase 1b re-clobbers the JSON only, so a signature over a PREVIOUS
# manifest keeps the right name; a zero-byte file does too. Until #644 verifies bytes under
# the OTA key, ANY draft carrying security-manifest.json ends this ceremony red.
run_block $'stable-manifest.json\nstable-manifest.json.sig\nsecurity-manifest.json\nsecurity-manifest.json.sig'
[ "$rc" -ne 0 ]
check "a NOMINAL .sig does not authorize the ceremony" "deny-closed until #644" $?
# have_signature no longer changes the exit code, so the DIAGNOSIS is the only place its
# guards remain observable — and therefore the only place their mutants can be witnessed.
# The operator's next action differs between the two, which is why both are pinned.
printf '%s' "$out" | command grep -q 'BY NAME'
check "the refusal says the .sig counted for its NAME only" "the two refusals differ" $?

# --- C · manifest without signature: RED -------------------------------------------------
run_block $'stable-manifest.json\nstable-manifest.json.sig\nsecurity-manifest.json'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'no security-manifest.json.sig on the draft at all'
check "security manifest with NO signature -> red, and says so" "the half-state" $?

# --- D · THE ESCAPE · gh cannot read the inventory: RED ----------------------------------
# Before the fix this finished rc=0 with no output at all: the `if` condition swallowed the
# failure and the step was indistinguishable from case A.
run_block "" GH_STUB_VIEW_RC=1
[ "$rc" -ne 0 ]
check "gh FAILS to read the inventory -> red, never a silent skip" "could-not-look" $?
# The prose IS pinned here, unlike the classifier battery's refusal messages, and for a
# reason: the whole finding is that this red must be DISTINGUISHABLE from the other red.
# A guard that fails closed with the "missing signature" message when the truth is "the API
# was down" sends the custodian to extend the ceremony over an outage.
printf '%s' "$out" | command grep -q 'could not read the draft'
check "the failure says it could not LOOK, not that there was nothing" "diagnosis, not silence" $?
[ "$rc" -ne 0 ] && ! printf '%s' "$out" | command grep -q 'carries security-manifest.json with NO'
check "a read failure is NOT reported as a missing signature" "the two reds stay apart" $?

# --- E · a different gh failure code is still a failure ----------------------------------
# Guards against a fix that only recognises rc=1 (gh exits 4 on auth failure).
run_block "" GH_STUB_VIEW_RC=4
[ "$rc" -ne 0 ]
check "a NON-1 gh exit code is still could-not-look" "auth failure, rc 4" $?

# --- F · exactly ONE inventory read ------------------------------------------------------
# Two `gh release view` calls are two snapshots of a MUTABLE draft: an asset can appear or
# vanish between them, and the guard would then decide on a state that never existed whole.
run_block $'stable-manifest.json\nsecurity-manifest.json\nsecurity-manifest.json.sig'
[ "$(views)" -eq 1 ]
check "the asset inventory is read EXACTLY once" "one snapshot, not two" $?
# Compared ARGUMENT BY ARGUMENT against the boundary-delimited record, not against a joined
# line: `gh "v26.8.0 --json" assets` and the correct call share a joined form, so a joined
# assertion would bless an argv the real CLI never sees.
want_argv="$(printf 'ARGV 7\nARG release\nARG view\nARG v26.8.0\nARG --json\nARG assets,isDraft,isImmutable\nARG --jq\nARG %s' "$EXPECT_JQ")"
[ "$(argv_record view)" = "$want_argv" ]
check "read for THIS tag, with the fields it decides on" "argv by boundaries" $?
# Pinned by PROPERTY here too, for the same reason: an expectation lifted out of the block
# cannot notice the block changing. These three are what the guard decides on.
sel="$(argv_record view | tail -1)"
case "$sel" in *".assets[].name"*) r1=0 ;; *) r1=1 ;; esac
case "$sel" in *isDraft*) r2=0 ;; *) r2=1 ;; esac
case "$sel" in *isImmutable*) r3=0 ;; *) r3=1 ;; esac
[ "$r1" -eq 0 ] && [ "$r2" -eq 0 ] && [ "$r3" -eq 0 ]
check "the selector asks for asset NAMES, isDraft and isImmutable" "not merely some field" $?

# --- G · exact names, both directions ----------------------------------------------------
run_block $'stable-manifest.json\nsecurity-manifest.json.txt\nxsecurity-manifest.json\nsecurity-manifest.json.sig.bak'
[ "$rc" -eq 0 ]
check "near-miss asset names are NOT the security manifest" "exact match, no firing" $?

run_block $'stable-manifest.json\nsecurity-manifest.json\nsecurity-manifest.json.sig.bak'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'at all'
check "a near-miss .sig does NOT satisfy the signature" "exact match, no excusing" $?

# --- I · the pipe that eats its own match -------------------------------------------------
# `gh … | command grep -qx NAME` reads as equivalent to the here-string the guard uses, and
# is not: `-q` exits at the FIRST match, the producer is SIGPIPEd, and under `pipefail` the
# pipeline reports FAILURE — so a MATCH becomes "no match" whenever the matched name is not
# the last one gh prints. Measured on the pre-fix block with a realistic draft (the security
# manifest among 64 assets, unsigned): 3 of 200 runs finished GREEN over exactly the state
# the guard exists to catch, and 197 refused. A 1.5% silent pass is the worst shape a
# release guard can have — it is green almost always, so it looks verified.
#
# TURNING THAT RACE INTO A CERTAINTY IS THE WHOLE JOB OF THIS CASE, and the first attempt
# got it wrong: an 80 KB inventory left the outcome a coin-flip (measured, external
# producer under pipefail: fail-open 4 of 20 runs), so the case passed while a pipe-shaped
# mutant went undetected — the mutation round reported that escape rather than review. What
# decides it is whether the producer still has bytes to write when the `-q` consumer
# leaves, and that is a function of SIZE against the pipe buffer:
#
#     80 KB    fail-open  4/20      still a race — useless as a witness
#     5.7 MB   fail-open 20/20      decided, every run
#
# So the inventory here is deliberately unrealistic in SIZE and realistic in SHAPE (the
# manifest first, unsigned, a long tail behind it). It is a regression detector for the
# pipe form, not a scenario: the 3-in-200 measurement on a 64-asset draft is the evidence
# that the class is real at production sizes. Passed by FILE, not by environment — an
# inventory this size fails execve with E2BIG.
big="$WORK/big-inventory.txt"
{
	printf 'security-manifest.json\n'
	seq -f 'olivares_26.8.0_asset_%g.tar.gz' 1 200000
} >"$big"
run_block_file "$big"
[ "$rc" -ne 0 ]
check "unsigned manifest EARLY in a long inventory still refuses" "no SIGPIPE fail-open" $?

# --- J · the guard must not depend on an external text tool -------------------------------
# Three fail-open modes of `if command grep -qx NAME <<<"$assets"`, all measured on this
# host, all of which end with have_manifest=0 and a GREEN ceremony over an unsigned
# manifest. They are asserted separately because they are closed by different properties.

# J1 · `-x` is not literal: `.` is a regex metacharacter.
#   measured: grep -qx 'security-manifest.json.sig' MATCHES 'security-manifestXjsonYsig'.
# A draft asset with that name would SATISFY the signature requirement.
run_block $'stable-manifest.json\nsecurity-manifest.json\nsecurity-manifestXjsonYsig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'at all'
check "a REGEX near-miss does not satisfy the signature" "names compared literally" $?

# J2 · the guard invokes NO grep at all — asserted on the block, not by hiding grep.
# It used to be measured by running with a grep-less PATH, and that case became unreachable
# the moment the block started refusing a `git` that PATH resolves anywhere but /usr/bin: a
# fixture cannot both hide grep and present git at its real path. The property is the same and
# the structural form is stronger — no grep ANYWHERE in the payload, rather than one PATH in
# which it happened not to matter. The erroring-grep cases below keep the behavioural half.
#
# ⛔ AND IT IS ASKED OF THE PAYLOAD, NOT OF THE BLOCK. The parser this case is about — the read
# loop over the inventory — moved into scripts/release-attach-stable-pair.sh with the rest of
# the custody ceremony, so `$_exec_only`, which is the BLOCK alone, no longer contains the code
# this case names. Measured: the mutant that puts `command grep -qx` back in place of the loop
# left this case GREEN while reddening five others, i.e. the assertion had stopped covering its
# own subject — exactly the failure the header of this file warns about ("asserting them
# against the block alone would silently stop covering the code that moved"). The parked-anchor
# audit of 2026-08-15 found it by planting that mutant.
_exec_only_payload="$(command grep -vE '^[[:space:]]*#' "$PAYLOAD")"
! printf '%s\n' "$_exec_only_payload" | command grep -qE '(^|[^-[:alnum:]_])grep '
check "the guard invokes no grep at all" "no external parser, structurally" $?

# J3 · a grep that CANNOT DECIDE (exit 2 is its "read/locale failure" code, 127 its absence).
# Shadowed first on PATH, so any surviving dependence on it is exercised rather than assumed.
mkdir -p "$WORK/badbin" || exit 1
printf '#!/bin/sh\nexit 2\n' >"$WORK/badbin/grep" && chmod +x "$WORK/badbin/grep"
BLOCK_PATH="$WORK/badbin:$WORK/bin:/usr/bin:/bin"
run_block $'stable-manifest.json\nsecurity-manifest.json'
[ "$rc" -ne 0 ]
check "a grep that errors (rc 2) cannot make the ceremony green" "error != no match" $?
printf '#!/bin/sh\nexit 127\n' >"$WORK/badbin/grep" && chmod +x "$WORK/badbin/grep"
run_block $'stable-manifest.json\nsecurity-manifest.json'
[ "$rc" -ne 0 ]
check "a grep that is broken (rc 127) cannot make the ceremony green" "error != no match" $?
rm -rf "$WORK/badbin"
BLOCK_PATH="$WORK/bin:/usr/bin:/bin"

# J4 · a name that only matches after a trailing CR is stripped is UNCLASSIFIABLE, and the
# third answer is the honest one. Whether gh can ever emit CRLF here is not a claim this
# repository has a source for, so the guard neither assumes LF nor silently accepts CR.
run_block $'stable-manifest.json\nsecurity-manifest.json\r'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'trailing CR'
check "a CR-suffixed security name refuses as unclassifiable" "the third answer" $?
# The SIGNATURE half of the same guard, asserted on its own: with the manifest name exact and
# only the .sig carrying the CR, the manifest alternative cannot be what refuses. Without
# this, a mutant removing just the .sig alternative survives.
run_block $'stable-manifest.json\nsecurity-manifest.json\nsecurity-manifest.json.sig\r'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'trailing CR'
check "a CR-suffixed SIGNATURE name refuses too" "both halves, separately" $?

# --- L · authorization rests on the TAG'S DECLARATION, not on the draft snapshot ----------
# THE RACE THE RE-AUDIT REPRODUCED, turned into a case. A GET is a snapshot of a mutable
# draft: the response can be formed without the security manifest, a concurrent writer can
# upload it, and `gh` then returns the older body — the ceremony finishing green while the
# draft already carries security bytes. The battery cannot fake that interleaving through a
# stub that answers from fixed text, and it does not need to: the authorization no longer
# depends on the snapshot at all. This case IS that race, with the snapshot showing the
# clean inventory the racing GET would have returned.
declare_security
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'DECLARED a security release'
check "a DECLARED security release refuses on a clean snapshot" "immutable fact, not a GET" $?
# and nothing was written to the draft before refusing — the upload is a --clobber
[ "$(command grep -c '^ARG upload$' "$log")" -eq 0 ]
check "it refuses BEFORE the clobbering upload runs" "no draft mutation on refusal" $?
printf '%s' "$out" | command grep -q 'CVE-2026-0001'
check "the refusal names the advisories it read" "the declaration is quoted" $?
declare_none

# NON-FIRING, and this is the half that keeps the gate from becoming "always red": with no
# declaration in the checkout, the ordinary ceremony proceeds and uploads.
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -eq 0 ] && [ "$(command grep -c '^ARG upload$' "$log")" -eq 1 ]
check "no declaration -> the ceremony proceeds and uploads" "not always-red" $?

# A classifier that cannot decide is not a green light either.
mkdir -p "$WORK/root/release/advisories" || exit 1
: >"$WORK/root/release/advisories/26.8.0.txt"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ]
check "a half-made declaration refuses the ceremony" "exit 2 from the gate is red" $?
declare_none

# --- M · the OID binding: the same OBJECT as phase 1, not merely the same name ------------
# A fully qualified `refs/tags/<tag>` fixes the NAMESPACE, not the OBJECT: the contrast moved
# the same qualified ref between two commits (phase1=1866293202ea…, phase2=5a9295bdbe…,
# moved=yes). So the dispatch names the commit phase 1 ran on and this step refuses unless the
# tag still resolves to it AND the checkout is it.
run_block $'stable-manifest.json\nstable-manifest.json.sig' RELEASE_COMMIT_OVERRIDE_UNUSED=1
[ "$rc" -eq 0 ]
check "the tag resolving to the dispatched commit proceeds" "non-firing direction" $?

# The TAG moves while the evidence and the input stay put, so this exercises the
# tag-vs-evidence comparison rather than the evidence-vs-input one that now precedes it.
(cd "$WORK/root" && /usr/bin/git tag -f v26.8.0 "$OTHER_OID" >/dev/null 2>&1)
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'does not resolve to the commit'
check "a tag that resolves ELSEWHERE refuses" "the object, not the name" $?
(cd "$WORK/root" && /usr/bin/git tag -f v26.8.0 "$FIXTURE_OID" >/dev/null 2>&1)
RELEASE_COMMIT_OVERRIDE="$OTHER_OID"
[ "$(command grep -c '^ARG upload$' "$log")" -eq 0 ]
check "the OID mismatch refuses BEFORE any upload" "no draft mutation" $?
RELEASE_COMMIT_OVERRIDE="0000000000000000000000000000000000000000"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ]
check "an unknown commit OID refuses" "not merely a shape check" $?
RELEASE_COMMIT_OVERRIDE="not-a-hex-oid"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ]
check "a malformed commit OID refuses" "shape checked too" $?
RELEASE_COMMIT_OVERRIDE=""

# --- M2 · a moved tag PLUS a matching input cannot impersonate phase 1 --------------------
# The audit's counterexample, executed against the real block: move the tag AND the checkout
# to another commit and dispatch that same OID. Every comparison agreed and the ceremony
# returned rc=0 — three values that move together are not evidence. The authority is
# release-commit.txt, whose digest sits in the cosign-verified checksums.txt of the run that
# actually built the artifacts.
(cd "$WORK/root" && /usr/bin/git checkout -q "$OTHER_OID" 2>/dev/null && /usr/bin/git tag -f v26.8.0 "$OTHER_OID" >/dev/null 2>&1)
RELEASE_COMMIT_OVERRIDE="$OTHER_OID"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'not the one phase 1 recorded'
check "a moved tag plus a matching raw input cannot impersonate phase 1" "evidence, not assertion" $?
[ "$(command grep -c '^ARG upload$' "$log")" -eq 0 ]
check "and it refuses before touching the draft" "no mutation on a forged identity" $?
RELEASE_COMMIT_OVERRIDE=""
(cd "$WORK/root" && /usr/bin/git checkout -q "$FIXTURE_OID" 2>/dev/null && /usr/bin/git tag -f v26.8.0 "$FIXTURE_OID" >/dev/null 2>&1)

printf '%s\n' "$FIXTURE_OID" >>"$WORK/root/ota-dist/release-commit.txt"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'does not match its entry'
check "evidence altered after the checksums were signed refuses" "the binding is checked" $?
_write_evidence
rm -f "$WORK/root/ota-dist/release-commit.txt"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'no commit evidence'
check "a draft with NO phase-1 evidence refuses" "absence is not permission" $?
_write_evidence
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -eq 0 ]
check "matching evidence, tag, checkout and input proceeds" "non-firing direction" $?

# --- M3 · the byte guard must not ask a git that PATH chose -------------------------------
# GITHUB_PATH lets an earlier action prepend a directory for the rest of the job. This shim is
# silent for `status --porcelain` and delegates everything else, so every OID stays identical:
# against this exact block it turned 61/61 into 59/61.
mkdir -p "$WORK/shim" || exit 1
cat >"$WORK/shim/git" <<'SHIM'
#!/usr/bin/env bash
if [ "${1:-}" = status ] && [ "${2:-}" = --porcelain ]; then exit 0; fi
exec /usr/bin/git "$@"
SHIM
chmod +x "$WORK/shim/git" || exit 1
printf '\n# injected by a prior uses: step\n' >>"$WORK/root/scripts/release-ota-channel.sh"
BLOCK_PATH="$WORK/shim:$WORK/bin:/usr/bin:/bin"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ]
check "a PATH shim that lies about status cannot pass a dirty tree" "the tool is the control" $?
printf '%s' "$out" | command grep -q 'PATH resolves git to'
check "and it names the tool, not the tree" "the right diagnosis" $?
(cd "$WORK/root" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ]
check "the shim is refused even with a CLEAN tree" "provenance, not outcome" $?
BLOCK_PATH="$WORK/bin:/usr/bin:/bin"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -eq 0 ]
check "with the trusted git first, the ceremony completes" "non-firing direction" $?

# --- N · the target must be a mutable DRAFT, checked BEFORE the clobber -------------------
# `gh release upload --clobber` deletes existing assets before uploading, and its own manual
# says "If the upload fails, the original assets will be lost". Doing that to a published
# release destroys a public artefact on the way to failing.
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_IS_DRAFT=false
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'is not a draft'
check "a PUBLISHED release refuses before the clobber" "no clobbering the public pair" $?
[ "$(command grep -c '^ARG upload$' "$log")" -eq 0 ]
check "and nothing was uploaded to it" "refusal precedes mutation" $?
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_IS_IMMUTABLE=true
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'isImmutable'
check "an IMMUTABLE release refuses" "clobber cannot apply" $?
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_IS_IMMUTABLE=maybe
[ "$rc" -ne 0 ]
check "an UNKNOWN immutability posture refuses" "unknown is not a yes" $?
# `null` is the ABSENCE of evidence that this release can be mutated, and this case used to
# encode that fail-open as a positive. Only the boolean false is affirmative.
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_IS_IMMUTABLE=null
[ "$rc" -ne 0 ]
check "a null immutability posture REFUSES" "absence is not permission" $?
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_IS_IMMUTABLE=false
[ "$rc" -eq 0 ]
check "only boolean false proceeds" "the non-firing direction" $?

# --- O · staging and rollback around the clobber ------------------------------------------
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$(command grep -c '^ARG download$' "$log")" -eq 1 ]
check "the existing pair is STAGED before the clobber" "recoverable by construction" $?
run_block $'olivares_26.8.0_linux_amd64.tar.gz\nchecksums.txt'
[ "$rc" -eq 0 ] && [ "$(command grep -c '^ARG download$' "$log")" -eq 0 ]
check "a draft with no previous pair stages nothing" "no pointless download" $?
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_DOWNLOAD_RC=1
[ "$rc" -ne 0 ] && [ "$(command grep -c '^ARG upload$' "$log")" -eq 0 ]
check "a failed STAGING refuses before deleting anything" "no unrecoverable clobber" $?
# ONE surviving asset is not "no original": it goes into the same destructive --clobber, and
# staging only when BOTH were present left it with no custody at all.
run_block $'stable-manifest.json' GH_STUB_UPLOAD_RC=1
[ "$(command grep -c '^ARG download$' "$log")" -eq 1 ] && [ "$(command grep -c '^ARG upload$' "$log")" -eq 2 ]
check "a partial existing pair is also staged and restored" "one original is custody too" $?
run_block $'stable-manifest.json.sig' GH_STUB_UPLOAD_RC=1
[ "$(command grep -c '^ARG download$' "$log")" -eq 1 ]
check "a lone signature is staged too" "either member counts" $?
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_UPLOAD_RC=1
[ "$rc" -ne 0 ] && [ "$(command grep -c '^ARG upload$' "$log")" -eq 2 ]
check "a failed upload triggers the ROLLBACK upload" "the pair is put back" $?
printf '%s' "$out" | command grep -q 'Restoring the asset'
check "the rollback says what it is doing" "audible recovery" $?

# --- P · the commit is not the bytes (phase 2 reads the same rule) ------------------------
# The guard reads the advisories declaration out of this checkout too, so the same custom
# action defeats it: HEAD, the tag and the OID all still agree while the rule that authorizes
# the ceremony has been rewritten under them.
printf '\n# injected by a prior uses: step\n' >>"$WORK/root/scripts/release-ota-channel.sh"
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'no longer matches the commit'
check "a TRACKED file modified without a checkout refuses" "the commit is not the bytes" $?
[ "$(command grep -c '^ARG upload$' "$log")" -eq 0 ]
check "and it refuses before touching the draft" "no mutation on a tampered tree" $?
(cd "$WORK/root" && git checkout -q -- scripts/release-ota-channel.sh)
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -eq 0 ]
check "a CLEAN tree still completes the ceremony" "not always-red" $?

# --- Q · custody is the exact SNAPSHOT, asserted on the remote state ----------------------
# gh uploads assets concurrently, so a failed call can leave one member uploaded and the other
# not. Counting invocations cannot see that; only the draft's final contents can. Each case
# below fixes the previous state, forces a partial failure, and then asserts EXACTLY what the
# draft carries afterwards.
remote_state() { # the asset names the draft ends up with, sorted
	local f out=""
	for f in "$REMOTE_DIR"/*; do
		[ -e "$f" ] || continue
		out="${out}${f##*/}\n"
	done
	printf '%b' "$out" | sort | tr '\n' ' '
}
setup_remote() { # setup_remote [existing asset names…]
	REMOTE_DIR="$WORK/remote"
	rm -rf "$REMOTE_DIR"
	mkdir -p "$REMOTE_DIR"
	local n
	for n in "$@"; do printf 'PREVIOUS\n' >"$REMOTE_DIR/$n"; done
	mkdir -p "$WORK/root/ota-dist"
	printf 'NEW\n' >"$WORK/root/ota-dist/stable-manifest.json"
	printf 'NEW\n' >"$WORK/root/ota-dist/stable-manifest.json.sig"
}

# 0 previous assets, first upload succeeds and the second fails: the created one must go.
setup_remote
run_block $'checksums.txt' GH_STUB_UPLOAD_PARTIAL=1
[ "$rc" -ne 0 ]
check "[custody 0] a partial upload fails the step" "no silent half-state" $?
[ "$(remote_state)" = "" ]
check "[custody 0] the draft carries NOTHING this attempt created" "exact snapshot" $?

# 1 previous asset: the created one goes, the previous one comes back.
setup_remote stable-manifest.json
run_block $'stable-manifest.json' GH_STUB_UPLOAD_PARTIAL=1
[ "$rc" -ne 0 ]
check "[custody 1] a partial upload fails the step" "no silent half-state" $?
[ "$(remote_state)" = "stable-manifest.json " ]
check "[custody 1] the draft carries exactly the one it had" "exact snapshot" $?

# 2 previous assets: both come back, nothing extra remains.
setup_remote stable-manifest.json stable-manifest.json.sig
run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_UPLOAD_PARTIAL=1
[ "$rc" -ne 0 ]
check "[custody 2] a partial upload fails the step" "no silent half-state" $?
[ "$(remote_state)" = "stable-manifest.json stable-manifest.json.sig " ]
check "[custody 2] the draft carries exactly the two it had" "exact snapshot" $?

# UNKNOWN IS KEPT AS UNKNOWN. If the draft cannot be read after the failure, no rollback is
# attempted and none is claimed — the one thing worse than an incomplete recovery is a log
# that says it happened.
setup_remote stable-manifest.json
run_block $'stable-manifest.json' GH_STUB_UPLOAD_PARTIAL=1 GH_STUB_POSTVIEW_RC=1
[ "$rc" -ne 0 ] && printf '%s' "$out" | command grep -q 'UNKNOWN'
check "[custody ?] an unreadable draft is declared UNKNOWN" "no invented rollback" $?
! printf '%s' "$out" | command grep -q 'Rollback done'
check "[custody ?] and no rollback is claimed" "manual repair, said plainly" $?

# NON-FIRING: a successful upload leaves the new pair and is not rolled back.
setup_remote stable-manifest.json stable-manifest.json.sig
run_block $'stable-manifest.json\nstable-manifest.json.sig'
[ "$rc" -eq 0 ] && [ "$(remote_state)" = "stable-manifest.json stable-manifest.json.sig " ]
check "[custody ok] a successful upload is not undone" "non-firing direction" $?
REMOTE_DIR=""

# --- K · the step's CONTEXT, which no amount of running the block can see -----------------
# The block can be perfect and never run: `continue-on-error: true` makes Actions ignore its
# failure, and a step-level `if:` can skip it entirely. Both live OUTSIDE the `run:` payload
# this battery executes, so they are asserted on the YAML — the one place where a structural
# lint is the right instrument rather than a weaker substitute for execution.
STEPBLOCK="$WORK/step.yaml"
awk -v step="      - name: ${STEP}" '
	$0 == step { instep = 1; print; next }
	instep && /^      - name: / { instep = 0 }
	instep { print }
' "$WF" >"$STEPBLOCK"

[ "$(command grep -c "^      - name: ${STEP}\$" "$WF")" -eq 1 ]
check "the guard step appears exactly once in the workflow" "one place to reason about" $?
! command grep -qE '^        continue-on-error:' "$STEPBLOCK"
check "the guard step has NO continue-on-error" "its failure must fail the job" $?
! command grep -qE '^        if:' "$STEPBLOCK"
check "the guard step has NO step-level if:" "it cannot be conditionally skipped" $?


# And the JOB's metadata, which the step block cannot show: `continue-on-error` at job level
# makes Actions record success for the whole job however the guard exits. The job name is
# derived from the workflow, never hardcoded — a guard moved to another job must still be
# checked, not silently unchecked.
JOBNAME="$(awk -v step="      - name: ${STEP}" '/^  [a-zA-Z0-9_-]+:$/ {j = $0} $0 == step {print j; exit}' "$WF")"
# NON-EMPTY WAS NOT ENOUGH. The quoting of this awk program was mangled, so JOBNAME came out
# as the file's first SPDX comment — non-empty, and the check passed on it while the job block
# below was extracted from nothing and the qualified-ref assertion failed with no explanation.
# The derived value must look like a job header, which is what makes this a probe.
case "$JOBNAME" in
"  "*":") ;;
*) JOBNAME="" ;;
esac
[ -n "$JOBNAME" ]
check "the job owning the guard step was identified" "a job header, not any line" $?
JOBBLOCK="$WORK/job.yaml"
awk -v job="$JOBNAME" '$0 == job {injob = 1; print; next} injob && /^  [a-zA-Z0-9_-]+:$/ {injob = 0} injob {print}' "$WF" >"$JOBBLOCK"
! command grep -qE '^    continue-on-error:' "$JOBBLOCK"
check "that JOB has NO continue-on-error either" "success cannot be forced above it" $?
# THE CHECKOUT THIS STEP STANDS ON. Every fact the guard calls immutable — above all the
# advisories declaration that authorizes it — comes out of the job's checkout. An unqualified
# `v26.8.0` lets the action resolve `refs/remotes/origin/v26.8.0` BEFORE `refs/tags/v26.8.0`,
# so a branch sharing the tag's name would supply the release's own truth from a commit that
# is not the release. Asserted on the YAML, since running the block cannot see it.
command grep -qE '^          ref: refs/tags/\$\{\{ inputs\.release_tag \}\}$' "$JOBBLOCK"
check "the job checks out the TAG, fully qualified" "a branch cannot impersonate it" $?

# --- H · the block still does its original job -------------------------------------------
run_block $'stable-manifest.json\nstable-manifest.json.sig'
# The PAIR, so the check earns its name: matching only the .sig would pass an upload that
# had quietly dropped the manifest itself. Fixed-string on each member, whole line for the
# verb and tag, because `.` in these names is a metacharacter to a pattern matcher.
want_upload=$'ARGV 6\nARG release\nARG upload\nARG v26.8.0\nARG ota-dist/stable-manifest.json\nARG ota-dist/stable-manifest.json.sig\nARG --clobber'
[ "$(argv_record upload)" = "$want_upload" ]
check "the stable PAIR is still uploaded to the draft" "both files exactly, this tag" $?

run_block $'stable-manifest.json\nstable-manifest.json.sig' GH_STUB_UPLOAD_RC=1
[ "$rc" -ne 0 ]
check "a failed stable upload still fails the step" "set -e intact" $?

echo
echo "== summary =="
printf 'pass=%d fail=%d\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf 'failed: %s\n' "${failed_names[*]}"
	echo "test-release-security-sig-guard: RED"
	exit 1
fi
echo "test-release-security-sig-guard: OK — ${pass} cases, refusing and finishing directions both covered"
