// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package executor is the real, pluggable, deny-closed backend of the deploy
// module's Executor seam (modules/deploy/ports.go) — the ONLY place in the
// product that MUTATES customer infrastructure.
//
// WHY THE MUTATION LIVES HERE (the AGPL engine), NOT IN A CONNECTOR.
// ARCHITECTURE.md and docs/SECURITY-HARDENING.md draw a hard line: the read-first connectors
// (connectors/runtime, connectors/{argocd,flux,crossplane}) DISCOVER — they are
// Apache-2.0, import only /sdk, and never write. ACTING is the exception to the
// read-first norm (docs/SECURITY-HARDENING.md), so it is the crown-jewel value of the AGPL motor
// and lives behind the AGPL boundary. A connector is never converted into an
// actuator (scripts/check-boundary.sh stays green: a connector never imports
// /core; the motor may reuse a connector's read mechanics, never the reverse).
//
// LAYERING. This package is self-contained: it imports ONLY the standard library —
// never core/model, never modules/deploy (which would invert the /core → /modules
// layering) and never /connectors. It speaks NEUTRAL terms (Desired, Diff,
// RealState). The thin seam adapter that satisfies deploy.Executor by translating
// the module's typed deploySpec into a Desired (and a Diff back into
// []deploy.Change) lives in the composition root (cmd/olivares), the only
// layer that imports both the module and this package — mirroring how the
// approval bridge and the notify dispatcher bridge module ports to their
// backends there.
//
// THE 2026 BAR (the session brief). Every backend offers the four
// operations a CTO / security team / platform SRE expects of governed actuation:
//
//   - plan(desired) -> Diff    a dry-run diff (create/update/delete) classified by
//     BlastRadius and Reversible, with a saved-plan handle
//     — nothing is applied blind.
//   - apply(plan)   -> Result  idempotent reconciliation of the SAVED plan.
//   - rollback(plan)-> Result  the reverse of an apply (reversibility).
//   - observe(unit) -> RealState  reads REAL infrastructure (drift), never mutates;
//     an unobservable unit SAYS SO (a gap is a signal).
//
// mapped onto the four deploy.Executor methods by the seam adapter:
// Plan→plan, Apply→plan+gate+apply, Verify→observe+diff, Retire→destroy (gated).
//
// SECURITY POSTURE (docs/SECURITY-HARDENING.md,§2,§3,§4,§5). Deny-closed at every new seam:
//   - no Backend for a runtime          => ErrNoBackend (the operation fails, never
//     a pretend success).
//   - no CredentialSource wired         => every actuation fails closed; NEVER a
//     default/long-lived key (credential.go).
//   - destructive change over threshold => the blast-radius gate blocks it without
//     an explicit allowance (blastradius.go) —
//     a SECOND control on top of the HITL
//     gate the module already enforces.
//   - minimal data                      => a Diff/Result carries only a kind, a
//     non-sensitive ref and a short detail —
//     never a payload, a command line, or a
//     secret. The short-lived credential is
//     used in the call and discarded; its
//     non-sensitive ID may be audited, the
//     material never is.
//
// Backends are selected by runtime/policy, never hardcoded (executor.go Registry).
// The defaults the brief fixes: OpenTofu is the OSS
// default of the declarative column (Terraform is the same backend, binary flag);
// GitOps is the safest K8s default (the motor holds no cluster credentials);
// Crossplane is interop (apply an XR + read status, never build a control plane).
package executor
