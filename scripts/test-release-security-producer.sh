#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-security-producer.sh — battery for the step that PRODUCES the security channel
# manifest (`.github/workflows/release.yml`, "generate unsigned security OTA channel manifest
# (deny-closed)"), executed as its real `run:` block against hermetic `go` and `gh`.
#
# WHY IT EXISTS, and it is the most embarrassing kind of gap. Everything else this branch built
# — the classifier, its battery, the phase-2 guard, its battery, the mutation round — could be
# green while THIS mutant lived in the tree:
#
#     -          security) ;;
#     +          security) exit 0 ;;   # skip the producer, silently
#
# Measured by the QA contrast on the exact tree: classifier battery 25/25, phase-2 guard
# battery 37/37, wiring battery 21/21, all green, while the workflow answered `security` and
# then produced NOTHING — no manifest generated, none verified, none uploaded. The one change
# exists to make, and no test could see it, because the harness only ever mutated the
# classifier and the phase-2 guard and nothing executed the producer (the model QA,
# 2026-08-10, P1-03).
#
# So this battery asserts the FULL argv of both `go run` invocations and of `gh release
# upload`, by boundary rather than by joined string, in the firing direction — and asserts
# that NONE of them happen in the non-firing one. A producer that runs the wrong command, or
# the right command with the wrong channel, version, advisories or output path, is not a
# producer.
#
# Hermetic: stub `go` and `gh` on PATH, a fixture checkout, no network, nothing built,
# nothing uploaded.
#
# OLIVARES_SIGPROD_WORKFLOW overrides the workflow read; the red-first proof points it at a
# tree where the producer is skipped.
#
# NO `set -e` (battery reports through check(); see test-pg-test-env.sh).
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# GIT ISOLATION — this battery builds a throwaway tree inside what may be a LINKED WORKTREE,
# where git exports GIT_DIR and GIT_DIR outranks `-C`.
_olivares_git_env="${ROOT}/scripts/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

WF="${OLIVARES_SIGPROD_WORKFLOW:-$ROOT/.github/workflows/release.yml}"
STEP="generate unsigned security OTA channel manifest (deny-closed)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/olivares-sigprod.XXXXXX")" || exit 1
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
# FAIL at column 0: the mutation round attributes a kill by matching `^FAIL  *<witness>`.
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

echo "security channel PRODUCER — the REAL phase-1b block against hermetic go/gh"

# --- extract the step's run: block --------------------------------------------------------
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

[ -s "$BLOCK" ] && [ "$(wc -l <"$BLOCK")" -ge 20 ]
check "the producer's run: block was extracted" "not an empty payload" $?
command grep -q 'release manifest' "$BLOCK" && command grep -q 'verify-manifest' "$BLOCK"
check "the extracted block is the producer" "generate + verify present" $?
bash -n "$BLOCK" 2>/dev/null
check "the extracted block is runnable bash" "bash -n" $?
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

if [ "$fail" -ne 0 ]; then
	echo "extraction failed — every case below would assert against nothing" >&2
	printf 'pass=%d fail=%d\n' "$pass" "$fail"
	exit 1
fi

# --- fixture checkout + stubs -------------------------------------------------------------
mkdir -p "$WORK/root/scripts" "$WORK/root/release/advisories" "$WORK/root/dist" "$WORK/bin" || exit 1
cp "$ROOT/scripts/release-ota-channel.sh" "$WORK/root/scripts/" || exit 1
: >"$WORK/root/dist/checksums.txt"

# argv BY BOUNDARY, never "$*": joining with spaces erases where one argument ends, and a
# repeated `--advisory` list is exactly where that matters.
cat >"$WORK/bin/go" <<'EOF'
#!/usr/bin/env bash
{
	printf 'ARGV %s\n' "$#"
	for _a in "$@"; do printf 'ARG %s\n' "$_a"; done
} >>"${STUB_LOG}"
exit "${STUB_GO_RC:-0}"
EOF
cat >"$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
{
	printf 'ARGV %s\n' "$#"
	for _a in "$@"; do printf 'ARG %s\n' "$_a"; done
} >>"${STUB_LOG}"
exit "${STUB_GH_RC:-0}"
EOF
chmod +x "$WORK/bin/go" "$WORK/bin/gh" || exit 1

