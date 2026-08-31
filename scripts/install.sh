#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# One-command installer for the Olivares AI single static binary.
#
#   curl -fsSL https://raw.githubusercontent.com/olivaresai/olivares/main/scripts/install.sh | sh
#
# It downloads the right release archive for your OS/arch, VERIFIES it (cosign signature
# over checksums.txt + SHA-256 of the archive — the same trust chain as
# scripts/verify-release.sh), and installs `olivares` into a bin dir. For a security
# product the supply chain is part of the trust model: this never runs an unverified
# binary unless you explicitly opt out.
#
# Knobs (environment variables):
#   OLIVARES_VERSION   release tag to install (default: latest, e.g. v26.7.0)
#   OLIVARES_BINDIR    install dir (default: /usr/local/bin; falls back to ~/.local/bin)
#   OLIVARES_OS        override OS detection (linux | darwin)
#   OLIVARES_ARCH      override arch detection (amd64 | arm64)
#   OLIVARES_SKIP_COSIGN=1   install with SHA-256 only when cosign is absent (NOT advised)
#
# Windows is not supported by this script (no goos:windows build yet — see INSTALL.md).
set -eu

REPO="olivaresai/olivares"
GITHUB="https://github.com"
API="https://api.github.com"
# The keyless Sigstore identity the release workflow signs as. FULLY ANCHORED: cosign
# matches --certificate-identity-regexp UNANCHORED, so the previous
# '^https://github.com/olivaresai/olivares' also accepted `.../olivares-anything/...`
# and any workflow file on any branch -- i.e. far more identities than the one that
# actually signs a release.
DEFAULT_CERT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
CERT_IDENTITY_REGEXP="${OLIVARES_CERT_IDENTITY:-$DEFAULT_CERT_IDENTITY}"
CERT_OIDC_ISSUER="${OLIVARES_CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"

say()  { printf '%s\n' "$*"; }
err()  { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- a downloader that works with curl or wget ------------------------------------
dl() { # dl <url> <dest>
  if have curl; then curl -fsSL "$1" -o "$2"
  elif have wget; then wget -qO "$2" "$1"
  else err "need curl or wget"; fi
}
dl_stdout() { # dl_stdout <url>
  if have curl; then curl -fsSL "$1"
  elif have wget; then wget -qO- "$1"
  else err "need curl or wget"; fi
}

# --- detect OS / arch -------------------------------------------------------------
os="${OLIVARES_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
case "$os" in
  linux)  os=linux ;;
  darwin) os=darwin ;;
  mingw*|msys*|cygwin*|windows*) err "Windows is not supported by this installer yet (see INSTALL.md)";;
  *) err "unsupported OS: $os" ;;
esac
arch="${OLIVARES_ARCH:-$(uname -m)}"
case "$arch" in
  x86_64|amd64)        arch=amd64 ;;
  aarch64|arm64)       arch=arm64 ;;
  *) err "unsupported architecture: $arch (amd64 and arm64 only)" ;;
esac

# --- resolve the release tag ------------------------------------------------------
tag="${OLIVARES_VERSION:-}"
if [ -z "$tag" ]; then
  say "==> resolving the latest release of $REPO"
  tag="$(dl_stdout "$API/repos/$REPO/releases/latest" \
        | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name"[^"]*"([^"]+)".*/\1/')"
  [ -n "$tag" ] || err "could not resolve the latest release (set OLIVARES_VERSION=vYY.M.PATCH). No public release yet?"
fi
version="${tag#v}"
archive="olivares_${version}_${os}_${arch}.tar.gz"
base="$GITHUB/$REPO/releases/download/$tag"
say "==> installing olivares $tag ($os/$arch)"

# --- download into a scratch dir --------------------------------------------------
tmp="$(mktemp -d "${TMPDIR:-/tmp}/olivares-install.XXXXXX")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT INT TERM
cd "$tmp"

say "==> downloading $archive + checksums"
dl "$base/$archive" "$archive"
dl "$base/checksums.txt" checksums.txt
dl "$base/checksums.txt.sig" checksums.txt.sig || true
dl "$base/checksums.txt.pem" checksums.txt.pem || true

# --- verify: cosign signature over checksums.txt, then SHA-256 of the archive -----
if have cosign && [ -f checksums.txt.sig ] && [ -f checksums.txt.pem ]; then
  say "==> verifying cosign signature over checksums.txt (keyless / Sigstore)"
  cosign verify-blob \
    --certificate checksums.txt.pem \
    --signature checksums.txt.sig \
    --certificate-identity-regexp "$CERT_IDENTITY_REGEXP" \
    --certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
    checksums.txt >/dev/null
  say "    signature OK"
elif [ "${OLIVARES_SKIP_COSIGN:-0}" = "1" ]; then
  say "!!  cosign not available — proceeding with SHA-256 only (OLIVARES_SKIP_COSIGN=1)."
  say "!!  the checksums file itself is UNVERIFIED. Install cosign for the real trust chain."
else
  err "cosign not found, so the signature can't be verified. Install cosign
     (https://docs.sigstore.dev/cosign/installation) and re-run, or download the
     archive and run scripts/verify-release.sh. To override (NOT advised): set
     OLIVARES_SKIP_COSIGN=1."
fi

say "==> verifying SHA-256 of $archive"
want="$(grep " $archive\$" checksums.txt | awk '{print $1}')"
[ -n "$want" ] || err "no checksum line for $archive in checksums.txt"
if have sha256sum; then got="$(sha256sum "$archive" | awk '{print $1}')"
elif have shasum;  then got="$(shasum -a 256 "$archive" | awk '{print $1}')"
else err "need sha256sum or shasum"; fi
[ "$want" = "$got" ] || err "checksum MISMATCH for $archive (want $want, got $got)"
say "    checksum OK"

# --- extract + install ------------------------------------------------------------
tar -xzf "$archive"
[ -f olivares ] || err "archive did not contain the 'olivares' binary"
chmod +x olivares

bindir="${OLIVARES_BINDIR:-/usr/local/bin}"
install_to() { # install_to <dir>
  mkdir -p "$1" 2>/dev/null || return 1
  if [ -w "$1" ]; then mv olivares "$1/olivares"
  elif have sudo;  then say "==> installing to $1 (sudo)"; sudo mv olivares "$1/olivares"
  else return 1; fi
}
if ! install_to "$bindir"; then
  bindir="${HOME:-}/.local/bin"
  [ -n "${HOME:-}" ] || err "HOME is not set and OLIVARES_BINDIR was not given: nowhere to install to."
  say "==> $bindir (no write access / no sudo for the default dir)"
  install_to "$bindir" || err "could not install to a bin dir; set OLIVARES_BINDIR to a writable path"
fi

say ""
say "✅ installed: $bindir/olivares"
"$bindir/olivares" version || true
say ""
say "Next:"
say "  olivares serve --insecure --seed-demo --data-dir \"\$(mktemp -d)\"   # bundled demo estate"
say "  $bindir is on your PATH? if not, add it. Production setup: INSTALL.md / the docs site."
case ":$PATH:" in *":$bindir:"*) ;; *) say "  (note: $bindir is not on your PATH)";; esac
