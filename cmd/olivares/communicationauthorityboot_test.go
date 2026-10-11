// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"testing"
)

func TestBootWiresExactCommunicationRequestAuthoritySourcesOnly(t *testing.T) {
	boot := gitpublishBootFunc(t, "boot.go", "boot")

	var authrAssignments, authzAssignments []*ast.AssignStmt
	var bindCalls, runtimeStarts, compositionBinds, leaderRuns, binderCalls []*ast.CallExpr
	forbidden := map[string]bool{}
	forbiddenPaths := map[string]bool{
		"set.sessionDependencies.CommunicationReadAuthorizer":      true,
		"set.sessionDependencies.CommunicationOperationAuthorizer": true,
		"set.sessionDependencies.CommunicationPumpReadiness":       true,
		"set.sessions.EnableCommunicationSessionCredentials":       true,
		"set.sessionDependencies.WorkOutboxAuthority":              true,
	}
	ast.Inspect(boot.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.AssignStmt:
			for _, target := range value.Lhs {
				path := communicationBootSelectorPath(target)
				if forbiddenPaths[path] {
					forbidden[path] = true
				}
				if path == "set.sessionDependencies.CommunicationAuthority" && len(value.Rhs) == 1 {
					if call, ok := communicationBootCall(value.Rhs[0], "sessions.NewCommunicationRequestAuthority", 2); ok {
						bindCalls = append(bindCalls, call)
					}
				}
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
			case "set.orchestration.UseWorkflowCredentialBinder":
				binderCalls = append(binderCalls, value)
			case "rt.Start":
				runtimeStarts = append(runtimeStarts, value)
			case "bindCommunicationComposition":
				compositionBinds = append(compositionBinds, value)
			case "st.Leader().Run":
				leaderRuns = append(leaderRuns, value)
			case "set.sessions.EnableCommunicationSessionCredentials":
				// Activation remains owned by bindCommunicationComposition,
				// before leadership, with the same requested/custody gates.
				forbidden[communicationBootSelectorPath(value.Fun)] = true
			}
		}
		return true
	})
	// Exactly one composition bind, and it precedes the first leader election so
	// the existing promotion recovery observes the enabled posture on first boot.
	if len(compositionBinds) != 1 || len(leaderRuns) != 1 ||
		compositionBinds[0].End() >= leaderRuns[0].Pos() {
		t.Fatalf("communication composition bind/leader run order = binds %#v runs %#v",
			compositionBinds, leaderRuns)
	}

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
	// CM-10: the exact-credential composition is admitted positively. The one
	// orchestration credential binder is the serving Authenticator itself, bound
	// once after it exists and before the runtime starts, under a nil guard; the
	// identity-only ports below stay forbidden.
	if len(binderCalls) != 1 || len(binderCalls[0].Args) != 1 ||
		!communicationBootIdentifier(binderCalls[0].Args[0], "authr") ||
		binderCalls[0].Pos() <= authrAssignments[0].End() ||
		binderCalls[0].End() >= runtimeStarts[0].Pos() {
		t.Fatalf("workflow credential binder calls = %#v", binderCalls)
	}
	guardedBinders := 0
	for _, statement := range boot.Body.List {
		conditional, ok := statement.(*ast.IfStmt)
		if !ok || !communicationBootOrchestrationNonNil(conditional.Cond) {
			continue
		}
		for _, guarded := range conditional.Body.List {
			expression, ok := guarded.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expression.X.(*ast.CallExpr)
			if ok && communicationBootSelectorPath(call.Fun) == "set.orchestration.UseWorkflowCredentialBinder" {
				guardedBinders++
			}
		}
	}
	if guardedBinders != 1 {
		t.Fatalf("workflow credential binders under the orchestration guard = %d, want one", guardedBinders)
	}

	directBinds, guardedCompositionBinds := 0, 0
	for _, statement := range boot.Body.List {
		conditional, ok := statement.(*ast.IfStmt)
		if !ok || !communicationBootSessionsNonNil(conditional.Cond) {
			continue
		}
		for _, guarded := range conditional.Body.List {
			switch guardedStatement := guarded.(type) {
			case *ast.AssignStmt:
				if len(guardedStatement.Lhs) == 1 && len(guardedStatement.Rhs) == 1 && communicationBootSelectorPath(guardedStatement.Lhs[0]) == "set.sessionDependencies.CommunicationAuthority" {
					if _, ok := communicationBootCall(guardedStatement.Rhs[0], "sessions.NewCommunicationRequestAuthority", 2); ok {
						directBinds++
					}
				}
				for _, rhs := range guardedStatement.Rhs {
					call, ok := rhs.(*ast.CallExpr)
					if ok && communicationBootSelectorPath(call.Fun) == "bindCommunicationComposition" {
						guardedCompositionBinds++
					}
				}
			}
		}
	}
	if directBinds != 1 {
		t.Fatalf("direct binds under exact sessions guard = %d, want one", directBinds)
	}
	if guardedCompositionBinds != 1 {
		t.Fatalf("composition binds under exact sessions guard = %d, want one", guardedCompositionBinds)
	}
	for path := range forbiddenPaths {
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
	case *ast.CallExpr:
		// A call inside a chain renders with its parentheses, so the leader
		// election `st.Leader().Run` is addressable while every plain selector
		// path keeps its historical spelling.
		callee := communicationBootSelectorPath(value.Fun)
		if callee == "" {
			return ""
		}
		return callee + "()"
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
	if assignment == nil || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 ||
		len(assignment.Rhs) != 1 || !communicationBootIdentifier(assignment.Lhs[0], "authr") {
		return false
	}
	call, ok := communicationBootCall(assignment.Rhs[0], "auth.NewAuthenticator", 2)
	return ok && communicationBootIdentifier(call.Args[0], "st") &&
		communicationBootIdentifier(call.Args[1], "nil")
}

