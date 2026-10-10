// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// migrationhash fingerprints Go migration constructors and their source dependencies.
// It parses source, never executes code from the baseline or candidate.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type declaration struct {
	node   ast.Node
	file   *sourceFile
	key    string
	method bool
}
type sourceFile struct {
	path    string
	tree    *ast.File
	imports map[string]string
}
type inventory struct {
	sources  map[string]string
	fset     *token.FileSet
	packages map[string]map[string][]*declaration
	files    map[string]*sourceFile
}

func (in *inventory) load(dir string) error {
	if _, ok := in.packages[dir]; ok {
		return nil
	}
	decls := map[string][]*declaration{}
	in.packages[dir] = decls
	for filename, source := range in.sources {
		if path.Dir(filename) != dir {
			continue
		}
		tree, err := parser.ParseFile(in.fset, filename, source, 0)
		if err != nil {
			return err
		}
		file := &sourceFile{path: filename, tree: tree, imports: map[string]string{}}
		in.files[filename] = file
		for _, imp := range tree.Imports {
			imported, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			const prefix = "github.com/olivaresai/olivares/"
			name := path.Base(imported)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			if strings.HasPrefix(imported, prefix) {
				file.imports[name] = strings.TrimPrefix(imported, prefix)
			} else {
				file.imports[name] = "" // External package selectors cannot call local methods.
			}
		}
		for _, node := range tree.Decls {
			switch d := node.(type) {
			case *ast.FuncDecl:
				key := d.Name.Name
				if d.Recv != nil {
					key = rendered(in.fset, d.Recv.List[0].Type) + "." + key
				}
				decls[d.Name.Name] = append(decls[d.Name.Name], &declaration{d, file, filename + "#" + key, d.Recv != nil})
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if typ, ok := spec.(*ast.TypeSpec); ok {
						decls[typ.Name.Name] = append(decls[typ.Name.Name], &declaration{typ, file, filename + "#" + typ.Name.Name, false})
					}
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range value.Names {
							decls[name.Name] = append(decls[name.Name], &declaration{value, file, filename + "#" + name.Name, false})
						}
					}
				}
			}
		}
	}
	return nil
}

func rendered(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		panic(err)
	}
	return buf.String()
}

func (in *inventory) version(file *sourceFile, expr ast.Expr, seen map[string]bool) (int, error) {
	switch v := expr.(type) {
	case *ast.BasicLit:
		n, err := strconv.Atoi(v.Value)
		if err == nil && n > 0 {
			return n, nil
		}
	case *ast.Ident:
		if !seen[v.Name] {
			seen[v.Name] = true
			for _, d := range in.packages[path.Dir(file.path)][v.Name] {
				if value, ok := d.node.(*ast.ValueSpec); ok {
					for i, name := range value.Names {
						if name.Name == v.Name && i < len(value.Values) {
							return in.version(file, value.Values[i], seen)
						}
					}
				}
			}
		}
	}
	return 0, fmt.Errorf("%s: cannot resolve migration version %s", file.path, rendered(in.fset, expr))
}

