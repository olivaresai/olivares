#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
set -euo pipefail

root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)}"
version="${1:-}"
output="${2:-}"
is_snapshot="${3:-false}"
marker='@OLIVARES_INSTALLER_VERSION@'

case "$is_snapshot" in
  false)
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
      printf 'error: release version must be YY.M.PATCH without v: %s\n' "$version" >&2
      exit 1
    }
    embedded_version="$version"
    ;;
  true)
    [[ -n "$version" ]] || { printf 'error: snapshot version is empty\n' >&2; exit 1; }
    embedded_version=SNAPSHOT
    ;;
  *)
    printf 'error: snapshot flag must be true or false: %s\n' "$is_snapshot" >&2
    exit 1
    ;;
esac
[[ -n "$output" ]] || { printf 'error: output path is required\n' >&2; exit 1; }
source_file="$root/scripts/install.sh"
[[ -f "$source_file" ]] || { printf 'error: missing installer source: %s\n' "$source_file" >&2; exit 1; }
[[ "$(grep -Foc "$marker" "$source_file")" -eq 1 ]] || {
  printf 'error: installer source must contain exactly one version marker\n' >&2
  exit 1
}

mkdir -p -- "$(dirname -- "$output")"
# GoReleaser can render the same asset for several targets concurrently. Keep
# each temporary beside the output so publication uses one atomic rename even
# when the caller's TMPDIR is on another filesystem.
tmp="$(mktemp "$(dirname -- "$output")/.olivares-render-installer.XXXXXX")"
trap 'rm -f -- "$tmp"' EXIT
sed "s/$marker/$embedded_version/" "$source_file" >"$tmp"
! grep -Fq "$marker" "$tmp" || { printf 'error: unresolved installer marker\n' >&2; exit 1; }
grep -Fq "EMBEDDED_VERSION='$embedded_version'" "$tmp" || {
  printf 'error: rendered installer does not contain the requested pin\n' >&2
  exit 1
}
/bin/sh -n "$tmp"
chmod 0755 "$tmp"
mv -- "$tmp" "$output"
trap - EXIT
printf 'rendered %s\n' "$output"
