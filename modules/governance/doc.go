// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package governance is module VI — identity, permissions and governance
// (README.md VI, ARCHITECTURE.md "AuthN/Z + multi-tenancy"). It is the control plane
// over the core authorization model: it does NOT re-implement the engine's
// enforcer (core/auth) nor the identity connectors; it consumes them.
//
// It builds five subsystems behind one namespace (one bounded context — identity
// and its governance — that the Agent↔Identity bridge ties together):
//
//	A. Identity roster reconciliation (roster.go). Reconciles an
//	   identitysource.Graph (AD/LDAP, Okta/Entra, Vault, Infisical, SPIFFE)
//	   into core Identity entities plus the module-owned collection/membership
//	   graph. Find-or-create on core Identity is keyed on external_id ALONE and
//	   UPGRADES in place, so it converges on the SAME row the access-map bridge
// creates from a raw audit reference — that single-row convergence is
//	   what makes firm attribution possible (ARCHITECTURE.md). Minimal data
//	   (docs/SECURITY-HARDENING.md): PII-bearing directory attributes (email/upn/mail) are dropped,
//	   never persisted into Identity.Metadata.
//
//	B. Agent↔identity binding (identity.go) — the NHI bridge that resolves
//	   hard dependency. Binds an agent to the INTERNAL id of the canonical NHI
//	   Identity its credential presents (Agent.IdentityID), so access-map's
//	   reconcileDrift can cancel the false permitted-vs-observed drift. Minting a
//	   fresh per-agent identity is offered for genuinely-new NHIs; it never
//	   pretends to reconcile an agent that runs as a pre-existing shared entity. An
//	   identity bound to more than one agent is surfaced as a governance finding —
//	   attribution collapses to the identity level, honestly, never faked.
//
//	C. Policy authoring + the ABAC engine (policy.go, evaluator.go). CRUD over the
//	   core Policy entity (kinds "abac" and "approval") with a TYPED, bounded,
//	   re-marshaled spec (no operator JSON is round-tripped verbatim, so a secret
//	   cannot enter a policy spec). The native Evaluator implements the core
//	   auth.PolicyEvaluator seam (docs/contracts): it runs AFTER RBAC and can
//	   only FURTHER-RESTRICT (deny when a deny-rule matches, else the RBAC decision
//	   stands), fails closed (a malformed enabled policy denies), and serves the
//	   authorization hot path from a per-tenant cache invalidated after a write
//	   commits. v1 deny-rules match only the attributes that actually reach the
//	   evaluator today (principal kind, permission/verb/resource); resource-attribute
//	   rules (sensitivity) need a core resource-attrs seam and are a documented
//	   follow-up, not shipped as inert syntax. OPA/Rego is the external-evaluator
//	   seam, never a dependency dragged into the engine.
//
//	D. Human-in-the-loop / approvals (approvals.go, sweep.go). An approval request
//	   (mutable lifecycle) plus an APPEND-ONLY decision trail = the action→human
//	   traceability the ledger anchors. Separation-of-duty and the duplicate-decider
//	   guard key on the stable Principal.UserID (not the audit-actor string, which a
//	   single human can vary across credentials). The multi-approval threshold is
//	   race-safe on Postgres: every decision re-counts within its transaction and
//	   commits under a version-checked update of the approval row, so a concurrent
//	   threshold crossing resolves to exactly one winner. Expiry is derived lazily at
//	   read and materialized by an explicit, tenant-scoped sweep (a module cannot
//	   enumerate tenants, so a background cross-tenant guarantee would be a lie);
//	   escalation/expiry emit a FindingReport once, gated on a persisted marker so a
//	   repeated sweep cannot double-emit (the finops alert pattern).
//
//	E. Everything privileged self-audits to the REAL principal (helpers.go,
//	   per-handler), and the recon-relevant identity/binding reads self-audit on
//	   read inside a committed transaction, exactly as the access-map graph reads do
//	   (docs/SECURITY-HARDENING.md, §4).
//
// Composition (honest, pending — the same Fase-C decision that applies to every
// module): the engine boot must (a) register this module so its routes/schema
// mount, (b) wire auth.NewAuthorizer(gov.Evaluator()) so the ABAC engine is
// enforced, and (c) inject the identity GraphProviders via UseRosterProviders. The
// module is built and tested against these seams with a hand-wired fake provider;
// the real boot fan-out does not exist yet. Until wired, governance state is authored and audited but the ABAC engine
// is not in force and /roster/sync has no providers — stated, never silently no-op.
//
// Update 2026-06-07 (Fase C/K composition root): the boot fan-out now EXISTS and all
// three requirements above are satisfied — (a) the module is registered in
// cmd/olivares/wire.go (buildModules); (b) cmd/olivares/boot.go wires
// auth.NewAuthorizer(gov.Evaluator()) and passes it to api.New, so the ABAC engine
// IS in force (also re-activates each tenant's stored Cedar/OPA PDP at boot via
// ReloadActivePDP); (c) the roster GraphProviders (incl. the WIF graph) are injected
// at wire time. The paragraph above is kept as design history.
//
// Layout: governance.go (lifecycle, bus, routes, the providers seam, Evaluator
// accessor) · schema.go (the four owned entities) · roster.go (subsystem A) ·
// identity.go (subsystem B) · policy.go + evaluator.go (subsystem C) · approvals.go
// + sweep.go (subsystem D) · helpers.go (shared HTTP/audit helpers, subsystem E).
package governance