// sourceHash includes named callback/generator helpers, including dialect methods.
// This is deliberately conservative: changing a shared migration helper requires
// keeping the historical helper and using a new one in a forward migration.
func (in *inventory) sourceHash(file *sourceFile, roots []ast.Node, separate ...string) (string, error) {
	chunks := map[string]string{}
	visited := map[string]bool{}
	var walk func(*sourceFile, ast.Node) error
	var add func(*declaration) error
	add = func(d *declaration) error {
		if visited[d.key] {
			return nil
		}
		visited[d.key] = true
		chunks[d.key] = rendered(in.fset, d.node)
		return walk(d.file, d.node)
	}
	walk = func(file *sourceFile, node ast.Node) error {
		var failure error
		ast.Inspect(node, func(n ast.Node) bool {
			if n == nil || failure != nil {
				return false
			}
			switch v := n.(type) {
			case *ast.KeyValueExpr:
				failure = walk(file, v.Value)
				return false
			case *ast.SelectorExpr:
				if id, ok := v.X.(*ast.Ident); ok {
					if dir, ok := file.imports[id.Name]; ok && id.Obj == nil {
						if dir == "" {
							return false
						}
						if failure = in.load(dir); failure != nil {
							return false
						}
						for _, d := range in.packages[dir][v.Sel.Name] {
							if failure = add(d); failure != nil {
								return false
							}
						}
						return false
					}
				}
				// Interface receivers (notably dialect.Dialect) have no concrete type in
				// the AST. Include matching methods in this package and its local imports.
				dirs := []string{path.Dir(file.path)}
				for _, dir := range file.imports {
					if dir != "" {
						dirs = append(dirs, dir)
					}
				}
				for _, dir := range dirs {
					if failure = in.load(dir); failure != nil {
						return false
					}
					for _, d := range in.packages[dir][v.Sel.Name] {
						if d.method {
							if failure = add(d); failure != nil {
								return false
							}
						}
					}
				}
				failure = walk(file, v.X)
				return false
			case *ast.Ident:
				// The blank identifier never refers to a package declaration.
				if v.Name == "_" || slices.Contains(separate, v.Name) {
					return false
				}
				for _, d := range in.packages[path.Dir(file.path)][v.Name] {
					if !d.method {
						if failure = add(d); failure != nil {
							return false
						}
					}
				}
			}
			return true
		})
		return failure
	}
	for i, root := range roots {
		chunks[fmt.Sprintf("root/%d", i)] = rendered(in.fset, root)
		if err := walk(file, root); err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(chunks))
	for key := range chunks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		fmt.Fprintf(hash, "%s\x00%s\x00", key, chunks[key])
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// bindingHash pins what callers actually inject, not just the factory that we
// expect them to call. Follow initializers and subsequent writes: boot builds
// callback inputs before assembling the plan. The plan and descriptor catalog
// have their own hashes so adding a version or descriptor does not rewrite every binding.
func (in *inventory) bindingHash(file *sourceFile, call *ast.CallExpr) (string, error) {
	roots := []ast.Node{call}
	seen := map[*ast.Object]bool{}
	writes := map[*ast.Object][]ast.Node{}
	// A tuple's sibling destinations are not inputs. Following the err in
	// `manifest, err = loadManifest()` would pull every unrelated runtime error
	// assignment into manifest's migration fingerprint. Keep the whole write in
	// the hash, but expand dependencies only from reads. Selector/index operands
	// remain reads, and the writes map still follows every write to actual inputs.
	destinations := map[*ast.Ident]bool{}
	var controls []ast.Node
	var stack []int
	// Keep control headers, not their bodies: unrelated boot work and newly
	// registered migrations must not become dependencies of an old binding.
	ast.Inspect(file.tree, func(node ast.Node) bool {
		if node == nil {
			controls = controls[:stack[len(stack)-1]]
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, len(controls))
		var targets []ast.Expr
		switch n := node.(type) {
		case *ast.IfStmt:
			controls = append(controls, n) // Select then/else for each write below.
		case *ast.ForStmt:
			controls = append(controls, &ast.ForStmt{For: n.For, Init: n.Init, Cond: n.Cond, Post: n.Post, Body: &ast.BlockStmt{}})
		case *ast.RangeStmt:
			controls = append(controls, &ast.RangeStmt{For: n.For, Key: n.Key, Value: n.Value, Tok: n.Tok, X: n.X, Body: &ast.BlockStmt{}})
			targets = []ast.Expr{n.Key, n.Value}
		case *ast.SwitchStmt:
			controls = append(controls, &ast.SwitchStmt{Switch: n.Switch, Init: n.Init, Tag: n.Tag, Body: &ast.BlockStmt{}})
		case *ast.TypeSwitchStmt:
			controls = append(controls, &ast.TypeSwitchStmt{Switch: n.Switch, Init: n.Init, Assign: n.Assign, Body: &ast.BlockStmt{}})
		case *ast.CaseClause:
			controls = append(controls, &ast.CaseClause{Case: n.Case, List: n.List})
		case *ast.CommClause:
			controls = append(controls, &ast.CommClause{Case: n.Case, Comm: n.Comm})
		case *ast.AssignStmt:
			targets = n.Lhs
		case *ast.IncDecStmt:
			targets = []ast.Expr{n.X}
		}
		for _, target := range targets {
			if id, ok := target.(*ast.Ident); ok {
				destinations[id] = true
			}
			obj := assignedObject(target)
			if obj == nil || obj.Decl == node {
				continue
			}
			for _, control := range controls {
				if branch, ok := control.(*ast.IfStmt); ok {
					header := &ast.IfStmt{If: branch.If, Init: branch.Init, Cond: branch.Cond, Body: &ast.BlockStmt{}}
					if branch.Else != nil && node.Pos() >= branch.Else.Pos() {
						header.Else = &ast.BlockStmt{}
					}
					control = header
				}
				writes[obj] = append(writes[obj], control)
			}
			if _, rangeWrite := node.(*ast.RangeStmt); !rangeWrite {
				writes[obj] = append(writes[obj], node)
			}
		}
		return true
	})
	for i := 0; i < len(roots); i++ {
		ast.Inspect(roots[i], func(node ast.Node) bool {
			id, ok := node.(*ast.Ident)
			if !ok || destinations[id] || id.Obj == nil || seen[id.Obj] {
				return true
			}
			seen[id.Obj] = true
			roots = append(roots, writes[id.Obj]...)
			var values []ast.Expr
			switch decl := id.Obj.Decl.(type) {
			case *ast.AssignStmt:
				values = decl.Rhs
			case *ast.ValueSpec:
				values = decl.Values
			}
			for _, value := range values {
				roots = append(roots, value)
			}
			return true
		})
	}
	// Moving a write across an initializer or the plan call changes its effect.
	// Positions order the roots but are never hashed, so unrelated insertions pass.
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Pos() < roots[j].Pos() })
	return in.sourceHash(file, roots, "buildCoreMigrations", "buildCoreMigrationPlan", "coreDescriptors")
}

