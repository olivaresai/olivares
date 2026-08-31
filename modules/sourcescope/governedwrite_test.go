// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"sort"
	"strings"
	"testing"
)

// governedwrite_test.go is the answer to the question the second surface asked, which
// was not "is E-1 fixed" but "why did a module with two access-deciding surfaces have one
// classifier". Twice in a row the fix for a case brought its brother: the forbid shrink
// brought the allow branch, and the allow branch brought assignments.
//
// A choke point alone does not close that. `classifyUpdate`'s whitelist default catches a
// SHAPE it does not recognize, but nothing catches a WRITER that never calls it — which is
// exactly how assignment.go went five sessions without a gate. The failure is not an
// unclassified input, it is an unrouted handler, and a default cannot see one.
//
// So the closure is a TOTALITY check over the package itself: enumerate every function that
// writes a kind whose rows decide access, and require each one to be a known, classified
// writer. It is deliberately not a grep — it parses the real package with go/ast, so it
// still holds when a writer is renamed, reformatted, wrapped in a closure, or written with
// the call spread over five lines. When someone adds the fourth surface, this test names the
// function and the file.
//
// What it CANNOT see, stated so the next session does not read more into a green than is
// there:
//
//   - a write that reaches the store through a helper this package does not own, or through
//     a kind resolved at runtime rather than named as a constant. Both surface as a governed
//     Ext with an unresolvable argument, which the check FAILS on rather than skips.
//
//   - a helper that RECEIVES an already-opened repo handle as a parameter and writes through
//     it. Such a function never calls Ext, so it has no governed kind to attribute and the
//     scan skips it, while its CALLER is credited with an Ext it does not visibly write
//     through. No such helper exists in this package today — every writer opens its own repo
//     — and this is named because an adversarial pass over the check found it, not because
//     it is currently exploitable. Closing it needs call-graph analysis (or the store handing
//     out governed handles only through the gate), which is a bigger change than the hole.

// governedKinds are the store kinds whose rows DECIDE ACCESS, by the identifier the package
// names them with. Adding a kind here is how a future access surface joins the check.
//
//   - bindingKind: the source→scope bindings the resolver evaluates.
//   - assignmentKind: the connector→workspace rows ConnectorAssigned reads, the
//     deny-closed gate for every unconfined source (resolver.go:257-264).
//
// wsConnectorKind is deliberately ABSENT, and the reason is structural rather than an
// oversight: a workspace connector's `workspace_ref` is immutable on update
// (wsconnector.go:301-303) and ListWorkspaceConnectors filters by it (:455), so the
// population that may reach one is fixed and singular at creation. Its writers decide
// whether the source EXISTS, not who may reach an existing one. If workspace_ref ever
// becomes mutable, it belongs in this map the same day.
var governedKinds = map[string]bool{
	"bindingKind": true, "assignmentKind": true,
}

// classifiedWriters are the functions allowed to write a governed kind, each paired with the
// classifier it must consult. A writer that appears here without calling its classifier is
// as much a hole as one that is not here at all, so both halves are asserted.
var classifiedWriters = map[string]string{
	"handleCreateBinding":    "classifyCreate",
	"handleUpdateBinding":    "classifyUpdate",
	"handleDeleteBinding":    "classifyDelete",
	"handleCreateAssignment": "classifyAssignmentCreate",
	"handleUpdateAssignment": "classifyAssignmentUpdate",
	"handleDeleteAssignment": "classifyAssignmentDelete",
}

// applyPosture is the one governed writer exempt from the "must call ITS classifier" rule:
// it executes a change a second principal has already approved, so it has no gate of its own
// to consult — re-deciding one there would either re-gate an approval into an infinite
// regress or quietly overrule the approver.
//
// It does call the classifiers, for a different question (E-6): not "should this be
// gated" but "is the state I am about to change still the state this was classified against".
// checkPremiseUnchanged compares a fresh verdict with the stored reason and refuses the
// approval if they diverge. That is a premise check, not a gate, which is why it does not
// belong in classifiedWriters.
const approvedWriter = "applyPosture"

