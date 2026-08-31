<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# HA leader routing — operator runbook

Active-passive HA (`engine=postgres`, `replicas > 1`) can run in one of two layouts.
This document explains both, how to move between them, and what the operator reports
when something goes wrong. (`cp` in the commands below is the ControlPlane resource's
short name, as declared by the CRD.)

## 1. Why there are two layouts

The control plane is a **single writer**: exactly one node holds the Postgres
advisory lock and may write; the rest are hot standbys that take over on failover
(`ARCHITECTURE.md`). Kubernetes needs two different answers from every
pod, and the original layout gave it only one:

| Question | Endpoint | Answer on a standby |
|---|---|---|
| Is this pod healthy? | `GET /pod-readyz` | **200** — it is a healthy hot standby |
| Should client traffic go here? | `GET /readyz` | **503** — it is not the writer |

**Legacy layout** (`spec.haRouting: Legacy`, the default) wires the container
`readinessProbe` to `/readyz`. Routing is correct — the headless Service only ever
resolves to the leader — but two structural costs follow:

- `ReadyReplicas` can never reach `spec.replicas`, so the ControlPlane never reports
  `Ready` (the operator says so explicitly: `Degraded/HALegacyReadinessBlocked`);
- a StatefulSet **rolling update wedges**. The controller replaces the highest
  ordinal first, that pod comes back as a standby, it never becomes Ready, and a
  never-Ready pod never satisfies the update barrier. The rollout stops there.
  `podManagementPolicy: Parallel` does not help: it governs replica-count changes,
  not the update barrier.

**Leader-routing layout** (`spec.haRouting: LeaderRouting`) splits the two
questions, the way Patroni does for Postgres:

- the `readinessProbe` becomes `/pod-readyz`, so every healthy replica is Ready and
  the rolling update progresses;
- the engine publishes `ops.olivares.ai/role=leader` **on its own pod** once it wins
  the election (and `=standby` otherwise);
- the operator creates `Service/<name>-leader`, a ClusterIP Service selecting that
  label — the client endpoint;
- every application request re-checks leadership inside the engine, so a briefly
  stale label costs a retryable `503 not_leader` rather than routing traffic to a
  node that knows it is not the leader (§7 states the residual window honestly).

The governing headless `Service/<name>` is unchanged (it keeps the StatefulSet's
per-pod DNS identity) and now includes Ready standbys — which is precisely why the
in-engine leader gate exists.

## 2. What the operator creates in the leader-routing layout

| Object | Purpose |
|---|---|
| `Service/<name>-leader` | ClusterIP; selector = workload labels + `ops.olivares.ai/role=leader`. **The client endpoint.** |
| `ServiceAccount/<name>-leader-publisher` | The identity the engine pods use to label themselves. |
| `Role/<name>-leader-publisher` | `pods` · `get,patch` · `resourceNames: [<name>-0 … <name>-N]`. Nothing else. |
| `RoleBinding/<name>-leader-publisher` | Binds the two. |
| Pod template changes | `readinessProbe: /pod-readyz`, `serviceAccountName`, `automountServiceAccountToken: true`, and `OLIVARES_HA_LEADER_LABEL=1` + `POD_NAME`/`POD_NAMESPACE` (downward API). |

**Blast radius, stated plainly.** In every other layout the engine has *no*
Kubernetes API access at all. Here it gets a namespaced credential that can `get`
and `patch` the pods of its own StatefulSet. Kubernetes RBAC cannot express "this
pod may patch only itself" for replicas sharing one ServiceAccount, so a compromised
standby could mislabel its peers — a bounded denial of service inside that
StatefulSet. It can never become the writer: the Postgres lock is the sole write
authority and every application request re-checks it. If that trade is unacceptable,
stay on `Legacy` and accept that HA cannot be rolling-updated in place.

The manager's own ClusterRole grows accordingly (`operator/config/rbac/role.yaml`):
`serviceaccounts` and `roles`/`rolebindings` CRUD, plus `pods` `get,list,watch,patch`.
`patch pods` is not used by the reconciler — Kubernetes' privilege-escalation
prevention refuses to let a caller create a Role granting rights it does not itself
hold. Granting `patch pods` is strictly narrower than the alternative (`escalate`
or `bind`, which would let the manager mint *any* permission).

