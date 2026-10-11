#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Resolve nFPM v2.47.0 by archive and binary digest; no unverified tool fallback.
# Pins: https://github.com/goreleaser/nfpm/releases/download/v2.47.0/checksums.txt
# Binary digests were measured after verifying the published archive digests.
set -euo pipefail
LC_ALL=C
export LC_ALL

VER=2.47.0
me=ensure-nfpm
refuse() {
	printf '%s: COULD NOT LOOK — %s\n' "$me" "$*" >&2
	exit 2
}
usage() {
	printf 'usage: %s --dest DIR   (or OLIVARES_NFPM_DIR=DIR)\n' "$me" >&2
	exit 2
}

[[ "$(uname -s)" == Linux ]] || refuse "the pinned nfpm assets are Linux builds; this is $(uname -s)"
case "$(uname -m)" in
x86_64 | amd64)
	ASSET=nfpm_2.47.0_Linux_x86_64.tar.gz
	TARBALL_SHA=0660ca602b2d2d2ae4781a06c692b3eeb9d437ffea05b831d76e41f4a3188783
	BIN_SHA=17133a2467ffb7cec851c2d7bae0c6098d09d7ed7d3d101a9605f6a473323936
	;;
aarch64 | arm64)
	ASSET=nfpm_2.47.0_Linux_arm64.tar.gz
	TARBALL_SHA=1c0f5f2999b9a974bfb04fdb0cc3306096de530ac5dbb25d739cc5f5219c919c
	BIN_SHA=4d7ddf169945f7f557ac5373035d373429050d00476925953560e1ac65e16c74
	;;
*) refuse "no pinned nfpm v$VER digest for architecture $(uname -m)" ;;
esac
URL="https://github.com/goreleaser/nfpm/releases/download/v${VER}/${ASSET}"

dest="${OLIVARES_NFPM_DIR:-}"
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
if [[ -n "${OLIVARES_NFPM:-}" ]]; then
	if serves "$OLIVARES_NFPM"; then
		printf '%s\n' "$OLIVARES_NFPM"
		exit 0
	fi
	refuse "OLIVARES_NFPM=$OLIVARES_NFPM is not an executable whose sha256 is the pinned v$VER digest"
fi
on_path="$(command -v nfpm 2>/dev/null || true)"
if [[ -n "$on_path" ]]; then
	if serves "$on_path"; then
		printf '%s\n' "$on_path"
		exit 0
	fi
	printf '%s: note: %s on PATH is not the pinned v%s digest; it is not used\n' "$me" "$on_path" "$VER" >&2
fi
if serves "$dest/nfpm"; then
	printf '%s\n' "$dest/nfpm"
	exit 0
fi

# Nothing valid: download and VERIFY before anything is installed. If that is impossible, refuse
# loudly — continuing with some other nfpm is exactly the fault this script closes.
for tool in curl tar sha256sum mktemp install; do
	command -v "$tool" >/dev/null 2>&1 || refuse "missing $tool, needed to obtain nfpm v$VER"
done
command mkdir -p -- "$dest"
tmp="$(command mktemp -d "$dest/.ensure-nfpm.XXXXXX")"
trap 'rm -rf -- "$tmp"' EXIT
if ! command curl -fsSL --retry 1 --connect-timeout 20 --max-time 100 -o "$tmp/$ASSET" "$URL"; then
	refuse "no nfpm v$VER available and $URL could not be downloaded"
fi
got="$(digest "$tmp/$ASSET")"
if [[ "$got" != "$TARBALL_SHA" ]]; then
	printf '    expected %s\n    obtained %s\n' "$TARBALL_SHA" "$got" >&2
	refuse "downloaded $ASSET does not match the pinned digest"
fi
command tar -xzf "$tmp/$ASSET" -C "$tmp" nfpm
got="$(digest "$tmp/nfpm")"
if [[ "$got" != "$BIN_SHA" ]]; then
	printf '    expected %s\n    obtained %s\n' "$BIN_SHA" "$got" >&2
	refuse "the nfpm binary extracted from a matching tarball does not match the pinned digest"
fi
command install -m 0755 -- "$tmp/nfpm" "$dest/nfpm"
"$dest/nfpm" --version >/dev/null 2>&1 ||
	refuse "the installed binary does not execute from $dest (noexec mount?)"
printf '%s\n' "$dest/nfpm"
