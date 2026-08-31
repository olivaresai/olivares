---
title: Verify what you downloaded
description: >-
  Verify a release's signature, SLSA provenance, SBOM and OpenVEX attestations
  before you run it — online (keyless) or fully offline (key-based). Never pipe
  an installer straight into a shell.
---

A control plane is a security product, so the first thing you should do with a
release is **prove it is the one the project published**. Olivares AI releases ship
everything you need to verify cryptographically: a signature over the checksums, a
SLSA provenance attestation, an SBOM (SPDX + CycloneDX), and an OpenVEX
attestation — all referenced **by digest, never by tag**.

:::danger[Never `curl | bash`]
Do not pipe an installer into a shell. Download the artifacts, **verify them**, and
only then run them. The steps below are how.
:::

## What ships with a release

| Artifact | What it is |
|---|---|
| `checksums.txt` (+ `.sig`, `.pem`) | SHA-256 of every artifact, with a cosign signature and certificate |
| `*_<os>_<arch>.tar.gz` | the release archive(s) |
| `*.sbom.sigstore.json` | SBOM (SPDX) as a signed in-toto attestation |
| `*.vex.sigstore.json` | OpenVEX as a signed in-toto attestation |
| `*.intoto.jsonl` | SLSA Build L3 provenance |
| container image + Helm chart | published to a registry at release, pinned by digest |

## The one-command path

The repository ships `scripts/verify-release.sh`, which runs the full chain:
verifies the signature over `checksums.txt`, re-computes every artifact's SHA-256,
then verifies the SBOM, OpenVEX and SLSA attestations.

```bash
# Default: keyless (Sigstore). Needs network access to the transparency log (Rekor).
scripts/verify-release.sh

# Key-based (air-gap friendly): verify against the project's public key.
scripts/verify-release.sh --key cosign.pub

# Fully offline: no Rekor / no transparency-log network at all.
scripts/verify-release.sh --key cosign.pub --offline

# Pin the SLSA provenance to a specific source tag.
scripts/verify-release.sh --source-tag v26.8.0
```

With `--offline` (or whenever a key is supplied) the script adds
`--insecure-ignore-tlog` to every cosign call, so no Sigstore/Rekor network is used —
this is the path for disconnected environments.

## What it checks, step by step

If you prefer to run the checks yourself, this is what the script does:

1. **Signature over the checksums** — keyless, verified against the project's GitHub
   Actions identity and OIDC issuer:

   ```bash
   cosign verify-blob \
     --certificate checksums.txt.pem \
     --signature checksums.txt.sig \
     --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
     checksums.txt
   ```

2. **Artifact integrity** — every downloaded artifact must match `checksums.txt`:

   ```bash
   sha256sum --check checksums.txt
   ```

3. **SBOM (SPDX) attestation:**

   ```bash
   cosign verify-blob-attestation --type spdxjson \
     --bundle <artifact>.sbom.sigstore.json --new-bundle-format \
     --check-claims <artifact>
   ```

4. **OpenVEX attestation** (the project's reachability-based vulnerability statement):

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
identity (this path is keyless and needs network):

```bash
IMAGE=docker.io/olivaresai/olivares
DIGEST="$(crane digest "$IMAGE:<version>")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
slsa-verifier verify-image "$REF" \
  --source-uri github.com/olivaresai/olivares --source-tag <version>
```

Always deploy the image **by digest** (`@sha256:…`), never by a mutable tag.

## In an air-gapped environment

If you cannot reach the network at all, use the **air-gap bundle**, which carries a
public key and verifies everything offline (no Rekor). See
[Install in an air-gapped environment](/how-to/air-gap-install/).

:::note[Honest note on attestation availability]
Verification is only as complete as the attestations a given release actually
published. The verifier reports each step it runs; if a release omits an artifact
(for example a build that did not attach an SBOM), the corresponding step has nothing
to check. The release workflow attaches the SBOM, OpenVEX and SLSA artifacts named
above for the standard build.
:::
