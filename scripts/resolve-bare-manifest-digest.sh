#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# resolve-bare-manifest-digest.sh — resolve the digest that SLSA provenance and the image
# attestations bind to, by selecting the EXACT versioned bare manifest from GoReleaser's
# artifact list and asserting it, instead of `head -1`.
#
# WHY (design 2026-07-24, finding P1-SUSPECT / §E.1.5). The previous step piped the
# artifact list through `jq … | head -1`: whichever Docker image artifact happened to be
# FIRST — a single-arch child, the FIPS variant, whatever order GoReleaser emitted —
# became the thing the SLSA container provenance and SBOM/VEX attestations were attached
# to, while comments and downstream verification implied the multi-arch image users pull.
# First-match selection feeding a verification or attestation is the same defect class as
# the OTA provenance `head -1` in scripts/verify-release.sh: an AMBIGUOUS selection must
# fail, never guess.
#
# Contract:
#   - exactly ONE artifact of type "Docker Manifest" whose name is exactly
#     ${OCI_IMAGE_REPO}:${RELEASE_VERSION} (the bare versioned multi-arch manifest) must
#     exist in ARTIFACTS; zero or several is an error that names what WAS found;
#   - that artifact MUST carry the pushed digest in extra.Digest. The pinned GoReleaser
#     v2.17.0 always records it after `manifest push` (internal/pipe/docker/
#     manifest.go:134-162), so its ABSENCE means the artifact list did not come from the
#     pinned engine — refuse, never adopt the live registry answer alone (R1 contrast
#     P2-01);
#   - the digest is re-resolved from the live registry and must EQUAL extra.Digest — a
#     mismatch means the tag moved between push and resolution, and nothing may attest
#     to it;
#   - the resolved object must BE the expected plan: an image index/list whose children
#     are exactly the expected platforms (default linux/amd64 + linux/arm64). A digest
#     fixes what was received; only this check ties it to what was intended.
#
# TRUST BOUNDARY, DECLARED: extra.Digest and both inspections come from the same
# registry and client. The cross-check detects skew (a moved tag, a stale cache, a
# push/read race); it is NOT two independent authorities, and a registry that lies
# consistently to this client defeats it. Content addressing bounds that adversary — a
# conformant client verifies received bytes against the requested sha256 (OCI
# distribution spec §content-verification) — but registry conformance itself is outside
# what this script can prove.
#
# Env in:
#   ARTIFACTS        GoReleaser artifacts JSON (goreleaser-action `artifacts` output)
#   OCI_IMAGE_REPO   validated profile value, e.g. ghcr.io/olivaresai/olivares
#   RELEASE_VERSION  version WITHOUT the leading v (GoReleaser's {{ .Version }})
#   EXPECTED_PLATFORMS
#                    space-separated os/arch children the bare index must contain,
#                    exactly; default "linux/amd64 linux/arm64"
#   OLIVARES_RESOLVE_SELECT_ONLY=1
#                    stop after selection + metadata checks; no registry access. Used by
#                    the offline battery (scripts/test-resolve-bare-manifest-digest.sh)
#                    and usable anywhere the registry is unreachable.
# Outputs (stdout + $GITHUB_OUTPUT when set): image, manifest, digest.
set -euo pipefail

fail() {
	printf '::error::resolve-bare-manifest-digest: %s\n' "$*" >&2
	exit 1
}

[ -n "${ARTIFACTS:-}" ] || fail "ARTIFACTS is empty — pass the GoReleaser artifacts JSON"
[ -n "${OCI_IMAGE_REPO:-}" ] || fail "OCI_IMAGE_REPO is empty — pass the validated profile value"
[ -n "${RELEASE_VERSION:-}" ] || fail "RELEASE_VERSION is empty — pass the version without the leading v"

want="${OCI_IMAGE_REPO}:${RELEASE_VERSION}"

