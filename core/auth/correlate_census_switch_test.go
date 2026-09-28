// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// TestCensusSizeIsAnEngineSwitchNeverASkip holds the census size switch to its
// terms. The tenant-size census compiles in every build. No census case, and
// neither engine loop, skips or reads the environment. No census case branches
// on the engine, since the size is censusScaleFor's alone, and no tenant-size
// case returns before its assertions. The fixture helpers a case builds and
// checks its rows with are held to the case's rules, so a size cannot be chosen
// by the engine inside them either. In each build exactly one engine runs the
// census at the full tenant limits while the other runs a hundredth.
func TestCensusSizeIsAnEngineSwitchNeverASkip(t *testing.T) {
	fset := token.NewFileSet()
	helpers := map[string]bool{
		"censusScaleFor": true, "censusLimitsAt": true, "forEachCensusEngine": true, "forEachConsentEngine": true,
		"censusFixture": true, "openConsentStore": true,
	}
	fixtureHelpers := map[string]bool{
		"buildTenantSizeCensus": true, "place": true, "wantUnchanged": true, "directoryFacts": true,
	}
	for _, name := range []string{"correlate_census_full_test.go", "correlate_test.go", "consent_internal_test.go"} {
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		fullSize := name == "correlate_census_full_test.go"
		if fullSize {
			for _, group := range file.Comments {
				for _, c := range group.List {
					if strings.HasPrefix(c.Text, "//go:build") {
						t.Errorf("%s: %s: the tenant-size census must compile in every build",
							fset.Position(c.Pos()), c.Text)
					}
				}
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			isCase := strings.HasPrefix(fn.Name.Name, "TestCensus") || fullSize && fixtureHelpers[fn.Name.Name]
			if !isCase && !helpers[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CallExpr:
					sel, ok := node.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch sel.Sel.Name {
					case "Skip", "Skipf", "SkipNow":
						t.Errorf("%s: %s calls %s: the census size is a switch, never a skip",
							fset.Position(node.Pos()), fn.Name.Name, sel.Sel.Name)
					case "Getenv", "LookupEnv", "Environ", "Short":
						t.Errorf("%s: %s calls %s: the census size keys on the engine, never the environment",
							fset.Position(node.Pos()), fn.Name.Name, sel.Sel.Name)
					}
				case *ast.IfStmt:
					if isCase && mentionsEngine(node.Cond) {
						t.Errorf("%s: %s branches on the engine: the census size is censusScaleFor's alone",
							fset.Position(node.Pos()), fn.Name.Name)
					}
				case *ast.SwitchStmt:
					if isCase && node.Tag != nil && mentionsEngine(node.Tag) {
						t.Errorf("%s: %s switches on the engine: the census size is censusScaleFor's alone",
							fset.Position(node.Pos()), fn.Name.Name)
					}
				case *ast.CaseClause:
					for _, expr := range node.List {
						if isCase && mentionsEngine(expr) {
							t.Errorf("%s: %s selects a case by the engine: the census size is censusScaleFor's alone",
								fset.Position(node.Pos()), fn.Name.Name)
						}
					}
				case *ast.ReturnStmt:
					if isCase && fullSize && len(node.Results) == 0 {
						t.Errorf("%s: %s returns before its assertions: a tenant-size case runs to its end",
							fset.Position(node.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	full := 0
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		switch scale := censusScaleFor(engine); scale {
		case 1:
			full++
		case 100:
		default:
			t.Errorf("the census runs at 1/%d of the tenant limits on %s, want the full limits or a hundredth", scale, engine)
		}
	}
	if full != 1 {
		t.Errorf("%d engines run the tenant-size census at the full limits in this build, want exactly one", full)
	}
}

// mentionsEngine reports whether expr refers to a store engine: an identifier
// named engine, or a selector naming Engine, EngineSQLite or EnginePostgres.
func mentionsEngine(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			found = found || x.Name == "engine"
		case *ast.SelectorExpr:
			switch x.Sel.Name {
			case "Engine", "EngineSQLite", "EnginePostgres":
				found = true
			}
		}
		return !found
	})
	return found
}
