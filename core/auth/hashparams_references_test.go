// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// hashParamsSetter names the function that lowers the argon2id cost for the
// whole process.
const hashParamsSetter = "SetTestHashParams"

// SetTestHashParams panics outside a test binary, but every test binary passes
// that check, so no test can exercise it. This scan pins the other half of the
// guarantee: no non-test Go file in any module of the workspace refers to the
// setter, apart from its own declaration in core/auth, so a production binary
// has no path to the reduced parameters.
func TestSetTestHashParamsHasNoNonTestReference(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate auth sources")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	refs, decls, files := scanHashParamsSetterRefs(t, root)
	for _, mod := range []string{"core", "cmd/olivares", "modules"} {
		if files[mod] == 0 {
			t.Errorf("module %s: no non-test Go file scanned; a scan that reads nothing must not pass", mod)
		}
	}
	if decls != 1 {
		t.Errorf("found %d declarations of %s in core/auth, want 1", decls, hashParamsSetter)
	}
	for _, ref := range refs {
		t.Errorf("%s: non-test reference to %s; only _test.go files may lower the argon2id cost", ref, hashParamsSetter)
	}
}

// The scan reports each way a non-test file can reach the setter: a call from
// another package, a function value inside core/auth and a //go:linkname
// directive. A test file and a comment that names the setter are not references.
func TestSetTestHashParamsScanReportsPlantedReferences(t *testing.T) {
	root := t.TempDir()
	for _, f := range []struct{ rel, src string }{
		{"go.work", "go 1.26\n\nuse (\n\t./cmd/olivares\n\t./core // engine\n\t./modules\n)\n"},
		{"core/auth/hashparams.go", "package auth\n\n// SetTestHashParams lowers the cost.\nfunc SetTestHashParams(mem, t uint32, p uint8) {}\n"},
		{"core/auth/hashparams_test.go", "package auth\n\nfunc init() { SetTestHashParams(64, 1, 1) }\n"},
		{"core/auth/lower.go", "package auth\n\nvar lower = SetTestHashParams\n"},
		{"cmd/olivares/boot.go", "package main\n\nimport \"github.com/olivaresai/olivares/core/auth\"\n\nfunc init() { auth.SetTestHashParams(64, 1, 1) }\n"},
		{"modules/knob/doc.go", "// Package knob does not call SetTestHashParams.\npackage knob\n"},
		{"modules/knob/knob.go", "package knob\n\nimport _ \"unsafe\"\n\n//go:linkname lower github.com/olivaresai/olivares/core/auth.SetTestHashParams\nfunc lower(mem, t uint32, p uint8)\n"},
	} {
		file := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(f.src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	refs, decls, files := scanHashParamsSetterRefs(t, root)
	slices.Sort(refs)
	want := []string{"cmd/olivares/boot.go:5", "core/auth/lower.go:3", "modules/knob/knob.go:5"}
	if !slices.Equal(refs, want) {
		t.Errorf("references = %q, want %q", refs, want)
	}
	if decls != 1 {
		t.Errorf("declarations = %d, want 1", decls)
	}
	if files["core"] != 2 || files["cmd/olivares"] != 1 || files["modules"] != 2 {
		t.Errorf("non-test files scanned per module = %v, want core:2 cmd/olivares:1 modules:2", files)
	}
}

// scanHashParamsSetterRefs walks every module that root's go.work uses. It
// returns each non-test reference to the setter as "path:line" relative to root,
// the number of declarations of the setter in core/auth, and the number of
// non-test Go files scanned per module.
func scanHashParamsSetterRefs(t *testing.T, root string) (refs []string, decls int, files map[string]int) {
	t.Helper()
	files = map[string]int{}
	seen := map[string]bool{}
	for _, mod := range workspaceModules(t, root) {
		base := filepath.Join(root, filepath.FromSlash(mod))
		err := filepath.WalkDir(base, func(file string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				name := entry.Name()
				if file != base && (name == "node_modules" || name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") || seen[file] {
				return nil
			}
			seen[file] = true
			files[mod]++
			src, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			// Neither an identifier nor a linkname target can be written without
			// the name itself, so a file that lacks it holds no reference.
			if !bytes.Contains(src, []byte(hashParamsSetter)) {
				return nil
			}
			rel, err := filepath.Rel(root, file)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			fset := token.NewFileSet()
			parsed, err := parser.ParseFile(fset, file, src, parser.ParseComments)
			if err != nil {
				return err
			}
			var decl *ast.Ident
			if path.Dir(rel) == "core/auth" && parsed.Name.Name == "auth" {
				for _, d := range parsed.Decls {
					if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == hashParamsSetter {
						decl = fn.Name
						decls++
					}
				}
			}
			report := func(pos token.Pos) {
				refs = append(refs, fmt.Sprintf("%s:%d", rel, fset.Position(pos).Line))
			}
			ast.Inspect(parsed, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == hashParamsSetter && id != decl {
					report(id.Pos())
				}
				return true
			})
			// A //go:linkname directive binds a local function to the setter
			// without naming it as an identifier.
			for _, group := range parsed.Comments {
				for _, c := range group.List {
					if strings.HasPrefix(c.Text, "//go:linkname ") && strings.Contains(c.Text, hashParamsSetter) {
						report(c.Pos())
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan module %s: %v", mod, err)
		}
	}
	return refs, decls, files
}

// workspaceModules returns the module directories that root's go.work uses, as
// slash-separated paths relative to root.
func workspaceModules(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatalf("read go.work: %v", err)
	}
	var mods []string
	inUse := false
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case inUse && fields[0] == ")":
			inUse = false
		case inUse:
			mods = append(mods, path.Clean(strings.Trim(fields[0], `"`)))
		case fields[0] == "use" && len(fields) == 2 && fields[1] == "(":
			inUse = true
		case fields[0] == "use" && len(fields) == 2:
			mods = append(mods, path.Clean(strings.Trim(fields[1], `"`)))
		}
	}
	if len(mods) == 0 {
		t.Fatalf("go.work in %s uses no modules", root)
	}
	return mods
}
