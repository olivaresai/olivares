<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Reference architecture (buyer-facing)

> For the security architects and platform teams sizing an Olivares AI deployment.
> Everything here is consolidated from the engineering docs it cites — topology and
> HA from `ARCHITECTURE.md`, DR from `docs/DR-RUNBOOK.md`, multi-region
> from `docs/MULTI-REGION-RESIDENCY.md`, sizing/SLO from `docs/SIZING-AND-CAPACITY.md`
> + `docs/17-PRODUCTION-READINESS-SLO.md`.
> Conceptual background: the public docs-site *Architecture overview* page.

## 1. Components and trust zones

One product, two planes, three trust zones — **all inside the customer perimeter; there are
no mandatory outbound calls at boot and no telemetry-to-vendor channel** (license validation
is offline Ed25519). Licensing and updates DO reach the vendor by design: `olivares
upgrade`, which fetches from the public repository's GitHub releases (or
`licenses.olivares.ai` with `--enterprise`) unless `--endpoint` points it at your own
mirror or `--bundle` installs from a carried bundle.

```
┌─ Zone A — Agent estate (least trusted) ────────────────────────────────┐
│  AI agents · MCP servers · dev endpoints (Claude Code, Cursor, Gemini  │
│  CLI) · model providers in use · cloud accounts                        │
│                                                                        │
│    collectors / connectors  (read-first, least-privilege, push-only:   │
│    NO inbound listener; eBPF sensor isolates CAP_BPF/CAP_PERFMON       │
│    away from the connector process)                                    │
└───────────────┬────────────────────────────────────────────────────────┘
                │ OTLP/gRPC over mTLS (push)
┌─ Zone B — Control plane (the engine) ──────────────────────────────────┐
│  olivares binary ×N (HA): core + 30 modules + embedded web UI          │
│  store: SQLite (single-node) │ Postgres + RLS FORCE (HA/scale)         │
│  audit ledger: append-only, hash-chained, Ed25519 per-event signatures │
└───────┬───────────────────────────────┬────────────────────────────────┘
        │ operator access (TLS, RBAC,   │ governed egress (all optional):
        │ step-up AAL3 for privileged)  │  · SIEM/ITSM push + pull export
┌─ Zone C — Operators & integrations ───┴───────────────────────────────┐
│  admins via web/CLI/API/Terraform · IdP & identity sources (LDAP/     │
│  SCIM/OIDC) · KMS for BYOK (AWS/GCP/Azure) · S3 Object-Lock WORM      │
│  archive · status page probes                                          │
└────────────────────────────────────────────────────────────────────────┘
```

Data-minimization invariant across all zones: the engine persists **relations,
metadata and hashes — never payloads, secrets or PII**; redacted fields are
hashed (SHA-256) at write time.

## 2. Supported production profile

**T2 is the one supported production profile:** an HA active-passive Olivares
deployment backed by Postgres, with row-level security enabled and forced on every
tenant table (`ENABLE` + `FORCE ROW LEVEL SECURITY`). Its availability objective is
a **design target of 99.9% over a rolling 28-day window**, not a measured SLA or a
claim about any deployment already in operation.

The other tiers are not alternative supported-production choices. T1 is the
evaluation/pilot profile and T3 is a roadmap tier.

| Topology | Store | Support status | Availability target |
|---|---|---|---|
| **T1 Single node** | SQLite (pure-Go, no CGO) | Evaluation/pilot; **not supported-production** | No production SLO target |
| **T2 HA active-passive** | Postgres + RLS FORCE | **The supported production profile** | Design target: 99.9%/28d |
| **T3 Multi-region** | Postgres per region | Roadmap tier; not currently supported-production | No current SLO target |
| **Air-gapped** (T1 or T2 deployment mode) | Local infrastructure | Support status follows T1 or T2 | Target follows the selected tier |

**T2 mechanics:** typically 3 replicas; leader election via Postgres
advisory lock (no orchestrator dependency — works on K8s, systemd, Docker,
air-gapped); standbys answer 503 on `/readyz` so the LB drains them without
restart; writes are leader-gated (defense in depth: `ErrNotLeader` → 503); the
audit-signing key is externalized (shared Secret / KMS seam) so failover never
forks the signed chain — semantics are CP: at most one writer, a few seconds of
write unavailability on hard crash instead of a forked audit chain. Schema changes
are online (expand-contract migrations, `CREATE INDEX CONCURRENTLY`). Optional
NATS bridge fans events across nodes; without it the binary is unchanged.

**T3 roadmap design:** separate instances per region (`--region eu` / `--region
us`), each with its own store; tenants opt into a pin (`orgs.data_region`); a
deny-closed guard rejects cross-region access with 403 `residency_violation`;
model `inference_geo` must match the pin. These mechanics do not make T3 a
currently supported production tier.

## 3. HA/DR numbers (what to put in your runbook)

| Tier | RPO objective | RTO objective |
|---|---|---|
| SQLite single-node (cron backups) | backup interval (15 min–6 h) | < 15 min |
| Postgres logical backups | cron interval (1–6 h) | < 30 min |
| Postgres PITR (WAL archiving) | ≈ seconds | < 30 min |

Backups are encrypted bundles (`.drbundle`) under an operator-held KEK (stored
separately — no KEK, no restore); restore **verifies** chain continuity, per-event
signatures and checkpoints, and fails closed on mismatch. Run drills on a cadence;
a failed drill is an incident (`docs/DR-RUNBOOK.md`).

