#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Convenience bootstrap intended for https://olivares.ai/install.sh and /get.
# This small script is trusted through HTTPS. Before executing its second stage it
# verifies that versioned installer against the release's cosign-signed checksums.
set -eu

REPO="olivaresai/olivares"
GITHUB="${OLIVARES_GITHUB_URL:-https://github.com}"
API="${OLIVARES_GITHUB_API_URL:-https://api.github.com}"
DEFAULT_CERT_IDENTITY='^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
CERT_IDENTITY_REGEXP="${OLIVARES_CERT_IDENTITY:-$DEFAULT_CERT_IDENTITY}"
CERT_OIDC_ISSUER="${OLIVARES_CERT_OIDC_ISSUER:-https://token.actions.githubusercontent.com}"

say() { printf '%s\n' "$*"; }
err() { printf 'error: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }
usage() {
  cat <<'EOF'
Usage: install.sh --version vYY.M.PATCH [--bindir DIR]
       [--user|--system] [--init auto|systemd|openrc|launchd]
       [--data-dir PATH] [--config PATH] [--start] [--dry-run]
       install.sh --uninstall (--plan|--preserve|--purge)
       [--data-dir PATH] [--bindir DIR] [--yes]

CI and piped/non-interactive use must pin --version. Interactive use may omit it
to resolve the latest release. --dry-run prints the trust boundary and actions
without downloading or changing files.
EOF
}

tag="${OLIVARES_VERSION:-}"
bindir="${OLIVARES_BINDIR:-}"
dry_run=0
service_mode=""
service_init=""
service_data_dir=""
service_config=""
service_start=0
uninstall=0
uninstall_action=""
uninstall_yes=0
uninstall_version_arg=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || err "--version needs a value"; tag="$2"; uninstall_version_arg=1; shift 2 ;;
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
  [ -z "$service_mode$service_init$service_config" ] && [ "$service_start" -eq 0 ] &&
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
  { [ -n "$service_init" ] || [ -n "$service_data_dir" ] || [ -n "$service_config" ]; }; then
  err "--init, --data-dir and --config require --user or --system"
fi

noninteractive=0
case "${CI:-}" in 1|true|TRUE) noninteractive=1 ;; esac
[ "${OLIVARES_NONINTERACTIVE:-0}" = 1 ] && noninteractive=1
[ -t 0 ] || noninteractive=1
if [ -z "$tag" ] && [ "$noninteractive" -eq 1 ]; then
  err "non-interactive installation must pin --version vYY.M.PATCH"
fi

dl() {
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
  tag="$(dl_stdout "$API/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | sed -n '1p')"
fi
case "$tag" in v*) ;; *) tag="v$tag" ;; esac
printf '%s\n' "$tag" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' ||
  err "invalid or unresolved release version: $tag"

version="${tag#v}"
name="olivares-install-${version}.sh"
base="$GITHUB/$REPO/releases/download/$tag"
say "Olivares AI HTTPS bootstrap plan"
say "  version: $tag"
say "  installer: $base/$name"
say "  bootstrap trust: this response is trusted through HTTPS"
say "  second-stage trust: cosign identity + signed checksums.txt + SHA-256"
say "  action: verify the versioned installer before executing it"
[ -n "$bindir" ] && say "  install directory: $bindir"
[ -z "$service_mode" ] || say "  service: $service_mode mode; start=$([ "$service_start" -eq 1 ] && printf explicit || printf no)"
if [ "$dry_run" -eq 1 ]; then
  say "  result: dry-run; no downloads or filesystem changes"
  exit 0
fi

have cosign || err "cosign is required; verification cannot be bypassed"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/olivares-bootstrap.XXXXXX")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

dl "$base/$name" "$tmp/$name"
dl "$base/checksums.txt" "$tmp/checksums.txt"
dl "$base/checksums.txt.sig" "$tmp/checksums.txt.sig"
dl "$base/checksums.txt.pem" "$tmp/checksums.txt.pem"
cosign verify-blob \
  --certificate "$tmp/checksums.txt.pem" \
  --signature "$tmp/checksums.txt.sig" \
  --certificate-identity-regexp "$CERT_IDENTITY_REGEXP" \
  --certificate-oidc-issuer "$CERT_OIDC_ISSUER" \
  "$tmp/checksums.txt" >/dev/null

if ! want="$(awk -v name="$name" '
  $2 == name && length($1) == 64 && $1 !~ /[^0-9a-fA-F]/ {
    count++; value=tolower($1)
  }
  END { if (count == 1) print value; else exit 1 }
' "$tmp/checksums.txt")"; then
  err "signed checksums.txt must contain exactly one SHA-256 row for $name"
fi
if have sha256sum; then got="$(sha256sum "$tmp/$name" | awk '{print tolower($1)}')"
elif have shasum; then got="$(shasum -a 256 "$tmp/$name" | awk '{print tolower($1)}')"
else err "sha256sum or shasum is required"; fi
[ "$want" = "$got" ] || err "checksum mismatch for $name (signed $want, obtained $got)"

set -- /bin/sh "$tmp/$name" --version "$tag"
[ -n "$bindir" ] && set -- "$@" --bindir "$bindir"
[ -z "$service_mode" ] || set -- "$@" "--$service_mode"
[ -z "$service_init" ] || set -- "$@" --init "$service_init"
[ -z "$service_data_dir" ] || set -- "$@" --data-dir "$service_data_dir"
[ -z "$service_config" ] || set -- "$@" --config "$service_config"
[ "$service_start" -eq 0 ] || set -- "$@" --start
exec "$@"
