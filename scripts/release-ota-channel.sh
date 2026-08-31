#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# release-ota-channel.sh — decide whether a tagged release ALSO produces a `security` OTA
# channel manifest, and hand the workflow the advisory ids that manifest must carry.
#
#   usage: scripts/release-ota-channel.sh <version> [advisories-dir]
#   stdout: exactly one line, "<verdict><TAB><detail>"
#             none<TAB><why>                    -> produce only the stable manifest
#             security<TAB><id>[,<id>...]       -> ALSO produce the security manifest
#   exit:   0 = a usable verdict; 2 = REFUSE (a declaration that cannot be honoured)
#
# WHY A FILE AND NOT A WORKFLOW INPUT
# `an internal design note (not shipped)` §7.1 sketches this gate as
# `if: ${{ inputs.security == true }}` with "a new workflow_dispatch input". That shape
# cannot work on the path that actually builds a release: phase 1 is triggered by
# `push: tags: ["v*"]` (`release.yml:35-36`), where `inputs.*` does not exist —
# `workflow_dispatch` in this workflow is the phase-2 signing ceremony only
# (`release.yml:38-49`). The sketch is right about the RULE (deny-closed, per release) and
# wrong about the CARRIER, so the rule is kept and the carrier is a file in the tree at the
# tagged commit, which a tag push can always see.
#
# It also has to carry data, not just a boolean: `core/release/manifest.go:587` REFUSES a
# security release that names no advisory — "the console and the operator have nothing to
# act on, and stripping the advisory list is how a substituted manifest hides WHAT it
# claims to fix". A checkbox could not satisfy that; the advisory ids have to come from
# somewhere reviewable, and a file in the release commit is reviewed by whoever reviews the
# release.
#
# DENY-CLOSED, AND THE THIRD ANSWER
# Absent file, or a file naming a different version -> `none`. The stable path is untouched
# in a tree that has never cut a security release. But a file that EXISTS and yields no
# usable id is NOT `none`: it is a half-made declaration — someone meant to cut a security
# release and gave nothing to act on — and answering `none` there would silently downgrade
# a security release to an ordinary one, which is the failure mode with no symptom. That
# refuses, loudly, naming the file.
set -uo pipefail
export LC_ALL=C

verdict() { printf '%s\t%s\n' "$1" "$2"; }
refuse() {
	verdict refuse "$1"
	exit 2
}

version="${1-}"
adv_dir="${2-release/advisories}"

[ -n "$version" ] || refuse "no version given (usage: release-ota-channel.sh <version> [advisories-dir])"

