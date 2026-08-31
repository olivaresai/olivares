# ADR-0019: Cedar as a positive, scoped grant engine (not a deny-only overlay)

- **Status:** accepted
- **Date:** 2026-06-15
- **Deciders:** Fran Olivares
- **References:** ADR-0013 (PDP — Cedar + OPA)

## Context and problem statement

ADR-0013 put Cedar behind the `auth.PolicyEvaluator` seam as a **deny-only overlay**:
an implicit base `permit(principal, action, resource)` was compiled ahead of the
operator's policy so a Cedar decision could only ever be a `forbid` (a restriction).
Authorization was therefore **flat at the tenant level** — the built-in RBAC granted a
permission across the whole tenant, and policy could only narrow it. There was no way
to express a **positive, scoped grant**: "this admin may manage agents only in workspace
X", "viewers may read resources only under folder Y", "this role may write to the
`payments` agent-group". The scoping plane (workspace → agent-group → agent →
resource/folder) modelled the tree, but nothing *enforced* grants along it; the
access-map merely *observed* (`AccessEdge.Permitted` = "not known to be permitted").

## Decision drivers

- Express positive grants scoped to the tree (workspace, agent-group, resource
  subtree) and to conditions (model, sensitivity, AAL) — enforced on the real path.
- Keep the deny-overlay and default-deny guarantees ADR-0013 established (forbid still
  overrides; a missing grant still denies).
- Do not reimplement hierarchy/membership resolution by hand — use the formally
  verified engine that models it natively.
- Back-compat: a deployment with no authored grants must decide exactly as before, and
  pay nothing on the hot path.

## Decision outcome

**Elevate the embedded Cedar engine from a deny-only overlay to a three-valued,
scope-aware grant engine, behind a NEW `auth.ScopedAuthorizer` seam beside (not inside)
the deny-overlay.**

1. **Three-valued decision, no base-permit hack.** cedar-go's `Authorize` is
   deny-by-default and forbid-overrides-permit, and its `Diagnostic.Reasons` names the
   determining policies. That recovers the effect the Authorizer needs from a single
   evaluation: `Allow` → **Grant**; `Deny` with reasons → **Forbid**; `Deny` with no
   reasons → **Abstain** (default-deny). The base permit of ADR-0013 is removed — a
   `permit` now genuinely grants, a `forbid` still restricts, and an empty/irrelevant
   policy abstains so the RBAC decision stands (the back-compat invariant).

2. **The Authorizer algebra becomes** `Allow = (RBAC ∨ Grant) ∧ ¬Forbid ∧ ¬deny-overlay`.
   The scoped engine runs first (a forbid short-circuits, overriding RBAC and any
   grant); the base is the tenant-wide RBAC grant OR a positive scoped grant; the
   native ABAC engine + any external PDP then narrow it (defense in depth). Default-deny
   and fail-closed are preserved; a nil scoped engine reduces the Authorizer to its
   ADR-0013 behaviour.

3. **The grant authority IS the per-tenant authored Cedar policy** (the policy authoring
   surface), now compiled in grant mode. The engine resolves the lineage of the
   request's *true* resource (read from the store by entity id — uncheatable) into a
   Cedar entity graph whose `Parents` encode containment, so Cedar's transitive `in`
   does the hierarchy walk. **We did not add a separate structured grant-row store**:
   structured/console grant authoring that *projects to* Cedar is the structured-authoring
   layer's concern; the scoped engine consumes the policy and enforces it.

4. **Grants are per-tenant only; the global env Cedar and OPA stay deny-only.** A
   too-broad global *forbid* only denies (safe); a too-broad global *permit* would grant
   cross-tenant (unsafe). That asymmetry is decisive: positive grants live in the
   per-tenant authored policy (which the engine keys by tenant), while the deployment-
   wide env Cedar (`OLIVARES_PDP_*`) and OPA remain restrict-only overlays.

### Consequences

- **Good:** enterprise-grade scoped authorization (workspace/agent-group/folder/model/
  sensitivity/AAL) enforced at the REST + gRPC chokepoint; the verified engine resolves
  hierarchy/membership; back-compat and default-deny intact; the hot path pays nothing
  until a tenant opts into grants (the engine abstains before any store read).
- **Bad / trade-offs:** a grant-enabled tenant pays a small, gated store read to resolve
  an entity's scope on entity-level requests (a per-tenant scope cache is the documented
  follow-up); scope-tree conditions only resolve against the live hierarchy in the
  activated engine, not in the authoring dry-run.
- **Behaviour change (documented):** an operator `permit` rule that the ADR-0013 overlay
  silently neutralised now GRANTS. Forbid-only authored policies are unaffected.

## Why the alternatives were rejected

- **A separate structured grant-row schema in the scoped engine** — duplicates Cedar's own policy
  model and hierarchy resolution; the verified engine already expresses grants as
  policies over an entity graph. Structured authoring belongs in the structured-authoring layer,
  projecting to the Cedar the engine already consumes.
- **One Cedar policy generated per grant** — does not scale (policy-set growth, churn on
  every grant edit); templated policies over a resolved entity graph let one rule cover a
  whole workspace/group/subtree.
- **Making the global env Cedar grant-capable** — a forgotten tenant guard on a global
  permit grants cross-tenant. Grants are confined to the per-tenant policy.
