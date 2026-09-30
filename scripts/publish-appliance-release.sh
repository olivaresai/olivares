#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# publish-appliance-release.sh — put a built appliance's public files on the PUBLISHED GitHub
# release, and everything a release asset may not carry on the R2 image route.
#
# It runs in the publishing job, over the artifact the build job handed off: the images and
# their manifests, the SBOM and source bundle the build writes, and the SHA256SUMS the build
# signed keyless with this workflow's identity. Before anything is uploaded, that signature is
# verified again HERE, against an identity derived from the run's own repository, workflow and
# ref — the publisher does not take the producer's word for the bytes it is about to make
# public, and it does not verify against a literal that another repository could satisfy.
#
# Routing, per file, with the maintainer's preference first:
#   ≤ 2 GiB  -> a release ASSET of the published GitHub release, uploaded create-only (a
#               re-run over an existing name stops red; it never replaces delivered bytes)
#   > 2 GiB  -> scripts/publish-appliance-images.sh, bucket olivares-appliance, immutable
#               versioned prefix, index last (the desktop image is expected here)
# The signed table --sums names (SHA256SUMS-<edition> in the workflow) and its signature
# always travel as release assets, so one verification path serves both routes: everything
# the origin serves is named in the signed table.
#
#   usage: publish-appliance-release.sh --version X.Y.Z --dir DIR [--sums NAME]
#   env: GH_TOKEN, GITHUB_REPOSITORY, GITHUB_REF (the publishing run's), OLIVARES_COSIGN_BIN
#   exit 0  every file is on its route, and every route serves the verified bytes
#   exit 1  refused (nothing further is uploaded after a refusal)
#   exit 2  could not run
set -euo pipefail
export LC_ALL=C

version="" dir="" sums="SHA256SUMS"
while [ "$#" -gt 0 ]; do
	case "$1" in
	--version) version="${2:?}" ; shift 2 ;;
	--dir) dir="${2:?}" ; shift 2 ;;
	--sums) sums="${2:?}" ; shift 2 ;;
	*) printf 'publish-appliance-release: unknown argument: %s\n' "$1" >&2; exit 2 ;;
	esac
done

