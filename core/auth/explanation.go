// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"fmt"
)

// DecisionNode is a policy scope or filter node in the scope-tree vocabulary.
type DecisionNode struct {
	Kind string
	Ref  string
}

// DecisionSubject is the user, role or directory group a determining policy matched.
type DecisionSubject struct {
	Kind string
	Ref  string
}

// DecisionGrant identifies one determining permit. GrantID is present only for a
// stored scoped grant; PolicyID identifies a free-form Cedar permit.
type DecisionGrant struct {
	GrantID  string
	PolicyID string
	Node     DecisionNode
	Subject  DecisionSubject
}

// DecisionRemoval identifies an inheritance filter or forbid that removed a right.
type DecisionRemoval struct {
	Kind string
	ID   string
	Node DecisionNode
}

// DecisionExplanation is the provenance of one decision. Empty slices mean the
// decision came from built-in RBAC or its producer has no attribution to report.
type DecisionExplanation struct {
	Because   []DecisionGrant
	RemovedBy []DecisionRemoval
}

// ExplainedDecision pairs the ordinary decision with its provenance.
type ExplainedDecision struct {
	Decision    Decision
	Explanation DecisionExplanation
}

// ScopedExplanationAuthorizer is an optional companion to ScopedAuthorizer. It
// evaluates the same request once and returns determining grants and removals;
// a scoped engine that does not implement it produces no explanation.
type ScopedExplanationAuthorizer interface {
	ScopedExplanation(context.Context, Request) (ScopedDecision, DecisionExplanation, error)
}

// AuthorizeExplanation is Authorize with provenance from the same evaluation.
// It never calls the engine a second time to reconstruct why it decided.
func (az *Authorizer) AuthorizeExplanation(ctx context.Context, req Request) (decision Decision, explanation DecisionExplanation) {
	ctx, capture := beginAuthorizationCapture(ctx, req)
	at := az.clock()
	defer func() {
		finishDecisionCapture(ctx, req, capture, at, decision)
	}()
	decision = az.authorizeWithExplanation(ctx, req, &explanation)
	return decision, explanation
}

// AuthorizeBatchExplanation is the sequential batch form of AuthorizeExplanation.
func (az *Authorizer) AuthorizeBatchExplanation(ctx context.Context, reqs []Request) ([]ExplainedDecision, error) {
	out := make([]ExplainedDecision, 0, len(reqs))
	for _, req := range reqs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		decision, explanation := az.AuthorizeExplanation(ctx, req)
		out = append(out, ExplainedDecision{Decision: decision, Explanation: explanation})
	}
	return out, nil
}

// scopedExplanation uses the opt-in explanation seam when present and otherwise
// preserves the legacy decision with empty provenance.
func (az *Authorizer) scopedExplanation(ctx context.Context, req Request) (sd ScopedDecision, explanation DecisionExplanation, err error) {
	producer, ok := az.scoped.(ScopedExplanationAuthorizer)
	if !ok {
		sd, err = az.scopedSafe(ctx, req)
		return sd, explanation, err
	}
	requireAuthorizationInputs(ctx, true)
	defer func() {
		if err != nil {
			IncompleteAuthorizationInputs(ctx)
		}
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("auth: scoped explanation authorizer panicked: %v", recovered)
		}
	}()
	sd, explanation, err = producer.ScopedExplanation(ctx, req)
	return sd, explanation, err
}
