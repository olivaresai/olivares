---
title: Verify what you downloaded
description: >-
  Verify a release's signature, SLSA provenance, SBOM and OpenVEX attestations
  before you run it. Never pipe an installer straight into a shell.
---

> Business deployment packages are supplied through the Business channel; their publication is unverified here. Verify the chart package and its publisher using the channel instructions before using the local chart below. The flat manifest example uses a Business-supplied file named `business-install.yaml`. Air-gapped installation requires Enterprise.


> Helm, Kubernetes operators, Terraform, appliance and FIPS/STIG images are Business deployment artifacts. The source paths below are in the Business distribution. Air-gapped installation requires Enterprise.

The next release is <!-- release -->`0.1`<!-- /release -->; its GitHub release is not published yet. The commands below describe the planned artifacts. Build from source until publication, then verify each artifact before use. See <!-- release -->`docs/releases/0.1-install-surfaces.json`<!-- /release --> for the observed publication state.

A control plane is a security product, so the first thing you should do with a
release is **prove it is the one the project published**. Olivares AI releases ship
everything you need to verify cryptographically: a signature over the checksums, a
SLSA provenance attestation, and one SBOM (SPDX) and one OpenVEX document, both
attested to the container image — all referenced **by digest, never by tag**.

:::danger[Never `curl | bash`]
Do not pipe an installer into a shell. Download the artifacts, **verify them**, and
only then run them. The steps below are how.
:::

## What ships with a release

| Artifact | What it is |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 of the archives, packages, installer, `release-commit.txt` and `release-build-context.json`, with a cosign signature and certificate |
| `*_<os>_<arch>.tar.gz` | the release archive(s) |
| `olivares.spdx.sbom.json` | the release SBOM (SPDX), generated from the container image and attested to it by digest |
| `olivares.vex.openvex.json` | the release OpenVEX, attested to the container image by digest |
| `*.intoto.jsonl` | SLSA Build L3 provenance |
| container image | published to GHCR and Docker Hub, pinned and verified by digest |
| Helm chart source | install from `./business-chart`; its OCI publication is unverified (`publication-unverified`: never published from this repository) |

Releases up to 26.10.1<!-- release-fixed --> instead carry a per-archive SBOM and OpenVEX bundle
(`*.sbom.sigstore.json`, `*.vex.sigstore.json`); later releases do not.

## The one-command path

The repository ships `scripts/verify-release.sh`, which runs the full chain:
verifies the signature over `checksums.txt`, re-computes every artifact's SHA-256,
then verifies the per-archive SBOM and OpenVEX bundles of releases up to 26.10.1<!-- release-fixed -->
(later releases report these two steps as skipped) and the SLSA provenance.

<!-- release -->
```bash
# The verifier is in a source checkout of the release tag, not a release asset; running it
# trusts the checkout. Without one, INSTALL.md shows the cosign + sha256sum commands.
# Run it from the directory that holds the downloaded files.

# Default: keyless (Sigstore). Needs Rekor and Sigstore trusted-root material.
/path/to/olivares/scripts/verify-release.sh

# Pin the SLSA provenance to a specific source tag.
/path/to/olivares/scripts/verify-release.sh --source-tag 0.1

# Key-based: only for files signed with a private key you control.
# Releases are signed keyless and do not publish a public key.
/path/to/olivares/scripts/verify-release.sh --key /path/to/your-cosign.pub
```
<!-- /release -->

`--key` checks signatures against a public key instead of the release workflow's identity, and
ignores the transparency log. It proves that the files were signed with the matching private
key, not that the project published them. Get that public key from its owner through a channel
separate from the files you verify.

`--offline` removes only the Rekor lookup from the cosign calls; it does not make verification
network-free. Keyless verification still needs Sigstore trusted-root material, which cosign
fetches unless it is already cached, and the script has no `--trusted-root` option. The per-archive
SBOM and OpenVEX bundle checks (releases up to 26.10.1<!-- release-fixed -->) need a trusted root even with `--key`, and the SLSA step runs
`slsa-verifier` without an offline option.

## What it checks, step by step

If you prefer to run the checks yourself, this is what the script does:

1. **Signature over the checksums** — keyless, verified against the project's GitHub
   Actions identity and OIDC issuer:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Artifact integrity** — every downloaded artifact must match `checksums.txt`:

   ```bash
   # checksums.txt lists all release artifacts; skip files you did not download.
   # Before use, ensure each downloaded artifact is listed and reports OK.
   # GNU sha256sum fails if no listed file is present or a checksum mismatches.
   sha256sum --check --ignore-missing checksums.txt
   ```

3. **SBOM (SPDX) attestation** — per-archive bundle, releases up to 26.10.1<!-- release-fixed --> only. Later
   releases carry one SBOM attested to the container image; see *Verifying the container image* below.

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **OpenVEX attestation** (the project's reachability-based vulnerability statement) — per-archive
   bundle, releases up to 26.10.1<!-- release-fixed --> only. Later releases carry one OpenVEX attested to the
   container image; see *Verifying the container image* below.

   ```bash
   cosign verify-blob-attestation --type openvex \
     --bundle <artifact>.vex.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

5. **SLSA provenance:**

   ```bash
   slsa-verifier verify-artifact <artifact> \
     --provenance-path <artifact>.intoto.jsonl \
     --source-uri github.com/olivaresai/olivares
   ```

## Verifying the container image

For the published image, resolve the digest and verify against the GitHub Actions
identity (this path is keyless and needs network). The `spdxjson` and `openvex` attestations
are the release SBOM (`olivares.spdx.sbom.json`) and OpenVEX (`olivares.vex.openvex.json`),
attested to the image by digest:

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
slsa-verifier verify-image "$REF" \
  --source-uri github.com/olivaresai/olivares --source-tag <version>
```

Always deploy the image **by digest** (`@sha256:…`), never by a mutable tag.

## In an air-gapped environment

An operator builds the **air-gap bundle** with a cosign key they control, and the bundle
carries the matching `cosign.pub`. Its scripts check the Helm chart and the saved images
against that key without Rekor; an image passes only if it carries a signature made with that
key. A key read from the bundle it verifies does not authenticate that bundle: before you trust
the bundle, compare its key with a copy that the key owner gave you through a separate channel.
See [Install in an air-gapped environment](/how-to/air-gap-install/).

:::note[Honest note on attestation availability]
Verification is only as complete as the attestations a given release actually
published. The verifier reports each step it runs; if a release omits an artifact
(for example a build that did not attach an SBOM), the corresponding step has nothing
to check. The release workflow attaches the SBOM, OpenVEX and SLSA artifacts named
above for the standard build.
:::
