<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->
# Olivares AI — Helm chart

The Kubernetes distribution channel for the engine (SCP-05). The chart source ships
in this repository; its publication to the public OCI registry is **unverified** as of the
v26.9.0 engine release. It is not published to the public OCI registry from this repository:
no independent `chart-v*` tag has exercised the chart workflow, and REL-87, the publication
act, has not completed. Whether the registry holds the chart is not established, because it
refuses an anonymous read. Install from the source tree today. Do not present the OCI
coordinate as a working download.

> A future OCI publish is automated by `../../.github/workflows/release-chart.yml`
> (lint → package → push → cosign-sign), gated on a `chart-v*` tag. The chart is
> versioned independently of the engine, so it has its own tag namespace. Cutting
> the tag is a deliberate human action (releases are cut manually by a maintainer; never automated).

## What it deploys

| Object | Purpose |
|---|---|
| `StatefulSet` (core) | the control-plane engine. SQLite is single-node on a data PVC; Postgres supports active-passive HA with shared signing-key Secrets and an ephemeral per-pod data dir. |
| `Service` (ClusterIP) | `8443` HTTPS (REST + web UI) and `8444` gRPC (ControlPlane/ingest API). |
| `DaemonSet` (collectors, opt-in) | the distributed ingest plane "C": runs `olivares collector`, pushing observations to the core over gRPC+mTLS. No inbound listener. Off by default. |
| `Job` (post-install hook, opt-in) | provisions the least-privilege `olivares_app` Postgres role from `01-app-role.sql` (engine mode `postgres`). |
| `ServiceAccount` | no Kubernetes API privileges; token not auto-mounted. |
| optional | `NetworkPolicy` (default-deny + DNS), `ServiceMonitor`, `PodDisruptionBudget`. |

Hardening is on by every pod: non-root `65532`, `readOnlyRootFilesystem`, all
capabilities dropped, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation:
false` — matching `connectors/ebpf/deploy` and `docs/SECURITY-HARDENING.md`.

## Install

```sh
# default: single-node, embedded SQLite (zero external deps, air-gap-ready)
helm install olivares deploy/helm/olivares

# multi-tenant: external Postgres (the engine REFUSES a superuser/BYPASSRLS role)
kubectl create secret generic olivares-pg \
  --from-literal=dsn='postgres://olivares_app:***@db:5432/olivares?sslmode=verify-full' \
  --from-literal=admin-dsn='postgres://olivares_admin:***@db:5432/olivares?sslmode=verify-full'
helm install olivares deploy/helm/olivares \
  --set core.engine=postgres --set postgres.dsnSecret=olivares-pg \
  --set postgres.adminDsnKey=admin-dsn \
  --set postgres.roleInit.enabled=true \
  --set postgres.roleInit.superuserDsnSecret=pg-super \
  --set postgres.roleInit.appPasswordSecret=pg-app-pw
```

### Scaling a Postgres release from one replica to HA

Changing `core.replicaCount` from 1 to 2+ also removes the StatefulSet's
`volumeClaimTemplates`: HA uses the shared Postgres store and signing-key Secrets,
so both the core and backup pods use `emptyDir` for their per-pod data directories.
`volumeClaimTemplates` is immutable in an existing StatefulSet, so make this a
planned StatefulSet replacement, not a blind in-place scale command. Move/verify the
store in Postgres and provision `core.auditSigningKeySecret` first.

The old ordinal-0 PVC (`data-<release>-olivares-core-0`) is retained and the HA
StatefulSet no longer mounts it. Keep it as rollback evidence until the Postgres HA
boot, ledger verification and DR drill are green; remove it later under the
operator's normal storage-retention procedure. The chart never deletes that PVC.

**No Helm on the cluster?** A pre-rendered flat manifest of the safe single-node
defaults ships at `../manifests/install.yaml`, so `kubectl` alone installs the control
plane (air-gapped / locked-down case):

```sh
kubectl create namespace olivares-system
kubectl apply -n olivares-system -f ../manifests/install.yaml
```

It is generated from THIS chart (`task manifests:gen`) and kept in sync by CI — see
`../gitops/README.md`.

Retrieve the one-time first-boot setup token from the pod's stdout:

```sh
kubectl logs sts/olivares-core | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

Enable the distributed collector plane (requires core mTLS + an `ingest:write` token):

```sh
helm upgrade olivares deploy/helm/olivares \
  --set tls.grpcClientCaSecret=collector-ca \
  --set collectors.enabled=true \
  --set collectors.ingestTokenSecret=olivares-ingest \
  --set collectors.clientTlsSecret=collector-client-tls \
  --set collectors.coreCaSecret=core-ca
```

Pin by **digest** in production / air-gap (`--set image.digest=sha256:…`, leave
`image.tag` empty). See `../compose` for the single-host Docker path and
`../../docs/RELEASE-VERIFICATION.md` for the full verification contract.

## GitOps consumption

