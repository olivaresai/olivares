---
title: "Module VI — identity, permissions & governance"
description: >-
  The control plane over the authorization model: identity roster reconciliation,
  the agent↔identity bridge, the deny-only ABAC engine, and the human-in-the-loop
  approval gate with an append-only decision trail. The root of governed actuation.
---

Module VI is the **governance plane over the engine's existing authorization model** —
it does **not** re-implement the enforcer or the identity connectors, it consumes them.
It binds five subsystems behind one bounded context (identity and its governance): a
directory **roster** reconciler, the **agent↔identity bridge** that makes attribution
firm, a deny-only **ABAC** engine, the **human-in-the-loop** approval gate, and
policy/identity **authoring backends**. This is the root of every *governed* action in
the product.

## What it is

The module sits on the Management layer and is the **decision** authority for the
control plane: who and what may do what, and which actions require a human first. Its
contract is the deny-only, deny-by-default posture made enforceable —

- **Roster reconciliation** converges a connected directory (the identity sources)
  into the engine's canonical `Identity` entities plus the module-owned
  collection/membership graph, find-or-create keyed on external id alone so it
  **upgrades the same row** the access map creates from an audit reference. That
  single-row convergence is what makes firm attribution possible.
- **The agent↔identity bridge** binds an agent to the internal id of the canonical
  non-human identity its credential presents, resolving the hard dependency that lets
  Module III (the access map) cancel false permitted-vs-observed drift.
- **The ABAC engine** is a native evaluator that runs **after** RBAC and can only
  *further-restrict* — it never widens a grant.

## Its contract & entities

Module VI owns four entities in the shared data model — a **collection** and a
**collection-member** edge (the source-derived group/role graph, resolved transitively
within bounds), an **approval** (a mutable HITL request), and an **append-only
approval-decision** trail. Identities are **not** duplicated into a module table; they
are reconciled into the engine's canonical `Identity` entity.

The **ABAC evaluator** implements the engine's policy-evaluator seam with verified
properties: every rule is a **deny** rule; it runs after RBAC inside an AND, so a policy
can never expand access; a malformed *enabled* policy **fails closed** (denies); the
authorization hot path is served from a per-tenant cache invalidated **after** a write
commits, isolated strictly by tenant. Policy specs are **typed and re-marshaled** on
write (operator JSON is never round-tripped verbatim), so a credential cannot enter a
spec. OPA/Rego is the external-evaluator seam, never a dependency dragged into the
engine.

The **approval gate** is the action→human traceability the audit ledger anchors:
separation-of-duty and the duplicate-decider guard key on the **stable user identity**
(a system token cannot decide), the multi-approval threshold is **race-safe** on the
store (a concurrent crossing resolves to exactly one winner), and expiry is derived
lazily at read then materialized by an explicit, tenant-scoped sweep. The authoring
backends (managed-settings/hooks, Cedar/OPA policy-as-code, the WIF object graph) add a
**publish→immutable-revision→drift** write path; for Cedar a published policy is
activated on the live per-tenant deny-only overlay and re-loaded at boot, so an
`active` claim survives a restart.

## What it consumes & produces

The module **consumes** the engine's authorization and audit base and the typed
identity roster from the configured directory sources; it fills the `Agent.IdentityID`
field the access map depends on. It **produces** `FindingReport` events on the
[event bus](/reference/events/) — a **shared identity** bound to more than one agent,
plus approval **escalation** and **expiry** — each emitted once, gated on a persisted
marker so a repeated sweep cannot double-emit. Every privileged mutation, and the
recon-relevant identity and binding reads, **self-audit to the real principal** inside a
committed transaction; the audit actor is always a typed principal reference, never an
email.

:::caution[Honest limits]
- **The ABAC engine is authored and audited, but enforcement depends on composition.**
  Governance state is written and audited today; the boot composition root wires the
  evaluator and injects the directory providers. Where those are unwired, the engine is
  not in force and a roster sync has no providers — this is **stated, never a silent
  no-op**.
- **Firm attribution requires identity-per-agent.** A binding ties an agent to a
  *canonical* identity, never to a freshly minted one used to fake reconciliation of a
  shared entity. An identity bound to more than one agent **collapses attribution** to
  the identity level — surfaced honestly as a finding, never recovered.
- **The deny-only grammar is bounded by design.** v1 rules match only the attributes
  that actually reach the evaluator; resource-attribute rules (e.g. sensitivity) need a
  core seam and are a documented follow-up — **not shipped as inert syntax**, an unknown
  field is rejected on write. Policy *restricts*; additive grants stay in RBAC.
- **A module cannot enumerate tenants.** Approval expiry/escalation is materialized by an
  **explicit, tenant-scoped sweep** — there is no background cross-tenant guarantee,
  because asserting one would be a lie. Effective expiry is still honored lazily at read.
:::

## Related

- [Modules catalog](/reference/modules/overview/) — where Module VI sits and its honest actuation status.
- [Access & resource map (III)](/reference/modules/iii-access-map/) — the consumer whose attribution dependency this module resolves.
- [Event bus reference](/reference/events/) — the `finding.reported` event this module emits.
- [Govern and approve](/how-to/govern-and-approve/) — using the policy and approval surfaces.
- [Architecture overview](/explanation/architecture/overview/) — the engine and layers this module composes onto.
- [Honesty & limits](/start/honesty-and-limits/) — the deny-closed, detective-by-default posture.