**This part is not opt-in.** Installing this operator version widens that ClusterRole
and starts Pod/ConfigMap/Secret informers even in a cluster where every ControlPlane
stays on `Legacy` — the manager must be able to provision the per-instance credential
the moment someone opts in, and the ConfigMap/Secret watches back the config-hash fix
that applies to every layout. What is bounded is what those informers hold: Pods are
cached only for workloads this operator renders, and Secrets are cached with their
payload **stripped** (the config hash reads Secret content through an uncached
client), so the manager never parks cluster credentials in its heap. Treat the
operator upgrade itself as a deployment-security change and review the ClusterRole
diff; if your policy does not permit it, pin the previous operator version — the
engine's `/pod-readyz` and label publisher are inert without it.

## 3. Enabling it on a NEW install

```yaml
apiVersion: ops.olivares.ai/v1alpha1
kind: ControlPlane
metadata: { name: cp }
spec:
  image: docker.io/olivaresai/olivares:26.8.0   # MUST serve /pod-readyz
  engine: postgres
  replicas: 3
  haRouting: LeaderRouting
  auditSigningKeySecret: olivares-audit-key
  postgres: { dsnSecret: olivares-pg }
```

Point clients (Ingress, service mesh, internal callers) at `cp-leader`, **not** `cp`.

## 4. Migrating an EXISTING HA install

The operator will not switch layouts on its own: setting `spec.haRouting` is the
acknowledgement that the two preconditions hold. It cannot verify either one — an
image reference does not reveal whether the binary serves `/pod-readyz`, and no
controller can know where your clients connect.

1. **Upgrade the engine first.** Roll `spec.image` to ≥ 26.7.0 *while still on
   `Legacy`*. On the legacy layout that rollout wedges at the first replaced standby,
   so drive it the documented way: `kubectl delete statefulset <name> --cascade=orphan`
   is **not** needed here — instead delete the standby pods one at a time
   (`kubectl delete pod <name>-2`, wait for it to come back Running, then `<name>-1`),
   and finally the leader pod. Each replacement comes up on the new image; the leader
   is replaced last so the outage is one failover.
2. **PREPARE — ask for the layout:** `kubectl patch cp <name> --type=merge
   -p '{"spec":{"haRouting":"LeaderRouting"}}'`. On a control plane that is already
   running, the operator deliberately stops here: it creates `Service/<name>-leader`
   and the publisher RBAC, leaves the pod template **untouched**, and reports
   `Degraded=True / HALeaderServiceMigrationRequired` with the next step. Nothing
   moves, nothing rolls, and the leader Service is empty (the old leader's engine
   cannot publish the label yet) — which is exactly why clients must not be pointed
   at it before the cut-over.
3. **Cut clients over to `<name>-leader`.** *Every* application caller: the
   Ingress/route in front of the console and the REST API, the CLI's `--server`, SDK
   base URLs, and the **collectors' gRPC ingest endpoint**. What does *not* move:
   Prometheus (scrape the pods — every pod serves `/metrics`), the kubelet probes,
   and anything that deliberately talks to one specific pod through its per-pod DNS
   name in the governing Service. Expect `503 not_leader` from the leader Service
   until step 4 completes; that is the correct, retryable answer while no pod
   publishes the label.
4. **COMMIT — acknowledge it:**
   `kubectl annotate cp <name> ops.olivares.ai/ha-leader-cutover=acknowledged`.
   Only now does the operator change the pod template. Each replaced pod publishes
   its role and passes `/pod-readyz`, so this rollout completes on its own — watch it
   with `kubectl get cp <name> -w`; `status.leaderPod` names the pod the
   leader Service resolves to. A fresh install skips steps 2–4 entirely: with no
   running clients there is nothing to cut over, so it is created in the split shape.

## 5. Rolling back

`kubectl patch cp <name> --type=merge -p '{"spec":{"haRouting":"Legacy"}}'`
reverts the pod template, deletes the leader Service and **revokes** the publisher
ServiceAccount/Role/RoleBinding.

Be aware of the asymmetry: the revert itself is a rollout, and rollouts wedge on the
legacy layout. Expect to finish it by hand — delete the pods still on the old
revision one at a time, highest ordinal first — and move clients back to `<name>`
before you start.

## 6. What the operator reports

