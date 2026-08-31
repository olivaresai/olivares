// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestInvariant_ItemsIsNeverANullableSlice is the STRUCTURAL half of the
// no-null-collections contract, and the reason this defect cannot come back the
// way it arrived.
//
// It arrived by copy-paste: 24 packages each declared their own
// `type listResponse[T any] struct { Items []T ... }`, and a handler that
// accumulated into `var items []T` served `{"items":null}` from every one of them
// that happened not to pre-initialize (12 endpoints did, on a CLEAN install).
// Fixing the 12 handlers would have left the 25th copy one paste away.
//
// So the rule is about the TYPE, not the handler: anywhere under core/ and
// modules/ — the two trees that SERVE the REST API — a struct field carrying the
// wire key "items" must be an api.JSONArray, whose MarshalJSON renders a nil slice
// as []. A plain []T is rejected; so is any other type, because "I cannot tell
// what this is" is not "this is safe" (deny-closed).
//
// Not covered here on purpose: cmd/, clients/, connectors/, sdk/ and operator/,
// which DECODE JSON (our own responses, a Kubernetes list, a vendor API). null
// decodes to a nil slice correctly, and forcing a marshal-side type on a decoder
// would be cargo cult.
func TestInvariant_ItemsIsNeverANullableSlice(t *testing.T) {
	root := workspaceRoot(t)

	// Request bodies are DECODED, never marshaled, so the emit-side guarantee does
	// not apply to them. Each entry is "<repo-relative file>:<type name>" and must
	// carry the reason it is here — an allowlist you can extend silently is not a
	// gate.
	allowed := map[string]string{
		"modules/evals/calibration.go:addCalibItemsRequest": "request body: the items are decoded from the caller, never rendered",
	}
	used := map[string]bool{}

	var violations []string
	for _, tree := range []string{"core", "modules"} {
		base := filepath.Join(root, tree)
		err := filepath.WalkDir(base, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if name := de.Name(); name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("read %s: %v", path, rerr)
				return nil
			}
			if strings.HasPrefix(string(src), "// Code generated") ||
				strings.Contains(string(src), "\n// Code generated") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, src, 0)
			if perr != nil {
				// A source file this test cannot read is a file it cannot clear.
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			rel, _ := filepath.Rel(root, path)

			// Track the enclosing named type so a violation names something a
			// reviewer can find (and so the allowlist can be type-scoped).
			typeName := ""
			ast.Inspect(f, func(n ast.Node) bool {
				if ts, ok := n.(*ast.TypeSpec); ok {
					typeName = ts.Name.Name
				}
				st, ok := n.(*ast.StructType)
				if !ok || st.Fields == nil {
					return true
				}
				for _, field := range st.Fields.List {
					if field.Tag == nil || jsonKey(field.Tag.Value) != "items" {
						continue
					}
					key := rel + ":" + typeName
					if _, ok := allowed[key]; ok {
						used[key] = true
						continue
					}
					if isJSONArray(field.Type) {
						continue
					}
					pos := fset.Position(field.Pos())
					violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+
						" (type "+typeName+") declares the wire key \"items\" as "+
						exprString(field.Type)+"; it must be api.JSONArray[…] so an empty "+
						"collection serializes as [] and never null")
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", base, err)
		}
	}

	for _, v := range violations {
		t.Errorf("list-envelope invariant: %s", v)
	}
	for key, why := range allowed {
		if !used[key] {
			t.Errorf("stale allowlist entry %q (%s): the type is gone or renamed — delete the entry", key, why)
		}
	}
}

// TestInvariant_NoPrivateListEnvelope pins the other half of the same story: a
// package may ALIAS the shared envelope (`type listResponse[T any] = api.ListResponse[T]`)
// but must not re-declare it as a struct of its own. The field rule above already
// rejects the `Items []T` such a copy would carry; this one names the mistake
// directly, so the failure reads as "you re-declared the envelope" rather than as
// an anonymous field complaint.
func TestInvariant_NoPrivateListEnvelope(t *testing.T) {
	root := workspaceRoot(t)
	var violations []string
	for _, tree := range []string{"core", "modules"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if name := de.Name(); name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Errorf("read %s: %v", path, rerr)
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, src, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			ast.Inspect(f, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || ts.Assign.IsValid() { // an alias is exactly what we want
					return true
				}
				if !strings.EqualFold(ts.Name.Name, "listResponse") {
					return true
				}
				if _, isStruct := ts.Type.(*ast.StructType); !isStruct {
					return true
				}
				// core/api owns the one true declaration.
				if rel == filepath.Join("core", "api", "listresponse.go") {
					return true
				}
				pos := fset.Position(ts.Pos())
				violations = append(violations, rel+":"+strconv.Itoa(pos.Line)+
					" re-declares the list envelope as its own struct; alias the shared one instead: "+
					"type listResponse[T any] = api.ListResponse[T]")
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
	}
	for _, v := range violations {
		t.Errorf("list-envelope invariant: %s", v)
	}
}

// jsonKey returns the json tag NAME of a struct tag literal ("" when there is no
// json tag). `json:"items,omitempty"` -> "items".
func jsonKey(tagLit string) string {
	unquoted, err := strconv.Unquote(tagLit)
	if err != nil {
		return ""
	}
	tag, ok := reflect.StructTag(unquoted).Lookup("json")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}

// isJSONArray reports whether the field type is api.JSONArray[…] (or, inside
// package api itself, JSONArray[…]).
func isJSONArray(e ast.Expr) bool {
	idx, ok := e.(*ast.IndexExpr)
	if !ok {
		return false
	}
	switch fn := idx.X.(type) {
	case *ast.Ident:
		return fn.Name == "JSONArray"
	case *ast.SelectorExpr:
		return fn.Sel.Name == "JSONArray"
	}
	return false
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.ArrayType:
		if v.Len == nil {
			return "[]" + exprString(v.Elt)
		}
		return "array"
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.IndexExpr:
		return exprString(v.X) + "[" + exprString(v.Index) + "]"
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.MapType:
		return "map"
	}
	return "an unrecognized type"
}

// workspaceRoot walks up from the test's working directory to the directory
// holding go.work — the workspace root every tree is relative to.
func workspaceRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.work not found above %s", dir)
		}
		dir = parent
	}
}
