#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
#
# update-cosign-digests.sh — the AUDITABLE procedure for refreshing the digest table that
# scripts/assert-cosign-binary.sh enforces.
#
# WHY THIS IS A SCRIPT AND NOT A PARAGRAPH. The integrator's question was: where does the
# expected digest come from at execution time? The answer must be "a list versioned in this
# repository", because fetching `cosign_checksums.txt` while signing would put a network
# dependency and a remote trust point ON THE SIGNING PATH. But "versioned in the repo" is
# only as good as the procedure that puts values there: a table someone pasted from a
# browser is a table nobody can re-derive. So the network step lives HERE — deliberate,
# separate, and repeatable — and produces output a reviewer can reproduce byte-for-byte.
#
# WHAT IT DOES
#   1. downloads cosign_checksums.txt and its keyless signature material for <version>;
#   2. VERIFIES that signature against sigstore's own release identity before reading a
#      single digest out of the file (an unverified checksums file proves nothing at all);
#   3. extracts every PLAIN raw binary line — not the -pivkey-pkcs11key- variants, which are
#      a different build, and not the .deb/.rpm/.apk/.sbom.json entries, which are not the
#      executable that signs;
#   4. prints the table in exactly the form assert-cosign-binary.sh embeds.
#
# WHAT IT DELIBERATELY DOES NOT DO. It does not edit assert-cosign-binary.sh. Pasting the
# output is a reviewed, human step: this table is the root of the release's binary trust,
# and a script that silently rewrote it would be a supply-chain hole shaped like automation.
#
# BEFORE PROMOTING A NEW VERSION, or opening a migration window for one, run
# scripts/check-cosign-contract.sh against the new binary. A digest table proves the binary
# is authentic; only the contract fixture proves it still speaks this repository's signing
# contract. Both are required, and they are not substitutes for one another.
#
# THE BOOTSTRAP IS CIRCULAR, AND THAT IS NOT FIXED HERE. This script verifies the checksums
# file USING cosign — the same tool whose next version it is about to approve. If the cosign
# doing the verifying were already compromised, its verdict would be worthless. The
# circularity is inherent to verifying a signing tool with a signing tool, and the honest
# mitigations are procedural, not scriptable:
#   * run it with a cosign that ALREADY passed scripts/assert-cosign-binary.sh, so the
#     verifier is itself a digest-matched upstream artifact;
#   * treat the printed table as a PROPOSAL to be reviewed in a pull request, never as an
#     automatic edit — which is why this script deliberately does not write the file;
#   * for the first cosign ever trusted here, or after any compromise, re-establish the
#     digest out of band from sigstore's published release notes rather than from this tool.
# Anyone relying on this procedure should know its limit rather than infer a guarantee.
#
# USAGE
#   scripts/update-cosign-digests.sh v2.6.4
#   scripts/update-cosign-digests.sh v3.1.2      # e.g. when preparing a migration window
set -euo pipefail

VERSION="${1:-}"
[ -n "$VERSION" ] || {
	echo "usage: $0 <version>   (e.g. v2.6.4)" >&2
	exit 2
}
case "$VERSION" in
v[0-9]*) ;;
*)
	echo "::error::update-cosign-digests: version must look like vX.Y.Z (got: $VERSION)." >&2
	exit 2
	;;
esac

# sigstore signs its own releases with a GCP service account, NOT a GitHub Actions identity.
# Asserting the latter fails with "none of the expected identities matched ... got subjects
# [keyless@projectsigstore.iam.gserviceaccount.com] with issuer https://accounts.google.com",
# which is how this was established on 2026-07-25 rather than assumed.
IDENTITY="${COSIGN_RELEASE_IDENTITY:-keyless@projectsigstore.iam.gserviceaccount.com}"
ISSUER="${COSIGN_RELEASE_ISSUER:-https://accounts.google.com}"
BASE="https://github.com/sigstore/cosign/releases/download/${VERSION}"

command -v cosign >/dev/null 2>&1 || {
	echo "::error::update-cosign-digests: cosign is not on PATH; it is needed to VERIFY the checksums file." >&2
	exit 1
}
command -v curl >/dev/null 2>&1 || {
	echo "::error::update-cosign-digests: curl is not on PATH." >&2
	exit 1
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"

for f in cosign_checksums.txt cosign_checksums.txt-keyless.sig cosign_checksums.txt-keyless.pem; do
	if ! curl -fsSL -o "$f" "${BASE}/${f}"; then
		echo "::error::update-cosign-digests: could not download ${BASE}/${f}." >&2
		echo "If the release exists, the signature artifact naming may have changed; check the release assets." >&2
		exit 1
	fi
	# GitHub answers a missing asset with a short body rather than a 404 in some paths;
	# a 9-byte "Not Found" must not be mistaken for a signature.
	[ "$(wc -c <"$f")" -gt 64 ] || {
		echo "::error::update-cosign-digests: ${f} is only $(wc -c <"$f") bytes — that is not the artifact, it is an error page." >&2
		exit 1
	}
done

# The one network-using verification. It is READ-ONLY: it cannot write to the transparency
# log, which is why the containment guard has a narrow, verb-restricted exception for it
# rather than requiring the blanket signing escape.
echo "update-cosign-digests: verifying cosign_checksums.txt for ${VERSION} …" >&2
if ! OLIVARES_COSIGN_ALLOW_VERIFY_NETWORK=1 cosign verify-blob \
	--certificate cosign_checksums.txt-keyless.pem \
	--signature cosign_checksums.txt-keyless.sig \
	--certificate-identity "$IDENTITY" \
	--certificate-oidc-issuer "$ISSUER" \
	cosign_checksums.txt; then
	echo "::error::update-cosign-digests: the checksums file for ${VERSION} did NOT verify against" >&2
	echo "  identity: ${IDENTITY}" >&2
	echo "  issuer:   ${ISSUER}" >&2
	echo "Do NOT copy any digest out of it. Either the identity changed (confirm from sigstore's" >&2
	echo "release documentation and update COSIGN_RELEASE_IDENTITY) or the file is not authentic." >&2
	exit 1
fi

echo "" >&2
echo "update-cosign-digests: verified. Paste the block below into APPROVED_DIGESTS (or" >&2
echo "MIGRATION_DIGESTS) in scripts/assert-cosign-binary.sh, and update the PROVENANCE note." >&2
echo "" >&2

# Plain raw binaries only. `grep -v pivkey` and the sbom/package exclusions are the scope
# decision documented in assert-cosign-binary.sh; keep the two in step.
grep -E '  cosign-(darwin|linux|windows)[a-z0-9.-]*$' cosign_checksums.txt |
	grep -v -- '-pivkey-' |
	grep -v -- '.sbom.json'

echo "" >&2
echo "update-cosign-digests: REMINDER — run scripts/check-cosign-contract.sh against this" >&2
echo "binary before approving it. Authenticity and contract compliance are different claims." >&2
