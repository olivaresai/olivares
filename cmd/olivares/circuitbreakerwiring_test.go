// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTheInferenceDeciderIsWiredWithEveryGateItReads is the regression that was missing, and its
// absence is the whole reason was dead for twelve days.
//
// THE DEFECT IT CATCHES. `circuitBreaker` was DECLARED on inferenceProxyDecider, READ by the gate
// in authorizeChain, and never ASSIGNED by the constructor. The field was nil in every build, so
// circuitBreakerGateCheck returned (false, "") on its first line and the gate enforced nothing —
// in the enterprise build too, because the assignment site lives in this open repository and no
// overlay can add a field to a struct literal it does not contain.
//
// WHY IT READS THE SOURCE INSTEAD OF THE VALUE. Both a wired and an unwired decider hold nil in
// the open build, so no runtime assertion can tell them apart; and buildClaudeMessagesProxyServer
// returns an *http.Server, so a test cannot reach the decider at all without refactoring the
// composition root. Reading the literal is what remains, and it is enough: the defect was a
// missing key, and a missing key is exactly what this sees.
//
// It checks EVERY gate field rather than the one that broke. The bug was not special to the
// circuit breaker — it is what happens when a gate is added to the struct and forgotten in the
// literal — so pinning only the known case would leave the next one free.
func TestTheInferenceDeciderIsWiredWithEveryGateItReads(t *testing.T) {
	const file = "inferenceproxy.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	// THE GATES, WRITTEN DOWN. A list derived from the struct definition would shrink with it —
	// the "expectation derived from the thing under test" defect this repository has paid for
	// more than once. Adding a gate is a deliberate edit here as well as there.
	gates := []string{"killSwitch", "circuitBreaker", "egress", "inspector", "computerUse", "residency"}

	assigned := map[string]bool{}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "buildClaudeMessagesProxyServer" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			id, ok := lit.Type.(*ast.Ident)
			if !ok || id.Name != "inferenceProxyDecider" {
				return true
			}
			found = true
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok {
					assigned[key.Name] = true
				}
			}
			return true
		})
		return false
	})

	// THE FIXTURE MUST HAVE FOUND ITS SUBJECT. Without this, renaming the constructor or the
	// struct would empty `assigned`, every gate would report missing, and the failure would name
	// the wrong thing — or, worse, a future refactor could make this test vacuous while green.
	if !found {
		t.Fatalf("no inferenceProxyDecider literal was found inside buildClaudeMessagesProxyServer in %s: this test can no longer see what it claims to check", file)
	}
	for _, gate := range gates {
		if !assigned[gate] {
			t.Errorf("buildClaudeMessagesProxyServer never assigns %s, so the decider reads a gate nobody wired: the field is nil in every build and its check returns immediately, which is a gate that reports nothing rather than a gate that allows",
				gate)
		}
	}
}