Consume the checked-out chart declaratively via Argo CD (app-of-apps), Flux, or
Kustomize (`helmCharts` inflation). After a `chart-v*` publication, those same tools
can consume the OCI artifact by immutable digest. Ready-to-adapt manifests, the
OpenGitOps 1.0 alignment, and digest-pinning guidance live in
[`../gitops/`](../gitops/README.md).

## Verify a published chart before installing

The chart's OCI publication is unverified (`publication-unverified`): it has never been published
from this repository. Once a `chart-v*` tag successfully runs
`release-chart.yml`, **signing policy = cosign-only**: the OCI chart is signed with cosign
(keyless OIDC, over the OCI manifest, by digest) and carries **no** Helm-native GPG
`.prov` layer — the tag-triggered `release-chart.yml` runs `helm package` without
`--sign`. So `helm install/pull --verify` does not verify that future chart; cosign is
the verification path:

```sh
# Keyless (public release). NOTE the identity is release-CHART.yml @ a chart-v* tag —
# NOT the engine's release.yml @ v*:
cosign verify ghcr.io/olivaresai/charts/olivares@sha256:<digest> \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release-chart\.yml@refs/tags/chart-v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# Key-based (air-gap, no Rekor) — the public key bundled by scripts/airgap-bundle.sh:
cosign verify --key cosign.pub --insecure-ignore-tlog ghcr.io/olivaresai/charts/olivares@sha256:<digest>
```

Resolve `<digest>` with `crane digest ghcr.io/olivaresai/charts/olivares:<chart-version>`
(or `cosign triangulate`). An in-cluster Kyverno/sigstore policy-controller (or Flux
`verify:`) enforces the same cosign signature at admission — that is the runtime gate.

> GPG `.prov` / `helm --verify` is available **only if you re-package the chart
> locally** with `helm package --sign` (see "Publishing"). That is a different,
> self-signed artifact; the automated pipeline does not emit a `.prov`, so do not
> expect `helm --verify` to pass against the `ghcr.io` chart.

## Publishing (maintainer)

The canonical publish is the **tag-triggered `release-chart.yml`** (push a
`chart-v<X.Y.Z>` tag that matches `Chart.yaml` `version`; cosign-only, no `.prov`).
The commands below are the manual/air-gap path; the `--sign` line adds a GPG `.prov`
that the automated pipeline does NOT produce (see "Verify the chart before installing").

```sh
helm lint deploy/helm/olivares
helm package deploy/helm/olivares                 # -> olivares-<v>.tgz
helm package --sign --key <gpg-id> --keyring <secret-keyring> deploy/helm/olivares   # -> + .prov (optional, local only)
echo "$GHCR_TOKEN" | helm registry login ghcr.io -u <user> --password-stdin
helm push olivares-<v>.tgz oci://ghcr.io/olivaresai/charts   # .prov auto-uploads as a layer
cosign sign --key cosign.key ghcr.io/olivaresai/charts/olivares@sha256:<digest>   # sign the manifest by digest
```

Air-gap consumers can package the chart source beside digest-pinned images and the
release verification material via `scripts/airgap-bundle.sh` →
`scripts/airgap-mirror.sh`. A future published chart must be verified before mirroring;
this paragraph does not treat the current source tree as a signed OCI artifact.

## Disaster recovery (backup CronJob)

Enable scheduled, ledger-continuity-safe DR bundles:

```sh
kubectl create secret generic dr-kek --from-literal=passphrase='a strong DR passphrase'
helm upgrade --install olivares deploy/helm/olivares \
  --set backup.enabled=true --set backup.kekSecret=dr-kek \
  --set backup.schedule='0 */6 * * *'          # RPO target = the schedule interval
```

In SQLite/single-node mode the CronJob mounts the core data PVC (signing keys +
store). In Postgres HA it mounts `data` as `emptyDir`; the store comes from the
consistent logical dump and the signing keys come from the same shared Secrets as
the core. Postgres backup additionally requires `postgres.adminDsnKey`: `pg_dump`
uses that read-only NOSUPERUSER BYPASSRLS role, so FORCE RLS cannot filter or reject
the multi-tenant dump. The application DSN remains the runtime role.

**Mirror the destination PVC OFFSITE** and keep `dr-kek` separate from the bundles.
Procedure, RPO/RTO and the DR drill: `../../docs/DR-RUNBOOK.md`.

## Local render gate

Run the same storage/backup matrix as CI (SQLite single-node; Postgres single/HA;
backup off/on). It runs `helm template`, strict `kubeconform`, checks every
`claimName` against an explicit PVC or StatefulSet claim template, and proves the
invalid combinations are rejected:

```sh
sh scripts/check-helm-render.sh

# In the noexec development container:
TMPDIR=/workspace/.olivares-tmptest sh scripts/check-helm-render.sh
```

The script requires Helm and kubeconform on `PATH`; it renders only and never talks
to a Kubernetes cluster. The CI implementation is the `helm-render` job in
`.github/workflows/mainline-ci.yml`, used when GitHub Actions is enabled.
