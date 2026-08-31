<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Olivares AI Control-Plane Operator

A Kubernetes operator that manages the lifecycle of the Olivares AI control plane
declaratively, via a single CRD: **`ControlPlane`** (`ops.olivares.ai/v1alpha1`).

It is a **separate Go module** (`github.com/olivaresai/olivares/operator`) that
imports **neither `/core` nor `/sdk`**. Like `terraform-provider-olivares/`, it
keeps a large dependency tree (`sigs.k8s.io/controller-runtime`, `client-go`) out
of the engine's SBOM. It is built separately and **does not alter the base
`olivares` binary or the Helm chart**. This tree does not yet contain a published
operator image or a complete manager Deployment manifest; it contains the manager
source, CRD, RBAC role and sample custom resource.

## What it reconciles

A `ControlPlane` object materializes the engine's Kubernetes workload from spec.
Its shapes are similar to the Helm chart's, but its Service topology is
operator-specific:

| Spec field              | Materializes                                                                       |
| ----------------------- | --------------------------------------------------------------------------------- |
| `image`                 | The `olivares` container image on a **StatefulSet**.                               |
| `replicas`              | StatefulSet replicas. `>1` is active-passive **HA** (requires postgres + a shared audit key); sqlite is clamped to 1. |
| `engine`                | `sqlite` (default) or `postgres` — wired as `OLIVARES_ENGINE` + `--engine`.        |
| `haRouting`             | `Legacy` (default) or `LeaderRouting` — the active-passive HA layout. `LeaderRouting` is what makes an HA rolling update possible; see *HA layouts* below and `docs/HA-LEADER-ROUTING.md`. Ignored outside HA. |
| `progressDeadlineSeconds` | How long a rollout may make no observable progress before it is reported stalled (`Degraded/RolloutStalled`). Default 600. |
| `postgres.dsnSecret`    | The DSN, wired to the engine as `--dsn=$(OLIVARES_DSN)` from a Secret (the engine has no DSN env fallback). `adminDsnKey` is opt-in **only while `backup` is unset** — validation REQUIRES it on postgres with backups, because `pg_dump` aborts as the application role under FORCE RLS. |
| `auditSigningKeySecret` | A shared `audit-signing.key` Secret mounted (0440) into **every** replica as `OLIVARES_AUDIT_SIGNING_KEY_FILE` — the audit hash-chain does not fork at failover. |
| `resources`             | The core container's compute requests/limits (defaulted to **Burstable**, never QoS BestEffort). |
| `persistence`           | The per-replica data PVC: `size` / `storageClass` (`-` disables dynamic provisioning) / `accessModes`. Immutable after create. |
| `configRef`             | A ConfigMap/Secret loaded via `envFrom` (extra **non-DSN** env).                   |
| `backup`                | An owned **CronJob** running the real `olivares dr backup` + a destination PVC.    |
| `restoreFrom`           | A declared restore request (see *Seams* below).                                   |

The reconciler creates/updates, with owner references:

- a **StatefulSet** mirroring the Helm `core-statefulset.yaml` hardening:
  non-root `uid/gid 65532`, `readOnlyRootFilesystem`, **all capabilities dropped**,
  `seccompProfile: RuntimeDefault`, the `8443` (https) / `8444` (grpc) ports,
  `/livez` + `/readyz` probes over HTTPS, the postgres DSN, the shared signing
  keys and the compute `resources`. HA (`postgres` + `replicas>1`) uses
  `podManagementPolicy: Parallel` (standbys are intentionally not Ready, so
  OrderedReady would wedge the scale-up);
- a **headless governing Service** for stable per-pod DNS, distinct from the Helm
  chart's client-facing ClusterIP Service;
- a **backup CronJob** + **destination PVC** when `spec.backup` is set;
- in the leader-routing HA layout only: a **leader-selecting Service**
  (`<name>-leader`) plus the operand's own **ServiceAccount + Role + RoleBinding**
  (see below).

It then writes **status**: `observedGeneration`, `readyReplicas`, `currentImage`,
`leaderPod`, the rollout-progress bookkeeping, `phase`
(`Pending`/`Progressing`/`Ready`/`Invalid`) and the `Available` / `Progressing` /
`Degraded` conditions (`metav1.Condition`). Convergence is derived from **observed**
StatefulSet status (`observedGeneration`, revisions, replica counters) and the live
pods — never from the desired pod template, which would claim an upgrade finished
before any pod ran it.

### HA layouts: `Legacy` vs `LeaderRouting`

Active-passive HA needs two different answers from every pod, and one probe cannot
give both: `/readyz` means "route client traffic here" (leader-only) while
`/pod-readyz` means "this pod is healthy" (leader-agnostic).

- **`Legacy`** (default) probes `/readyz`. Routing is correct, but `ReadyReplicas`
  can never reach `replicas` (so `phase` never reaches `Ready` — reported honestly
  as `Degraded/HALegacyReadinessBlocked`) and a **rolling update wedges** at the
  first replaced standby, because a never-Ready pod never satisfies the update
  barrier.