# COMMITTED, like the real thing: `release/advisories/<version>.txt` is reviewed in the tagged
# commit. Gitignoring it here would have hidden the tree from the integrity check and made
# these cases pass over a fixture the workflow would have refused.
_restamp() {
	(
		cd "$WORK/root" || exit 1
		git add -A >/dev/null 2>&1
		git commit -q --amend --no-edit >/dev/null 2>&1
		git tag -f v26.8.0 >/dev/null 2>&1
	)
}
declare_security() {
	printf '%s\n' "$@" >"$WORK/root/release/advisories/26.8.0.txt"
	_restamp
}
declare_none() {
	rm -f "$WORK/root/release/advisories/26.8.0.txt"
	_restamp
}

# A REAL repository, because the producer now checks that the workspace still matches its
# commit before it reads the release rule out of it. git-env.sh above is what keeps `git init`
# here from acting on the LIVE repository through an inherited GIT_DIR.
(
	cd "$WORK/root" || exit 1
	git init -q . >/dev/null 2>&1
	git config user.email fixture@example.invalid
	git config user.name fixture
	git config commit.gpgsign false
	# The repository's own rules, copied — see test-release-workspace-e2e.sh for why an
	# invented ignore file is how this branch hid a workflow that denied every release.
	cp "$ROOT/.gitignore" .gitignore 2>/dev/null || :
	git add -A >/dev/null 2>&1
	git commit -q -m "fixture release commit" >/dev/null 2>&1
	git tag v26.8.0
) || exit 1
[ -z "$(git -C "$WORK/root" status --porcelain)" ]
check "the fixture checkout starts clean" "the dirty cases are meaningful" $?

n=0
rc=0
out=""
log=""
PROD_PATH=""
run_block_path() { PATH_OVERRIDE="$PROD_PATH" run_block "$@"; }
run_block() { # run_block [VAR=VAL …]
	n=$((n + 1))
	log="$WORK/log.$n"
	: >"$log"
	(cd "$WORK/root" && env -i PATH="${PATH_OVERRIDE:-$WORK/bin:/usr/bin:/bin}" HOME="$WORK" \
		RELEASE_TAG="v26.8.0" RELEASE_VERSION="26.8.0" GH_TOKEN="stub-token" \
		MANIFEST_EXPIRES_IN="2160h" STUB_LOG="$log" "$@" \
		bash "$BLOCK") >"$WORK/out.$n" 2>&1
	rc=$?
	out="$(cat "$WORK/out.$n")"
}
# argv_nth <k> — the k-th recorded invocation, whole. Keyed by ORDINAL rather than by a verb
# position, because `go run ./cmd/olivares release manifest` puts the PACKAGE second and the
# subcommand fourth: a matcher that assumed "argument 2 is the verb" silently matched nothing
# and every expectation built on it compared against an empty string.
argv_nth() {
	awk -v k="$1" '
		/^ARGV / { i++; if (i == k) { grab = 1; print; next } else if (grab) exit; next }
		grab { print }
	' "$log"
}
calls() { command grep -c '^ARGV ' "$log"; }

# --- A · THE FIRING DIRECTION, with full argv ---------------------------------------------
declare_security CVE-2026-0001
run_block
[ "$rc" -eq 0 ]
check "a declared security release produces" "the firing direction" $?