## 4. Sizing (measured baseline, reference HW: Ryzen 7 5800U, 32 GiB)

- Single-writer durable ceiling: **≈1.5k writes/s** (signed audit append p99
  ≈1.2 ms) on both SQLite and Postgres (NVMe).
- Concurrency: SQLite degrades (−55% at 16 writers); Postgres scales (**5,203
  ev/s @4 writers; 7,829 @16**). Empirical crossover: **1–2 concurrent writers**.
- In-process bus: ≈3.8M ev/s — never the bottleneck.
- Rate-limit admission (HA, shared Postgres buckets): 0.45 ms p50 / 0.67 ms p99.
- Decision plane: a full governed decision runs **≈4.8k/s** (hook PEP) or **≈1.7k/s**
  (proxy PEP, including a signed audit append per allowed call), p99 ≤ 1.1 ms — far
  inside the API SLO; the policy algebra itself is ~200k/s.
- Governed RAG: exact linear cosine to a **100k chunks/tenant** ceiling (10k ≈ 100 ms,
  100k ≈ 1 s p99, recall 1.0); beyond it, an external vector backend (pgvector/Qdrant).
- Storage growth: ≈**0.42 KiB per audit event** (≈0.39 GiB/million) — size retention
  from your write rate × window.
- Tenants & concurrent agents: **no fixed per-node cap** — RLS rows bounded by write
  throughput + per-tenant storage; provisioning ≈930/s, RLS adds no per-tenant penalty.

Rule of thumb: move to Postgres when you need HA **or** sustained concurrent
writers; re-run `task bench` on your own storage before committing capacity
(`docs/SIZING-AND-CAPACITY.md`, which adds the decision, retrieval and storage-growth planes).

T2 design targets (`docs/17-PRODUCTION-READINESS-SLO.md`): availability
99.9%/28d; API p99 < 300 ms; ingest p99 < 250 ms; request success 99.9% — with
burn-rate alerting and an error-budget release-freeze policy. These are design
targets for the supported production architecture, not measured SLAs. T1 and T3
do not currently carry a production SLO target.

## 5. Security architecture highlights

- **Transport:** TLS 1.3 everywhere; mTLS collector↔core; hybrid post-quantum key
  establishment (X25519MLKEM768) by default.
- **Tenancy:** Postgres RLS `FORCE` + application-layer tenant guard + SQLite
  triggers; region guard on top for T3.
- **Crypto custody:** BYOK/CMEK envelope encryption (AWS/GCP/Azure KMS),
  fail-closed sealed configs; optional FIPS 140-3 build (CMVP cert #5247) and
  STIG-profiled image (OpenSCAP-scannable).
- **Privileged access:** RBAC; AAL3 step-up (WebAuthn/PIV) for privileged actions;
  dual-control approvals; audited break-glass; graduated estate kill switch with
  structural two-person re-enable.
- **Supply chain:** signed releases (cosign), SBOM + SLSA Build L3 (SLSA v1.2) provenance + OpenVEX,
  reproducible build (`task build:repro`), distroless non-root images.

## 6. Integration surface (what plugs into your stack)

| Integration | Mechanism |
|---|---|
| **IdP / identity** | LDAP, SCIM (Groups), OIDC/IdP identity sources (live gather); agent-identity federation: Entra Agent ID, AWS AgentCore, Google agent identities |
| **SIEM/SOC** | Pull export: CEF, LEEF, syslog, OTLP, OCSF; **push forwarder** (at-least-once, per-sink profiles) for ledger + findings; posture export for control towers (Agent 365 / ServiceNow AI Control Tower) |
| **ITSM** | Push sinks for findings/incidents |
| **Observability** | Prometheus `/metrics` + shipped SLO alert rules (`deploy/monitoring/olivares-slo.rules.yaml`) + blackbox status-page probes (Gatus); OTel GenAI ingest (`gen_ai.*`) from agents/frameworks |
| **Cloud management planes** | Read-only audit ingestion: AWS (CloudTrail), GCP (audit), Azure (activity) |
| **Manage-as-code** | Terraform provider + client SDKs (Go/Python/TypeScript) + versioned REST API with published deprecation policy (RFC 9745/8594 headers) |
| **Eventing** | Typed platform webhooks with retries/replay (cursor-based), SSRF-hardened |
| **Storage/archive** | S3 Object Lock (COMPLIANCE mode) WORM archival of the ledger with chained, verifiable manifests |
| **KMS** | AWS KMS, GCP Cloud KMS, Azure Key Vault for BYOK envelope + signing custody |

## 7. What this architecture does **not** include (honesty corner)

- No vendor-hosted control plane today (managed offering is design-stage).
- The 99.9%/28d figure is a design target for T2, not a measured SLA. T1 is an
  evaluation/pilot profile with no production SLO; T3 is roadmap.
- A process can verify its store engine and effective RLS posture at boot, but it
  cannot prove the external replica count, load-balancer health or failover setup.
  Operators must validate those HA parts of T2 in deployment and recovery drills.
- At-rest encryption is operator-provided (and attested in-product); the engine
  does not transparently encrypt its own store files.
- The kill switch stops actuation; rollback of *deployed customer infrastructure*
  is deploy-scoped only — documented, not magical.