func communicationBootAuthorizerAssignment(assignment *ast.AssignStmt) bool {
	if assignment == nil || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 ||
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

func communicationBootOrchestrationNonNil(expression ast.Expr) bool {
	comparison, ok := expression.(*ast.BinaryExpr)
	return ok && comparison.Op == token.NEQ &&
		communicationBootSelectorPath(comparison.X) == "set.orchestration" &&
		communicationBootIdentifier(comparison.Y, "nil")
}

func communicationBootSessionsNonNil(expression ast.Expr) bool {
	comparison, ok := expression.(*ast.BinaryExpr)
	return ok && comparison.Op == token.NEQ &&
		communicationBootSelectorPath(comparison.X) == "set.sessions" &&
		communicationBootIdentifier(comparison.Y, "nil")
}

func TestExpandedBootSourcePreservesDirectAuthorityCalls(t *testing.T) {
	src, err := os.ReadFile("boot.go")
	if err != nil {
		t.Fatal(err)
	}
	finish := []byte("\treturn b.finish(ctx)")
	if bytes.Count(src, finish) != 1 {
		t.Fatal("boot finish mutant target moved")
	}
	for _, tc := range []struct {
		name, statement, path string
		want                  int
	}{
		{"forbidden authority", "b.set.sessionDependencies.CommunicationReadAuthorizer = nil", "set.sessionDependencies.CommunicationReadAuthorizer", 1},
		{"duplicate publication binding", "b.set.gitpublish.UseAuthority(b.authr, b.authz)", "set.gitpublish.UseAuthority", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutant := bytes.Replace(src, finish, []byte("\t"+tc.statement+"\n"+string(finish)), 1)
			expanded, err := expandedBootSource(mutant)
			if err != nil {
				t.Fatal(err)
			}
			file, err := parser.ParseFile(token.NewFileSet(), "expanded.go", expanded, 0)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			ast.Inspect(file, func(node ast.Node) bool {
				if assignment, ok := node.(*ast.AssignStmt); ok {
					for _, target := range assignment.Lhs {
						if communicationBootSelectorPath(target) == tc.path {
							calls++
						}
					}
				}
				if call, ok := node.(*ast.CallExpr); ok && communicationBootSelectorPath(call.Fun) == tc.path {
					calls++
				}
				return true
			})
			if calls != tc.want {
				t.Fatalf("wiring guards see %d %s calls, want %d", calls, tc.path, tc.want)
			}
		})
	}
}

// expandedBootSource follows the phase calls, then reparses their bodies in that
// order so the existing wiring guards still compare execution order, not the
// locations of method declarations. Original statements remain visible to the
// guards, including direct wiring outside phases. State selectors are rendered
// as the former local names; string literals and comments are never rewritten.
func expandedBootSource(src []byte) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "boot.go", src, 0)
	if err != nil {
		return nil, err
	}
	var boot *ast.FuncDecl
	phases := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "boot" && fn.Recv == nil {
			boot = fn
		}
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			if ptr, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok && communicationBootIdentifier(ptr.X, "bootState") {
				phases[fn.Name.Name] = fn
			}
		}
	}
	if boot == nil {
		return nil, fmt.Errorf("boot function not found")
	}
	var body []ast.Stmt
	expandedPhase := false
	for _, statement := range boot.Body.List {
		body = append(body, statement)
		ast.Inspect(statement, func(node ast.Node) bool {
			if _, ok := node.(*ast.DeferStmt); ok {
				return false
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !communicationBootIdentifier(selector.X, "b") {
				return true
			}
			if phase := phases[selector.Sel.Name]; phase != nil {
				body = append(body, phase.Body.List...)
				expandedPhase = true
			}
			return true
		})
	}
	if !expandedPhase {
		return nil, fmt.Errorf("boot calls no startup phases")
	}
	boot.Body.List = body
	var rendered bytes.Buffer
	rendered.WriteString("package main\n")
	if err := format.Node(&rendered, token.NewFileSet(), boot); err != nil {
		return nil, err
	}
	source := rendered.Bytes()
	var scan scanner.Scanner
	tokens := token.NewFileSet().AddFile("expanded.go", -1, len(source))
	scan.Init(tokens, source, nil, 0)
	var output bytes.Buffer
	copied := 0
	for {
		pos, kind, literal := scan.Scan()
		if kind == token.EOF {
			break
		}
		if kind != token.IDENT || literal != "b" {
			continue
		}
		dot, next, _ := scan.Scan()
		if next == token.PERIOD {
			output.Write(source[copied:tokens.Offset(pos)])
			copied = tokens.Offset(dot) + 1
		}
	}
	output.Write(source[copied:])
	return output.Bytes(), nil
}
