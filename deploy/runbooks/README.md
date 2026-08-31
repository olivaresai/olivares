<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# On-call runbooks — Olivares control plane

Real, tested-against-the-code runbooks for the failure modes an operator of the
control plane will actually hit. Each is structured:
**symptom → detect → diagnose → mitigate → verify → prevent**, and is honest about
what the default single-node topology and the optional Postgres active-passive HA
topology can and cannot do (automatic takeover applies only to configured HA; there
is no invented key-rotation command).

| Runbook | When it fires | Alert |
|---|---|---|
| [ledger-verify-failure.md](ledger-verify-failure.md) | the evidence ledger fails chain / checkpoint / event-signature verification | scheduled `audit verify --strict` cron returns non-zero |
| [ledger-recovery.md](ledger-recovery.md) | a corrupt audit tail must be sealed and re-anchored (guided, dual-controlled, append-only-preserving) when re-attaching the correct volume is not possible | follows a ledger-verify-failure diagnosis (`OlivaresAuditCheckpointFailing`) |
| [collector-backpressure.md](collector-backpressure.md) | collectors push faster than core can persist; ingest stalls, or the NATS bridge disconnects/drops | `OlivaresIngestP99High` / `OlivaresEventBusBridgeDisconnected` / `OlivaresEventBusBridgeDropping` |
| [failover.md](failover.md) | a single-node control plane fails, or an HA writer/store fails and takeover must be verified | `OlivaresStoreDown` / `OlivaresControlPlaneUnscrapeable` |
| [key-rotation.md](key-rotation.md) | rotating the audit signing key (planned, or on suspected compromise) | n/a (planned op / security incident) |
| [support-bundle.md](support-bundle.md) | collecting a redacted diagnostic bundle to share with support / IR without leaking secrets | n/a (on request / during an escalation) |

**Prerequisites for the on-call:** `olivares` binary on PATH (or `kubectl exec` into the core pod), the data-dir / `--dsn`, and — for the attacker-resistant ledger check — the **off-box-retained audit public key** (fetch once when healthy: `GET /v1/audit/pubkey`, store it outside the box). SLOs, severities and the comms process: `docs/17-PRODUCTION-READINESS-SLO.md`, `docs/STATUS-AND-INCIDENT-COMMS.md`. Alerts: `deploy/monitoring/olivares-slo.rules.yaml`.