// Only the destination is written; an index expression's operands are reads.
func assignedObject(expr ast.Expr) *ast.Object {
	switch n := expr.(type) {
	case *ast.Ident:
		return n.Obj
	case *ast.SelectorExpr:
		return assignedObject(n.X)
	case *ast.IndexExpr:
		return assignedObject(n.X)
	case *ast.StarExpr:
		return assignedObject(n.X)
	case *ast.ParenExpr:
		return assignedObject(n.X)
	}
	return nil
}

// controlHash keeps return values, append destinations and branch conditions,
// while allowing the separately fingerprinted migration list to grow.
func controlHash(fset *token.FileSet, node ast.Node, elementType string) string {
	var restore []func()
	ast.Inspect(node, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CompositeLit:
			if array, ok := n.Type.(*ast.ArrayType); ok {
				if elt, ok := array.Elt.(*ast.SelectorExpr); ok && elt.Sel.Name == elementType {
					elements := n.Elts
					restore = append(restore, func() { n.Elts = elements })
					n.Elts = nil
					return false
				}
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "append" && len(n.Args) > 0 {
				arguments, ellipsis := n.Args, n.Ellipsis
				restore = append(restore, func() { n.Args, n.Ellipsis = arguments, ellipsis })
				n.Args, n.Ellipsis = n.Args[:1], token.NoPos
			}
		}
		return true
	})
	skeleton := rendered(fset, node)
	for _, undo := range restore {
		undo()
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(skeleton)))
}

// Auxiliary plans use the same runner with independent tracking namespaces.
// Fingerprint each literal and its bound inputs, not the executor or siblings:
// appending v2 must not rewrite v1. SQL-file plans are checked separately.
func (in *inventory) runnerFingerprints(out map[string]string) error {
	var files []*sourceFile
	for filename, source := range in.sources {
		if !strings.Contains(source, `"github.com/olivaresai/olivares/core/migrate"`) {
			continue
		}
		if err := in.load(path.Dir(filename)); err != nil {
			return err
		}
		files = append(files, in.files[filename])
	}
	for _, file := range files {
		for _, decl := range file.tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var failure error
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || failure != nil {
					return failure == nil
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !slices.Contains([]string{"Apply", "ApplyTx", "ReconcileTx"}, sel.Sel.Name) || len(call.Args) != 5 {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || file.imports[pkg.Name] != "core/migrate" {
					return true
				}
				plan := call.Args[4]
				if id, ok := plan.(*ast.Ident); ok && id.Obj != nil {
					if assignment, ok := id.Obj.Decl.(*ast.AssignStmt); ok && len(assignment.Rhs) == 1 {
						plan = assignment.Rhs[0]
					}
				}
				list, ok := plan.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, element := range list.Elts {
					lit, ok := element.(*ast.CompositeLit)
					if !ok {
						continue // Core constructors are individually fingerprinted below.
					}
					for _, field := range lit.Elts {
						kv, ok := field.(*ast.KeyValueExpr)
						if !ok || rendered(in.fset, kv.Key) != "Version" {
							continue
						}
						version, err := in.version(file, kv.Value, map[string]bool{})
						if err != nil {
							failure = err
							return false
						}
						binding := &ast.CallExpr{Fun: ast.NewIdent(sel.Sel.Name), Args: []ast.Expr{call.Args[3], lit}}
						digest, err := in.bindingHash(file, binding)
						if err != nil {
							failure = err
							return false
						}
						key := fmt.Sprintf("runner/%s/%s/%s/%d", file.path, fn.Name.Name, rendered(in.fset, call.Args[3]), version)
						if _, exists := out[key]; exists {
							failure = fmt.Errorf("duplicate migration %s", key)
							return false
						}
						out[key] = digest
						out["runner/control/"+file.path+"/"+fn.Name.Name] = controlHash(in.fset, fn, "Migration")
					}
				}
				return true
			})
			if failure != nil {
				return failure
			}
		}
	}
	return nil
}

