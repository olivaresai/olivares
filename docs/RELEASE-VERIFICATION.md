<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Release verification contract

For a self-hosted security product the build pipeline is part of the trust model
(`docs/SECURITY-HARDENING.md`): if the artifact is not verifiably ours, nothing else matters.
This document is the contract for **what accompanies every release** and **exactly
how to verify it** — for a buyer, an auditor, or a distrustful sysadmin.

Releases are built on **GitHub Actions** (the substrate where the OIDC issuer
`https://token.actions.githubusercontent.com` is real, where `slsa-github-generator`
and keyless cosign/Sigstore work natively, and where the OpenSSF Scorecard runs).
The pipeline is `.github/workflows/release.yml`; it builds and signs into a **draft**
release — publishing is a deliberate human action.

## What ships with a release

| Artifact | Produced by | Signature / trust |
|---|---|---|
| `checksums.txt` (SHA-256 of the archives, packages and per-archive SBOMs it lists) | goreleaser | cosign signature `checksums.txt.sig` (+ `.pem` cert, keyless) |
| static binaries + `tar.gz` archives | goreleaser | covered transitively by the signed checksums |
| `stable-manifest.json` + `.sig` | hub workflow + off-box ceremony | Ed25519 with the dedicated OTA key; the shipped community binary verifies it before attachment |
| `*.spdx.sbom.json` + `*.cdx.sbom.json` (SPDX 2.3 + CycloneDX 1.6) per archive | goreleaser (syft) | attached as a signed in-toto attestation (below) |
| `*.sbom.sigstore.json` | `cosign attest-blob` | **SBOM in-toto attestation** per archive (SCP-03) |
| `image.spdx.sbom.json` + image attestation | syft + `cosign attest` | **SBOM of the container image** (SCP-03) |
| `*.vex.openvex.json` + `*.vex.sigstore.json` | `govulncheck -format openvex` + `cosign attest[-blob]` | **OpenVEX** driven by reachability (SCP-04) |
| `*.intoto.jsonl` | `slsa-github-generator` (generic + container) | **SLSA Build L3 (SLSA v1.2) provenance** (SCP-01) |
| container image `docker.io/olivaresai/olivares` (Docker Hub — official; `ghcr.io/olivaresai/olivares` is the fallback, identical content by digest) | goreleaser (builds + signs on ghcr.io) + the `mirror-dockerhub` job (`cosign copy` by digest) | cosign signature (keyless) + SBOM + VEX + SLSA attestations, by digest |
| Helm chart (OCI) `ghcr.io/olivaresai/charts/olivares` — **not an asset of v26.9.0**: the chart source ships in `deploy/helm/olivares`, the OCI publication runs only on a `chart-v*` tag (REL-87) | helm | cosign over the OCI manifest + (optional) GPG `.prov` (SCP-05) |
| Release notes support-period header | goreleaser | declares the release line support period; see [`CRA-READINESS.md`](CRA-READINESS.md#support-period-declarations) |

All container/image/chart references are pinned **by digest**, never by tag.

## Verify a binary release (one command)

From the directory holding the downloaded release files:

```sh
scripts/verify-release.sh                         # keyless / Sigstore (default)
scripts/verify-release.sh --source-tag v1.2.3     # also pin the SLSA provenance source tag
```

The release workflow signs keyless: a release carries `checksums.txt.sig` and
`checksums.txt.pem`, and no cosign public key. By default the script requires the fully anchored
identity of `release.yml` on a `vX.Y.Z` tag and the GitHub OIDC issuer. That identity accepts
any such tag; `--source-tag` pins the source tag that the SLSA provenance must name.

It verifies, in order, whatever is present (skipping with a clear note when an
attestation or its verifier tool is absent):

1. the cosign signature over `checksums.txt`;
2. the SHA-256 of every file listed in `checksums.txt`;
3. the **SBOM** in-toto attestation per archive (`cosign verify-blob-attestation --type spdxjson`);
4. the **OpenVEX** attestation per archive (`cosign verify-blob-attestation --type openvex`);
5. the **SLSA** provenance per archive (`slsa-verifier verify-artifact`).

### Key-based verification with an operator-owned key

`--key FILE` checks `checksums.txt.sig` and the SBOM and OpenVEX bundles against that public key
instead of a certificate identity, and implies `--insecure-ignore-tlog`. It applies only to files
signed with the matching private key. The operator owns that key pair; this script does not
create the signatures. A key signature proves possession of the key, not that this repository's
release workflow signed the tag. Obtain the public key from its owner through a channel separate
from the files it verifies.

```sh
scripts/verify-release.sh --key /path/to/operator-cosign.pub
```

`--strict-publication` is the publisher mode that `scripts/release-finalize-stable.sh` runs. It
refuses `--key` and `--offline`, and requires `--cert-identity` and `--source-uri` from its caller.

### Network and trusted-root prerequisites

- Keyless verification needs Sigstore trusted-root material (TUF), which cosign fetches unless it
  is already cached.
- `--offline` removes the Rekor lookup. In keyless mode, step 1 passes cosign `--offline` and
  steps 3 and 4 pass `--insecure-ignore-tlog`. It does not remove the trusted-root requirement,
  and the script has no `--trusted-root` option. Keyless verification with `--offline` on a host
  without network has not been measured.
- The SBOM and OpenVEX bundles use cosign's new bundle format, which needs a trusted root even
  with `--key`. `scripts/check-cosign-contract.sh` passes a minimal local root to verify a
  key-signed bundle; `verify-release.sh` cannot pass one.
- The same contract check measures a key-based `verify-blob` with the Rekor URL and the TUF
  mirror pointed at a refused loopback port, for the cosign version it approves.
- Step 5 runs `slsa-verifier` without an offline option in every mode.

## Verify the container image

```sh
IMAGE=docker.io/olivaresai/olivares       # official registry; the fallback is ghcr.io/olivaresai/olivares
DIGEST=$(crane digest "$IMAGE:<version>")
REF="$IMAGE@$DIGEST"

# signature (keyless):
cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# SBOM attestation, VEX attestation:
cosign verify-attestation "$REF" --type spdxjson  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex   --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' --certificate-oidc-issuer https://token.actions.githubusercontent.com

# SLSA provenance:
slsa-verifier verify-image "$REF" --source-uri github.com/olivaresai/olivares --source-tag <version>
```

The image is built and signed on ghcr.io and then copied to Docker Hub **by digest** with
`cosign copy`, which carries the signatures and every attestation across. So the commands above
verify identically against `ghcr.io/olivaresai/olivares` — the digest, signing identity and OIDC
issuer are unchanged regardless of which registry you pull from. The ghcr.io coordinate is the
fallback to reach for when Docker Hub is unreachable or its **anonymous-pull rate limit** bites
(ghcr.io does not rate-limit anonymous pulls of public images). For production, pin by digest
(`docker.io/olivaresai/olivares@sha256:…`); mutable tags are for evaluation only.

## Air-gap / offline

`scripts/airgap-bundle.sh` builds a bundle for a disconnected site, and
`scripts/airgap-mirror.sh` imports it there. Whoever runs the bundle script supplies the cosign
private key with `--cosign-key` and owns it. The project does not supply that key.

```sh
# build side (online once), with a cosign key pair you control:
scripts/airgap-bundle.sh --version v1.2.3 \
  --image docker.io/olivaresai/olivares:1.2.3-amd64 \
  --chart deploy/helm/olivares --cosign-key cosign.key

# air-gap side (no internet):
scripts/airgap-mirror.sh --bundle olivares-airgap-v1.2.3.tar.gz --registry registry.internal:5000
```

The output is `olivares-airgap-<version>.tar.gz`:

| Path | Content |
|---|---|
| `cosign.pub` | public key derived from the `--cosign-key` key |
| `chart/<chart>.tgz` + `.tgz.sig` | packaged chart, signed with that key without a transparency-log upload; a GPG `.prov` only with `--gpg-key` |
| `images/<name>/` | each image, pinned by digest and saved with `cosign save`, which keeps the signatures and attestations the image already has; the script does not sign images |
| `digests.txt` | the pinned digest of every image |
| `sbom/`, `vex/`, `prov/` | files copied only from `OLIVARES_SBOM_FILES`, `OLIVARES_VEX_FILES` and `OLIVARES_PROV_FILES` |
| `VERIFY.md`, `airgap-mirror.sh`, `verify-release.sh` | offline steps, and copies of the two scripts when the bundle script runs from the repository root |

The bundle does not contain `checksums.txt`, and the tarball has no signature or checksum of its
own. `airgap-mirror.sh` checks each image with
`cosign verify --local-image --insecure-ignore-tlog --key` and the chart with
`cosign verify-blob --key --insecure-ignore-tlog`, both against the bundled `cosign.pub`. An
image passes only if it carries a signature made with the matching private key; the release
workflow signs images keyless.

**Authenticate the key separately.** A `cosign.pub` taken from the bundle it verifies proves only
that the bundle is consistent with itself: whoever can replace the bundle can replace the key.
Before the disconnected site trusts a bundle, compare its `cosign.pub` byte for byte with a copy
that the key owner delivered through a separate trusted channel.

These key-based checks do not use Rekor or Fulcio. The chart check has the `verify-blob` form that
`scripts/check-cosign-contract.sh` measures; `cosign verify --local-image` and a complete bundle
import have not been measured without network.

The engine makes **no mandatory outbound calls at boot** (it binds loopback by
default, `docs/SECURITY-HARDENING.md`), so nothing reaches us on its own. The one
command that does is `olivares upgrade`, and in an air gap it installs from the
carried bundle (`--bundle`) instead of the update channel.

## OTA channel manifest: two-phase, no private key in CI

The tag workflow generates `stable-manifest.json` from the final GoReleaser archives, with a freshness
bound (`--expires-in`, default 90 days — see *Manifest freshness* below), and attaches the **unsigned**
exact bytes to the draft.

Because that draft asset is unsigned, anyone able to write to the release could swap it before the
ceremony. The custodian therefore **cross-checks it against `checksums.txt` before signing** —
`checksums.txt` is the file CI signed with keyless cosign, so it is the link an attacker cannot forge:

```sh
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt

olivares release verify-manifest --manifest stable-manifest.json \
  --checksums checksums.txt --dir . --expect-channel stable --expect-version 26.9.0

# --checksums is REQUIRED: the signing step itself re-runs the cross-check.
olivares release sign-manifest --manifest stable-manifest.json \
  --checksums checksums.txt --sign-key @prod-ota.key
```

The identity regexp is **fully anchored** on purpose: cosign matches
`--certificate-identity-regexp` unanchored, so a bare `^https://github.com/olivaresai/olivares`
would also accept a signature from `…/olivares-anything/…` or from any workflow on any branch.

`verify-manifest` fails unless every digest in the manifest equals the `checksums.txt` entry for the
same file, every published archive re-hashes to it, and each filename is the archive that platform
actually carries (a manifest may not point `linux/amd64` at the FIPS variant, whose digest is also in
`checksums.txt`). It additionally bounds-checks the manifest's POLICY — `expires`, `min_version`,
`rollout`, `security`/`advisories` — because `checksums.txt` binds digests and nothing else, and
prints every one of those fields for the custodian to read before signing. A failure means the
manifest does not describe the release that was actually built and signed: do not sign it.

Only the resulting base64 signature (public data) is passed to the protected
`publish-ota-manifest` workflow dispatch. That job runs the chain in an order chosen so that no step
depends on the artifact it is testing:

1. it verifies the cosign signature over `checksums.txt`;
2. it binds the linux/amd64 archive to that `checksums.txt` with plain `sha256sum`
   (`scripts/verify-archive-digest.sh`) **before extracting anything** — without this the rest would
   be circular, since the fingerprints the job checks are public repo variables and a substituted
   archive could simply print them;
3. it runs `release verify-manifest` from the CHECKOUT (`go run`, public OTA anchor passed
   explicitly), so the digest and policy verdict comes from source, not from the downloaded binary;
4. it validates both embedded public anchors and only then runs the *shipped community binary*'s
   `olivares release verify-manifest --sig … --checksums … --dir …` **without `--pubkey`** — which
   adds the one fact the checkout cannot establish: that the EMBEDDED client-side anchor, the one in
   every user's binary, accepts these exact published bytes.

(`upgrade --bundle … --check` also runs, to exercise the updater path; on a same-version manifest it
returns before any digest is compared, so it is not the digest proof.) It attaches the signature only
after all of that passes. The draft is still published by a human.

### Manifest freshness (anti-freeze)

Production manifests carry `expires`. Past it, `olivares upgrade` refuses the manifest even though the
signature is valid — otherwise a hostile or stale mirror could serve one old, validly-signed manifest
forever and pin installations to a superseded version. The default window is **90 days** (repo variable
`OLIVARES_MANIFEST_EXPIRES_IN`). Cutting a release renews it; during a long quiet period the publisher
re-signs before it lapses. If you see the expiry error, you are talking to a
stale endpoint or an out-of-date bundle — fetch a fresh one.

Version contract with the commerce deployment: the git tag is `v26.9.0`; GoReleaser archive names,
`manifest.version`, `ENTERPRISE_VERSION`, and channel-object paths use `26.9.0` without `v`.
The commerce deployment mirrors the verified pair and listed archives under
`https://olivares.ai/updates/stable/{manifest.json,manifest.json.sig,<archive>}`; it never signs.

## Reproducible build & the two embedded public anchors

Release binaries are reproducible: same source + same inputs → byte-identical bytes
(CGO off, `-trimpath`, build id stripped, date pinned to the commit timestamp). The independent
license and OTA **verification public keys** plus `-tags release` are part of that input vector.
A from-source rebuild must supply both to match the published `checksums.txt`:

```sh
# Rebuild a release binary from source and compare its SHA-256 to checksums.txt:
git checkout v1.2.3
OLIVARES_LICENSE_PUBKEY="<published license public key, below>" \
OLIVARES_OTA_PUBKEY="<published OTA public key, below>" task build:repro
sha256sum bin/olivares          # compare to checksums.txt
```

Both public keys are public reproducible inputs, not secrets. Their private custody is intentionally
different: license signing is online in the narrow Worker; OTA signing is always off-box/HSM.
Every release publishes both full values and SHA-256 fingerprints here and in its notes. A row
states the FIRST release the pair covers and stays current until the next rotation, so the bound
below is a coverage floor, not a claim about which version ships:

| Releases | Domain | Public key (base64-std) | SHA-256 fingerprint | `version` prefix |
|---|---|---|---|---|
| ≥ v26.8.0 | license | `45NQjsMHDkEzf12QI9BmOnjX03j3bC/iOwOkvyuHwXk=` | `5144ae08df0ebfd419a8c57a81dc003755fad043c96fa5542dc83779c32b192b` | `5144ae08` |
| ≥ v26.8.0 | OTA | `2KnIDwyx6cwji/A0zCf61ITEin0I3U66Rdlb7dnrpqA=` | `1eee9d7615cfbb31a8a945c0b7a4a3e4de5e5f0e4a0e09e5b31d13c7ffdfa53a` | `1eee9d76` |

> Rotated in an offline ceremony on 2026-08-23 on the same trusted host (the 2026-07-27 pairs were retired
> the moment a command trace exposed their private halves; no tag or install had ever used them).
> Generated with `olivares license keygen`, never in a code session/repo/CI.
> Fingerprints were verified independently on both sides of the transfer with `base64 -d | sha256sum`
> before the repository Variables (`OLIVARES_LICENSE_PUBKEY` / `OLIVARES_OTA_PUBKEY`) were set.
>
> ⛔ **The clause that used to follow — "and re-verified from the Variables API round-trip" — was
> not true, and it is corrected here rather than quietly deleted.** Measured 2026-08-28T23:2xZ: the
> two Variables still held the RETIRED 2026-07-27 pair (`061f8a36…` / `a1240d22…`), with
> `updated_at` **2026-07-27**, four weeks before the rotation. GitHub stamps `updated_at` on every
> write, so the round-trip that sentence attests to never happened: the rotation reached the tree
> and this table, and never the deployment. The Variables were actually set on
> **2026-08-28T23:46Z**, and the values above were then verified against them independently, in
> both fingerprint forms, by a different lane than the one that wrote them.
>
> This is why `scripts/check-release-anchor-identity.sh` exists and why `release-preflight` §C.4.8
> now COMPARES the anchor instead of printing its fingerprint: for five days a sentence in this
> file was the only thing asserting a fact that no machine was checking, and it was wrong. Development builds report the public license dev key
> and no OTA key. The sandbox licence key — the one the deployed sandbox worker actually signs with — is a
> third, independent pair (key id `0e73e1a0…`, `~/.config/license_signing_key_sandbox` on the
> operator box) and never appears in release artifacts. The O03 record kept with the internal key
> inventory (fingerprint `11a7693c…`) is NOT that signer — its path is not repeated here, because
> this tree does not publish that inventory and a public page must not cite what it cannot show;
> citing it as the
> sandbox anchor was the error this file carried until 2026-08-31.

Confirm a downloaded binary embeds both expected keys without trusting the build narrative:

```sh
olivares version    # → ... license-key=release/<fp>, ota-key=release/<different-fp>
```

The two fields are build-provenance aids, not attestations; trust still comes from cosign/SLSA and
comparison with the published full keys.

## Migration window

OTA starts clean at the first public release: no public manifest was ever signed by a
former shared key, and the license and OTA key domains are separate from day one. The
legacy `OLIVARES_RELEASE_PUBKEY` build variable is rejected rather than mapped to both
domains.

## Honesty notes

- **CISA SBOM "minimum elements" is a DRAFT.** Our SBOM linter (`scripts/sbom-check-cisa.sh`)
  checks the 2025 draft's fields (Component Hash, License, Tool Name, Generation
  Context, …) but treats them as **draft-pending**: it fails when a field type is
  wholly absent and warns on the per-component omissions the draft itself permits.
  Nothing here is presented as a finalized federal requirement.
- **SLSA Build L2 vs Build L3.** The reusable `slsa-github-generator` workflows are SLSA Build L3 by
  construction (signing runs in an isolated context the build steps can't reach).
  The reusable workflow is pinned by semver tag, not SHA — `slsa-verifier`
  validates the builder ID against the tag, so a SHA pin would break verification.
- **Keyless vs key-based.** Public releases are keyless (Fulcio/Rekor, publicly
  transparent) and publish no cosign public key. The key-based path verifies files signed with
  an operator-owned key, such as an air-gap bundle's chart. It proves possession of that key,
  not that this repository's release workflow signed a tag, and the operator must authenticate
  the public key separately from the files it verifies.