# EVERY argument, in order. Not "does it mention security" — the channel, the version, the
# advisory list, the expiry and the output path are each a way to produce the wrong manifest
# while still producing one.
want_gen=$'ARGV 16\nARG run\nARG ./cmd/olivares\nARG release\nARG manifest\nARG --dir\nARG dist\nARG --channel\nARG security\nARG --version\nARG 26.8.0\nARG --advisory\nARG CVE-2026-0001\nARG --expires-in\nARG 2160h\nARG --out\nARG dist/security-manifest.json'
[ "$(argv_nth 1)" = "$want_gen" ]
check "generation runs with the exact expected argv" "channel, version, advisory, out" $?

# The generator's own self-check is structural; the POLICY bounds run in the verifier, so a
# producer that generates and skips verification ships an unchecked manifest.
want_ver=$'ARGV 14\nARG run\nARG ./cmd/olivares\nARG release\nARG verify-manifest\nARG --manifest\nARG dist/security-manifest.json\nARG --checksums\nARG dist/checksums.txt\nARG --dir\nARG dist\nARG --expect-channel\nARG security\nARG --expect-version\nARG 26.8.0'
[ "$(argv_nth 2)" = "$want_ver" ]
check "the manifest is VERIFIED, same channel and version" "generate is not enough" $?

# CREATE-ONLY (INT-22, M60/M67-M73). This asserted ARGV 5 ending in --clobber until
# 2026-08-18. The flag OVERWRITES an asset the release already carries, so on a second
# pass it rewrites evidence somebody may already hold — the one property of this chain
# that cannot be undone. Without it `gh` refuses a name that is taken, which is the
# guarantee the ceremony wants: a re-run stops and a human decides.
#
# The assertion stays argv-EXACT rather than being loosened to "contains upload": an
# exact argv is what makes a silently re-added flag fail here, and loosening it to
# accommodate the change would have removed the only witness that can catch its return.
want_upl=$'ARGV 4\nARG release\nARG upload\nARG v26.8.0\nARG dist/security-manifest.json'
[ "$(argv_nth 3)" = "$want_upl" ]
check "the manifest is uploaded CREATE-ONLY to the draft" "the producer's last act, no --clobber" $?
[ "$(calls)" -eq 3 ]
check "exactly three commands run: generate, verify, upload" "no more, no fewer" $?

# --- B · repeated --advisory, never one joined argument -----------------------------------
declare_security CVE-2026-0001 CVE-2026-0002
run_block
[ "$rc" -eq 0 ] && [ "$(command grep -c '^ARG --advisory$' "$log")" -eq 2 ]
check "two advisories become TWO --advisory flags" "not one joined string" $?
command grep -qx 'ARG CVE-2026-0001' "$log" && command grep -qx 'ARG CVE-2026-0002' "$log"
check "each id is its own argument" "per-id bounds stay reachable" $?
! command grep -qx 'ARG CVE-2026-0001,CVE-2026-0002' "$log"
check "the ids are never smuggled as one value" "the joined form is absent" $?

# --- C · THE NON-FIRING DIRECTION ---------------------------------------------------------
# The half that the missing battery made invisible: with no declaration the producer must do
# NOTHING, and quietly. A producer that fires on every release passes every "it produces" test.
declare_none
run_block
[ "$rc" -eq 0 ] && [ "$(calls)" -eq 0 ]
check "no declaration -> nothing is generated, verified or uploaded" "non-firing" $?
printf '%s' "$out" | command grep -q 'no security channel'
check "and it says so" "an audible no-op" $?

# --- D · the classifier's refusal stops the producer ---------------------------------------
: >"$WORK/root/release/advisories/26.8.0.txt" # exists, declares nothing => refuse (exit 2)
run_block
[ "$rc" -ne 0 ] && [ "$(calls)" -eq 0 ]
check "a half-made declaration stops the producer" "refusal is not 'none'" $?
declare_none