func fingerprints(sources map[string]string) (map[string]string, error) {
	in := &inventory{sources: sources, fset: token.NewFileSet(), packages: map[string]map[string][]*declaration{}, files: map[string]*sourceFile{}}
	const core = "core/internal/store/sqlstore"
	if err := in.load(core); err != nil {
		return nil, err
	}
	out := map[string]string{}
	// Copy the initial set: resolving dependencies loads additional source files.
	var files []*sourceFile
	for _, file := range in.files {
		files = append(files, file)
	}
	for _, file := range files {
		for _, decl := range file.tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "loadFileMigrations" {
				continue
			}
			var failure error
			bindings := map[string]int{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok || failure != nil {
					return failure == nil
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || (id.Name != "buildCoreMigrations" && id.Name != "buildCoreMigrationPlan") {
					return true
				}
				digest, err := in.bindingHash(file, call)
				if err != nil {
					failure = err
					return false
				}
				bindings[id.Name]++
				key := fmt.Sprintf("core/binding/%s/%s/%s/%d", file.path, fn.Name.Name, id.Name, bindings[id.Name])
				out[key] = digest
				return true
			})
			if failure != nil {
				return nil, failure
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				// An element of []migrate.Migration may omit its type.
				if lit.Type == nil && fn.Name.Name != "buildCoreMigrations" {
					return true
				}
				if lit.Type != nil {
					sel, ok := lit.Type.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Migration" {
						return true
					}
				}
				var version ast.Expr
				hasName := false
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if key.Name == "Version" {
						version = kv.Value
					}
					if key.Name == "Name" {
						hasName = true
					}
				}
				if version == nil || !hasName {
					return true
				}
				v, err := in.version(file, version, map[string]bool{})
				if err != nil {
					failure = err
					return false
				}
				roots := []ast.Node{fn}
				if fn.Name.Name == "buildCoreMigrations" {
					roots = []ast.Node{lit}
					// Include local statement generation without hashing sibling versions.
					if strings.Contains(rendered(in.fset, lit), "Stmts: entity") {
						for _, statement := range fn.Body.List {
							if statement.Pos() >= lit.Pos() {
								break
							}
							if _, ok := statement.(*ast.ReturnStmt); ok {
								continue
							}
							if statement.End() < lit.Pos() {
								roots = append(roots, statement)
							}
						}
					}
				}
				// Constructor references in the registered plan are part of each
				// version's identity. Deleting a call cannot leave a dormant
				// constructor masquerading as a registered migration.
				for _, planName := range []string{"buildCoreMigrations", "buildCoreMigrationPlan"} {
					if fn.Name.Name == planName {
						continue
					}
					for _, plan := range in.packages[core][planName] {
						ast.Inspect(plan.node, func(node ast.Node) bool {
							call, ok := node.(*ast.CallExpr)
							if !ok {
								return true
							}
							id, ok := call.Fun.(*ast.Ident)
							if ok && id.Name == fn.Name.Name {
								roots = append(roots, call)
							}
							return true
						})
					}
				}
				digest, err := in.sourceHash(file, roots)
				if err != nil {
					failure = err
					return false
				}
				// Boot supplies these callback factories. Descriptor factories
				// are fingerprinted separately so adding a new descriptor does
				// not rewrite all existing version hashes.
				injected := map[int][]string{
					6:  {"guardBootstrapExec"},
					7:  {"directoryDescriptors", "guardEditionTwoMigrationExec"},
					9:  {"accessEvidenceDescriptors"},
					11: {"calibrateEvidenceStates"},
				}
				for _, name := range injected[v] {
					for _, dependency := range in.packages[core][name] {
						extra, err := in.sourceHash(dependency.file, []ast.Node{dependency.node})
						if err != nil {
							failure = err
							return false
						}
						digest = fmt.Sprintf("%x", sha256.Sum256([]byte(digest+extra)))
					}
				}
				key := fmt.Sprintf("core/%d", v)
				if _, exists := out[key]; exists {
					failure = fmt.Errorf("duplicate migration %s", key)
					return false
				}
				out[key] = digest
				return false
			})
			if failure != nil {
				return nil, failure
			}
		}
	}
	for _, name := range []string{"buildCoreMigrations", "buildCoreMigrationPlan"} {
		for _, plan := range in.packages[core][name] {
			out["core/plan/"+name] = controlHash(in.fset, plan.node, "Migration")
		}
	}

	// Pin the ordered catalog inputs individually. New tail entries can be
	// appended, but leaving a descriptor declaration behind cannot disguise
	// removing it from the schema generated by core v2.
	for _, catalog := range in.packages[core]["coreDescriptors"] {
		var entries []ast.Expr
		ast.Inspect(catalog.node, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CompositeLit:
				if array, ok := n.Type.(*ast.ArrayType); ok {
					if elt, ok := array.Elt.(*ast.SelectorExpr); ok && elt.Sel.Name == "EntityDescriptor" {
						entries = append(entries, n.Elts...)
						return false
					}
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "append" {
					entries = append(entries, n.Args[1:]...)
					return false
				}
			}
			return true
		})
		out["core/descriptor-registration/control"] = controlHash(in.fset, catalog.node, "EntityDescriptor")
		if len(entries) == 0 {
			return nil, fmt.Errorf("coreDescriptors: no catalog inputs")
		}
		for i, entry := range entries {
			digest, err := in.sourceHash(catalog.file, []ast.Node{entry})
			if err != nil {
				return nil, err
			}
			out[fmt.Sprintf("core/descriptor-registration/%d", i)] = digest
		}
	}

	for name, decls := range in.packages[core] {
		if !strings.HasSuffix(name, "Descriptor") {
			continue
		}
		for _, d := range decls {
			if _, ok := d.node.(*ast.ValueSpec); !ok {
				continue
			}
			digest, err := in.sourceHash(d.file, []ast.Node{d.node})
			if err != nil {
				return nil, err
			}
			out["core/descriptor/"+name] = digest
		}
	}
	for _, d := range in.packages[core]["attachSchemaTransitionHooks"] {
		digest, err := in.sourceHash(d.file, []ast.Node{d.node})
		if err != nil {
			return nil, err
		}
		out["modules/transition-hooks"] = digest
	}
	// Module transition metadata belongs to a numbered SQL migration. Hash
	// the binding declaration too: conditions, trigger identity and the current
	// definition determine what Before/After actually verify.
	for filename, source := range sources {
		if !strings.Contains(source, "SchemaTriggerTransition") || !strings.HasPrefix(filename, "modules/") {
			continue
		}
		if err := in.load(path.Dir(filename)); err != nil {
			return nil, err
		}
		file := in.files[filename]
		ordinal := map[int]int{}
		var failure error
		for _, binding := range file.tree.Decls {
			ast.Inspect(binding, func(node ast.Node) bool {
				lit, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					id, ok := kv.Key.(*ast.Ident)
					if !ok || id.Name != "MigrationVersion" {
						continue
					}
					v, err := in.version(file, kv.Value, map[string]bool{})
					if err != nil {
						failure = err
						return false
					}
					digest, err := in.sourceHash(file, []ast.Node{binding})
					if err != nil {
						failure = err
						return false
					}
					ordinal[v]++
					out[fmt.Sprintf("%s/transition/%d/%d", filename, v, ordinal[v])] = digest
					return false
				}
				return true
			})
			if failure != nil {
				return nil, failure
			}
		}
	}
	if err := in.runnerFingerprints(out); err != nil {
		return nil, err
	}
	return out, nil
}

func run(input io.Reader, output io.Writer) error {
	var sources []map[string]string
	if err := json.NewDecoder(input).Decode(&sources); err != nil {
		return err
	}
	result := make([]map[string]string, 0, len(sources))
	for _, source := range sources {
		hashes, err := fingerprints(source)
		if err != nil {
			return err
		}
		result = append(result, hashes)
	}
	return json.NewEncoder(output).Encode(result)
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migration hashes: UNVERIFIED:", err)
		os.Exit(2)
	}
}