| Condition | Meaning | What to do |
|---|---|---|
| `Degraded/HALegacyReadinessBlocked` | Legacy HA, fully rolled, leader serving. `Ready` is unreachable by construction. | Migrate (§4), or accept it. |
| `Degraded/HALeaderServiceMigrationRequired` | The layout was requested on a RUNNING legacy deployment: the leader Service and publisher RBAC exist, the pod template is untouched. | Move clients to `<name>-leader`, then annotate `ops.olivares.ai/ha-leader-cutover=acknowledged` (§4). |
| `Degraded/LeaderNotPublished` | Converged, but no Ready pod carries the leader label → the leader Service has **no endpoint**. | Check the engine logs for election failures (DSN, Postgres reachability) and for label-publication failures (is `spec.image` new enough? is the publisher ServiceAccount mounted?). |
| `Degraded/MultipleLeadersPublished` | Two Ready pods claim the label. | Usually transient during failover; the stale pod answers `503 not_leader` and its publisher resyncs within seconds. If it persists, inspect the old leader's logs (it may have lost Kubernetes API access). |
| `Degraded/RolloutStalled` + `Progressing=False/ProgressDeadlineExceeded` | No progress for `spec.progressDeadlineSeconds` (default 600). | A real wedge: image pull, PVC binding, failing probes. Inspect the pods. |
| `Degraded/HARequiresRecreate` | The live StatefulSet is `OrderedReady`; HA needs `Parallel` (immutable). | `kubectl delete statefulset <name> --cascade=orphan`, then let the operator recreate it. |
| `Progressing=True/WaitingForPodHealth` | Every pod is on the update revision; some do not pass `/pod-readyz`. | Check store reachability from those pods. |

`Available` answers "can clients reach a leader right now?" independently of
`Progressing` — an image rollout is normally `Progressing=True` *and*
`Available=True`.

## 7. The residual failover window (inherited, not introduced)

This layout does not change *who* may write: the Postgres advisory lock does, and
it behaves exactly as it did before. That contract has a bounded window worth
stating plainly, because the leader-routing layout does not remove it:

> A node learns it has lost the lock on its **own poll tick** (2s). If only its
> dedicated lock session dies — a `pg_terminate_backend`, a proxy dropping that one
> connection — Postgres releases the lock immediately and a standby can acquire it
> while the old process still believes it is the leader. For up to that tick, two
> nodes can consider themselves active: the old one still answers, and its
> in-flight writes are not fenced at commit.

This is the CP-over-AP trade the elector already documents (`core/store/leader.go`,
`ARCHITECTURE.md`): at most one *elected* writer, with a few seconds of
ambiguity on a hard failure, in exchange for never blocking on consensus. Closing it
properly means fencing writes with the epoch **inside the transaction** (the elector
already maintains a monotonic `Epoch()`; no production write path consults it yet) —
a change to the store's write path, not to this layout, and tracked as such.

What this layout adds is not a new window but two extra guards inside it: every
application request re-checks *established* leadership at the edge, and the label a
demoted node published is withdrawn by its resync loop, so the stale endpoint stops
receiving traffic. Do not read the sections above as a claim that two writers are
impossible for any interval; read them as "routing follows the elected leader, and
nothing routes to a node that knows it is not one".

## 8. What is verified, and what is not

`.github/workflows/e2e-operator-kind.yml` runs `operator/test/e2e` on a real kind
cluster with an in-cluster Postgres and two engine images, with the manager
authenticated **as its own ServiceAccount** so the shipped ClusterRole is what is
exercised. Its assertions are: a healthy 3-replica HA reaches `Ready` with three
Ready pods and exactly one `/readyz` leader; the leader Service resolves to exactly
that pod; a rolling image update **completes** (the wedge regression) with
`status.currentImage` lagging until it does; and killing the leader promotes a
standby, moves the label and the endpoint, and never lets two pods serve application
traffic.

Those are the assertions the harness makes — not yet a result to cite. The workflow
lands with this change and runs for the first time on its own pull request; until a
green run exists, treat cluster behaviour as **designed and asserted, not
demonstrated**. Everything else above is covered by unit tests that run in the
ordinary gate.

Not covered, and honestly so: an authenticated end-to-end write (the one-time setup
token is minted into the pod's own data dir by design and cannot be injected from
CI — the request-gate response codes are used instead), NATS-backed inter-node
behaviour, and the **Helm chart**, which still ships the legacy layout only; the
chart parity for `/pod-readyz` + the leader Service is a follow-up.
