#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Install only the artifact approved by assert-cosign-binary.sh, before executing it.
set -euo pipefail
[[ $# == 2 && "$1" == --dest && "$2" == /* ]] || { echo 'usage: ensure-cosign.sh --dest /absolute/directory' >&2; exit 2; }
dest=$2
case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) exit 2 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) exit 2 ;; esac
asset="cosign-$os-$arch"
# Single source of approved binary digests; do not execute or source the assertion script.
expected=$(awk -v name="$asset" '$2 == name { print $1 }' "$(dirname "$0")/assert-cosign-binary.sh")
[[ ${#expected} == 64 && "$expected" != *[!0-9a-f]* ]] || { echo 'missing pinned cosign digest' >&2; exit 2; }
digest() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}
mkdir -p "$dest"
if [[ -x "$dest/cosign" && $(digest "$dest/cosign") == "$expected" ]]; then
  printf '%s\n' "$dest/cosign"
  exit 0
fi
tmp=$(mktemp -d "$dest/.cosign.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
fetch() { curl --proto '=https' --proto-redir '=https' -fsSL --retry 1 --connect-timeout 20 --max-time 120 "$@"; }
if ! fetch -o "$tmp/cosign" "https://github.com/sigstore/cosign/releases/download/v2.6.4/$asset"; then
  echo 'ensure-cosign: release download failed; trying release API' >&2
  url=$(fetch https://api.github.com/repos/sigstore/cosign/releases/tags/v2.6.4 |
    jq -er --arg name "$asset" '.assets[] | select(.name == $name) | .url')
  fetch -H 'Accept: application/octet-stream' -o "$tmp/cosign" "$url"
fi
[[ $(digest "$tmp/cosign") == "$expected" ]] || { echo 'cosign digest mismatch; refusing to install' >&2; exit 1; }
chmod 0755 "$tmp/cosign"
mv -f "$tmp/cosign" "$dest/cosign"
printf '%s\n' "$dest/cosign"
