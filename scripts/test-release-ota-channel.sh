#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# test-release-ota-channel.sh — battery for scripts/release-ota-channel.sh, the rule that
# decides whether a tagged release also produces a `security` OTA channel manifest.
#
# WHY THIS EXISTS, measured: `.github/workflows/release.yml:356` is the ONLY `release
# manifest` invocation in all of `.github/`, and it is hard-wired to `--channel stable`.
# So `security` — the channel `an internal design note (not shipped)` §1 calls
# "the central promise of a subscription" — is NEVER PRODUCED. The vocabulary
# (`core/release/manifest.go:44-50`), the generator (`cmd_release_manifest.go:109`) and the
# server (`gate.ts`) all support three channels already; the producer is the missing link.
#
# The rule is DENY-CLOSED and the classifier reports a verdict the way
# `scripts/prepush-refclass.sh` does — one `<verdict><TAB><detail>` line — because that
# shape is already the repository's idiom for "a script whose answer a workflow consumes".
#
# THE NON-FIRING DIRECTION IS THE POINT. A producer that emits a security manifest for
# every release passes any "it produces security" test. Half of these cases assert that it
# does NOT fire, and the mutation round below proves each guard is load-bearing.
#
# Hermetic: a temp tree, no network, no repository writes, no Cloudflare, nothing signed.
set -uo pipefail

# GIT ISOLATION, and it applies even though this battery never invokes git. The class is
# "builds a throwaway directory in a tree that may be a LINKED WORKTREE", and there git
# exports GIT_DIR, which outranks `-C`: a later case added to this file that so much as
# calls `git init "$tmp"` would act on the LIVE repository. That is not hypothetical — it
# stamped core.bare=true on a live repo and left PR #526's branch pointing at a fixture
# commit (scripts/check-git-env-isolation.sh:9-16). Sourcing this is inert while no git
# call exists and correct the moment one does; fail closed, because a missing sanitiser is
# "I could not isolate", never "isolation was not needed".
_olivares_git_env="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)/lib/git-env.sh"
# shellcheck source=/dev/null
. "$_olivares_git_env" || {
	echo "FATAL: cannot source $_olivares_git_env (git-env isolation)" >&2
	exit 2
}
unset _olivares_git_env

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sut="${here}/release-ota-channel.sh"

pass=0
fail=0
failed_names=()

# run <name> <want_rc> <want "verdict[<TAB>detail]"> -- <args...>
#
# THE DETAIL IS COMPARED EXACTLY, never as a prefix. A prefix match here was a real defect,
# caught by the mutation round rather than by review: with `case "$got" in "$want"*)`, a
# mutant that deleted the trailing-whitespace trim left `security<TAB>CVE-2026-0005  ` and
# the case still PASSED, because the expected value is a prefix of the wrong answer. The
# battery was green for the wrong reason and reported a guard as unverified-but-fine.
#
# When the expectation carries no detail after the TAB, only the verdict field is asserted
# — the refusal PROSE is deliberately not pinned, so that improving a message does not
# redden the battery, while the verdict it carries still is.
run() {
	local name="$1" want_rc="$2" want_out="$3"
	shift 4 # name, rc, out, --
	local got_out got_rc want_verdict want_detail got_verdict got_detail
	got_out="$(bash "$sut" "$@" 2>/dev/null)"
	got_rc=$?
	if [ "$got_rc" != "$want_rc" ]; then
		printf 'FAIL %-58s rc=%s want rc=%s (out=%q)\n' "$name" "$got_rc" "$want_rc" "$got_out"
		fail=$((fail + 1))
		failed_names+=("$name")
		return
	fi
	want_verdict="${want_out%%$'\t'*}"
	want_detail="${want_out#*$'\t'}"
	[ "$want_detail" = "$want_out" ] && want_detail=""
	got_verdict="${got_out%%$'\t'*}"
	got_detail="${got_out#*$'\t'}"
	[ "$got_detail" = "$got_out" ] && got_detail=""

	if [ "$got_verdict" != "$want_verdict" ]; then
		printf 'FAIL %-58s verdict=%q want=%q\n' "$name" "$got_verdict" "$want_verdict"
		fail=$((fail + 1))
		failed_names+=("$name")
		return
	fi
	if [ -n "$want_detail" ] && [ "$got_detail" != "$want_detail" ]; then
		printf 'FAIL %-58s detail=%q want EXACTLY=%q\n' "$name" "$got_detail" "$want_detail"
		fail=$((fail + 1))
		failed_names+=("$name")
		return
	fi
	printf 'ok   %-58s rc=%s\n' "$name" "$got_rc"
	pass=$((pass + 1))
}

