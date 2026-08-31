<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Runbook — control-plane failover / node recovery

**Severity:** SEV1 (the control plane is a single writer today; if it is down, governance/HITL/enforcement are down with it).

> **Read this first — the honest state (verified 2026-06-12).** On the **default topology** (SQLite, one StatefulSet replica, one ReadWriteOnce PVC) there is **NO automatic failover**: recovery is mechanical (reschedule + volume re-attach) or manual (restart), and `replicaCount > 1` without Postgres + a shared audit signing key is still a hard chart failure (the guard now *conditions*, it no longer forbids). The **HA topology shipped**: with `core.engine=postgres`, `core.replicaCount=3|5` and `core.auditSigningKeySecret`, a Postgres advisory-lock leader election runs one active writer with hot standbys (`/readyz` answers `503 standby` off-leader and drains them), and a standby takes over automatically. The scenarios below are written for the single-replica default; on the HA topology, scenario A is handled by the leader election and your job is to verify a leader exists.

## Detect
- `OlivaresStoreDown` (`olivares_store_up == 0`) or `OlivaresControlPlaneUnscrapeable` (`absent(olivares_store_up)`) fires; the external `/readyz` status-page probe goes red.
- `kubectl get pods -l app.kubernetes.io/component=core` shows the pod `NotReady`, `Pending`, `Terminating`, or `CrashLoopBackOff`.

## Diagnose & mitigate by scenario

### A) Node/pod failure (pod gone or stuck Pending)
The StatefulSet reschedules the single pod, but the **RWO PVC must detach from the dead node and re-attach to the new one**:
```bash
kubectl get pods -l app.kubernetes.io/component=core -o wide
kubectl describe pod <core-pod>          # look for "Multi-Attach error" / volume stuck
kubectl get events --sort-by=.lastTimestamp | grep -iE "volume|attach|FailedScheduling"
```
- **Multi-Attach / volume stuck:** the volume can't detach from the failed node (common single-AZ failure). Force-detach per your CSI driver / cloud, or (if the node is truly gone) delete the stuck VolumeAttachment so the scheduler can re-attach. The volume pins recovery to its AZ.
- Once the PVC re-attaches, the pod cold-starts (TLS + signing-key load + store open) and `/readyz` returns 200.

### B) Store wedged, process alive (the silent trap)
`/readyz` → **503 `store:down`** but `/livez` → **200**, so the pod is **NotReady forever** — liveness deliberately runs **no** dependency check (no restart loop on a backend), so nothing self-heals.
```bash
kubectl rollout restart statefulset/<release>-core      # manual restart
```
Then diagnose the store: SQLite PVC corruption/full disk, or (Postgres) DB reachability/credentials. `/readyz` body distinguishes `store:down`.

### C) Probe failing but the app is healthy
Probes are `httpGet` on **:8443 over HTTPS** against a **self-signed** cert by default (the kubelet doesn't verify it). If probe TLS verification was hardened, probes fail though the app is fine — check the probe `scheme`/cert config in `values.yaml`, not the app.

## Timings (what Kubernetes does, when)
- **Readiness** drains after ~3 failures × 10s ≈ **30s** (removes the pod from Service Endpoints; does not restart).
- **Liveness** restarts after ~6 failures × 15s ≈ **90s**, but **only** for a process hang — never for a store dependency (scenario B is why).

## Recovery & data
- Recovery is **restore-from-PVC**. If the PVC is **lost**, the audit signing key and checkpoint continuity are lost **unless an off-box backup exists** ([key-rotation.md](key-rotation.md), [ledger-verify-failure.md](ledger-verify-failure.md)).
- **Never** run production on `persistence.enabled=false` (emptyDir): it loses the signing key + setup token on every reschedule. Demo only.
- **PodDisruptionBudget** is disabled by default and, with one replica, cannot provide availability — enabling it only guards voluntary drains (and can wedge node maintenance). It is not HA.

## Verify
`/readyz` 200 `store:up`, `olivares_store_up == 1`, the status page green, request-success SLI recovering. Re-run the ledger check after any restore ([ledger-verify-failure.md](ledger-verify-failure.md)) — a restored older snapshot can pass a naive walk but fail the off-box checkpoint.

## The HA topology (shipped)
Postgres-backed leader election (one active writer + standbys, a Postgres advisory lock), failover wired to `/readyz` draining the non-leader (`503 {"status":"standby"}`), shared audit signing key via `core.auditSigningKeySecret` (the chart enforces it for `replicaCount > 1`, Postgres only). This is the path to the 99.9% availability tier (`docs/17-PRODUCTION-READINESS-SLO.md`). On that topology: verify a leader exists (exactly one replica answers `/readyz` 200 with `leader:true`); if all replicas report standby, the election is stuck — check Postgres advisory-lock connectivity. Standby `ErrNotLeader` handler logs are demoted to Debug and are not a fault signal.