# Exactly-one selection. The filter is an equality on the full name — nothing about
# ordering can change what it returns, and 0 or >1 matches are both refusals.
count="$(printf '%s' "$ARTIFACTS" | jq -r --arg want "$want" \
	'[.[] | select(.type == "Docker Manifest" and .name == $want)] | length')" ||
	fail "ARTIFACTS is not parseable JSON"
if [ "$count" -ne 1 ]; then
	present="$(printf '%s' "$ARTIFACTS" | jq -r \
		'[.[] | select(.type == "Docker Manifest") | .name] | join(", ")' 2>/dev/null || true)"
	fail "expected exactly one 'Docker Manifest' artifact named '$want', found $count. Manifests present: ${present:-none}. Refusing to guess — first-match selection is how a single-arch child gets attested as the release image"
fi

meta_digest="$(printf '%s' "$ARTIFACTS" | jq -r --arg want "$want" \
	'[.[] | select(.type == "Docker Manifest" and .name == $want)][0].extra.Digest // empty')"
# MANDATORY under the exact pin (P2-01): v2.17.0 always records the pushed digest.
[ -n "$meta_digest" ] ||
	fail "the '$want' artifact carries no extra.Digest — the pinned GoReleaser v2.17.0 always records it after manifest push, so this artifact list is not from the pinned engine; refusing to adopt the live registry answer alone"
printf '%s' "$meta_digest" | grep -Eq '^sha256:[0-9a-f]{64}$' ||
	fail "GoReleaser recorded digest '$meta_digest' for '$want', which is not a sha256 digest"

digest="$meta_digest"
if [ "${OLIVARES_RESOLVE_SELECT_ONLY:-}" != "1" ]; then
	# The registry is asked what the tag NOW points at; it must agree with what was
	# pushed. Anything that is not a sha256 digest — including an empty answer — fails
	# rather than flowing into an attestation.
	digest="$(docker buildx imagetools inspect "$want" --format '{{.Manifest.Digest}}')" ||
		fail "docker buildx imagetools inspect failed for '$want'"
	printf '%s' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' ||
		fail "resolved '$digest' for '$want', which is not a sha256 digest"
	if [ "$digest" != "$meta_digest" ]; then
		fail "the registry resolves '$want' to $digest but GoReleaser pushed $meta_digest — the tag moved between push and resolution; nothing may be attested to it"
	fi

	# THE PLAN ASSERTION (P2-01): the digest fixes what was received; this ties it to
	# what was intended. The bare versioned object must be an image index/manifest list
	# whose children are exactly the expected platforms — no missing arch, no extra
	# child smuggled in, no single-arch manifest posing as the release image.
	manifest_json="$(docker buildx imagetools inspect "$want" --format '{{json .Manifest}}')" ||
		fail "cannot inspect the manifest content of '$want'"
	media="$(printf '%s' "$manifest_json" | jq -r '.mediaType // empty')"
	case "$media" in
	application/vnd.oci.image.index.v1+json | application/vnd.docker.distribution.manifest.list.v2+json) ;;
	*)
		fail "'$want' has mediaType '${media:-<none>}' — the bare versioned tag must be a multi-arch index/list, not a single manifest"
		;;
	esac
	got_platforms="$(printf '%s' "$manifest_json" |
		jq -r '[.manifests[]? | "\(.platform.os)/\(.platform.architecture)"] | sort | join(" ")')"
	want_platforms="$(printf '%s\n' ${EXPECTED_PLATFORMS:-linux/amd64 linux/arm64} | sort | tr '\n' ' ' | sed 's/ $//')"
	if [ "$got_platforms" != "$want_platforms" ]; then
		fail "'$want' contains children [$got_platforms] but the release plan expects exactly [$want_platforms] — refusing to attest an object that is not the intended plan"
	fi
fi

echo "resolved ${want} -> ${digest}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	{
		echo "image=${OCI_IMAGE_REPO}"
		echo "manifest=${want}"
		echo "digest=${digest}"
	} >>"$GITHUB_OUTPUT"
fi
