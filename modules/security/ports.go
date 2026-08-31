// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// This file defines the integration SEAMS the security module depends on but does
// not own, each expressed in the module's own terms so the module stays decoupled
// from its neighbors' packages. The composition root injects real adapters; until
// it exists (the honest Fase C caveat), each seam defaults to a SAFE behavior:
// the guardrail runs only its own explainable rules (no external classifier), and
// enabling enforcement is permitted but flagged ungoverned (the safe state is
// still DETECTIVE — enforcement off).

// ----------------------------------------------------------------------------
// Detector — a single guardrail check (in-process, deterministic, explainable).
// ----------------------------------------------------------------------------

// Detector inspects a guardrail input and returns zero or more detections. Each
// detector is one explainable, reproducible rule family (PII/secrets, prompt
// injection, jailbreak, content, output validation, OWASP Agentic) — the right
// default for findings that must hold up in forensics/compliance (docs/SECURITY-HARDENING.md). A
// detector NEVER returns raw payload: a Detection carries a short redacted excerpt
// and a hash only (docs/SECURITY-HARDENING.md).
type Detector interface {
	// Class is the stable detector class label (e.g. "pii", "prompt_injection").
	Class() string
	// Inspect returns the detections found in the input (empty if clean). It must
	// be pure and side-effect free.
	Inspect(in GuardrailInput) []Detection
}

// ----------------------------------------------------------------------------
// Classifier — the OPTIONAL external guardrail-LLM / hosted-classifier seam.
// ----------------------------------------------------------------------------

// Classifier is an optional, pluggable second opinion behind the deterministic
// detectors (a guardrail-LLM, a hosted moderation model). The module's own rules
// always run first and are authoritative for reproducibility; a classifier only
// ADDS detections, it cannot suppress one. An error from the classifier is logged
// and ignored (the deterministic detections still stand) — it never fails the
// inspection (read-first: a guardrail dependency must not break the caller).
type Classifier interface {
	// Classify returns additional detections for the input, or an error.
	Classify(ctx context.Context, in GuardrailInput) ([]Detection, error)
}

// ----------------------------------------------------------------------------
// ApprovalGate — the HITL governance seam for ENABLING inline enforcement.
// ----------------------------------------------------------------------------

// ApprovalRequest asks the governance gate to authorize a privileged security
// posture change. The only privileged, production-affecting posture change in this
// module is ENABLING inline enforcement (turning a detective guardrail into one
// that can block) — so it is the only thing gated (docs/SECURITY-HARDENING.md).
type ApprovalRequest struct {
	// Action is the verb being authorized (e.g. "security.enforcement.enable").
	Action string
	// SubjectKind/SubjectRef name what the change targets (a guardrail class).
	SubjectKind string
	SubjectRef  string
	// Reason is the operator's short, non-sensitive justification.
	Reason string
	// Actor is the requesting principal (audit-actor string), for provenance.
	Actor string
}

// ApprovalDecision is the gate's answer. Governed reports whether a real
// governance authority decided it: an UNGOVERNED approval is recorded and
// surfaced so an operator knows the enforcement was enabled without a human gate.
type ApprovalDecision struct {
	// Approved is whether the action may proceed.
	Approved bool
	// Governed is whether a real governance authority decided it.
	Governed bool
	// Status is a short, non-sensitive explanation (e.g. "approved",
	// "rejected", "ungoverned", "pending").
	Status string
}

// ApprovalGate resolves whether a privileged enforcement-posture change is
// authorized. The real adapter bridges to (governance approvals / HITL); this
// module only ASKS, it does not decide policy.
type ApprovalGate interface {
	// Authorize returns the decision for the request. An error is treated by the
	// caller as a denial of the posture change (fail closed for enabling
	// enforcement — the safe state is detective/off).
	Authorize(ctx context.Context, tenant model.TenantID, req ApprovalRequest) (ApprovalDecision, error)
}

// ungovernedGate is the default gate until is wired. It APPROVES the posture
// change (an authenticated admin already passed the engine's authz for
// security:enforcement:admin) but marks it UNGOVERNED, so the change is auditable
// and visibly lacking a human gate. It never errors (it makes no external call).
// The safe default remains detective: enforcement is off until an admin explicitly
// enables it, and even then a guardrail trip only blocks when the persisted
// enforcement policy says so.
type ungovernedGate struct{}

func (ungovernedGate) Authorize(_ context.Context, _ model.TenantID, _ ApprovalRequest) (ApprovalDecision, error) {
	return ApprovalDecision{Approved: true, Governed: false, Status: "ungoverned"}, nil
}
