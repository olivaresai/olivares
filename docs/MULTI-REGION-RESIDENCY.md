<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Multi-region & data sovereignty of the control plane

Status: implemented. Feeds the reference architecture and composes with
HA/clustering and DR.

## Problem

Residency used to be modeled only at the **model/inference** layer (`inference_geo`,
the workspace `DataResidency.AllowedInferenceGeos`). The control plane's **own data** —
a tenant's access graph, sessions, identity roster and audit metadata — had no
multi-region topology: every tenant lived in one database, wherever the single
deployment ran. A multinational buyer with EU/US obligations could not pin a tenant's
control-plane data to a region. That is the gap this addresses.

## Topology — region-scoped instances (Model B)

Chosen over the alternative (a single process holding every
region's backend and routing per tenant): the single-process model has more surface,
weaker physical sovereignty (the process holds every region's credentials), and a
heavier System/Auth path (cross-region fan-out, a global directory). Region-scoped
instances are simpler, give **physical** isolation, and compose cleanly with HA.

```
            ┌──────────── edge / DNS / LB ────────────┐
            │  residency directory:  tenant → region   │
            │   acme  → eu      globex → us             │
            └───────────────────┬──────────────────────┘
                  ┌─────────────┴──────────────┐
        ┌─────────▼─────────┐         ┌─────────▼─────────┐
        │  EU instance       │         │  US instance       │
        │  --region eu       │         │  --region us       │
        │  Postgres (eu)     │         │  Postgres (us)     │
        │  HA intra-region   │         │  HA intra-region   │
        └────────────────────┘         └────────────────────┘
   deny-closed guard: org.data_region must equal HOME_REGION
   (or be empty/unpinned), else store.ErrResidencyViolation → 403
```

- **Each instance owns one home region** (`--region eu`). Its store is only that
  region's backend. The EU instance has no US DSN — it physically cannot read US data.
- **A tenant's control-plane data lives only in its region's instance.** Provisioning a
  tenant is routed (by the edge, using the residency directory) to its region.
- **The deny-closed guard is the backstop.** Even if a request is misrouted to the wrong
  region, it is refused — not silently served an empty set, and never served
  cross-region.

This wraps the stable `store.Store` seam, so it is **orthogonal to HA**: HA makes
each regional backend highly available (leader-election + standby) *inside* a region;
multi-region pins and routes tenants *across* regions. The two layers compose without
touching each other.

## The pieces

### 1. The pin — `orgs.data_region`

A nullable first-class core column on the `orgs` table (`core/model` `Org.DataRegion`),
not a free-form `Settings` key: residency is a governance fact. It is added additively
to existing databases via the store's `reconcileCoreColumns` path (nullable-only).
**Empty = unpinned = no residency requirement** — residency is opt-in per tenant; only
pinned tenants are subject to cross-region enforcement.

### 2. The region registry — `--region` / `--known-regions`

Region codes are operator-defined (a configurable registry), not a fixed `eu`/`us`
enum: adding a region is configuration, not a code change.

| Flag | Meaning |
|---|---|
| `--region <code>` | This instance's **home** region. Empty = single-region mode (no enforcement; today's behavior, zero overhead). |
| `--known-regions a,b,…` | The region codes valid across the whole deployment. A tenant pin must be one of these; the home region is always included. |

A malformed region config fails the **boot**, not a later request.

### 3. The deny-closed guard — `core/residency`

`residency.Guard(store, registry, log)` wraps the home-region store. On every
tenant-scoped `View`/`Mutate` it resolves the bound tenant's pin (an indexed org read
folded into the same transaction) and:

- pin empty (unpinned) **or** pin == home → allowed;
- pin == another region → `store.ErrResidencyViolation` (deny-closed);
- the tenant has no org row here (resident elsewhere) → `store.ErrResidencyViolation`
  — never run as if it simply had no data.

The reserved **system tenant** and the **Auth/System** paths pass through (the local
auth/provisioning partition every instance holds for itself). In single-region mode the
guard is not installed at all. The API maps the error to **403 `residency_violation`**.

### 4. Provisioning & re-pin — the org API

- `POST /v1/system/orgs` accepts an optional `data_region`, validated against the
  registry (known region; the home region on a region-scoped instance) before the
  tenant is created — never created half-pinned.
- `PUT /v1/system/orgs/{tenant}/region` sets/clears a tenant's pin (superadmin),
  version-checked so a concurrent settings update cannot revert it, and audited
  (`org.set_region`). Adopting residency for an existing **local** tenant is safe;
  **moving** a tenant between regions is a data migration (out of scope — handled by DR).

### 5. Coherence with inference residency

`residency.InferenceGeoCompatible(pin, geo)` is the coordination seam with the existing
model-level `inference_geo`: a pinned tenant is compatible **only** when the inference
geo equals the pinned region. `global` (may route anywhere), another region's geo, and
an empty/`not_available` geo are all incompatible when pinned — strict on purpose
(residency means inference stays in-region; loosen by leaving the tenant unpinned).

The compliance residency scan (`POST /v1/m/compliance/residency/scan`) folds in the
observed inference geos (read inline from the FinOps cost read-model) and flags each
distinct out-of-region geo as a `residency_violation` Finding + the existing
`compliance_residency_violation` bus signal. It does not block
inference; it makes the crossing visible and routed.

### 6. Coherence with compliance attestation (module XIII)

The same scan cross-checks the pin against the **self-hosted attestations**: a tenant
pinned to a region with no backing self-hosted attestation for that region is flagged
(`attestation_gap`). The pin says *where data must be*; the attestation claims *where it
is*; the scan makes a disagreement loud.

## Operating a multi-region deployment

1. Provision one regional Postgres per region (`deploy/postgres/01-app-role.sql` per
   cluster) and one control-plane instance per region with its `--region` /
   `--known-regions`. HA applies within each regional cluster.
2. Front the instances with an edge that routes each tenant to its region using the
   residency directory (the tenant→region mapping; `data_region` is the source of
   truth, surfaced on the org API). The deny-closed guard backstops any misroute.
3. Provision each tenant on its region's instance with its `data_region` pin; pin
   existing local tenants via the re-pin endpoint.
4. Run the compliance residency scan to surface pin/attestation/inference incoherence.

## Scope

**In:** per-tenant region pin; region-scoped routing/enforcement of the store
(deny-closed cross-region); coherence with inference residency and with compliance
(XIII); this topology doc; tests (pin respected, no cross-region).

**Out:** HA intra-region; DR / backup-restore / region migration; the Claude
inference residency itself (consumed, not reimplemented); re-architecting the
RLS multi-tenancy model (RLS remains the base — it still isolates tenants *within* a
region; residency adds the cross-region dimension on top).
