#!/bin/sh
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Print the shared version stamps. Optional arguments preserve snapshot/container overrides.
# Stripping and release trust anchors remain the caller's build policy.
set -eu
mode=flags
case "${1:-}" in
  --version) mode=version; shift ;;
  --check-version) mode=check; shift ;;
esac
[ "$#" -le 3 ] || { echo 'usage: build-ldflags.sh [version [commit [date]]]' >&2; exit 2; }
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$root/scripts/lib/git-env.sh"
canon=$(awk '!/^[[:space:]]*(#|$)/ {print}' "$root/RELEASE-VERSION")
printf '%s\n' "$canon" | grep -Eq '^[0-9]+\.[0-9]+$' || {
  echo 'build-ldflags: RELEASE-VERSION must contain one MAJOR.MINOR record' >&2; exit 2;
}
case "$canon" in *[!0-9.]*) echo 'build-ldflags: invalid RELEASE-VERSION record' >&2; exit 2 ;; esac
if [ "$mode" = check ]; then
  case "${1:-}" in
    "$canon"|"$canon"-SNAPSHOT-?*) exit 0 ;;
    *) echo 'build-ldflags: build version differs from RELEASE-VERSION' >&2; exit 2 ;;
  esac
fi
if [ "$mode" = version ]; then printf '%s\n' "$canon"; exit; fi
version=${1:-$canon}
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+(-[A-Za-z0-9_.+-]+)?$' || {
  echo 'build-ldflags: build version must have a MAJOR.MINOR core' >&2; exit 2;
}
commit=${2:-$(git -C "$root" rev-parse --short HEAD 2>/dev/null || printf none)}
if [ -n "${3:-}" ]; then
  build_date=$3
else
  epoch=${SOURCE_DATE_EPOCH:-$(git -C "$root" log -1 --format=%ct 2>/dev/null || printf 0)}
  build_date=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$epoch" +%Y-%m-%dT%H:%M:%SZ)
fi
# Go parses -ldflags a second time: whitespace/quotes could inject a second -X.
for value in "$version" "$commit" "$build_date"; do
  case "$value" in
    ''|*[!a-zA-Z0-9_.:+-]*) echo 'build-ldflags: invalid version, commit or date' >&2; exit 2 ;;
  esac
done
printf '%s\n' "-X main.version=$version -X main.commit=$commit -X main.date=$build_date"
