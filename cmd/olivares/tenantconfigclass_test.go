// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// tenantconfigclass_test.go is the CLASS half. The per-site tests prove each
// known reader refuses a broken tenant; this one exists so a SEVENTH reader cannot
// quietly reintroduce the defect.
//
// The invariant: inside this package, a tenant that is read out of a `.Tenant` FIELD —
// operator config or a decision/request DTO — must go through parseBusinessTenant, not
// through a hand-written model.ParseTenantID plus whatever subset of the checks the
// author happened to remember. That subset is exactly how this bug existed: of the six
// readers the brief listed, five refused a broken tenant and one widened, and two
// of them never checked the reserved system tenant at all.
//
// model.ParseTenantID itself stays legal for tenants of a DIFFERENT provenance — a CLI
// --tenant flag, a store key, an already-validated id being re-parsed — which is why
// the rule keys on the `.Tenant` field access rather than banning the function.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// readsATenantField reports whether an expression tree reads a `.Tenant`/`.Tenants`
// field, at any depth: ParseTenantID(cfg.Tenant), ParseTenantID(strings.TrimSpace(
// tc.Tenant)) and ParseTenantID(x.X.Tenant) all count. The plural is included because a
// per-destination scope list is the same operator-authored provenance
// (notifydispatch.go), and an external contrast found it slipping past the singular.
func readsATenantField(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel != nil && (sel.Sel.Name == "Tenant" || sel.Sel.Name == "Tenants") {
			found = true
			return false
		}
		return true
	})
	return found
}

// isParseTenantIDCall reports whether the call is model.ParseTenantID(...).
func isParseTenantIDCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "ParseTenantID" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "model"
}

// isHelperCall reports whether the call is parseBusinessTenant(...).
func isHelperCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == helperName
}

// helperName is assembled from pieces so a literal-rewriting sweep over this package
// cannot silently turn the self-check below into a vacuous one.
const helperName = "parse" + "BusinessTenant"

func TestTenantFieldReadersGoThroughTheSharedHelper(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob package sources: files=%d err=%v", len(files), err)
	}

	var rawParseCalls, helperCalls, offenders int
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		// scopeOf names the enclosing func for the error message.
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			scope := "<package-scope>"
			var node ast.Node = decl
			if ok {
				if fn.Body == nil {
					continue
				}
				scope, node = fn.Name.Name, fn.Body
			}
			ast.Inspect(node, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				switch {
				case isHelperCall(call):
					helperCalls++
				case isParseTenantIDCall(call):
					rawParseCalls++
					for _, arg := range call.Args {
						if readsATenantField(arg) {
							offenders++
							t.Errorf("%s: %s reads a .Tenant field through model.ParseTenantID — a configured or decision-carried tenant must go through %s, which is the ONE place that decides the policy (parse error, the unset nil UUID, and the reserved system tenant)",
								fset.Position(call.Pos()), scope, helperName)
							break
						}
					}
				}
				return true
			})
		}
	}

	// SELF-CHECK. Without these, a detector that silently stopped matching anything
	// would report a clean package: the assertion above can only ever be as good as
	// its ability to SEE the two call shapes it discriminates between.
	if rawParseCalls == 0 {
		t.Errorf("the scan found NO model.ParseTenantID call anywhere in the package: the detector is broken, not the package clean")
	}
	if helperCalls == 0 {
		t.Errorf("the scan found NO %s call: the detector cannot see the helper, so a violation could not be told from a compliant reader", helperName)
	}
	t.Logf("scanned %d sources: %d raw model.ParseTenantID calls, %d %s calls, %d offenders",
		len(files), rawParseCalls, helperCalls, helperName, offenders)
}
