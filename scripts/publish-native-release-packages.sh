#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
# Finish the draft's native set before provenance and the publication ceremony.
set -euo pipefail
root="${OLIVARES_ROOT:-$(cd -- "$(dirname -- "$0")/.." && pwd)}"
cd "$root"
repo="${RELEASE_GITHUB_OWNER:?}/${RELEASE_GITHUB_NAME:?}"
tag="${RELEASE_TAG:?}"
version="${RELEASE_VERSION:?}"
[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ && "$tag" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || {
  printf '%s\n' 'native publication: invalid repository or tag' >&2; exit 1;
}
[[ "${tag#v}" == "$version" ]] || { printf '%s\n' 'native publication: tag/version mismatch' >&2; exit 1; }
# A draft is visible in the paginated release list, not the published-tag route.
release="$(gh api --paginate "repos/$repo/releases?per_page=100" | jq -es --arg tag "$tag" '
  if length > 0 and all(type == "array") then
    [.[][] | select(.tag_name == $tag)] |
    if length == 1 then .[0] else error("expected one release for the tag") end
  else error("release inventory is not a stream of arrays") end')"
jq -e --arg tag "$tag" '.tag_name == $tag and .draft == true and .prerelease == false and (.id | type == "number")' <<<"$release" >/dev/null || {
  printf '%s\n' 'native publication: target must be the exact non-prerelease draft' >&2; exit 1;
}
release_id="$(jq -r .id <<<"$release")"
flags=()
case "$tag" in v0.0.0-rehearsal.*) flags=(--snapshot) ;; esac
python3 scripts/build-native-release-packages.py --version "$version" --dist dist "${flags[@]}"
certificate=''
if [[ "${COSIGN_MODE:-keyless}" == keyless ]]; then certificate=dist/checksums.txt.pem; fi
bash scripts/cosign-verified.sh sign-blob \
  --output-signature=dist/checksums.txt.sig --output-certificate="$certificate" dist/checksums.txt --yes
assets=(dist/checksums.txt dist/checksums.txt.sig)
[[ -z "$certificate" ]] || assets+=("$certificate")
for arch in amd64 arm64; do
  for fmt in deb rpm apk; do assets+=("dist/olivares_${version}_linux_${arch}.${fmt}"); done
done
assets+=("dist/olivares_${version}_linux_amd64.pkg.tar.zst")
# Re-read the retained draft immediately before upload. GitHub provides no CAS;
# this detects observed changes and retains the serialized release ceremony.
release="$(gh api "repos/$repo/releases/$release_id")"
jq -e --arg tag "$tag" --argjson id "$release_id" '.id == $id and .tag_name == $tag and .draft == true and .prerelease == false' <<<"$release" >/dev/null || {
  printf '%s\n' 'native publication: draft changed before upload' >&2; exit 1;
}
gh release upload "$tag" --repo "$repo" --clobber "${assets[@]}"
