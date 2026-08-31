# ADR-0015: Supply chain — signed releases, SBOM, SLSA provenance, OpenVEX, distroless

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions (T6/T7); supply-chain & release-verification design

## Context and problem statement

For a security product, an unsigned or unverifiable release is unacceptable. Buyers
need to verify *what they downloaded* — including fully offline, in air-gapped
environments — and to know the provenance and known-vulnerability status of every
artifact.

## Decision drivers

- Cryptographic verifiability of every artifact, online and offline.
- Provenance (who built it, from what source) and a software bill of materials.
- A minimal, pinned runtime image.

## Considered options

- **cosign/sigstore signatures + SBOM (syft) + SLSA Build L3 (SLSA v1.2) provenance + OpenVEX +
  distroless images pinned by digest**, with an offline verification path and an
  air-gap bundle.
- **Checksums only / unsigned releases.**

## Decision outcome

Chosen option: the **full supply-chain set**. Releases ship cosign signatures, SPDX +
CycloneDX SBOMs, SLSA Build L3 provenance and OpenVEX attestations; container images
are **distroless, pinned by digest**. A verification script checks the whole chain,
including a **fully offline** mode, and an **air-gap bundle** carries a public key so a
disconnected site can verify everything without a transparency log.

### Consequences

- **Good:** every artifact is verifiable, online or offline; provenance and an SBOM
  ship with each release; the runtime image is minimal and immutable (by digest).
- **Bad / trade-offs:** more release machinery to maintain; the air-gap bundle requires
  the SBOM/VEX/provenance to be supplied to the bundler.
- **Neutral:** deploy is always by digest, never by tag.

## Why the alternatives were rejected

- **Checksums only / unsigned** — provides no provenance, no offline trust root, and no
  vulnerability statement; unacceptable in a security product.

## Addendum (2026-07-03): SLSA v1.2 wording + Source track evaluation

SLSA wording is normalized to **SLSA Build L3 (SLSA v1.2)**. In SLSA v1.2,
the Build track tops out at L3, so this ADR claims only the Build-track level.

Source-track evaluation remains separate. Source L1-L3 would require retained source
revisions plus provenance attestations from the source control system; Source L3 adds
continuous tamper-evident enforcement, for example gittuf or platform attestations.

Current status: branch protection is scripted in `scripts/apply-branch-protection.sh`,
but source-provenance attestations are not deployed.

Decision: no Source-track level is claimed; watch the Source track and revisit at public launch.