# The version becomes a PATH component below, so it is validated as data before it is used
# as a path. A `..` or a `/` here would read an arbitrary file and turn its contents into
# advisory ids in a signed manifest. Only the release grammar the preflight already pins
# (vMAJOR.MINOR.PATCH, core only — no pre-release or build suffix) is accepted.
case "$version" in
*/* | *\\* | ..* | *..*) refuse "version $(printf '%q' "$version") is not a bare version (path separators and .. are refused)" ;;
esac
# THE GLOB THAT USED TO BE HERE WAS NOT THIS GRAMMAR. `[0-9]*[0-9] | [0-9]*[0-9][-+]*`
# accepts anything that starts and ends with a digit: measured, `1x2`, `1 2` and `12` all
# passed as versions (the model QA, 2026-08-10, P3-06). Both production callers validate
# more strictly upstream, so no release bypass was observed — but the script's own contract
# was false, and "version that is not a version" only ever killed an alphabetic sample.
#
# Checked FIELD BY FIELD, in shell, with no external parser (same reason as the guard in
# release.yml: no binary to be missing, no regex dialect to differ). Each of the three parts
# must be a non-empty run of digits with no leading zero, unless it is exactly "0".
# RESTRICTED TO THE CORE, HONESTLY. The comment used to promise "an optional pre-release/build
# suffix" and the check only asked that the suffix be non-empty, so `1.2.3-01`, `1.2.3-a_b`,
# `1.2.3-a b`, `1.2.3-a+b+c`, `1.2.3+meta+again` and `1.2.3-?` were all accepted as versions
# (the model repair audit, 2026-08-10, P3-05). Implementing SemVer 2.0's identifier grammar
# would be the other honest option; it is not what this project releases. The production tag
# regex is `^v[0-9]+\.[0-9]+\.[0-9]+$` — core only — so the classifier now says exactly that
# and refuses every suffix rather than pretending to validate one.
_v_rest="$version"
_v_ok=1
case "$version" in
*[-+]*) _v_ok=0 ;;
esac
case "$_v_rest" in
*.*.*.*) _v_ok=0 ;;                  # four fields or more
*.*.*) ;;
*) _v_ok=0 ;;                        # fewer than three
esac
if [ "$_v_ok" -eq 1 ]; then
	_v_major="${_v_rest%%.*}"
	_v_tail="${_v_rest#*.}"
	_v_minor="${_v_tail%%.*}"
	_v_patch="${_v_tail#*.}"
	for _v_part in "$_v_major" "$_v_minor" "$_v_patch"; do
		case "$_v_part" in
		'' | *[!0-9]*) _v_ok=0 ;;    # empty, or carries a non-digit
		0) ;;                        # a bare zero is the one legal leading zero
		0*) _v_ok=0 ;;               # 01 is not a field
		esac
	done
fi
if [ "$_v_ok" -ne 1 ]; then
	refuse "version $(printf '%q' "$version") does not look like MAJOR.MINOR.PATCH"
fi
unset _v_rest _v_ok _v_major _v_minor _v_patch _v_part

file="${adv_dir%/}/${version}.txt"

# A missing directory and a missing file are the SAME answer, and it is the common one:
# an ordinary release. Distinguished only in the detail, so a workflow log says which.
[ -d "$adv_dir" ] || {
	verdict none "no advisories directory at ${adv_dir}"
	exit 0
}
# A path that exists but is NOT a regular file is a half-made declaration, not an absent
# one: a directory or a device where the file belongs is somebody's mistake, and answering
# `none` would turn it into an ordinary release in silence. Only genuine absence is `none`.
if [ -e "$file" ] || [ -L "$file" ]; then
	# Symlinks are refused rather than followed. A followed symlink resolves OUTSIDE the
	# advisories directory and turns an arbitrary file's contents into advisory ids inside
	# a signed manifest — the same class as the path-traversal guard on the version above,
	# arriving by a different door (the model contrast, 2026-08-09, finding 2).
	[ -L "$file" ] && refuse "advisories path ${file} is a symlink; a declaration must be a regular file in the tree"
	[ -f "$file" ] || refuse "advisories path ${file} exists but is not a regular file"
	[ -r "$file" ] || refuse "advisories file ${file} exists but is not readable"
else
	verdict none "no advisories file at ${file}"
	exit 0
fi

# ⛔ NUL IS REJECTED ON THE FILE'S BYTES, BEFORE ANY OF THEM REACH A SHELL VARIABLE.
#
# This check cannot be moved into the loop below, and that is the whole point. `read`
# SILENTLY DROPS NUL bytes — a bash variable cannot hold one — so by the time `line` is
# tested for `[[:cntrl:]]`, the NUL is already gone and the control-character guard sees a
# string that never contained it. Measured on this script: an advisories file holding
# `CVE-2026-0001<NUL>ATTACK` exits 0 and answers `security<TAB>CVE-2026-0001ATTACK`. The
# two halves are WELDED into one id that appears nowhere in the reviewed file, and that id
# then travels into a signed security manifest.
#
# So the question has to be asked of the FILE, by a tool that counts bytes rather than
# builds strings. Everything else here is downstream of this line being true.
# AND THE COUNT ITSELF CAN FAIL. There is no `set -e` here (the script reports through
# `refuse`), so an unavailable or erroring `wc`/`tr` leaves these empty, makes the arithmetic
# test fail, and that reads as "the counts matched" — the guard waved through by the very
# failure it exists to survive. Measured with a PATH without `wc` or `tr`: a file carrying a
# NUL classified `rc=0` (the model audit, 2026-08-10, P2-01). "I could not count" is the
# third answer, and here it refuses.
total_bytes="$(wc -c <"$file")" || refuse "cannot size ${file}: wc failed"
nul_free_bytes="$(tr -d '\000' <"$file" | wc -c)" || refuse "cannot scan ${file} for NUL: tr/wc failed"
case "${total_bytes}${nul_free_bytes}" in
'' | *[!0-9]*) refuse "byte counts for ${file} are not numbers; refusing to judge NUL on them" ;;
esac
if [ "$total_bytes" -ne "$nul_free_bytes" ]; then
	refuse "advisories file ${file} contains NUL byte(s); an id that survives \`read\` would not be the id in the file"
fi

# Parse: strip comments, CR and surrounding blanks; keep order and drop empties.
ids=()
CR=$'\r' # named once so both the strip below and its mutation witness are quote-free
while IFS= read -r line || [ -n "$line" ]; do
	line="${line%"$CR"}"
	line="${line%%#*}"
	# Trim leading and trailing whitespace without invoking a subshell per line.
	line="${line#"${line%%[![:space:]]*}"}"
	line="${line%"${line##*[![:space:]]}"}"
	[ -n "$line" ] || continue
	# A comma would forge two advisories out of one id once the list is joined below.
	case "$line" in
	*,*) refuse "advisory $(printf '%q' "$line") in ${file} contains a comma, which would split it into two ids" ;;
	esac
	# Control characters reach a custodian's terminal and the console verbatim;
	# core/release/manifest.go:602 refuses them downstream, so never build the bytes.
	case "$line" in
	*[[:cntrl:]]*) refuse "advisory $(printf '%q' "$line") in ${file} contains control characters" ;;
	esac
	ids+=("$line")
done <"$file"

# The file exists, so a security release was DECLARED. Yielding nothing is not `none`.
[ "${#ids[@]}" -gt 0 ] || refuse "advisories file ${file} exists but names no advisory: a security release that names none is refused by core/release/manifest.go:587, and answering 'none' here would silently downgrade it to an ordinary release"

joined="$(
	IFS=,
	printf '%s' "${ids[*]}"
)"
verdict security "$joined"
exit 0
