---
title: "Module XX — multi-tenancy & org management"
description: >-
  The isolation foundation: every core entity carries a tenant_id, and the store
  refuses to open unless that boundary is enforced at the query layer. What the
  data model guarantees today, and what org hierarchy and delegated admin still are.
---

Module XX is not a service that hangs off the engine — it is a **property of the
engine itself**. There is no separate tenancy module to attach; instead, the core
data model carries a tenant boundary on every entity and the store enforces it
below every query. This page is the reference for what that boundary guarantees
today, and for the parts of org management that are still design-stage.

## What it is

Multi-tenancy lives in the Engine layer (layer 0), alongside the platform's own API
(module XIX), because retrofitting isolation onto a running data model is the kind of
change you cannot do safely later. Every core entity carries a **`tenant_id`**, and a
caller never passes one as a free parameter: it **fixes the tenant once** and receives
a scope whose repositories are already bound to it. There is **no vocabulary in the
API to cross tenants** — that absence is the first isolation barrier, before any
database mechanism. The privileged cross-tenant scope (creating an org, listing orgs,
dropping a tenant) is reachable **only by the engine's own startup**, never by a
module.

## The contract and the entities

The tenant model is owned by the data-model contract, not by a per-module
schema. The root entity is the **`Org`**, which *is* the tenant: when the engine seeds
an org, its identifier becomes the tenant identifier and the org's own audit chain is
established at the same moment. Every other core entity — agents, sessions, resources,
identities, policies, cost records, findings, deployments, the access map and the
audit ledger — is created **inside** a tenant scope and stamped with that tenant on
write; the caller cannot override it.

Isolation is enforced at the query layer, by deployment:

- On **PostgreSQL**, each table carrying `tenant_id` runs under `FORCE ROW LEVEL
  SECURITY` with a `tenant_isolation` policy bound per transaction. A transaction that
  fails to bind a tenant **raises an error** rather than silently returning zero rows
  (fail-closed). The application role is non-superuser and never has `BYPASSRLS`, and
  `FORCE` binds the policy even for the table owner. Ownership is a **deployment
  choice**: the default single-role install leaves the application role as database
  owner — RLS still binds it, but an owner can alter its own tables, so that posture is
  tamper-evident rather than owner-proof. The **hard** privilege boundary — an
  application role that is also non-owner — comes from the split owner/app topology,
  where a separate owner role runs provisioning and the application role receives only
  the DML it needs.
- On **SQLite** (the single-node deployment) there is no row-level security; the
  equivalence comes from two facts — the *only* path to the database is the
  descriptor-generated SQL that always appends the tenant predicate, and **tripwire
  triggers** abort any write whose tenant does not match the pinned scope.

A **startup self-test** queries the live isolation guards after migrating and
**refuses to open** the store if any table carrying `tenant_id` is unprotected — so a
forgotten guard on a new table becomes a boot failure, not a silent leak.

## What it consumes and produces

Module XX has no event-bus surface and no actuation. It does not consume `edge.observed`,
emit findings, or call any provider — it is the substrate the other modules write
*through*. Its only observable effect is structural: every entity any module persists
is already tenant-scoped, and every mutation on an audited entity appends to that
tenant's [hash-chained audit ledger](/reference/events/) within the same transaction.

:::caution[Honest limits]
- **What the data model actually models is `Org`-as-tenant + the isolation boundary** — not the
  full org hierarchy. **Teams, projects, delegated admin, per-level roles, and
  usage/billing per org are design-stage**, not shipped entities. Treat the
  product's tenancy guarantee today as: *one org = one isolated tenant, enforced at the
  query layer.*
- **Read isolation on SQLite is by the query layer, not the engine.** SQLite has no
  row-level security: read scoping is a property of the generated SQL (writes are also
  covered by the tripwire triggers). Multi-tenant **at scale is PostgreSQL with RLS** as
  the kernel-level backstop; SQLite is the single-node / air-gapped deployment.
- **The cross-tenant admin scope is deployment-dependent on PostgreSQL.** Listing orgs
  across tenants needs a dedicated admin role on PostgreSQL and concerns the
  deployment, not application code. It works directly on SQLite (single-writer).
- **Tenancy is not delegated administration.** Who may act *within* a tenant — roles,
  approvals, segregation of duties — is governed by [module VI](/reference/modules/vi-governance/),
  not here. Module XX guarantees the wall between tenants; module VI guards the door
  inside one.
:::

## Related

- [Modules catalog](/reference/modules/overview/) — where module XX sits and its honest actuate status.
- [Identity, permissions & governance](/reference/modules/vi-governance/) — roles and delegated authority within a tenant.
- [Architecture overview](/explanation/architecture/overview/) — the engine layer and the general data model.
- [Event bus reference](/reference/events/) — the per-tenant audit ledger every mutation appends to.
- [Honesty & limits](/start/honesty-and-limits/) — what is built today versus design-stage.
