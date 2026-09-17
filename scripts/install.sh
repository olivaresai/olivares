#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Verified second-stage installer. Release builds replace the marker below and publish
# olivares-install-<version>.sh as an asset covered by the cosign-signed checksums.txt.
# The tracked source can also install an explicitly pinned release. It never bypasses
# cosign and never invokes sudo; privilege changes remain an operator decision.
set -eu

REPO="olivaresai/olivares"
GITHUB="${OLIVARES_GITHUB_URL:-https://github.com}"
API="${OLIVARES_GITHUB_API_URL:-https://api.github.com}"
EMBEDDED_VERSION='@OLIVARES_INSTALLER_VERSION@'
DEFAULT_CERT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
CERT_IDENTITY_REGEXP="${OLIVARES_CERT_IDENTITY:-$DEFAULT_CERT_IDENTITY}"
CERT_OIDC_ISSUER="${OLIVARES_CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"
# cosign is the verifier and is never optional. When it is not on PATH the installer fetches
# the pinned cosign release below into its own temporary directory, checks the binary against
# the SHA-256 embedded here (from sigstore's signed cosign_checksums.txt; the same rows
# scripts/assert-cosign-binary.sh approves) and removes it together with that directory.
# --install-cosign keeps the verified copy next to olivares; OLIVARES_COSIGN names your own.
COSIGN_VERSION='v2.6.4'
COSIGN_RELEASE="${OLIVARES_COSIGN_RELEASE_URL:-https://github.com/sigstore/cosign/releases/download}"
cosign_digest() { # cosign_digest <os> <arch> -> pinned SHA-256 of cosign-<os>-<arch>
  case "$1-$2" in
    linux-amd64) printf '%s' 309779b0c4e409186b0a80daba99041fe2cf65a920ce645013901df6211895a9 ;;
    linux-arm64) printf '%s' df408e5418129306fed7349ec46e27be0445d05c5127c07f435e9a566af67593 ;;
    darwin-amd64) printf '%s' ec648fddfedf1dad59dff9fbab177284a618204e03126ea37a87ab3cec4e7cb1 ;;
    darwin-arm64) printf '%s' b2987c1b55a1e2735c59ac5c3e140acbf7ba5c1ed0cc07dbbf1b85676595237e ;;
    *) return 1 ;;
  esac
}

say() { printf '%s\n' "$*"; }
err() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<'EOF'
Usage: olivares-install-<version>.sh [--version vYY.M.PATCH] [--bindir DIR]
       [--user|--system] [--init auto|systemd|openrc|launchd]
       [--data-dir PATH] [--config PATH] [--start] [--install-cosign] [--dry-run]
       olivares-install-<version>.sh --uninstall (--plan|--preserve|--purge)
       [--data-dir PATH] [--bindir DIR] [--yes]

The release asset is pinned to its embedded version. The tracked source requires
--version/OLIVARES_VERSION or resolves the latest release. cosign verifies every
download and is never bypassed: cosign on PATH is used; otherwise a pinned copy is
fetched into a temporary directory, checked against the SHA-256 embedded in this
script, and removed afterwards (--install-cosign keeps it in the install directory;
OLIVARES_COSIGN=/path/to/cosign uses your own). sudo is never invoked. Service
installation is opt-in; --start requires --user or --system and runs only after
configuration validation.
EOF
}

requested="${OLIVARES_VERSION:-}"
bindir="${OLIVARES_BINDIR:-}"
cosign_bin="${OLIVARES_COSIGN:-}"
cosign_temporary=0
exedir=""
install_cosign=0
dry_run=0
service_mode=""
service_init=auto
service_data_dir=""
service_config=""
service_start=0
uninstall=0
uninstall_action=""
uninstall_yes=0
uninstall_version_arg=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || err "--version needs a value"; requested="$2"; uninstall_version_arg=1; shift 2 ;;
    --bindir) [ "$#" -ge 2 ] || err "--bindir needs a value"; bindir="$2"; shift 2 ;;
    --user) [ -z "$service_mode" ] || err "choose exactly one of --user or --system"; service_mode=user; shift ;;
    --system) [ -z "$service_mode" ] || err "choose exactly one of --user or --system"; service_mode=system; shift ;;
    --init) [ "$#" -ge 2 ] || err "--init needs a value"; service_init="$2"; shift 2 ;;
    --data-dir) [ "$#" -ge 2 ] || err "--data-dir needs a value"; service_data_dir="$2"; shift 2 ;;
    --config) [ "$#" -ge 2 ] || err "--config needs a value"; service_config="$2"; shift 2 ;;
    --start) service_start=1; shift ;;
    --install-cosign) install_cosign=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    --uninstall) uninstall=1; shift ;;
    --plan) [ -z "$uninstall_action" ] || err "choose exactly one uninstall action"; uninstall_action=plan; shift ;;
    --preserve) [ -z "$uninstall_action" ] || err "choose exactly one uninstall action"; uninstall_action=preserve; shift ;;
    --purge) [ -z "$uninstall_action" ] || err "choose exactly one uninstall action"; uninstall_action=purge; shift ;;
    --yes|-y) uninstall_yes=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) err "unknown argument: $1" ;;
  esac
