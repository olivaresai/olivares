<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# ADR-0028: Managed-cloud database — managed PostgreSQL, with row-level security as the tenant boundary

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0005 (SQLite by default, PostgreSQL at scale), ADR-0027
  (managed-cloud ingress), ADR-0029 (managed-cloud regions), ADR-0022 (source-scoping
  subject axes); the platform decision record for the managed cloud; PostgreSQL
  documentation on row security policies and the AWS database guidance on multi-tenant
  isolation with row-level security, consulted 2026-08-02:
  `https://aws.amazon.com/blogs/database/multi-tenant-data-isolation-with-postgresql-row-level-security/`.

## Context and problem statement

ADR-0005 already put PostgreSQL under the product at scale, and the product already
carries the row-level-security machinery for tenant scoping. The managed cloud does not
need a new data model; it needs a decision about **who operates the database** and about
**what, exactly, is relied upon to keep one tenant's rows away from another's**.

The second half matters more than the first. "We use row-level security" is not a
property until the roles are arranged so that the policies actually apply. PostgreSQL
excludes two categories of caller from table policies: superusers and roles carrying the
`BYPASSRLS` attribute — and, by default, **the owner of a table is not subject to that
table's policies at all** unless the table is altered with `FORCE ROW LEVEL SECURITY`.
An application that connects as the role that created the schema therefore has *no*
tenant isolation while appearing to have it. This is the single most expensive mistake
available in this design, and it is silent.

## Decision drivers

- Tenant isolation must be enforced **by the database**, not by the diligence of every
  future query.
- The single operator should not be operating PostgreSQL: patching, failover and
  point-in-time recovery are precisely the work the managed offering exists to remove.
- Recovery must be a property of the platform, not of a runbook someone has to
  remember to run.
- Whatever is claimed about isolation must be **testable from outside the application**.

## Considered options

- **A — self-managed PostgreSQL on virtual machines.** Full control, lowest unit cost,
  and every upgrade, failover drill and backup verification becomes ours.
- **B — the cloud provider's managed PostgreSQL service, multi-AZ**, with automated
  backups and point-in-time recovery.
- **C — the provider's PostgreSQL-compatible cluster service** (shared-storage
  architecture, per-request I/O billing on the standard configuration).
- **D — a third-party PostgreSQL platform** reachable from the same region.

## Decision outcome

Chosen option: **B — managed PostgreSQL, multi-AZ**, with row-level security as the
tenant boundary and the role layout below treated as part of the decision rather than as
implementation detail.

The role layout is normative:

1. The application connects as a role that **does not own** the tenant-scoped tables and
   **does not hold `BYPASSRLS`**.
2. Every tenant-scoped table carries **`FORCE ROW LEVEL SECURITY`**, so ownership alone
   cannot bypass a policy — this protects against a future migration that changes who
   owns a table.
3. The administrative role used for migrations is not the role in the application's
   connection string.
4. **Scope, stated so it is never assumed:** this record governs the **tenant data plane** — the
   schema holding tenant-owned rows, where the engine already emits `ENABLE ROW LEVEL SECURITY`,
   `FORCE ROW LEVEL SECURITY` and a per-tenant policy bound to a session setting. The managed
   plane's **own control metadata** (tenant registry, billing ledger, usage snapshots) is a
   **separate schema with a separate posture**: today it relies on application-level scoping with a
   single application role and no tenant-facing SQL. That may well be the right answer for control
   metadata — but it is currently **inherited rather than decided**, and it is not what "we use
   row-level security" implies to a reader. Whoever builds the managed plane must **state which
   posture that schema has and why**, in writing, before it holds a paying customer's records.

### Consequences

- **Good:** patching, multi-AZ failover, automated backups and point-in-time recovery
  become platform properties. The disaster-recovery runbook the product ships remains
  the artefact for self-hosted deployments; it stops being a daily operational duty of
  the managed plane.
- **Good:** isolation becomes externally testable. The acceptance criterion is a query
  run **as the application role** that attempts to read another tenant's rows and gets
  none — not an assertion in a design document.
- **Bad / trade-offs:** a higher fixed monthly floor than a plain virtual machine, and
  engine-version upgrades arrive on the provider's calendar rather than ours.
- **Neutral:** the managed service's administrative role is a privileged database role,
  **not** a PostgreSQL superuser — it has no operating-system access and cannot rewrite
  the host authentication configuration. That is a useful reduction in blast radius, but
  it is not the thing that makes row-level security hold; the role layout above is.
- **Explicitly NOT verified, and not to be assumed:** whether that administrative role
  carries `BYPASSRLS` on the running engine. It is a one-query check
  (`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user;`) against a real
  instance, and it belongs to the phase that first creates one. Until it is run, no
  document should state that the administrative role is subject to tenant policies.

## Why the alternatives were rejected

- **A (self-managed PostgreSQL)** — rejected because it hands back exactly the operational
  load the managed plane exists to absorb, concentrated on one operator: version upgrades,
  failover rehearsal, and backup verification that is only real if someone restores from
  it regularly. Its cost advantage is real and small in absolute terms; the operational
  exposure is neither.
- **C (PostgreSQL-compatible cluster service)** — rejected as premature. The workload is a
  small transactional schema with a modest write rate; the shared-storage architecture
  solves scaling problems this workload does not have, at a higher floor and with
  per-request I/O billing on the standard configuration. It remains the natural upgrade
  path if the write rate ever justifies it.
- **D (third-party PostgreSQL platform)** — rejected for the primary store. Row-level
  security behaviour, the superuser model and the available role attributes vary by
  vendor and would each have to be re-verified against the isolation property above.
  There is no reason to take vendor-specific risk on the one boundary that must not fail.
