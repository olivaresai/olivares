// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// ScopeGate is the source-scoping pre-flight on the EXECUTE path: it answers
// "may the acting session/principal USE this model?" against the model→workspace/
// agent-group binding, decided by the grant engine + containment (model B). It
// is a peer of BudgetGate/StopGate and, like StopGate (and unlike the fail-OPEN
// budget gate), it is DENY-CLOSED: an unreadable scope/grant state must never mean
// "allow this model". The composition root backs it with the sourcescope resolver
// (cmd/olivares); the unwired default allows everything (no scoping configured).
//
// It runs at EXECUTE, not /resolve: /resolve is a read-only capability preview with
// no acting session, while EXECUTE is the point the agent actually resolves a model
// to use it — the binding's enforcement point ("the point where the agent resolves a
// model" goal). The gate filters the resolved chain so a model the session is
// out of scope for is never served, nor tried as a fallback.
type ScopeGate interface {
	// Allowed reports whether q.Principal/q.SessionRef may use the model. An error
	// is a DENY (fail closed). An unbound model (no binding) is allowed.
	Allowed(ctx context.Context, tenant model.TenantID, q ScopeQuery) (ScopeVerdict, error)
}

// ScopeQuery identifies the actor and the model a scope check is about. SessionRef is
// the execute request's session reference (the unforgeable actor scope is read from
// the stored session server-side); it may be empty (then a bound model is deny-closed
// unless the principal has a grant or tenant RBAC).
type ScopeQuery struct {
	Principal   auth.Principal
	SessionRef  string
	ProviderRef string
	ModelRef    string
}

// ScopeVerdict is the gate's decision for one model.
type ScopeVerdict struct {
	// Allowed reports whether the model may be used.
	Allowed bool
	// Reason is a short, non-sensitive explanation for the audit log.
	Reason string
}

// allowScopeGate is the unwired default: with no source-scoping resolver wired no
// model is ever out of scope (a control plane that has not configured scoping behaves
// exactly as before — back-compat). The composition root replaces it with the
// resolver-backed gate.
type allowScopeGate struct{}

func (allowScopeGate) Allowed(context.Context, model.TenantID, ScopeQuery) (ScopeVerdict, error) {
	return ScopeVerdict{Allowed: true, Reason: "source scoping not configured"}, nil
}

// WithScopeGate wires the source-scope pre-flight on the execute path. Without it
// the module behaves as before (no model is out of scope; see allowScopeGate).
func WithScopeGate(g ScopeGate) Option { return func(m *Module) { m.scopeGate = g } }
