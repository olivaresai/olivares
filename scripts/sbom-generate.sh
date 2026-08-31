#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# Generate an SBOM with syft and add the CISA-2025-draft Generation Context
# field in the format-specific location used by scripts/sbom-check-cisa.sh.
set -euo pipefail

usage() {
  echo "usage: $0 <artifact> <spdx-json|cyclonedx-json@1.6> <output>" >&2
}

if [ "$#" -ne 3 ]; then
  usage
  exit 2
fi

artifact="$1"
format="$2"
output="$3"

case "$format" in
  spdx-json|cyclonedx-json@1.6) ;;
  *)
    echo "error: unsupported SBOM format: $format" >&2
    usage
    exit 2
    ;;
esac

command -v syft >/dev/null || { echo "error: syft not found" >&2; exit 2; }
command -v jq >/dev/null || { echo "error: jq not found" >&2; exit 2; }

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(dirname "$script_dir")"
outdir="$(dirname -- "$output")"
[ -d "$outdir" ] || { echo "error: output directory does not exist: $outdir" >&2; exit 2; }

tmp="$(mktemp "${output}.syft.XXXXXX")"
edited="$(mktemp "${output}.edited.XXXXXX")"
cleanup() {
  rm -f "$tmp" "$edited"
}
trap cleanup EXIT

syft_args=("$artifact")
if [ -f "$repo_root/.syft.yaml" ]; then
  syft_args+=("--config" "$repo_root/.syft.yaml")
fi
syft_args+=("--output" "${format}=${tmp}")

syft "${syft_args[@]}"
jq -e . "$tmp" >/dev/null

case "$format" in
  spdx-json)
    jq '.creationInfo.comment = "Generation context: during build - generated from the release artifact by the release pipeline (post-build, pre-publish)."' \
      "$tmp" > "$edited"
    ;;
  cyclonedx-json@1.6)
    jq '.metadata = ((.metadata // {}) + {lifecycles: [{phase: "build"}]})' \
      "$tmp" > "$edited"
    ;;
esac

jq -e . "$edited" >/dev/null
mv "$edited" "$output"
