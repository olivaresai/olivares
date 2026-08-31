#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# alias-image-digest.sh — create a mutable alias tag for an already-verified digest and
# PROVE the alias still resolves to that exact digest before declaring success.
#
# WHY (R1 contrast P1-01, measured against Docker's primary documentation). The naive
# `docker buildx imagetools create --tag REPO:alias REPO@sha256:D` is NOT an aliasing
# operation for every source: with a single source that is not already a manifest
# list/index, imagetools defaults to `--prefer-index=true` and WRAPS the manifest in a
# NEW index — a new object with a NEW digest. The per-arch `latest-amd64`/`latest-arm64`
# aliases would then point at unsigned, unattested indexes while the job log claims the
# verified signature covers them: signature verified on the child, a DIFFERENT object
# published. Identity of artifact is the whole point of promotion, so this script:
#
#   1. inspects the SOURCE mediaType and passes `--prefer-index=false` when the source
#      is a single image manifest (index/list sources are copied as-is; an unknown
#      mediaType refuses — fail-closed, never guess a format);
#   2. creates the alias tag from the digest, never from a tag;
#   3. re-resolves the ALIAS from the registry and REQUIRES digest(alias) == SRC_DIGEST.
#      The equality assertion is the load-bearing part: whatever imagetools, a proxy or
#      the registry did, an alias that stopped being the verified bytes fails RED here,
#      before any success line is printed.
#
# The caller performs signature verification of SRC_DIGEST *before* invoking this script
# (.github/workflows/release.yml promote-latest); this script owns format preservation
# and identity, not trust.
#
# Env in:
#   OCI_IMAGE_REPO  registry/repository, e.g. ghcr.io/olivaresai/olivares
#   SRC_DIGEST      sha256:<64 hex> — the verified source digest
#   ALIAS_TAG       the alias to create, e.g. latest, latest-amd64
set -euo pipefail

fail() {
	printf '::error::alias-image-digest: %s\n' "$*" >&2
	exit 1
}

[ -n "${OCI_IMAGE_REPO:-}" ] || fail "OCI_IMAGE_REPO is empty"
[ -n "${ALIAS_TAG:-}" ] || fail "ALIAS_TAG is empty"
printf '%s' "${SRC_DIGEST:-}" | grep -Eq '^sha256:[0-9a-f]{64}$' ||
	fail "SRC_DIGEST '${SRC_DIGEST:-}' is not a sha256 digest"

src_ref="${OCI_IMAGE_REPO}@${SRC_DIGEST}"
alias_ref="${OCI_IMAGE_REPO}:${ALIAS_TAG}"

media="$(docker buildx imagetools inspect "$src_ref" --format '{{.Manifest.MediaType}}')" ||
	fail "cannot inspect the source mediaType of ${src_ref}"
case "$media" in
*application/vnd.oci.image.index.v1+json* | *application/vnd.docker.distribution.manifest.list.v2+json*)
	# Index/list source: created as-is; the digest assertion below still decides.
	create_args=()
	;;
*application/vnd.oci.image.manifest.v1+json* | *application/vnd.docker.distribution.manifest.v2+json*)
	# Single-manifest source: WITHOUT this flag imagetools mints a NEW wrapping index
	# with a NEW digest (--prefer-index defaults to true) — the P1-01 defect.
	create_args=(--prefer-index=false)
	;;
*)
	fail "source ${src_ref} has unrecognized mediaType '${media}' — refusing to alias a format this script has not reasoned about"
	;;
esac

docker buildx imagetools create "${create_args[@]}" --tag "$alias_ref" "$src_ref" ||
	fail "imagetools create failed for ${alias_ref}"

# THE assertion (P1-01 remediation 3): the alias must BE the verified object.
dest="$(docker buildx imagetools inspect "$alias_ref" --format '{{.Manifest.Digest}}')" ||
	fail "cannot re-inspect ${alias_ref} after creation"
if [ "$dest" != "$SRC_DIGEST" ]; then
	fail "alias ${alias_ref} resolves to ${dest}, not the verified source ${SRC_DIGEST} — the alias does NOT carry the signature/attestations just verified; refusing to declare success"
fi

echo "aliased ${alias_ref} -> ${SRC_DIGEST} (mediaType ${media}; alias digest re-verified equal)"