# --- F · the commit is not the bytes ------------------------------------------------------
# Eight `uses:` steps run before this one, and any of them executes code in this workspace. A
# custom action can rewrite the release rule — or the verifier — WITHOUT touching HEAD: the
# tag, the OID and the checkout all still agree, and the rule that decides the release is no
# longer the reviewed one. These cases are that action.
declare_security CVE-2026-0001
printf '\n# injected by a prior uses: step\n' >>"$WORK/root/scripts/release-ota-channel.sh"
run_block
[ "$rc" -ne 0 ] && [ "$(calls)" -eq 0 ]
check "a TRACKED file modified without a checkout refuses" "the commit is not the bytes" $?
printf '%s' "$out" | command grep -q 'no longer matches the commit'
check "and it names the tree, not the declaration" "the right diagnosis" $?
(cd "$WORK/root" && git checkout -q -- scripts/release-ota-channel.sh)

printf 'x\n' >"$WORK/root/scripts/injected-helper.sh"
run_block
[ "$rc" -ne 0 ] && [ "$(calls)" -eq 0 ]
check "an UNTRACKED file dropped into the tree refuses" "additions count too" $?
rm -f "$WORK/root/scripts/injected-helper.sh"

(cd "$WORK/root" && printf '# staged\n' >>scripts/release-ota-channel.sh && git add -A >/dev/null 2>&1)
run_block
[ "$rc" -ne 0 ] && [ "$(calls)" -eq 0 ]
check "a change staged in the INDEX refuses" "index counts too" $?
(cd "$WORK/root" && git reset -q --mixed >/dev/null 2>&1 && git checkout -q -- scripts/release-ota-channel.sh)

# NON-FIRING: with the tree clean the producer runs. Without this the check above would be
# satisfied by a producer that refuses always.
run_block
[ "$rc" -eq 0 ] && [ "$(calls)" -eq 3 ]
check "a CLEAN tree still produces" "not always-red" $?
declare_none

# --- G · the byte guard must not ask a git that PATH chose --------------------------------
# The audit's shim: silent for `status --porcelain`, delegating everything else, so every OID
# stays identical. Against this exact block it turned 24/24 into 20/24 — modified tracked
# bytes, an untracked addition and a staged change all produced.
mkdir -p "$WORK/shim" || exit 1
cat >"$WORK/shim/git" <<'SHIM'
#!/usr/bin/env bash
if [ "${1:-}" = status ] && [ "${2:-}" = --porcelain ]; then exit 0; fi
exec /usr/bin/git "$@"
SHIM
chmod +x "$WORK/shim/git" || exit 1
declare_security CVE-2026-0001
printf '\n# injected by a prior uses: step\n' >>"$WORK/root/scripts/release-ota-channel.sh"
PROD_PATH="$WORK/shim:$WORK/bin:/usr/bin:/bin"
run_block_path
[ "$rc" -ne 0 ] && [ "$(calls)" -eq 0 ]
check "a PATH shim that lies about status cannot pass a dirty tree" "the tool is the control" $?
printf '%s' "$out" | command grep -q 'PATH resolves git to'
check "and it names the tool, not the tree" "the right diagnosis" $?
(cd "$WORK/root" && /usr/bin/git checkout -q -- scripts/release-ota-channel.sh)
run_block_path
[ "$rc" -ne 0 ]
check "the shim is refused even with a CLEAN tree" "provenance, not outcome" $?
PROD_PATH=""
run_block
[ "$rc" -eq 0 ] && [ "$(calls)" -eq 3 ]
check "with the trusted git first, the producer runs" "non-firing direction" $?
declare_none

# --- E · a failing tool is a failing step --------------------------------------------------
declare_security CVE-2026-0001
run_block STUB_GO_RC=1
[ "$rc" -ne 0 ]
check "a failed generation fails the step" "set -e intact" $?
run_block STUB_GH_RC=1
[ "$rc" -ne 0 ]
check "a failed upload fails the step" "set -e intact" $?
declare_none

echo
echo "== summary =="
printf 'pass=%d fail=%d\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf 'failed: %s\n' "${failed_names[*]}"
	echo "test-release-security-producer: RED"
	exit 1
fi
echo "test-release-security-producer: OK — ${pass} cases, producing and non-firing both covered"