// writeMethods are the repository calls that mutate rows. List/Get are reads and are what
// keeps the list/get handlers out of this check.
var writeMethods = map[string]bool{"Create": true, "Update": true, "Delete": true}

// TestEveryGovernedWriteIsClassified fails when a function writes a governed kind without
// being a declared, classified writer.
func TestEveryGovernedWriteIsClassified(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["sourcescope"]
	if !ok {
		t.Fatalf("package sourcescope not found in parsed dirs %v", keysOf(pkgs))
	}

	seen := map[string]bool{}
	for path, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, isFn := decl.(*ast.FuncDecl)
			if !isFn || fn.Body == nil {
				continue
			}
			govKinds, unresolved, writes, calls := scanFunc(fn)
			if len(govKinds) == 0 && len(unresolved) == 0 {
				continue
			}
			// A governed Ext whose argument is not a resolvable identifier is failed, not
			// skipped: "I could not tell what kind this is" must never read as "it is fine".
			if len(unresolved) > 0 && len(writes) > 0 {
				t.Errorf("%s: %s writes through sc.Ext() with an argument this check cannot resolve (%s); name the kind with a constant so it can be governed",
					shortPath(path), fn.Name.Name, strings.Join(unresolved, ", "))
				continue
			}
			if len(govKinds) == 0 || len(writes) == 0 {
				continue // reads a governed kind, or writes an ungoverned one: not our class
			}
			name := fn.Name.Name
			seen[name] = true
			if name == approvedWriter {
				continue
			}
			classifier, allowed := classifiedWriters[name]
			if !allowed {
				t.Errorf(`%s: %s writes governed kind(s) %s (via %s) but is not a declared classified writer.

A row of these kinds decides who may reach a source, so a write that relaxes it is never one
actor's call (ADR-0022 §5). Either route this write through a classifier and add it to
classifiedWriters, or — if it genuinely cannot relax enforcement — say so there with the
reason, the way wsConnectorKind is excluded above.`,
					shortPath(path), name, strings.Join(govKinds, ", "), strings.Join(writes, "/"))
				continue
			}
			if !calls[classifier] {
				t.Errorf("%s: %s is a declared classified writer but never calls %s — the allowlist would be a rubber stamp",
					shortPath(path), name, classifier)
			}
		}
	}

	// The allowlist must not outlive its entries either: a stale name would silently permit
	// a future function that happens to reuse it.
	for name := range classifiedWriters {
		if !seen[name] {
			t.Errorf("classifiedWriters lists %s, but no function of that name writes a governed kind — remove the stale entry", name)
		}
	}
	if !seen[approvedWriter] {
		t.Errorf("approvedWriter names %s, but it no longer writes a governed kind", approvedWriter)
	}
}

// scanFunc reports, for one function body: which governed kinds it opens with sc.Ext(), any
// Ext argument it cannot resolve to an identifier, which write methods it calls, and every
// function name it calls.
func scanFunc(fn *ast.FuncDecl) (govKinds, unresolved, writes []string, calls map[string]bool) {
	calls = map[string]bool{}
	gk, un, wr := map[string]bool{}, map[string]bool{}, map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			switch {
			case fun.Sel.Name == "Ext" && len(call.Args) == 1:
				if id, isID := call.Args[0].(*ast.Ident); isID {
					if governedKinds[id.Name] {
						gk[id.Name] = true
					}
				} else {
					un[exprText(call.Args[0])] = true
				}
			case writeMethods[fun.Sel.Name]:
				wr[fun.Sel.Name] = true
			}
			calls[fun.Sel.Name] = true
		case *ast.Ident:
			// allExt(ctx, sc, <kind>, …) reaches the store through this package's own read
			// helper; it only ever Lists, but the kind it opens is still recorded so a future
			// writing helper cannot hide a governed kind behind an indirection.
			if fun.Name == "allExt" && len(call.Args) >= 3 {
				if id, isID := call.Args[2].(*ast.Ident); isID && governedKinds[id.Name] {
					gk[id.Name] = true
				}
			}
			calls[fun.Name] = true
		}
		return true
	})
	return sortedKeys(gk), sortedKeys(un), sortedKeys(wr), calls
}

func exprText(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "non-identifier argument"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func shortPath(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
