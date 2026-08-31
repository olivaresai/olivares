---
title: Module VII — deployment & integration
description: "The one module that acts on your infrastructure: it plans and
  governs the declarative lifecycle of agents and MCP servers and their wiring
  to the estate. Mutations are HITL-gated, dry-run-before-apply, and reversible
  — and live apply stays deny-closed (503) until an executor is provisioned."
slug: 2026-06/reference/modules/vii-deploy
---

Module VII is the **only** module that mutates customer infrastructure — every other
part of the product is read-first. It provisions, updates and retires agents and MCP
servers as **declarative, versioned, reversible** operations, and declares the
connectivity and referenced identity an agent uses to reach an enterprise resource.
Because it acts, its security bar is the highest in the product, and live actuation is
held behind a deny-closed seam until an operator explicitly provisions it.

## Plan and govern, then (maybe) apply

The lifecycle is `plan → apply → verify → retire`, reconciling a **desired** state
against the **real** one. The split that matters is **declare ≠ mutate**:

* **Declaring** desired state — create, update, rollback a definition (also via the
  manage-as-code `olivares_deployment` resource) — is control-plane only and **never
  touches infrastructure**.
* **`plan`** is a pure dry-run diff; **`verify`** checks drift and refreshes the
  snapshot. Neither mutates.
* **`apply` and `retire`** are the only mutating operations. They are **two-phase** and
  **deny-by-default**: phase one computes the diff and *requests* a human approval bound
  to the plan hash without changing anything; phase two proceeds only if the approval is
  `approved` **and** the plan hash still matches — any other state (pending, expired,
  rejected, no gate, stale plan) is refused and recorded. Re-specifying changes the hash
  and invalidates the approval (anti-TOCTOU).

Mutating apply/retire is **not live by default**. The actuation seam ([`Executor`](/2026-06/reference/modules/overview/))
is deny-closed: with no executor provisioned, apply/retire/plan/verify **fail closed
with a `503`** — the control plane can declare desired state but cannot reconcile to
real infrastructure. A real engine (Tofu/Terraform, GitOps, Kubernetes, Docker, Nomad,
Crossplane) plus a short-lived, per-operation, attested credential source wire in **only
on operator configuration**; absent that, the module never silently acts.

## Entities and the declared contract

The module declares four namespaced entities plus the core `Deployment` as the applied
snapshot:

| Entity | Role |
|---|---|
| **definition** | desired state — desired vs applied version, spec hash, link to the core `Deployment` |
| **revision** | append-only, immutable spec history — the reversible source for rollback |
| **wiring** | the **permitted** connectivity `agent → resource` it declares (the contract module III contrasts) |
| **operation** | append-only change-management ledger — version, plan hash, who approved, result |

The desired spec is **typed and re-serialized from the struct** (never an operator JSON
round-trip): unknown fields are rejected, an inline-credential guard runs, and a spec
carrying credential material in cleartext is **refused at declaration**. Credentials
travel **by reference only** (`<scheme>:<locator>`, allow-listed scheme) — a property of
the wire, never a stored secret.

## What it produces on the bus (the PERMITTED side of module III)

Module VII never writes the access map; module III is the sole writer of its edges. On a
committed `apply`, for each wiring the module publishes a policy-grant
[`edge.observed`](/2026-06/reference/events/) event (`Source = policy`) carrying only references
and the mode. Module III reconciles it into the **PERMITTED** side of its
permitted-vs-observed diff — so what this module declares is exactly what module III
contrasts against what it observes. Identity is bound per agent through governance: a
firm, unique non-human identity yields an `attributed` edge; a shared or absent identity
is reported as `approximate` — **marked, never faked**.

:::caution[Honest limits]
* **Live apply is a deny-closed seam.** With no executor provisioned, `apply`/`retire`
  (and `plan`/`verify`) return a clear `503`. The module plans, governs, versions and
  declares desired state today; it reconciles to real infrastructure only once an
  operator wires an executor — never by default, never a silent no-op.
* **Approval and attribution fail safe too.** Without the approval gate every mutation is
  denied; without the identity binder a wiring's attribution is degraded, not fabricated.
  `Start()` warns once per unwired seam so a broken deployment is visible.
* **Retiring a wiring does not retract its published PERMITTED edge.** The edge model has
  no retraction verb; the wiring is marked revoked and module III reconciles the
  staleness. Declared, not hidden.
* **Backend depth varies.** Across the actuation backends, some observe paths are
  shallower than others (e.g. surface-level health on certain runtimes); these are noted
  as honest gaps, never reported as a fabricated in-sync.
:::

## Related

* [Modules catalog](/2026-06/reference/modules/overview/) — the Govern/Observe vs Actuate split and the `503` seam.
* [Module III — the access map](/2026-06/reference/modules/iii-access-map/) — consumes the PERMITTED wiring this module declares.
* [Event bus reference](/2026-06/reference/events/) — the `edge.observed` event and its minimal-data payload.
* [Govern and approve](/2026-06/how-to/govern-and-approve/) — the HITL approval flow behind every mutation.
* [Honesty & limits](/2026-06/start/honesty-and-limits/) — what actuates today and what does not.
* [Architecture overview](/2026-06/explanation/architecture/overview/) — where module VII sits in the Management layer.
