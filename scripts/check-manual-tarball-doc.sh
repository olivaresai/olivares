#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# check-manual-tarball-doc.sh — run the INSTALL.md "Manual binary (tarball)" block VERBATIM
# in an empty directory against the published release, stopping at the first failing line.
# Then prove that cosign verified the signed checksums, that the SHA-256 of the extracted
# archive matched, and that /usr/local/bin/olivares is the binary from that archive. A reader
# who copies the block gets exactly this.
#
# The block once ran `scripts/verify-release.sh` from the download directory, where no such
# path exists: exit 127 at the one security step, and a reader who carried on installed an
# unverified binary. Exiting 0 is not enough on its own: a block without its verify lines also
# exits 0, so the tools' own results are required too.
#
# Needs network, `cosign` on PATH, passwordless sudo and no /usr/local/bin/olivares yet (a
# hosted CI runner). Usage: check-manual-tarball-doc.sh [INSTALL.md]
set -euo pipefail

fail() { echo "check-manual-tarball-doc: $*" >&2; exit 1; }

doc="${1:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)/INSTALL.md}"
block="$(awk '
	inblock && /^```$/ { exit }
	inblock { print; next }
	section && /^```sh$/ { inblock = 1; next }
	section && /^#+ / { exit }
	/^### Manual binary \(tarball\)$/ { section = 1 }
' "$doc")"
[ -n "$block" ] || fail "no sh block under '### Manual binary (tarball)' in $doc"

# A release PR bumps `ver=` before that release exists, so the block's first download would 404.
# Probe the block's own $base (its lines before the first download). On a pull request a 404 says
# plainly that nothing was verified; the workflow runs the block again once the release is
# published. Anywhere else a 404 is a failure, as is any other answer (network, 403, 5xx).
vars="$(printf '%s\n' "$block" | awk '/^curl /{exit} {print}')"
base="$(bash -euc "$vars"$'\nprintf "%s\\n" "$base"' </dev/null)" ||
	fail "could not evaluate the block's variables before its first download"
[ -n "$base" ] || fail "the block sets no \$base before its first download"
code="$(curl -sSL -o /dev/null -w '%{http_code}' --range 0-0 "$base/checksums.txt")" || code=000
case "$code" in
200 | 206) ;;
404)
	[ "${GITHUB_EVENT_NAME:-}" = pull_request ] || fail "$base/checksums.txt is 404: the release the block names is not published"
	msg="NOT VERIFIED, $base/checksums.txt is 404 (release not published yet); nothing was run or installed"
	echo "::notice::check-manual-tarball-doc: $msg"
	[ -z "${GITHUB_STEP_SUMMARY:-}" ] || echo "manual-tarball-doc: $msg" >>"$GITHUB_STEP_SUMMARY"
	exit 0
	;;
*) fail "probe of $base/checksums.txt answered HTTP $code" ;;
esac

bin=/usr/local/bin/olivares
[ ! -e "$bin" ] || fail "$bin already exists, so it could not prove what the block installed"

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
printf '%s\n' "$block" >"$work/block.sh"
mkdir "$work/dl"
cd "$work/dl"
bash -e -o pipefail "$work/block.sh" </dev/null 2>&1 | tee "$work/log"

archives=(*.tar.gz)
if [ "${#archives[@]}" -ne 1 ] || [ ! -f "${archives[0]}" ]; then
	fail "expected one downloaded archive, found: ${archives[*]}"
fi
grep -Fxq 'Verified OK' "$work/log" || fail "cosign did not verify the signature over checksums.txt"
grep -Fxq "${archives[0]}: OK" "$work/log" || fail "no SHA-256 check passed for ${archives[0]}"
cmp -s olivares "$bin" || fail "$bin is not the binary extracted from ${archives[0]}"
"$bin" version
echo "check-manual-tarball-doc: OK, the documented block verified ${archives[0]} and installed it"
