// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

const operator = "ada@appliance"

// question is one thing the console asked the product's authorizer.
type question struct {
	identity string
	verb     Verb
}

// countingAuthorizer answers with a fixed answer and records every question, so a case can
// state how many times one act asked the product and with what.
type countingAuthorizer struct {
	answer Answer
	asked  []question
}

func (a *countingAuthorizer) AuthorizeVerb(_ context.Context, identity string, v Verb) Answer {
	a.asked = append(a.asked, question{identity: identity, verb: v})
	return a.answer
}

func TestStepUp_UsesProductAuthorizerAndAddsNoSecondDecisionPath(t *testing.T) {
	ctx := context.Background()

	for _, answer := range []Answer{AnswerAllowed, AnswerStepUpRequired, AnswerRefused, AnswerUndecided} {
		t.Run(answer.String(), func(t *testing.T) {
			az := &countingAuthorizer{answer: answer}
			identity := NewProductIdentity(az)

			got := identity.Ask(ctx, operator, VerbApplyUpdate)
			if got != answer {
				t.Errorf("the console answered %s where the product answered %s", got, answer)
			}
			if got.Allows() != (answer == AnswerAllowed) {
				t.Errorf("%s allows the act: only an allowance does", answer)
			}
			if len(az.asked) != 1 {
				t.Fatalf("one act asked the product %d times, want exactly 1", len(az.asked))
			}
			if az.asked[0] != (question{identity: operator, verb: VerbApplyUpdate}) {
				t.Errorf("the product was asked %+v", az.asked[0])
			}

			// Asking again is a second question. A console that answered the second one
			// from the first would be holding a decision of its own.
			if second := identity.Ask(ctx, operator, VerbApplyUpdate); second != answer {
				t.Errorf("the second ask answered %s", second)
			}
			if len(az.asked) != 2 {
				t.Errorf("the second act asked the product %d times in total, want 2", len(az.asked))
			}
		})
	}

	// The acts that always require a step-up are the dangerous six the design names. The
	// console knows which they are; whether this operator has satisfied one is the
	// product's answer, never the console's.
	for v, want := range map[Verb]bool{
		VerbApplyUpdate:     true,
		VerbRollBack:        true,
		VerbNetwork:         true,
		VerbVPN:             true,
		VerbCertificate:     true,
		VerbPower:           true,
		VerbSupportBundle:   false,
		VerbRepairPortalPAM: false,
		Verb("unknown"):     false,
	} {
		if got := v.RequiresStepUp(); got != want {
			t.Errorf("%q requires a step-up: %t, want %t", v, got, want)
		}
	}

	// Absent wiring refuses and decides nothing itself: no answer at all is not an
	// allowance, and it never falls back to a decision of the console's own.
	for name, identity := range map[string]*ProductIdentity{
		"no authorizer wired": NewProductIdentity(nil),
		"no adapter at all":   nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := identity.Ask(ctx, operator, VerbPower)
			if got != AnswerUndecided || got.Allows() {
				t.Errorf("absent wiring answered %s", got)
			}
		})
	}

	// The whole of the decision is the one call above, and that is read from the source
	// rather than assumed: a second call in Ask would be a second decision path, whatever
	// it asked.
	calls := authorizerCalls(t)
	if calls != 1 {
		t.Errorf("the adapter calls the product's authorizer %d times, want exactly 1", calls)
	}
}

// authorizerCalls counts the calls to the product authorizer's one method in this package's
// non-test sources.
func authorizerCalls(t *testing.T) int {
	t.Helper()
	calls := 0
	for _, file := range packageSources(t, ".") {
		ast.Inspect(file.syntax, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "AuthorizeVerb" {
				calls++
			}
			return true
		})
	}
	return calls
}

// parsed is one parsed non-test source file.
type parsed struct {
	path   string
	syntax *ast.File
}

// parse parses one Go source and fails the case when it cannot.
func parse(t *testing.T, path, src string) parsed {
	t.Helper()
	syntax, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return parsed{path: path, syntax: syntax}
}
