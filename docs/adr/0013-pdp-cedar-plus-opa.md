# ADR-0013: Authorization PDP — Cedar embedded + OPA-over-HTTP adapter

- **Status:** accepted (restrict-only, scoped to the `auth.PolicyEvaluator` seam this record
  creates) — **amended by ADR-0019 (2026-06-15)**, which removes the base permit: an operator
  permit rule that this overlay silently neutralised now grants, and moved Cedar itself to a
  positive, scoped grant engine in a separate seam. Forbid-only policies are unaffected; the
  "never widen" wording of the Context and the Decision drivers below is superseded in that
  reading — see the amendment note at the end.
- **Date:** 2026-06-04
- **Deciders:** Fran Olivares
- **References:** NHI/MCP-auth contract; amended by ADR-0019 (Cedar scoped grants)

## Context and problem statement

Beyond RBAC, the platform needs a policy decision point (PDP) for attribute-based
authorization. Organizations differ: some want a self-contained engine, others have an
existing OPA estate. The PDP must never *widen* access — only restrict.

## Decision drivers

- Work self-contained (single binary, air-gap) with no external policy service.
- Fit an existing OPA deployment when the operator has one.
- A restrict-only invariant: policy can deny, never grant beyond RBAC.

## Considered options

- **Both:** Cedar embedded (primary, pure-Go) **and** an OPA-over-HTTP adapter behind
  one seam, operator-selected.
- **Cedar only.**
- **OPA only.**

## Decision outcome

Chosen option: **both, behind a single `PolicyEvaluator` seam**. **Cedar** is the
embedded, pure-Go primary PDP; an **OPA-over-HTTP** adapter is available; the operator
selects the engine via `OLIVARES_PDP_ENGINE = cedar | opa | none`. The ABAC seam
**only restricts** (it ANDs with RBAC and never widens). The restrict-only invariant
is tested end-to-end.

### Consequences

- **Good:** self-contained by default (Cedar, no sidecar); fits an OPA estate when
  desired; one seam, two engines.
- **Bad / trade-offs:** two adapters to maintain; the OPA path's transport hardening
  (e.g. mTLS to the sidecar) is a documented extension, not yet complete.
- **Neutral:** `none` disables the ABAC layer, leaving RBAC deny-by-default.

## Why the alternatives were rejected

- **Cedar only** — excludes organizations standardized on OPA.
- **OPA only** — forces an external policy service onto every install, breaking the
  self-contained / air-gap default.

## Amendment (2026-06-15, ADR-0019)

*(The amending decision is dated 2026-06-15; this note was added on 2026-08-17, when a
sweep of the decision register found the two records signed eleven days apart with no
link of precedence between them. Nothing above is rewritten.)*

**What no longer holds as written.** The Context's *"The PDP must never widen access — only
restrict"* and the driver *"policy can deny, never grant beyond RBAC"* read, as written, as
statements about **the whole authorization decision** and about **Cedar**. Since
**ADR-0019** neither is true in that reading: Cedar was
elevated from a deny-only overlay to a three-valued, scope-aware **grant** engine, and the
implicit base `permit` that made a Cedar decision restriction-only was removed, so an
authored `permit` now genuinely grants.

**What is true instead.** The restrict-only invariant survives **scoped to the seam this
record created**: `auth.PolicyEvaluator` still runs after RBAC and may only further-restrict
(`core/auth/authorizer.go:100-104`). The positive grant lives in a **different, new** seam,
`auth.ScopedAuthorizer`, wired **beside** the deny-overlay rather than into it, and the
Authorizer combines them as `Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`
(`core/auth/authorizer.go:157-163`, algebra at `:161` and `:200`). Deny-by-default,
forbid-overrides-permit and fail-closed on error are preserved, and a deployment with no
authored grants decides exactly as it did under this record. Everything else here stands: the
two engines behind one seam and the `OLIVARES_PDP_ENGINE = cedar | opa | none` selector are
the shipped behaviour (`cmd/olivares/wire.go:994-1018`).

**Where the current decision lives.** `docs/adr/0019-cedar-scoped-grants.md` (accepted,
2026-06-15, Fran Olivares), which references this record explicitly. A reader who quotes only
this ADR — the one the term *ABAC* leads to — can conclude that the shipped positive-grant
path violates a signed decision. It does not; it follows ADR-0019.
