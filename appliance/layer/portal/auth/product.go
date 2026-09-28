// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import "context"

// Answer is what the product's authorizer answered about one act. It is the whole of what
// this console knows about that act: the console records it and keeps nothing, so the next
// act is a new question.
type Answer int

const (
	// AnswerUndecided is the zero value, and it is a refusal: nothing was decided, which
	// on this console is never an allowance. It is also the answer when the product client
	// plane is not wired, because absent wiring must name what is missing and never fall
	// back to a decision of the console's own.
	AnswerUndecided Answer = iota
	// AnswerAllowed means the product authorized the act.
	AnswerAllowed
	// AnswerStepUpRequired means the product wants this operator to prove who they are
	// again before it decides. It is not a denial and its remedy is different: the console
	// routes to the product's own step-up and asks again afterwards.
	AnswerStepUpRequired
	// AnswerRefused means the product denied the act.
	AnswerRefused
)

// Allows reports whether the act may proceed. Only an allowance does; every other answer,
// the zero value included, does not.
func (a Answer) Allows() bool { return a == AnswerAllowed }

// String names the answer for a page and for the audit record.
func (a Answer) String() string {
	switch a {
	case AnswerAllowed:
		return "allowed"
	case AnswerStepUpRequired:
		return "step-up-required"
	case AnswerRefused:
		return "refused"
	}
	return "undecided"
}

// Authorizer is the product's existing authorizer, reached through the product client
// plane. While the product answers it is the only decision path there is.
//
// In the product's own core the question lands on the route authorizer, which checks the
// assurance an act demands before it evaluates any policy and answers "step up first"
// separately from "you may not" — two answers with two different remedies. This console
// reproduces none of that. It asks, once, and obeys, which is why the four answers below
// are exactly the four that entry can give.
type Authorizer interface {
	// AuthorizeVerb asks whether the signed-in identity may perform v on this host now.
	AuthorizeVerb(ctx context.Context, identity string, v Verb) Answer
}

// ProductIdentity is the product-identity adapter: this console's side of the one decision
// path that exists while the product answers.
//
// It holds no policy, no role, no grant and no cache, and it has no state to read. Ask is
// one call to the product's authorizer and returns exactly what the product answered: it
// neither allows an act the product refused nor refuses one the product allowed, and it
// remembers no answer, so a second act is a second question. That is the whole of "no
// second decision path".
type ProductIdentity struct {
	authorizer Authorizer
}

// NewProductIdentity returns the adapter over the product's authorizer. A nil authorizer
// is absent wiring, which refuses and says so rather than deciding anything here.
func NewProductIdentity(authorizer Authorizer) *ProductIdentity {
	return &ProductIdentity{authorizer: authorizer}
}

// Ask asks the product's authorizer, once, whether identity may perform v now, and returns
// its answer unchanged. Which acts always demand a step-up is Verb.RequiresStepUp; whether
// this operator has satisfied one is the product's answer to give, never this console's.
func (p *ProductIdentity) Ask(ctx context.Context, identity string, v Verb) Answer {
	if p == nil || p.authorizer == nil {
		return AnswerUndecided
	}
	return p.authorizer.AuthorizeVerb(ctx, identity, v)
}