done
if [ "$uninstall" -eq 1 ]; then
  [ -n "$uninstall_action" ] || err "--uninstall requires exactly one of --plan, --preserve or --purge"
  [ -z "$service_mode$service_config" ] && [ "$service_init" = auto ] && [ "$service_start" -eq 0 ] &&
    [ "$dry_run" -eq 0 ] && [ "$uninstall_version_arg" -eq 0 ] && [ "$install_cosign" -eq 0 ] ||
    err "--uninstall accepts only its action, --data-dir, --bindir and --yes"
  [ "$uninstall_yes" -eq 0 ] || [ "$uninstall_action" = purge ] || err "--yes is valid only with --uninstall --purge"
  if [ -n "$bindir" ]; then
    case "$bindir" in /*) ;; *) err "--bindir must be an absolute path: $bindir" ;; esac
    installed="$bindir/olivares"
  elif installed="$(command -v olivares 2>/dev/null)" && [ -n "$installed" ]; then
    :
  elif [ -x /usr/local/bin/olivares ]; then installed=/usr/local/bin/olivares
  elif [ -n "${HOME:-}" ] && [ -x "$HOME/.local/bin/olivares" ]; then installed="$HOME/.local/bin/olivares"
  elif [ -x /usr/bin/olivares ]; then installed=/usr/bin/olivares
  else err "cannot find an installed olivares binary; pass --bindir"
  fi
  case "$installed" in /*) ;; *) err "installed binary path is not absolute: $installed" ;; esac
  [ -x "$installed" ] || err "installed binary is not executable: $installed"
  set -- "$installed" uninstall "--$uninstall_action"
  [ -z "$service_data_dir" ] || set -- "$@" --data-dir "$service_data_dir"
  [ "$uninstall_yes" -eq 0 ] || set -- "$@" --yes
  exec "$@"
fi
[ -z "$uninstall_action" ] && [ "$uninstall_yes" -eq 0 ] || err "--plan/--preserve/--purge/--yes require --uninstall"
[ "$service_start" -eq 0 ] || [ -n "$service_mode" ] || err "--start requires --user or --system"
if [ -z "$service_mode" ] &&
  { [ "$service_init" != auto ] || [ -n "$service_data_dir" ] || [ -n "$service_config" ]; }; then
  err "--init, --data-dir and --config require --user or --system"
fi
if [ "$dry_run" -eq 0 ] && [ "$service_mode" = system ] && [ "$(id -u)" -ne 0 ]; then
  err "--system requires an explicitly privileged process; this installer never invokes sudo"
fi

case "$EMBEDDED_VERSION" in
  SNAPSHOT) err "snapshot installers are not installable; use a tagged release asset" ;;
  @*) pinned="" ;;
  *) pinned="v$EMBEDDED_VERSION" ;;
esac
if [ -n "$pinned" ]; then
  if [ -n "$requested" ] && [ "${requested#v}" != "${pinned#v}" ]; then
    err "this installer is pinned to $pinned, not $requested"
  fi
  tag="$pinned"
else
  tag="$requested"
fi

dl() { # dl <url> <destination>
  if have curl; then curl -fsSL "$1" -o "$2"
  elif have wget; then wget -qO "$2" "$1"
  else err "curl or wget is required"; fi
}
dl_stdout() {
  if have curl; then curl -fsSL "$1"
  elif have wget; then wget -qO- "$1"
  else err "curl or wget is required"; fi
}
sha256_of() { # sha256_of <file> -> lowercase hex digest
  if have sha256sum; then sha256sum "$1" | awk '{print tolower($1)}'
  elif have shasum; then shasum -a 256 "$1" | awk '{print tolower($1)}'
  else err "sha256sum or shasum is required"; fi
}
# resolve_cosign sets cosign_bin: OLIVARES_COSIGN, then PATH, then a pinned copy fetched
# into $tmp and used only if its SHA-256 equals the digest embedded in this script.
resolve_cosign() {
  if [ -n "$cosign_bin" ]; then
    [ -x "$cosign_bin" ] || err "OLIVARES_COSIGN is not an executable file: $cosign_bin"
    if [ "${OLIVARES_COSIGN_TEMPORARY:-0}" = 1 ]; then cosign_temporary=1; fi
    return 0
  fi
  if have cosign; then
    cosign_bin="$(command -v cosign)"
    return 0
  fi
  want="$(cosign_digest "$os" "$arch")" ||
    err "cosign is not on PATH and this installer pins no cosign for $os/$arch; install cosign (https://docs.sigstore.dev/cosign/system_config/installation/) and retry"
  say "==> cosign is not on PATH: fetching the pinned cosign $COSIGN_VERSION for $os/$arch into a temporary directory (about 120 MB; checked against its pinned SHA-256 before use)"
  dl "$COSIGN_RELEASE/$COSIGN_VERSION/cosign-$os-$arch" "$tmp/cosign"
  got="$(sha256_of "$tmp/cosign")"
  [ "$want" = "$got" ] ||
    err "the downloaded cosign does not match its pinned SHA-256 (pinned $want, obtained $got); it was not executed"
  chmod 0755 "$tmp/cosign"
  if "$tmp/cosign" version >/dev/null 2>&1; then
    cosign_bin="$tmp/cosign"
  else
    # ${TMPDIR:-/tmp} may be mounted noexec: stage the verified copy where binaries can run.
    exedir="${XDG_CACHE_HOME:-${HOME:-/tmp}/.cache}/olivares-install.$$"
    mkdir -p "$exedir" && mv "$tmp/cosign" "$exedir/cosign" && "$exedir/cosign" version >/dev/null 2>&1 ||
      err "the verified cosign cannot execute from $tmp or $exedir (noexec mount?); set TMPDIR to an executable filesystem or OLIVARES_COSIGN=/path/to/cosign"
    cosign_bin="$exedir/cosign"
  fi
  cosign_temporary=1
}

if [ -z "$tag" ]; then
  say "==> resolving the latest release of $REPO"
  tag="$(dl_stdout "$API/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | sed -n '1p')"
  [ -n "$tag" ] || err "could not resolve a release; pass --version vYY.M.PATCH"
fi
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
  err "invalid release version: $tag"

os="${OLIVARES_OS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
case "$os" in
  linux) os=linux ;;
  darwin) os=darwin ;;
  *) err "unsupported OS: $os (linux and darwin only)" ;;
esac
arch="${OLIVARES_ARCH:-$(uname -m)}"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) err "unsupported architecture: $arch (amd64 and arm64 only)" ;;
esac

if [ -z "$bindir" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    bindir=/usr/local/bin
  else
    [ -n "${HOME:-}" ] || err "HOME is unset; pass --bindir DIR"
    bindir="$HOME/.local/bin"
  fi
fi
case "$bindir" in /*) ;; *) err "--bindir must be an absolute path: $bindir" ;; esac

version="${tag#v}"
archive="olivares_${version}_${os}_${arch}.tar.gz"
base="$GITHUB/$REPO/releases/download/$tag"
say "Olivares AI verified installer plan"
say "  version: $tag"
say "  platform: $os/$arch"
say "  archive: $base/$archive"
say "  install: $bindir/olivares"
say "  trust: cosign identity + signed checksums.txt + archive SHA-256"
say "  privilege: none (sudo is never invoked)"
if [ -n "$service_mode" ]; then
  say "  service: $service_mode mode; init=$service_init; start=$([ "$service_start" -eq 1 ] && printf explicit || printf no)"
  [ -z "$service_data_dir" ] || say "  data: $service_data_dir"
  [ -z "$service_config" ] || say "  config: $service_config"
else
  say "  service: binary only (pass --user or --system to install an adapter)"
fi
if [ -n "$cosign_bin" ]; then
  say "  cosign: $cosign_bin (OLIVARES_COSIGN)"
elif have cosign; then
  say "  cosign: $(command -v cosign) (on PATH)"
else
  say "  cosign: not on PATH; a temporary copy of cosign $COSIGN_VERSION, checked against the SHA-256 pinned in this installer, verifies the download and is removed afterwards"
  if [ "$install_cosign" -eq 1 ]; then
    say "          --install-cosign: that verified copy is kept at $bindir/cosign"
  else
    say "          (pass --install-cosign to keep it at $bindir/cosign, or set OLIVARES_COSIGN=/path/to/cosign)"
  fi
fi
if [ "$dry_run" -eq 1 ]; then
  say "  action: dry-run; no downloads or filesystem changes"
  exit 0
fi

have install || err "the POSIX install utility is required"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/olivares-install.XXXXXX")"
cleanup() { rm -rf "$tmp"; [ -z "$exedir" ] || rm -rf "$exedir"; }
trap cleanup EXIT HUP INT TERM

resolve_cosign
dl "$base/$archive" "$tmp/$archive"
dl "$base/checksums.txt" "$tmp/checksums.txt"
dl "$base/checksums.txt.sig" "$tmp/checksums.txt.sig"
dl "$base/checksums.txt.pem" "$tmp/checksums.txt.pem"

say "==> verifying the release identity and signed checksum manifest"
"$cosign_bin" verify-blob \
  --certificate "$tmp/checksums.txt.pem" \
  --signature "$tmp/checksums.txt.sig" \
  --certificate-identity-regexp "$CERT_IDENTITY_REGEXP" \
  --certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
  "$tmp/checksums.txt" >/dev/null

if ! want="$(awk -v name="$archive" '
  $2 == name && length($1) == 64 && $1 !~ /[^0-9a-fA-F]/ {
    count++; value=tolower($1)
  }
  END { if (count == 1) print value; else exit 1 }
' "$tmp/checksums.txt")"; then
  err "signed checksums.txt must contain exactly one SHA-256 row for $archive"
fi
got="$(sha256_of "$tmp/$archive")"
[ "$want" = "$got" ] || err "checksum mismatch for $archive (signed $want, obtained $got)"

tar -xOf "$tmp/$archive" olivares >"$tmp/olivares" ||
  err "the verified archive does not contain a top-level olivares binary"
[ -s "$tmp/olivares" ] || err "the verified olivares binary is empty"

if [ -n "$service_mode" ]; then
  mkdir -p "$tmp/service-assets"
  tar -xzf "$tmp/$archive" -C "$tmp/service-assets" \
    scripts/install-service.sh packaging/service ||
    err "the verified archive does not contain its service adapter and templates"
  /bin/sh -n "$tmp/service-assets/scripts/install-service.sh" ||
    err "the verified service adapter does not parse as POSIX shell"
  # Resolve and disclose the service plan before replacing a binary. The real
  # call below repeats every check against the installed path.
  set -- /bin/sh "$tmp/service-assets/scripts/install-service.sh" "--$service_mode" \
    --binary "$bindir/olivares" --init "$service_init" --managed-binary --dry-run
  [ -z "$service_data_dir" ] || set -- "$@" --data-dir "$service_data_dir"
  [ -z "$service_config" ] || set -- "$@" --config "$service_config"
  OLIVARES_ASSET_ROOT="$tmp/service-assets" "$@"
fi
mkdir -p "$bindir" || err "cannot create $bindir; create it with the intended owner and retry"
stage="$bindir/.olivares-install.$$"
install -m 0755 "$tmp/olivares" "$stage" ||
  err "cannot write $bindir; choose a writable --bindir or perform the privilege step explicitly"
mv "$stage" "$bindir/olivares"

say "==> installed verified binary: $bindir/olivares"
"$bindir/olivares" version || err "the installed binary did not report its version"
if [ "$install_cosign" -eq 1 ]; then
  if [ "$cosign_temporary" -eq 1 ]; then
    want="$(cosign_digest "$os" "$arch")" || err "olivares is installed; this installer pins no cosign for $os/$arch, so none was kept"
    [ "$want" = "$(sha256_of "$cosign_bin")" ] ||
      err "olivares is installed; the temporary cosign no longer matches its pinned SHA-256 and was not kept"
    stage="$bindir/.cosign-install.$$"
    install -m 0755 "$cosign_bin" "$stage" || err "olivares is installed; cosign could not be written to $bindir"
    mv "$stage" "$bindir/cosign"
    say "==> installed cosign $COSIGN_VERSION: $bindir/cosign"
  else
    say "note: cosign is already available at $cosign_bin; --install-cosign changed nothing"
  fi
elif [ "$cosign_temporary" -eq 1 ]; then
  say "note: the temporary cosign $COSIGN_VERSION is removed with the temporary directory; rerun with --install-cosign to keep it, or install cosign from https://docs.sigstore.dev/cosign/system_config/installation/"
fi
if [ -n "$service_mode" ]; then
  set -- /bin/sh "$tmp/service-assets/scripts/install-service.sh" "--$service_mode" \
    --binary "$bindir/olivares" --init "$service_init" --managed-binary
  [ -z "$service_data_dir" ] || set -- "$@" --data-dir "$service_data_dir"
  [ -z "$service_config" ] || set -- "$@" --config "$service_config"
  [ "$service_start" -eq 0 ] || set -- "$@" --start
  OLIVARES_ASSET_ROOT="$tmp/service-assets" "$@" ||
    err "verified binary remains installed, but service configuration failed; correct the named precondition and rerun this pinned installer"
fi
case ":$PATH:" in *":$bindir:"*) ;; *) say "note: $bindir is not on PATH" ;; esac
if [ -n "$service_mode" ]; then
  say "Next: $bindir/olivares doctor --data-dir ${service_data_dir:-<resolved-by-service-mode>}"
else
  say "Next: olivares quickstart"
fi
