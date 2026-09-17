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

say() { printf '%s\n' "$*"; }
err() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
  cat <<'EOF'
Usage: olivares-install-<version>.sh [--version vYY.M.PATCH] [--bindir DIR]
       [--user|--system] [--init auto|systemd|openrc|launchd]
       [--data-dir PATH] [--config PATH] [--start] [--dry-run]
       olivares-install-<version>.sh --uninstall (--plan|--preserve|--purge)
       [--data-dir PATH] [--bindir DIR] [--yes]

The release asset is pinned to its embedded version. The tracked source requires
--version/OLIVARES_VERSION or resolves the latest release. Installation refuses
without cosign and never invokes sudo. Service installation is opt-in; --start
requires --user or --system and runs only after configuration validation.
EOF
}

requested="${OLIVARES_VERSION:-}"
bindir="${OLIVARES_BINDIR:-}"
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
    [ "$dry_run" -eq 0 ] && [ "$uninstall_version_arg" -eq 0 ] ||
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
if [ "$dry_run" -eq 1 ]; then
  say "  action: dry-run; no downloads or filesystem changes"
  exit 0
fi

have cosign || err "cosign is required; install it and retry (verification cannot be bypassed)"
have install || err "the POSIX install utility is required"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/olivares-install.XXXXXX")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

dl "$base/$archive" "$tmp/$archive"
dl "$base/checksums.txt" "$tmp/checksums.txt"
dl "$base/checksums.txt.sig" "$tmp/checksums.txt.sig"
dl "$base/checksums.txt.pem" "$tmp/checksums.txt.pem"

say "==> verifying the release identity and signed checksum manifest"
  cosign verify-blob \
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
if have sha256sum; then got="$(sha256sum "$tmp/$archive" | awk '{print tolower($1)}')"
elif have shasum; then got="$(shasum -a 256 "$tmp/$archive" | awk '{print tolower($1)}')"
else err "sha256sum or shasum is required"; fi
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