blind() { printf 'publish-appliance-release: UNABLE TO LOOK — %s\n' "$*" >&2; exit 2; }
refuse() { printf 'publish-appliance-release: REFUSED — %s\n' "$*" >&2; exit 1; }

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || blind '--version must be X.Y.Z'
[[ "$sums" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || blind '--sums must be a safe file name'
[[ "$dir" == /* && -d "$dir" && ! -L "$dir" ]] || blind '--dir must be an existing absolute directory'
for tool in gh curl sha256sum jq; do
	command -v "$tool" >/dev/null 2>&1 || blind "missing required tool: $tool"
done
[ -n "${GH_TOKEN:-}" ] || blind 'GH_TOKEN is not set'
[ -n "${GITHUB_REPOSITORY:-}" ] || blind 'GITHUB_REPOSITORY is not set'
[ -n "${GITHUB_REF:-}" ] || blind 'GITHUB_REF is not set (the publishing run ref anchors the signature)'
cosign_bin="${OLIVARES_COSIGN_BIN:-}"
[ -n "$cosign_bin" ] || blind 'OLIVARES_COSIGN_BIN is not set (assert-cosign-binary.sh must run first)'
case "$cosign_bin" in /*) ;; *) blind 'OLIVARES_COSIGN_BIN must be absolute' ;; esac
[ -x "$cosign_bin" ] || blind "OLIVARES_COSIGN_BIN names $cosign_bin, which is not executable"

# One file of a GitHub release may not pass 2 GiB (docs.github.com, About releases); the image
# formats declare the same ceiling as release_asset_max_bytes.
GITHUB_ASSET_MAX_BYTES=2147483648
here="$(cd "$(dirname "$0")" && pwd)"

# --- the signed table and its signature, verified before anything moves -------------------
# --sums names the edition's table (SHA256SUMS-server, SHA256SUMS-desktop): two editions
# publish to one release, and each carries its own signed table so neither replaces the
# other's bytes — the create-only upload enforces the rest.
for required in "$sums" "$sums.sig" "$sums.pem"; do
	[ -s "$dir/$required" ] || blind "the handoff directory is missing $required"
done
( cd "$dir" && sha256sum --check --quiet "$sums" ) || refuse "the files do not match the $sums the build signed"
# The identity: THIS repository, THIS workflow, THIS run's ref — derived, never a literal,
# because a literal satisfied by another repository's run would verify nothing about ours.
# shellcheck disable=SC2016 # The sed pattern needs literal regex metacharacters.
repo_rx="$(printf '%s' "$GITHUB_REPOSITORY" | sed 's/[.[\*^$()+?{}|]/\\&/g')"
# shellcheck disable=SC2016 # The ref uses the same literal regex metacharacters.
ref_rx="$(printf '%s' "$GITHUB_REF" | sed 's/[.[\*^$()+?{}|]/\\&/g')"
cert_identity="^https://github\.com/${repo_rx}/\.github/workflows/appliance-image\.yml@${ref_rx}\$"
"$cosign_bin" verify-blob \
	--certificate "$dir/$sums.pem" --signature "$dir/$sums.sig" \
	--certificate-identity-regexp "$cert_identity" \
	--certificate-oidc-issuer https://token.actions.githubusercontent.com \
	--certificate-github-workflow-repository "$GITHUB_REPOSITORY" \
	"$dir/$sums" || refuse "$sums is not signed by this workflow on ${GITHUB_REF}"

# --- the release must be PUBLISHED, and this run must be building its commit -------------
release_json="$(mktemp "${TMPDIR:-/tmp}/par-release.XXXXXX")" || blind 'cannot allocate a temp file'
if ! gh release view "$version" --json isDraft,tagName,url >"$release_json" 2>/dev/null; then
	blind "could not read the release $version (is it published? the appliance publishes only after the stable ceremony)"
fi
[ "$(jq -r '.tagName' "$release_json")" = "$version" ] || refuse "the release found for $version reports tag $(jq -r '.tagName' "$release_json")"
[ "$(jq -r '.isDraft' "$release_json")" = "false" ] ||
	refuse "the release $version is still a DRAFT: the stable ceremony's finalizer refuses assets it did not admit, so the appliance route attaches only to a published release"
# The asset table is read once into a file and filtered with jq here (gh's own --jq would run
# inside the CLI, where a stub or a future refactor could answer something the rest of this
# script never sees).
assets_json="$(mktemp "${TMPDIR:-/tmp}/par-assets.XXXXXX")" || blind 'cannot allocate the asset table'
gh release view "$version" --json assets >"$assets_json" || blind "could not read the assets of $version"
asset_url() { jq -r --arg n "$1" '.assets[] | select(.name == $n) | .browser_download_url // empty' "$assets_json"; }
asset_exists() { jq -e --arg n "$1" '.assets[] | select(.name == $n)' "$assets_json" >/dev/null; }

# --- route each file ----------------------------------------------------------------------
mapfile -t all_names < <(cd "$dir" && find . -maxdepth 1 -type f -printf '%f\n' | sort)
github_names=() r2_names=()
for name in "${all_names[@]}"; do
	[[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || refuse "unsafe public file name: '$name'"
	size="$(wc -c <"$dir/$name" | tr -d ' ')"
	if [ "$size" -lt "$GITHUB_ASSET_MAX_BYTES" ]; then
		github_names+=("$name")
	else
		r2_names+=("$name")
	fi
done
[ "${#github_names[@]}" -gt 0 ] || refuse 'nothing fits a release asset; the whole set belongs on the R2 route'

# SHA256SUMS and its signature always land among the release assets by their own size (they
# are kilobytes against the 2 GiB ceiling), which is what keeps ONE verification path serving
# both routes: everything the R2 origin serves is named in the signed table on the release.

# CREATE-ONLY: no --clobber. gh refuses a name that already exists, which is the guarantee
# this route wants — a re-run stops red instead of replacing delivered bytes.
set +e
( cd "$dir" && gh release upload "$version" "${github_names[@]}" )
upload_rc=$?
set -e
if [ "$upload_rc" -ne 0 ]; then
	already=0
	for name in "${github_names[@]}"; do
		if asset_exists "$name"; then
			already=$((already + 1))
		fi
	done
	if [ "$already" -eq 0 ]; then
		refuse "the release asset upload failed (gh exit ${upload_rc})"
	else
		refuse "the release already carries ${already} of this set's names (gh exit ${upload_rc}); create-only refuses to replace delivered bytes"
	fi
fi

# THE TABLE IS RE-READ AFTER THE UPLOAD: the pre-upload table cannot name what this run just
# added, and the postconditions below resolve download URLs from it.
gh release view "$version" --json assets >"$assets_json" || blind "could not re-read the assets of $version after the upload"

# --- the R2 leg for what a release asset may not carry -----------------------------------
if [ "${#r2_names[@]}" -gt 0 ]; then
	r2_dir="$(mktemp -d "${TMPDIR:-/tmp}/par-r2.XXXXXX")" || blind 'cannot allocate the R2 staging directory'
	for name in "${r2_names[@]}"; do
		ln "$dir/$name" "$r2_dir/$name" 2>/dev/null || cp "$dir/$name" "$r2_dir/$name"
	done
	# shellcheck disable=SC2154
	bash "$here/publish-appliance-images.sh" --version "$version" --dir "$r2_dir" \
		--origin "${OLIVARES_APPLIANCE_ORIGIN:?}" --run-id "${GITHUB_RUN_ID:-unknown}" ||
		refuse 'the R2 image route refused'
fi

# --- postconditions: every route serves the verified bytes --------------------------------
for name in "${github_names[@]}"; do
	url="$(asset_url "$name")"
	[ -n "$url" ] || refuse "the published release exposes no download URL for $name"
	tmp="$(mktemp "${TMPDIR:-/tmp}/par-dl.XXXXXX")" || blind 'cannot allocate a download temp file'
	curl --fail --location --silent --show-error --max-time 1800 --output "$tmp" "$url" ||
		refuse "public delivery of $name failed ($url)"
	cmp -s "$tmp" "$dir/$name" || refuse "the delivered $name is not the verified bytes"
	rm -f "$tmp"
done
printf 'publish-appliance-release: PUBLISHED — %d release assets' "${#github_names[@]}"
[ "${#r2_names[@]}" -eq 0 ] || printf ' and %d R2 objects' "${#r2_names[@]}"
printf ', every byte verified before upload and read back after\n'
