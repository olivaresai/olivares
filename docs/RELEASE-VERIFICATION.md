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

## Container first-hour qualification

Before creating a stable tag, dispatch `release-first-hour.yml` on the exact
candidate commit. It builds the canonical GoReleaser binary and `Dockerfile.release`
runtime, using the version in `RELEASE-VERSION` and the release license/OTA public
keys. No tag is created and nothing is published.

The job caches the newest published version older than the candidate as `:latest`,
then runs the [Compose Quickstart](../deploy/compose/README.md#quickstart-sqlite-one-command)
with an empty store and a fresh Chromium context. It uses the README's literal
setup-token log filter and leaves Compose image selection unchanged. Before the
browser opens, the running image and executable must match the candidate build;
the binary version, source commit and public-key fingerprints must also match.

It runs on `vars.CI_RUNNER` (the shared self-hosted Docker daemon) or, when that
is unset, on a GitHub-hosted runner; no step differs between them. Each run owns
its Compose project, so its network and volume are its own. A CI-only layer,
`deploy/compose/docker-compose.first-hour.ci.yml`, drops the fixed container name
and publishes only the console, on loopback and a free host port that the script
reads back from Compose; the gRPC port stays unpublished. Before anything is
pulled, the run reads the effective Compose configuration and refuses a Compose
that did not apply the layer, and after `up` it refuses any binding beyond
loopback. The `:latest` cache and the candidate's release tag are daemon-wide, so
a run holds a non-blocking lock on one host path (`/tmp/olivares-first-hour.lock`,
shared by runners that are processes of one host) and a second run refuses to
start rather than race for them; rerun it when the first ends. At the end the run
removes its containers, volumes and network, untags exactly those two tags, and
puts back any image a tag pointed at before the run. The older release image it
pulled as the `:latest` cache stays on the daemon.

The journey walks administrator setup, provider registration, real tool installation,
a session prompt and response, Settings, Deploy, Workspaces and Sessions. The only
stand-in is a labelled local model provider; vendor-account qualification is not
claimed. O1 covers setup through the first answer. O7 rejects unresolved translation
keys, HTTP codes shown to users, loading over five seconds, console errors,
unexplained disabled primary actions, version drift and filesystem-path fields.
Both must PASS.

Artifacts retain the image and executable identities, binary/UI versions, videos,
masked screenshots, `failure-list.json` and the redacted engine log. O1/O7 verdicts
are printed in the job log. Browser output lives in a child directory so Playwright
cannot clear identity evidence. Traces, HTML action reports and automatic DOM failure
snapshots are disabled because they can contain setup credentials. The workflow runs
`task test:release-first-hour` before the journey to verify evidence rejection,
identity checks, log redaction, artifact retention and browser guards.

From the release checkout, verify qualification before tagging (authenticated `gh`):

```sh
GITHUB_REPOSITORY=olivaresai/olivares GITHUB_SHA=$(git rev-parse HEAD) \
  bash scripts/check-first-hour-evidence.sh
```

`release.yml` repeats this check before building the draft. A missing, skipped,
failed or different-commit run blocks it.

## What ships with a release

| Artifact | Produced by | Signature / trust |
|---|---|---|
| `checksums.txt` (SHA-256 of the archives, packages, installer, `release-commit.txt` and `release-build-context.json`) | goreleaser | cosign signature `checksums.txt.sig` (+ `.pem` cert, keyless) |
| static binaries + `tar.gz` archives | goreleaser | covered transitively by the signed checksums |
| `stable-manifest.json` + `.sig` | hub workflow + off-box ceremony | Ed25519 with the dedicated OTA key; the shipped community binary verifies it before attachment |
| `olivares.spdx.sbom.json` (SPDX), one per release | syft over the shipped container image (its binary carries the same Go module set as every archive) + `cosign attest` | **SBOM** in-toto attestation on the image, by digest (SCP-03) |
| `olivares.vex.openvex.json`, one per release | `govulncheck -format openvex` + `cosign attest` | **OpenVEX** driven by reachability, attested to the image by digest (SCP-04) |
| `*.intoto.jsonl` | `slsa-github-generator` (generic + container) | **SLSA Build L3 (SLSA v1.2) provenance** (SCP-01) |
| container image `docker.io/olivaresai/olivares` (Docker Hub — official; `ghcr.io/olivaresai/olivares` is the fallback, identical content by digest) | goreleaser (builds + signs on ghcr.io) + the `mirror-dockerhub` job (`cosign copy` by digest) | cosign signature (keyless) + SBOM + VEX + SLSA attestations, by digest |
| Historical Helm chart (OCI) `ghcr.io/olivaresai/charts/olivares` — **not an asset of Olivares <!-- release -->0.1<!-- /release -->**: from 0.1 the chart source and signed packages are supplied through Business, outside the Community channel | helm | cosign over the OCI manifest + (optional) GPG `.prov` (SCP-05) |
| Release notes support-period header | goreleaser | declares the release line support period; see [`CRA-READINESS.md`](CRA-READINESS.md#support-period-declarations) |

All container/image/chart references are pinned **by digest**, never by tag.

Releases up to 26.10.1<!-- release-fixed --> carried per-archive SBOM and OpenVEX files instead: `<archive>.spdx.sbom.json`,
`<archive>.cdx.sbom.json`, `<archive>.sbom.sigstore.json`, `<archive>.vex.openvex.json`,
`<archive>.vex.sigstore.json`, plus `image.spdx.sbom.json`. Later releases carry none of them; the
signed `checksums.txt` covers every archive.

## Verify a binary release (one command)

Run `scripts/verify-release.sh` from a source checkout of the release tag, in the directory
holding the downloaded release files. It is not a release asset: running it trusts the checkout,
so read it first. Without a checkout, the manual binary block in
[`INSTALL.md`](../INSTALL.md#manual-binary-tarball) checks the two required links (steps 1 and 2
below) with `cosign verify-blob` and `sha256sum` alone.

<!-- release -->
```sh
/path/to/olivares/scripts/verify-release.sh                    # keyless / Sigstore (default)
/path/to/olivares/scripts/verify-release.sh --source-tag 0.1   # also pin the SLSA provenance source tag
```
<!-- /release -->

The release workflow signs keyless: a release carries `checksums.txt.sig` and
`checksums.txt.pem`, and no cosign public key. By default the script requires the fully anchored
identity of `release.yml` on a `MAJOR.MINOR` tag and the GitHub OIDC issuer. That identity accepts
any such tag; `--source-tag` pins the source tag that the SLSA provenance must name.

It verifies, in order, whatever is present (skipping with a clear note when an
attestation or its verifier tool is absent):

1. the cosign signature over `checksums.txt`;
2. the SHA-256 of every file listed in `checksums.txt`;
3. the **SBOM** in-toto attestation per archive (`cosign verify-blob-attestation --type spdxjson`),
   releases up to 26.10.1<!-- release-fixed --> only;
4. the **OpenVEX** attestation per archive (`cosign verify-blob-attestation --type openvex`),
   releases up to 26.10.1<!-- release-fixed --> only;
5. the **SLSA** provenance per archive (`slsa-verifier verify-artifact`).

Later releases carry no per-archive bundle, so steps 3 and 4 report "skipped". Their one SBOM and
one OpenVEX are attested to the container image by digest: verify them with
`cosign verify-attestation` as shown in [Verify the container image](#verify-the-container-image).

### Key-based verification with an operator-owned key

`--key FILE` checks `checksums.txt.sig` and, for releases up to 26.10.1<!-- release-fixed -->, the per-archive SBOM and
OpenVEX bundles against that public key
instead of a certificate identity, and implies `--insecure-ignore-tlog`. It applies only to files
signed with the matching private key. The operator owns that key pair; this script does not
create the signatures. A key signature proves possession of the key, not that this repository's
release workflow signed the tag. Obtain the public key from its owner through a channel separate
from the files it verifies.

```sh
/path/to/olivares/scripts/verify-release.sh --key /path/to/operator-cosign.pub
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
- The per-archive SBOM and OpenVEX bundles of releases up to 26.10.1<!-- release-fixed --> use cosign's new bundle
  format, which needs a trusted root even with `--key`. `scripts/check-cosign-contract.sh` passes a minimal local root to verify a
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
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# release SBOM (olivares.spdx.sbom.json) and OpenVEX (olivares.vex.openvex.json) attestations:
cosign verify-attestation "$REF" --type spdxjson  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type openvex   --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' --certificate-oidc-issuer https://token.actions.githubusercontent.com

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

Offline installation requires Enterprise. The complete bundle-building, mirror and
key-authentication procedure is supplied with the Enterprise distribution.
Community verifies a carried update bundle without reading a license or installing it:

```sh
olivares upgrade --bundle ./release-bundle --check
```

Installation without `--check` refuses and names Enterprise before opening the bundle,
license or target. Connected Community upgrades continue to use the signed update channel.

## OTA channel manifest: two-phase, no private key in CI

The tag workflow generates `stable-manifest.json` from the final GoReleaser archives, with a freshness
bound (`--expires-in`, default 90 days — see *Manifest freshness* below), and attaches the **unsigned**
exact bytes to the draft.

Because that draft asset is unsigned, anyone able to write to the release could swap it before the
ceremony. The custodian therefore **cross-checks it against `checksums.txt` before signing** —
`checksums.txt` is the file CI signed with keyless cosign, so it is the link an attacker cannot forge:

<!-- release -->
```sh
cosign verify-blob --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt

olivares release verify-manifest --manifest stable-manifest.json \
  --checksums checksums.txt --dir . --expect-channel stable --expect-version 0.1

# --checksums is REQUIRED: the signing step itself re-runs the cross-check.
olivares release sign-manifest --manifest stable-manifest.json \
  --checksums checksums.txt --sign-key @prod-ota.key
```
<!-- /release -->

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

Version contract with the commerce deployment: the release tag is <!-- release -->`0.1`<!-- /release -->; GoReleaser archive names,
`manifest.version`, `ENTERPRISE_VERSION`, and channel-object paths use release <!-- release -->`0.1`<!-- /release --> without `v`.
The commerce deployment mirrors the verified pair and listed archives under
`https://olivares.ai/updates/stable/{manifest.json,manifest.json.sig,<archive>}`; it never signs.

## Reproducible build & the two embedded public anchors

Release binaries are reproducible: same source + same inputs → byte-identical bytes
(CGO off, `-trimpath`, build id stripped, date pinned to the commit timestamp). The independent
license and OTA **verification public keys** plus `-tags release` are part of that input vector.
A from-source rebuild must supply both to match the published `checksums.txt`:

<!-- release -->
```sh
# Rebuild a release binary from source and compare its SHA-256 to checksums.txt:
git checkout 0.1
OLIVARES_LICENSE_PUBKEY="<published license public key, below>" \
OLIVARES_OTA_PUBKEY="<published OTA public key, below>" task build:repro
sha256sum bin/olivares          # compare to checksums.txt
```
<!-- /release -->

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
> both fingerprint forms, by someone other than whoever wrote them.
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
