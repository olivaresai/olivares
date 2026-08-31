---
title: Install in an air-gapped environment
description: >-
  Carry a signed release bundle across the gap, verify every image and the Helm
  chart fully offline, mirror them into a private registry by digest, and install
  — with no outbound calls on the disconnected side.
---

Olivares AI is **self-host-first and air-gap-ready**. This guide takes a signed
release across an air gap with **no network on the disconnected side**: you verify
every image and the Helm chart offline against a public key, mirror them into your
private registry **by digest**, and install. The product makes **no mandatory
outbound calls at boot**, so nothing inside the gap reaches the internet. The one command
that would reach a vendor endpoint, `olivares upgrade`, takes `--endpoint` (or `--bundle`)
and reads from your mirror instead.

The flow is two-sided:

1. **Online, once** — a maintainer builds a self-contained bundle.
2. **Inside the gap** — you verify it offline and mirror it into your registry.

This page documents how to **use** the bundle and the shipped scripts; it does not
rebuild the release pipeline.

## 1. Build the bundle (online, once)

On a connected machine, `scripts/airgap-bundle.sh` pulls every image **pinned by
digest**, packages and signs the Helm chart, gathers the SBOM/OpenVEX/provenance,
and emits a single tarball with a `VERIFY.md`:

```bash
scripts/airgap-bundle.sh \
  --version v26.8.0 \
  --image docker.io/olivaresai/olivares:26.8.0-amd64 \
  --chart deploy/helm/olivares \
  --cosign-key cosign.key \
  [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
```

The image pulls from Docker Hub by its official coordinate
(`docker.io/olivaresai/olivares`); the `ghcr.io/olivaresai/olivares` fallback carries
identical content by digest, if you prefer to mirror from there — useful when the
build host is unauthenticated and Docker Hub's anonymous-pull rate limit gets in the way.

:::caution[The SBOM/VEX/provenance are supplied, not generated]
The bundler copies the SBOM, OpenVEX and provenance into the bundle **best-effort
from environment variables** (`OLIVARES_SBOM_FILES`, `OLIVARES_VEX_FILES`,
`OLIVARES_PROV_FILES`). If those are not set, the `sbom/`, `vex/` and `prov/`
directories in the bundle are empty — set them so your disconnected site receives
the attestations.
:::

### What the bundle contains

```text
images/<name>/   cosign-saved image + its signatures/attestations (offline)
chart/<chart>.tgz   packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
sbom/  vex/  prov/   SBOM, OpenVEX and SLSA provenance for the release
cosign.pub          the public key to verify everything offline (key mode)
digests.txt         the pinned digest of every image (the manifest of record)
VERIFY.md           the exact offline verification + mirror walkthrough
```

The bundle also carries copies of `airgap-mirror.sh` and `verify-release.sh`, so the
disconnected side needs nothing from the network.

## 2. Verify and mirror inside the gap

On the disconnected side you need only `cosign`, `crane`, `helm` and `tar` — and a
reachable **private registry**. No internet.

### Verify every image offline (no transparency log)

```bash
for d in images/*/; do
  cosign verify --local-image "$d" --insecure-ignore-tlog --key cosign.pub
done
```

`--insecure-ignore-tlog` skips Sigstore's online transparency log; trust comes from
the bundled `cosign.pub`. (This is *not* the same as the keyless `--offline` flag —
in key mode the offline trust root is the public key.)

### Verify the Helm chart offline

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --signature chart/*.tgz.sig chart/*.tgz
# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz
# (needs the signer's GPG public key in your keyring)
```

### Mirror into your private registry by digest

`scripts/airgap-mirror.sh` verifies each image offline, loads it into your registry,
and **re-pins by digest** to confirm the digest survived the mirror (it uses `crane`
and `cosign load` — **not** `oras`):

```bash
scripts/airgap-mirror.sh \
  --bundle olivares-airgap-v26.8.0.tar.gz \
  --registry registry.internal:5000 [--insecure]
```

### Install by digest, never by tag

```bash
helm install olivares \
  oci://registry.internal:5000/charts/olivares \
  --version <chart-version> \
  --set image.repository=registry.internal:5000/olivares \
  --set image.digest=<digest-from-digests.txt>
```

Always install from the **digest** in `digests.txt`, never a mutable tag — a digest
is immutable and is what you verified.

## Nothing calls out inside the gap

> The engine makes **no mandatory outbound calls at boot** (it binds loopback by
> default), so nothing inside the gap reaches the internet. `olivares upgrade` is the one
> command that would reach a vendor endpoint; `--endpoint` or `--bundle` points it at your
> own mirror.

The license is validated **offline** (an Ed25519 signature, no license server), and
none of the verification or install steps above touch the internet once the bundle
is across the gap. There is no telemetry-home default to disable.

The vendor is reached on the **online** side, and that is by design: building the
bundle downloads the release, and for a commercial estate the subscription is the
credential with which the add-ons, their updates and their patches are fetched. That
is the SUSE/Novell model — an air-gapped estate is served from a local mirror that
still carries the entitlement. See [self-hosting](/how-to/self-hosting/).

:::note[Container vs. binary listen defaults]
Run directly, the binary binds **loopback** by default. The release **container
image's** default command binds `0.0.0.0` inside the container so you can front it
with your ingress/service — that is an in-container bind, not an outbound call. Set
your listen addresses explicitly for your deployment.
:::

## FIPS / STIG variants

Hardened build variants exist (a FIPS-mode build linking the CMVP-validated Go
cryptographic module, and a STIG-oriented image). These are **post-v1** and carry
their own honesty ledger — notably, **no FedRAMP/DoD ATO is claimed**, and only the
specifically validated module version should be represented as validated. Treat them
as available-but-not-yet-v1 rather than a certified offering.

## See also

- [Verify what you downloaded](/how-to/verify-a-release/) — the non-air-gapped
  verification chain (signature, SBOM, OpenVEX, SLSA).
- [Self-host the control plane](/how-to/self-hosting/) — the single-binary, Compose
  and Kubernetes paths and their secure defaults.
