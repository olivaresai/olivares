#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# ensure-goreleaser.sh — resolve a goreleaser v2.17.0 PINNED BY DIGEST and print its path.
#
# WHY. scripts/test-nfpm-openrc.sh builds fixture .deb/.rpm/.apk with goreleaser. Until
# 2026-09-07 it reached goreleaser through an absolute path inside ONE developer workspace, so
# mainline-ci run 34131797918 (job control-plane) died with «goreleaser is not executable:
# /workspace/…» after five green mutants. The gate now discovers `goreleaser` on PATH or takes an
# explicit OLIVARES_GORELEASER; THIS script is how a CI job (or a box) obtains that binary: exactly
# one version, verified against the digests the release itself publishes, or nothing at all.
# Same shape as ensure-shellcheck.sh: a candidate is accepted ONLY if its digest matches.
#
# PROVENANCE OF THE PINS (verified 2026-09-07 from the primary source — the release's own
# checksums.txt at https://github.com/goreleaser/goreleaser/releases/download/v2.17.0/checksums.txt):
#   goreleaser_Linux_x86_64.tar.gz  dde10e2d5a13cef969c0eec00c74f359c0ac306d702b1bd291ad9337b4e54c1d
#   goreleaser_Linux_arm64.tar.gz   75f93fc0e25d10d8535ffd0e4abcf39d6784a2467ba453d479ae513729a9ebbf
# The binary digests below were computed by extracting `goreleaser` from those two tarballs AFTER
# each tarball matched its published digest. A goreleaser of another version on PATH would package
# differently and make two boxes' verdicts incomparable, so it is not accepted.
#
# USAGE:  ensure-goreleaser.sh --dest DIR        (or OLIVARES_GORELEASER_DIR=DIR)
#   Prints the absolute path of a verified goreleaser on stdout.
#   exit 0  printed a verified binary
#   exit 2  could not provide one: unsupported OS/arch, no --dest, no network, digest mismatch,
#           or a binary that does not execute from DIR. Never a fallback to another version.
#   Candidates, in order: $OLIVARES_GORELEASER (must match or the run refuses), `goreleaser` on
#   PATH (skipped with a note if it is another version), DIR/goreleaser. If none matches, the
#   tarball is downloaded into DIR, verified, extracted, the binary verified again and installed.
set -euo pipefail
LC_ALL=C
export LC_ALL

VER=2.17.0
me=ensure-goreleaser
refuse() {
	printf '%s: NO HE PODIDO MIRAR — %s\n' "$me" "$*" >&2
	exit 2
}
usage() {
	printf 'usage: %s --dest DIR   (or OLIVARES_GORELEASER_DIR=DIR)\n' "$me" >&2
	exit 2
}

[[ "$(uname -s)" == Linux ]] || refuse "the pinned goreleaser assets are Linux builds; this is $(uname -s)"
case "$(uname -m)" in
x86_64 | amd64)
	ASSET=goreleaser_Linux_x86_64.tar.gz
	TARBALL_SHA=dde10e2d5a13cef969c0eec00c74f359c0ac306d702b1bd291ad9337b4e54c1d
	BIN_SHA=7607a984c6ce2c5e660a115916e09a6bccf87147602da8c1095c7afc7fe615b6
	;;
aarch64 | arm64)
	ASSET=goreleaser_Linux_arm64.tar.gz
	TARBALL_SHA=75f93fc0e25d10d8535ffd0e4abcf39d6784a2467ba453d479ae513729a9ebbf
	BIN_SHA=03140e93a6b240ec07c907eb6c40ab60955bd512f93054aac59ab4aca44573d2
	;;
*) refuse "no pinned goreleaser v$VER digest for architecture $(uname -m)" ;;
esac
URL="https://github.com/goreleaser/goreleaser/releases/download/v${VER}/${ASSET}"

dest="${OLIVARES_GORELEASER_DIR:-}"
while [[ $# -gt 0 ]]; do
	case "$1" in
	--dest)
		[[ $# -ge 2 ]] || usage
		dest=$2
		shift 2
		;;
	--dest=*)
		dest=${1#--dest=}
		shift
		;;
	*) usage ;;
	esac
done
case "$dest" in
/*) ;;
*) refuse "--dest must be an absolute directory (got '${dest:-<empty>}')" ;;
esac

digest() { command sha256sum -- "$1" 2>/dev/null | command cut -d' ' -f1; }
serves() { [[ -n "${1:-}" && -f "$1" && -x "$1" && "$(digest "$1")" == "$BIN_SHA" ]]; }

# An explicit override that does not match is an error, not something to route around.
if [[ -n "${OLIVARES_GORELEASER:-}" ]]; then
	if serves "$OLIVARES_GORELEASER"; then
		printf '%s\n' "$OLIVARES_GORELEASER"
		exit 0
	fi
	refuse "OLIVARES_GORELEASER=$OLIVARES_GORELEASER is not an executable whose sha256 is the pinned v$VER digest"
fi
on_path="$(command -v goreleaser 2>/dev/null || true)"
if [[ -n "$on_path" ]]; then
	if serves "$on_path"; then
		printf '%s\n' "$on_path"
		exit 0
	fi
	printf '%s: note: %s on PATH is not the pinned v%s digest; it is not used\n' "$me" "$on_path" "$VER" >&2
fi
if serves "$dest/goreleaser"; then
	printf '%s\n' "$dest/goreleaser"
	exit 0
fi

# Nothing valid: download and VERIFY before anything is installed. If that is impossible, refuse
# loudly — continuing with some other goreleaser is exactly the fault this script closes.
for tool in curl tar sha256sum mktemp install; do
	command -v "$tool" >/dev/null 2>&1 || refuse "missing $tool, needed to obtain goreleaser v$VER"
done
command mkdir -p -- "$dest"
tmp="$(command mktemp -d "$dest/.ensure-goreleaser.XXXXXX")"
trap 'rm -rf -- "$tmp"' EXIT
if ! command curl -fsSL --retry 1 --connect-timeout 20 --max-time 100 -o "$tmp/$ASSET" "$URL"; then
	refuse "no goreleaser v$VER available and $URL could not be downloaded"
fi
got="$(digest "$tmp/$ASSET")"
if [[ "$got" != "$TARBALL_SHA" ]]; then
	printf '    expected %s\n    obtained %s\n' "$TARBALL_SHA" "$got" >&2
	refuse "downloaded $ASSET does not match the pinned digest"
fi
command tar -xzf "$tmp/$ASSET" -C "$tmp" goreleaser
got="$(digest "$tmp/goreleaser")"
if [[ "$got" != "$BIN_SHA" ]]; then
	printf '    expected %s\n    obtained %s\n' "$BIN_SHA" "$got" >&2
	refuse "the goreleaser binary extracted from a matching tarball does not match the pinned digest"
fi
command install -m 0755 -- "$tmp/goreleaser" "$dest/goreleaser"
"$dest/goreleaser" --version >/dev/null 2>&1 ||
	refuse "the installed binary does not execute from $dest (noexec mount?)"
printf '%s\n' "$dest/goreleaser"
