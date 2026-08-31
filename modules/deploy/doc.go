// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package deploy is module VII — deployment & integration: the ONLY module that
// ACTS on the customer's infrastructure (the rest of the product is read-first,
// docs/SECURITY-HARDENING.md). It provisions/updates/retires agents and MCP servers as
// declarative, versioned, reversible operations, and wires them to the
// enterprise estate (network/LAN, servers, DBs, stores, APIs) — declaring the
// connectivity and the referenced (never cleartext) credentials/identity an
// agent uses to reach a resource.
//
// Because it mutates, the security bar is the highest in the product (alongside
//). Every operation that mutates infrastructure is governed: it passes
// through the human-in-the-loop (HITL) approval gate of (deny-by-default
// when no approval / an expired approval is present), runs plan/dry-run before
// apply, acts with least privilege and no default credentials (docs/SECURITY-HARDENING.md,§4),
// references secrets out of a secret-store and never persists them in cleartext
// (docs/SECURITY-HARDENING.md), and is recorded — what was deployed/wired/retired, at which
// version, who approved it, and the result — to the append-only/hash-chain
// ledger (docs/SECURITY-HARDENING.md) and to an append-only operation log this module owns.
//
// # Bounded context (what this module is, and is not)
//
// This module ORCHESTRATES; it does not re-implement its neighbors:
//
//   - (connectors/runtime,cloudflare,aws) DISCOVERS where the AI runs
//     (read-only inventory). This module consumes that inventory's targets but
//     Never deploys — the action is here.
//   - owns the engine's authz/RBAC, the ledger and the manage-as-code /
//     Terraform provider base; this module extends/consumes them, it does not
//     re-implement the provider or the enforcer.
//   - (governance) owns the HITL decision and the agent↔identity binding;
//     this module REQUESTS the gate and consumes its result, and emits the
//     per-agent identity through contract — it never decides approvals nor
//     re-implements the binding logic.
//   - (access-map) is the sole writer of AccessEdge (decision A). This module
//     never writes AccessEdge: it DECLARES the permitted wiring (agent→resource)
//     and publishes it as a policy-grant EdgeObservation (SignalPolicy) on the
//     bus, which reconciles into the PERMITTED side of the
//     permitted-vs-observed diff. What PERMITTED declares here is exactly what
//     Contrasts against what it OBSERVES.
//   - (catalog) owns the approved definition of MCPs/skills/templates; this
//     module deploys what the catalog approves.
//   - owns the simulation/test sandbox; here a sandbox/microVM is only a
//     governed deployment TARGET, never the test environment.
//
// # Seams wired at the composition root (honest Fase C caveat)
//
// Like every Fase C module, this one is built and tested against the
// engine seams; the real boot fan-out that registers modules into the binary
// does not exist yet. Three integration ports are therefore injected by the
// composition root and default to a SAFE, deny-closed behavior so an un-wired
// deployment can never silently mutate infrastructure:
//
//   - ApprovalGate: unset ⇒ every governed mutation is DENIED (deny by
//     default). Tests inject a fake gate to exercise approved/pending/expired.
//   - Executor (runtimes / IaC): unset ⇒ plan/apply/verify/retire FAIL
//     CLOSED ("no runtime executor configured"). The control plane can still
//     declare desired state (definitions/revisions/wirings); it cannot reconcile
//     to real infrastructure until an executor is wired. Tests inject a mock.
//   - IdentityBinder (binding): unset ⇒ a wiring's attribution is reported
//     as DEGRADED (marked, never faked); a wired binder yields FIRM attribution
//     and closes the per-agent-identity bridge needs.
//
// Start() warns once when a port is unset, so a silently-broken deployment is
// visible — declared, never a silent no-op.
//
// Update 2026-06-07 (Fase K): the "real boot fan-out that registers
// modules into the binary" now EXISTS (cmd/olivares/wire.go, buildModules), and
// all three ports have real adapters wired at the composition root — the ApprovalGate
// via the OUTBOUND HITL bridge, and the Executor (the real core/runtime/executor)
// plus the firm IdentityBinder via (env OLIVARES_DEPLOY_EXECUTOR_CONFIG).
// IMPORTANT — the per-port deny-closed semantics above remain LITERALLY TRUE: with no
// executor provisioned the module keeps unwiredExecutor and plan/apply/verify/retire
// still return 503; the HITL bridge being wired does NOT actuate infrastructure on its
// own. The Executor therefore remains a genuine, operator-gated seam. Kept as design
// history; the deny-closed body is current, not stale.
package deploy