tmp="$(mktemp -d "${TMPDIR:-/tmp}/ota-channel-test.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
adv="$tmp/advisories"
mkdir -p "$adv"

echo "== release-ota-channel: the DENY-CLOSED direction (must NOT produce security) =="

# The overwhelmingly common case: an ordinary release. No file, no security manifest.
run "no advisories file at all => none" 0 "none	" -- 26.8.0 "$adv"

# A file for ANOTHER version must not enrol this one. Without this, cutting 26.8.1 after a
# security 26.8.0 would silently re-publish a security manifest with last release's CVEs.
printf 'CVE-2026-0001\n' >"$adv/26.8.0.txt"
run "advisories for a DIFFERENT version => none" 0 "none	" -- 26.8.1 "$adv"

# A directory that does not exist is "no declaration", not an error: the stable path must
# keep working in a tree that has never cut a security release.
run "advisories dir absent => none" 0 "none	" -- 26.8.0 "$tmp/nope"

echo
echo "== release-ota-channel: the FIRING direction (must produce security) =="

run "one advisory => security, id carried" 0 "security	CVE-2026-0001" -- 26.8.0 "$adv"

printf 'CVE-2026-0001\nGHSA-aaaa-bbbb-cccc\n' >"$adv/26.9.0.txt"
run "two advisories => comma-joined, order kept" 0 "security	CVE-2026-0001,GHSA-aaaa-bbbb-cccc" -- 26.9.0 "$adv"

# Comments and blank lines are editorial, not data. A release note in the file must not
# become an "advisory id" the console shows to an operator.
printf '# the Q3 batch\n\nCVE-2026-0002\n\n#trailing\n' >"$adv/26.9.1.txt"
run "comments and blanks ignored" 0 "security	CVE-2026-0002" -- 26.9.1 "$adv"

# CRLF: a file edited on Windows must not yield an id with a trailing \r, which would be
# carried verbatim into a signed manifest and shown to an operator.
printf 'CVE-2026-0003\r\nCVE-2026-0004\r\n' >"$adv/26.9.2.txt"
run "CRLF stripped" 0 "security	CVE-2026-0003,CVE-2026-0004" -- 26.9.2 "$adv"

# Leading/trailing whitespace is editorial too.
printf '  CVE-2026-0005  \n' >"$adv/26.9.3.txt"
run "surrounding whitespace trimmed" 0 "security	CVE-2026-0005" -- 26.9.3 "$adv"

echo
echo "== release-ota-channel: the AMBIGUOUS declaration REFUSES (never silently 'none') =="

# THE case this script exists for. An empty file is someone who meant to declare a security
# release and gave nothing to act on. Treating it as "none" would silently downgrade a
# security release to an ordinary one — the failure mode with no symptom. core/release/
# manifest.go:587 refuses the same shape downstream; refusing here names it at the source.
: >"$adv/27.0.0.txt"
run "empty advisories file => REFUSE" 2 "refuse	" -- 27.0.0 "$adv"

printf '# only a comment\n\n\n' >"$adv/27.0.1.txt"
run "comments only => REFUSE" 2 "refuse	" -- 27.0.1 "$adv"

# A control character in an advisory id reaches a custodian's terminal and the console.
# manifest.go:602 refuses it downstream; the producer must not build the bytes at all.
printf 'CVE-2026-0006\033[31m\n' >"$adv/27.0.2.txt"
run "control characters => REFUSE" 2 "refuse	" -- 27.0.2 "$adv"

# A comma inside an id would forge two advisories out of one in the joined output.
printf 'CVE-2026-0007,CVE-2026-0008\n' >"$adv/27.0.3.txt"
run "comma inside an id => REFUSE" 2 "refuse	" -- 27.0.3 "$adv"

echo
echo "== release-ota-channel: bad usage REFUSES (a missing argument is not 'none') =="

run "no version argument => REFUSE" 2 "refuse	" --
run "empty version argument => REFUSE" 2 "refuse	" -- "" "$adv"
# A version with a path separator must never be able to read outside the advisories dir.
run "path traversal in version => REFUSE" 2 "refuse	" -- "../../etc/passwd" "$adv"
run "version with a slash => REFUSE" 2 "refuse	" -- "26.8/0" "$adv"
# The SHAPE half of the version guard, which no case reached before. A version carrying no
# path separator at all still has to be refused, or the separator guard is the only thing
# between a stray argument and a file read. Found by the mutation round, not by review:
# deleting the shape check changed nothing measurable until this case existed.
run "version that is not a version => REFUSE" 2 "refuse	" -- "notaversion" "$adv"


