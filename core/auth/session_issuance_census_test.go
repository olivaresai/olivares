// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Every producer has behavioral policy controls, not just a permitted filename.
// References to the minting methods are counted too: assigning one to a function
// variable must not hide a new issuer. Empty error-return values are not sessions.
func TestSessionIssuanceCensus(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate auth sources")
	}
	root := filepath.Dir(filepath.Dir(thisFile))
	// Login delegates to LoginFrom, so the existing password policy controls
	// exercise the latter's one minting site as well.
	covered := map[string][]string{
		"auth/authenticator.go:LoginFrom->mintSession":                      {"TestLoginPolicy_NetworkBlocksPasswordLogin", "TestLoginPolicy_RequireSSOBlocksPasswordOnly", "TestLoginPolicy_NilIsNoOp"},
		"auth/federation_login.go:CompleteSSO->mintSession":                 {"TestLoginPolicy_NetworkBlocksSSOCompletion", "TestLoginPolicy_RequireSSONeverBlocksSSOCompletion"},
		"auth/onboarding.go:AcceptInvite->mintSession":                      {"TestInviteLoginPolicyRefusal", "TestInviteLoginPolicyAllowedAndNil", "TestInviteLoginPolicyRevalidatesStoredUser"},
		"auth/authenticator.go:mintSession->mintSessionTx":                  {"TestInviteLoginPolicyRefusal", "TestLoginPolicy_RequireSSONeverBlocksSSOCompletion"},
		"auth/authenticator.go:mintSessionTx->AuthSession":                  {"TestInviteLoginPolicyRefusal", "TestInviteLoginPolicyAllowedAndNil"},
		"auth/authenticator.go:mintSessionTx->Sessions.Create":              {"TestInviteLoginPolicyRefusal", "TestInviteLoginPolicyAllowedAndNil"},
		"auth/authenticator.go:mintSessionTx->NewCredential(PrefixSession)": {"TestInviteLoginPolicyRefusal", "TestInviteLoginPolicyAllowedAndNil"},
		// Refresh rotates an existing admitted credential; it is not a new login
		// (docs/LOGIN-ENFORCEMENT-OPERATIONS.md, Existing sessions are unaffected).
		"auth/authenticator.go:RefreshSession->NewCredential(PrefixSession)": {"TestLoginComponentAbsent_ExistingSessionsKeepRefreshAndRevoke"},
		// Store decoding constructs the typed value of an already persisted row.
		"internal/store/sqlstore/authcatalog.go:authSessionCodec->AuthSession": {"TestInviteLoginPolicyAllowedAndNil"},
	}
	seen := map[string]int{}
	tests := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		isTest := strings.HasSuffix(path, "_test.go")
		if isTest && filepath.Dir(path) != filepath.Dir(thisFile) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			name := ""
			switch d := decl.(type) {
			case *ast.FuncDecl:
				name = d.Name.Name
				if isTest {
					tests[name] = true
					continue
				}
			case *ast.GenDecl:
				if isTest {
					continue
				}
				for _, spec := range d.Specs {
					if v, ok := spec.(*ast.ValueSpec); ok && len(v.Names) == 1 {
						name = v.Names[0].Name
					}
				}
			}
			if isTest {
				continue
			}
			ast.Inspect(decl, func(node ast.Node) bool {
				kind := ""
				switch n := node.(type) {
				case *ast.SelectorExpr:
					if n.Sel.Name == "mintSession" || n.Sel.Name == "mintSessionTx" {
						kind = n.Sel.Name
					}
				case *ast.CompositeLit:
					if typ, ok := n.Type.(*ast.SelectorExpr); ok && typ.Sel.Name == "AuthSession" && len(n.Elts) > 0 {
						kind = "AuthSession"
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Create" {
						if call, ok := sel.X.(*ast.CallExpr); ok {
							if accessor, ok := call.Fun.(*ast.SelectorExpr); ok && accessor.Sel.Name == "Sessions" {
								kind = "Sessions.Create"
							}
						}
					}
					if fn, ok := n.Fun.(*ast.Ident); ok && fn.Name == "NewCredential" && len(n.Args) == 1 {
						if arg, ok := n.Args[0].(*ast.Ident); ok && arg.Name == "PrefixSession" {
							kind = "NewCredential(PrefixSession)"
						}
					}
					if fn, ok := n.Fun.(*ast.Ident); ok && fn.Name == "new" && len(n.Args) == 1 {
						if typ, ok := n.Args[0].(*ast.SelectorExpr); ok && typ.Sel.Name == "AuthSession" {
							kind = "AuthSession"
						}
					}
				}
				if kind != "" {
					site := filepath.ToSlash(rel) + ":" + name + "->" + kind
					seen[site]++
					if _, ok := covered[site]; !ok {
						t.Errorf("unlisted session producer %s at %s: add its policy refusal and allowed controls", site, fset.Position(node.Pos()))
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for site, controls := range covered {
		if seen[site] != 1 {
			t.Errorf("session producer %s occurs %d times, want one explicitly covered site", site, seen[site])
		}
		if len(controls) == 0 {
			t.Errorf("session producer %s has no behavioral controls", site)
		}
		for _, control := range controls {
			if !tests[control] {
				t.Errorf("session producer %s names absent test %s", site, control)
			}
		}
	}
}
