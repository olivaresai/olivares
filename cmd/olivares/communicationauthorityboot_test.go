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

func TestBootWiresExactCommunicationRequestAuthoritySourcesOnly(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "boot.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var boot *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		candidate, ok := declaration.(*ast.FuncDecl)
		if ok && candidate.Name.Name == "boot" {
			boot = candidate
			break
		}
	}
	if boot == nil || boot.Body == nil {
		t.Fatal("boot function not found")
	}

	var authrAssignments, authzAssignments []*ast.AssignStmt
	var bindCalls, runtimeStarts []*ast.CallExpr
	forbidden := map[string]bool{}
	ast.Inspect(boot.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for _, target := range value.Lhs {
				identifier, ok := target.(*ast.Ident)
				if !ok {
					continue
				}
				switch identifier.Name {
				case "authr":
					authrAssignments = append(authrAssignments, value)
				case "authz":
					authzAssignments = append(authzAssignments, value)
				}
			}
		case *ast.CallExpr:
			switch communicationBootSelectorPath(value.Fun) {
			case "set.sessions.UseCommunicationRequestAuthority":
				bindCalls = append(bindCalls, value)
			case "rt.Start":
				runtimeStarts = append(runtimeStarts, value)
			case "set.sessions.UseCommunicationCoreEntityReadAuthorizer",
				"set.sessions.UseCommunicationCoreEntityOperationAuthorizer",
				"set.sessions.UseCommunicationPumpReadinessWitness",
				"set.sessions.EnableCommunicationSessionCredentials":
				forbidden[communicationBootSelectorPath(value.Fun)] = true
			}
		}
		return true
	})

	if len(authrAssignments) != 1 || !communicationBootAuthenticatorAssignment(authrAssignments[0]) {
		t.Fatalf("boot authenticator assignment count/shape = %d/%#v",
			len(authrAssignments), authrAssignments)
	}
	if len(authzAssignments) != 1 || !communicationBootAuthorizerAssignment(authzAssignments[0]) {
		t.Fatalf("boot composed authorizer assignment count/shape = %d/%#v",
			len(authzAssignments), authzAssignments)
	}
	if len(bindCalls) != 1 || len(bindCalls[0].Args) != 2 ||
		!communicationBootIdentifier(bindCalls[0].Args[0], "authr") ||
		!communicationBootIdentifier(bindCalls[0].Args[1], "authz") ||
		bindCalls[0].Pos() <= authzAssignments[0].End() {
		t.Fatalf("exact communication authority bind calls = %#v", bindCalls)
	}
	if len(runtimeStarts) != 1 || bindCalls[0].End() >= runtimeStarts[0].Pos() {
		t.Fatalf("authority bind/runtime start order = binds %#v starts %#v",
			bindCalls, runtimeStarts)
	}

	directBinds := 0
	for _, statement := range boot.Body.List {
		conditional, ok := statement.(*ast.IfStmt)
		if !ok || !communicationBootSessionsNonNil(conditional.Cond) {
			continue
		}
		for _, guarded := range conditional.Body.List {
			expression, ok := guarded.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expression.X.(*ast.CallExpr)
			if ok && communicationBootSelectorPath(call.Fun) ==
				"set.sessions.UseCommunicationRequestAuthority" {
				directBinds++
			}
		}
	}
	if directBinds != 1 {
		t.Fatalf("direct binds under exact sessions guard = %d, want one", directBinds)
	}
	for _, path := range []string{
		"set.sessions.UseCommunicationCoreEntityReadAuthorizer",
		"set.sessions.UseCommunicationCoreEntityOperationAuthorizer",
		"set.sessions.UseCommunicationPumpReadinessWitness",
		"set.sessions.EnableCommunicationSessionCredentials",
	} {
		if forbidden[path] {
			t.Fatalf("preparatory authority composition activated %q", path)
		}
	}
}

func communicationBootSelectorPath(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		prefix := communicationBootSelectorPath(value.X)
		if prefix == "" {
			return ""
		}
		return prefix + "." + value.Sel.Name
	default:
		return ""
	}
}

func communicationBootIdentifier(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)
	return ok && identifier.Name == name
}

func communicationBootCall(expression ast.Expr, path string, arguments int) (*ast.CallExpr, bool) {
	call, ok := expression.(*ast.CallExpr)
	return call, ok && communicationBootSelectorPath(call.Fun) == path && len(call.Args) == arguments
}

func communicationBootAuthenticatorAssignment(assignment *ast.AssignStmt) bool {
	if assignment == nil || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !communicationBootIdentifier(assignment.Lhs[0], "authr") {
		return false
	}
	call, ok := communicationBootCall(assignment.Rhs[0], "auth.NewAuthenticator", 2)
	return ok && communicationBootIdentifier(call.Args[0], "st") &&
		communicationBootIdentifier(call.Args[1], "nil")
}

func communicationBootAuthorizerAssignment(assignment *ast.AssignStmt) bool {
	if assignment == nil || assignment.Tok != token.DEFINE || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !communicationBootIdentifier(assignment.Lhs[0], "authz") {
		return false
	}
	call, ok := communicationBootCall(assignment.Rhs[0], "auth.NewAuthorizer", 2)
	if !ok {
		return false
	}
	requestEvaluator, ok := communicationBootCall(call.Args[0], "set.gov.RequestEvaluator", 0)
	if !ok || requestEvaluator == nil {
		return false
	}
	scopedOption, ok := communicationBootCall(call.Args[1], "auth.WithScopedGrants", 1)
	if !ok {
		return false
	}
	scopedGrants, ok := communicationBootCall(scopedOption.Args[0], "set.gov.ScopedGrants", 0)
	return ok && scopedGrants != nil
}

func communicationBootSessionsNonNil(expression ast.Expr) bool {
	comparison, ok := expression.(*ast.BinaryExpr)
	return ok && comparison.Op == token.NEQ &&
		communicationBootSelectorPath(comparison.X) == "set.sessions" &&
		communicationBootIdentifier(comparison.Y, "nil")
}