echo
echo "== release-ota-channel: a WRONG-TYPE or SYMLINKED declaration REFUSES =="

# the model contrast finding 2. A symlink resolves OUTSIDE the advisories directory, so
# following one turns an arbitrary file's contents into advisory ids inside a signed
# manifest -- the path-traversal guard arriving by a different door.
printf 'CVE-2026-9001\n' >"$tmp/outside.txt"
ln -s "$tmp/outside.txt" "$adv/28.0.0.txt"
run "symlinked declaration => REFUSE" 2 "refuse	" -- 28.0.0 "$adv"

# A dangling symlink is still a symlink: it must refuse, not fall through to "none".
ln -s "$tmp/does-not-exist.txt" "$adv/28.0.1.txt"
run "dangling symlink => REFUSE (not none)" 2 "refuse	" -- 28.0.1 "$adv"

# A directory where the declaration belongs is a half-made declaration, not an absent one.
mkdir -p "$adv/28.0.2.txt"
run "directory where the file belongs => REFUSE" 2 "refuse	" -- 28.0.2 "$adv"

echo
echo "== release-ota-channel: a NUL is refused on the FILE'S BYTES =="

# THE ONE CONTROL CHARACTER THE [[:cntrl:]] GUARD CANNOT SEE. `read` drops NUL silently —
# a bash variable cannot hold one — so by the time the loop tests `line`, the NUL is gone
# and the two halves around it have been WELDED into a single id. Measured before the fix:
# this file exited 0 and answered `security<TAB>CVE-2026-0001ATTACK`, an id that appears
# nowhere in the reviewed declaration and that would have travelled into a signed security
# manifest. (the model contrast, 2026-08-09.)
printf 'CVE-2026-0001\0ATTACK\n' >"$adv/29.0.0.txt"
run "NUL inside an advisory => REFUSE" 2 "refuse	" -- 29.0.0 "$adv"

# A NUL anywhere in the file is enough, including on a line that would be discarded as a
# comment: the answer must not depend on where in the file the byte happens to sit.
printf '# note\0\nCVE-2026-0002\n' >"$adv/29.0.1.txt"
run "NUL in a comment line => REFUSE" 2 "refuse	" -- 29.0.1 "$adv"

# NON-FIRING DIRECTION: a guard that refused every file would pass both cases above.
printf 'CVE-2026-0003\n' >"$adv/29.0.2.txt"
run "a NUL-free file is still accepted" 0 "security	CVE-2026-0003" -- 29.0.2 "$adv"

echo
echo "== mutation: the NUL guard must be load-bearing =="

mut="$tmp/mutant-nul.sh"
sed 's/^if \[ "\$total_bytes" -ne "\$nul_free_bytes" \]; then$/if false; then/' "$sut" >"$mut"
if cmp -s "$sut" "$mut"; then
	printf '  NOT-APPLIED  %-52s (sed matched nothing)\n' "remove the NUL byte check"
	fail=$((fail + 1))
	failed_names+=("NUL mutant not applied")
elif ! bash -n "$mut" 2>/dev/null; then
	# A mutant that does not parse is a broken file, not evidence about a guard.
	printf '  INVALID      %-52s (mutant does not parse)\n' "remove the NUL byte check"
	fail=$((fail + 1))
	failed_names+=("NUL mutant invalid")
else
	mut_out="$(bash "$mut" 29.0.0 "$adv" 2>/dev/null)"
	mut_rc=$?
	if [ "$mut_rc" -eq 0 ] && [ "$mut_out" = "security	CVE-2026-0001ATTACK" ]; then
		printf '  killed       %-52s rc 2 -> 0, id welded to %s\n' \
			"remove the NUL byte check" "CVE-2026-0001ATTACK"
		pass=$((pass + 1))
	else
		printf '  ESCAPED      %-52s rc=%s out=%q\n' "remove the NUL byte check" "$mut_rc" "$mut_out"
		fail=$((fail + 1))
		failed_names+=("NUL mutant escaped")
	fi
fi