- **`LeaderRouting`** probes `/pod-readyz`, so every healthy replica is Ready and
  the rollout progresses. The engine publishes `ops.olivares.ai/role=leader` on its
  own pod after it wins the election, `Service/<name>-leader` selects that label,
  and the engine re-checks leadership on every application request — so a stale
  label costs a retryable `503 not_leader`, never a second writer.

`LeaderRouting` is an explicit opt-in because the operator cannot verify its two
preconditions: `spec.image` must serve `/pod-readyz` (olivares ≥ 26.7.0) and clients
must move to `<name>-leader`. It also grants the operand a **namespaced** credential
(`pods` · `get,patch` · `resourceNames` pinned to this StatefulSet's pods) — the only
layout in which the engine talks to the Kubernetes API at all.

On an ALREADY-RUNNING deployment the switch is staged, not immediate: the operator
first creates the leader Service and the publisher RBAC, leaves the pod template
untouched, and reports `Degraded/HALeaderServiceMigrationRequired` until the
administrator confirms the client cut-over with
`ops.olivares.ai/ha-leader-cutover=acknowledged`. Flipping readiness before that
would expose health-Ready standbys through the legacy client Service. A fresh
install has no clients to move and is created in the split shape directly.

Note that installing this operator version widens the **manager's** ClusterRole
(serviceaccounts, roles/rolebindings, `pods get,list,watch,patch`) and starts
Pod/ConfigMap/Secret informers whether or not any ControlPlane opts in — see
`docs/HA-LEADER-ROUTING.md` §2, which also explains what those caches deliberately
do *not* hold. Migration, rollback, the failure modes and the residual failover
window are documented there too; the real-cluster qualification is
`.github/workflows/e2e-operator-kind.yml` (kind + Postgres + two engine images),
which owns the assertions a fake client cannot make.

### Admission validation (CEL) — reject impossible specs before they crashloop

The CRD carries `x-kubernetes-validations` (CEL) rules — the apiserver-native,
certless equivalent of the chart's render-time `fail` guards — that **reject at
admission**:

- `engine=postgres` without `postgres.dsnSecret` (the engine would have no DSN);
- `engine=postgres` with `replicas>1` without `auditSigningKeySecret` (the audit
  ledger would fork at failover).

`ControlPlaneSpec.Validate` re-checks the same invariants inside the controller as
defense-in-depth (a cluster with CEL disabled gets a clear `phase: Invalid`
instead of a crashloop). The `sqlite`+`replicas>1` case is deliberately **not**
rejected — it is a safe clamp to one effective replica, surfaced as `Degraded`.

## Operator Capability Level 3 — "Full Lifecycle"

The target is **Capability Level 3** of the Operator Framework maturity model
(L3 includes L1 *Basic Install* and L2 *Seamless Upgrades*). The Operator
Framework's definition of L3 is **"Full Lifecycle"**: app **install**,
**upgrades**, **reconfigure**, **and backup *and* restore** of the operand.

How each L3 capability is realized here:

- **Install (L1):** create a `ControlPlane` → StatefulSet + Service appear.
- **Upgrade (L2):** change `spec.image` → the StatefulSet's image is updated; the
  controller reports `phase=Progressing` (`reason=Upgrading`) until
  `readyReplicas` match the desired count at the new image, then `phase=Ready`.
  (Covered by a fake-client test.)
- **Reconfigure (L3):** change `spec.configRef` (or the referenced object's
  content) → a content hash on the pod template rolls the StatefulSet.
- **Backup (L3):** `spec.backup` materializes an owned CronJob that runs the
  **real** `olivares dr backup` over the operand's data PVC; the last successful
  run is mirrored into `status.lastBackup`.
- **Restore (L3):** `spec.restoreFrom` records a restore request (declared seam).

### Backup — the real `dr backup`, not a placeholder

The backup CronJob runs the **real** `olivares dr backup` (the same
ledger-continuity-safe path the chart's DR runbook uses), invoked **shell-free**:
the release engine image is based on `gcr.io/distroless/static-debian12:nonroot` — it has **no shell,
`date` or `find`** — so the job invokes the `olivares` entrypoint directly. The
unique per-run bundle name comes from the downward-API `POD_NAME` (`--out=…-$(POD_NAME).drbundle`),
local retention from `dr backup --retain-days`, and the data volume is the
operand's StatefulSet PVC (`data-<name>-0`, pinned to that pod's node by
podAffinity so the RWO volume is mountable). The signing keys are sealed under the
`kekSecret` KEK; in HA the shared `auditSigningKeySecret` is mounted so the
manifest signer uses the same ledger key. For `engine=postgres` a `pg_dump`
initContainer (the `pgClientImage`) produces the store snapshot the bundle wraps —
running on the **BYPASSRLS admin DSN** (`spec.postgres.adminDsnKey`, which
validation requires whenever backups are enabled on postgres). Two distinct
failure modes make that DSN non-negotiable: `pg_dump` keeps `row_security=off`
and **aborts** as a role that cannot bypass the `FORCE ROW LEVEL SECURITY`
policies — an application-DSN dump means every scheduled run fails and no backup
exists — and, separately, a `dr backup` without the admin DSN enumerates tenants
RLS-scoped, so the bundle's manifest inventory is silently incomplete (see the
DR runbook's *Postgres (logical) and PITR* section).

A same-cluster bundle is **not** disaster recovery — mirror the destination PVC
**offsite** (3-2-1). The destination PVC the operator creates is intentionally
**not** garbage-collected with the ControlPlane.

### Honest seams (declared, not faked)

- **Restore** is a declared path: `spec.restoreFrom` is recorded on the pod
  template (`ops.olivares.ai/restore-from`). The actual restore is the symmetric
  `olivares dr restore` runbook operation (`docs/DR-RUNBOOK.md`), kept **manual**
  on purpose — a restore overwrites a live ledger, so it is not auto-driven.
- **TLS in HA**: each replica self-signs its own certificate in its per-pod data
  dir. Front the Service with an ingress that terminates TLS, or provide shared
  TLS material, for a uniform server certificate across replicas.

These seams are marked as such in the code comments where they live
(`internal/controller`).

## Agent-runtime CRD interop — **TRACK, don't block**

There is an emerging ecosystem of Kubernetes **agent-runtime** CRDs. This operator
**tracks** them but deliberately **does not depend on, watch, or introspect** them
— their APIs are **pre-stable and moving**. The posture is a
**data-only registry** plus an **opt-in, presence-only** discovery helper that is
**OFF by default** (`internal/agentruntime`). No controller watch is registered
against these CRDs.

> **Verified at 2026-06 against primary sources — subject to change. Track, do not
> hard-code.** Re-verify against the cited upstream docs before building on any of
> these names/versions.

- **Agent Sandbox** — `kubernetes-sigs/agent-sandbox` (K8s SIG Apps),
  <https://agent-sandbox.sigs.k8s.io>. Maturity: **v0.21, "moving fast", API NOT
  stable (pre-1.0)**. CRDs: `Sandbox`, `SandboxTemplate`, `SandboxClaim`,
  `SandboxWarmPool`.
- **kagent** — CNCF **Sandbox** project (by Solo.io), <https://kagent.dev>,
  <https://www.cncf.io/projects/kagent/>. API group **`kagent.dev/v1alpha2`**
  (migrated from `kagent.io/v1alpha1`). CRDs include `Agent`, `ModelConfig`,
  `MCPServer`, `RemoteMCPServer`, `Memory`, `SandboxAgent`, `ToolServer`.
  **Note:** `ToolServer` is being **removed** in favor of the kmcp APIs.

Every exported symbol in `internal/agentruntime` carries a caveat citing the
above. `DiscoverInstalled(ctx, discoveryClient)` is the opt-in presence check
(by API group, via the discovery API only) — it never reads the CRDs' schemas.

## Layout

```
operator/
  api/v1alpha1/        ControlPlane CRD types + generated DeepCopy + SchemeBuilder
  internal/controller/ the ControlPlane reconciler (+ fake-client tests)
  internal/agentruntime/ data-only TRACK registry + opt-in presence discovery
  cmd/manager/         controller-runtime manager entrypoint (leader election, probes)
  config/crd/          generated CustomResourceDefinition YAML
  config/rbac/         generated manager ClusterRole
  config/samples/      a sample ControlPlane CR
```

## Build / test

```sh
cd operator
go mod tidy
go build ./...
go test ./...   # fake-client reconcile tests — NO envtest, NO real cluster
```

Pinned: `sigs.k8s.io/controller-runtime v0.24.1` with
`k8s.io/{api,apimachinery,client-go} v0.36.2`.

DeepCopy / CRD / RBAC are generated with `controller-gen` (kubebuilder markers
live on the Go types). To regenerate:

```sh
controller-gen object:headerFile=<spdx-header> paths=./api/...
controller-gen crd  paths=./api/...            output:crd:artifacts:config=config/crd
controller-gen rbac:roleName=olivares-operator-manager-role \
  paths=./internal/controller/... output:rbac:artifacts:config=config/rbac
```

## Run from source against a development cluster

```sh
cd operator
kubectl apply -f config/crd/ops.olivares.ai_controlplanes.yaml
go run ./cmd/manager --metrics-bind-address=0 --health-probe-bind-address=:8081
# In another shell, after the manager is ready:
# Replace spec.image with an image available to the development cluster first.
kubectl apply -f config/samples/ops_v1alpha1_controlplane.yaml
```

The manager uses the active kubeconfig in this development path. The generated
`config/rbac/role.yaml` is the manager ClusterRole, but this directory does not ship
the ServiceAccount, binding, Deployment and image needed for a production install.
The manager binary is `cmd/manager` (`--leader-elect`, `--metrics-bind-address`,
`--health-probe-bind-address`) and is separate from the engine.