# --- the version grammar, both directions --------------------------------------------------
# The old glob accepted anything that began and ended with a digit; the case named "version
# that is not a version" only ever killed an alphabetic sample, so `1x2`, `1 2` and `12` all
# passed as versions (the model QA, 2026-08-10, P3-06). Positives are asserted too: a
# grammar that refuses everything would pass every "it refuses" row here.
run "version 1x2 is not a version" 2 "refuse" -- 1x2 "$adv"
run "version with a space is not a version" 2 "refuse" -- "1 2" "$adv"
run "a bare integer is not a version" 2 "refuse" -- 12 "$adv"
run "two fields are not a version" 2 "refuse" -- 1.2 "$adv"
run "four fields are not a version" 2 "refuse" -- 1.2.3.4 "$adv"
run "a leading zero is not a field" 2 "refuse" -- 01.2.3 "$adv"
run "an empty pre-release suffix refuses" 2 "refuse" -- 1.2.3- "$adv"
run "a non-numeric field is not a version" 2 "refuse" -- 1.2.x "$adv"
# NON-FIRING: real versions must pass the grammar and reach the ordinary `none` answer.
run "MAJOR.MINOR.PATCH passes the grammar" 0 "none	" -- 9.9.9 "$adv"
run "an all-zero version passes" 0 "none	" -- 0.0.0 "$adv"
# SUFFIXES ARE REFUSED, and that is the honest contract rather than a pretend one: the check
# only asked that a suffix be non-empty, so `1.2.3-01`, `1.2.3-a_b`, `1.2.3-a b`, `1.2.3-a+b+c`,
# `1.2.3+meta+again` and `1.2.3-?` all passed as versions. Production releases core tags only.
run "a leading-zero pre-release refuses" 2 "refuse" -- 1.2.3-01 "$adv"
run "an underscore in a suffix refuses" 2 "refuse" -- 1.2.3-a_b "$adv"
run "a space in a suffix refuses" 2 "refuse" -- "1.2.3-a b" "$adv"
run "multiple build separators refuse" 2 "refuse" -- 1.2.3-a+b+c "$adv"
run "a repeated + refuses" 2 "refuse" -- 1.2.3+meta+again "$adv"
run "a question mark in a suffix refuses" 2 "refuse" -- "1.2.3-?" "$adv"
run "a well-formed pre-release ALSO refuses" 2 "refuse" -- 1.2.3-rc.1 "$adv"
run "a well-formed build suffix ALSO refuses" 2 "refuse" -- 1.2.3+build.7 "$adv"
# NON-FIRING for the restriction: the core forms this project actually releases still pass.
run "a plain core version passes" 0 "none	" -- 1.2.3 "$adv"

# --- the NUL guard's own tools -------------------------------------------------------------
# `wc` and `tr` ANSWER the NUL question, so their failure is not a detail: with no `set -e`
# here, an empty count makes the arithmetic test fail, and a failed test reads as "the counts
# matched". The guard is then waved through by exactly the breakage it must survive. Measured
# on a PATH carrying only bash: a NUL-bearing declaration classified rc=0 before this.
nulwork="$tmp/nultools"
mkdir -p "$nulwork/release/advisories" "$nulwork/bin"
ln -sf "$(command -v bash)" "$nulwork/bin/bash"
printf 'CVE-2026-0001\000ATTACK\n' >"$nulwork/release/advisories/26.8.0.txt"
nt_out="$(cd "$nulwork" && env -i PATH="$nulwork/bin" bash "$sut" 26.8.0 2>/dev/null)"
nt_rc=$?
# calibration: the tools really are unreachable, or this case proves nothing
env -i PATH="$nulwork/bin" bash -c 'command -v wc >/dev/null 2>&1 || command -v tr >/dev/null 2>&1'
nt_cal=$?
if [ "$nt_cal" -ne 0 ] && [ "$nt_rc" -eq 2 ] && [ "${nt_out%%$'\t'*}" = "refuse" ]; then
	printf 'ok   %-58s rc=%s\n' "NUL guard REFUSES when wc/tr are unavailable" "$nt_rc"
	pass=$((pass + 1))
else
	printf 'FAIL %-58s rc=%s cal=%s out=%q\n' "NUL guard REFUSES when wc/tr are unavailable" "$nt_rc" "$nt_cal" "$nt_out"
	fail=$((fail + 1))
	failed_names+=("NUL guard vs missing wc/tr")
fi

echo
echo "== summary =="
printf 'pass=%d fail=%d\n' "$pass" "$fail"
if [ "$fail" -ne 0 ]; then
	printf 'failed: %s\n' "${failed_names[*]}"
	echo "test-release-ota-channel: RED"
	exit 1
fi
echo "test-release-ota-channel: OK — ${pass} cases, deny-closed and firing directions both covered"
