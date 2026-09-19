// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command cli-render-tabwriter answers ONE question for check-cli-render.sh: on
// which lines of the command layer does a hand-built column block start?
//
// WHY THIS ONE RULE HAS A PARSER AND THE OTHER FOUR HAVE REGEXES. `column`,
// `status`, `envelope` and `next` are LITERAL SPELLINGS in the printed line — a
// `%-12s` verb, a `[ok]` token, `failed: HTTP`, a `Next:` heading — and a line is
// the whole evidence. `table` is the only rule whose subject is a TYPE:
// *text/tabwriter.Writer, reached through whatever local name the file gave the
// package and through whatever function hands one out. A type has to be resolved,
// and resolution is not a grep. Three compiling, ordinary-Go ways past the regex
// were built on 2026-09-19 and every one of them was a resolution question:
//
//	(d) import tw "text/tabwriter"; func columns(io.Writer) *tw.Writer
//	    — the identifier `tabwriter` never appears. Renaming the PACKAGE was
//	    enough, which falsified the old header's "a signature cannot go stale".
//	(c) var columnsVar = newTabWriter; columnsVar(w)
//	    — the factory is handed around as a VALUE, so no line reads `newTabWriter(`.
//	(b) the factory lives in a sibling module of this workspace and is called as
//	    `core.Columns(w)` — a selector whose meaning is in another directory.
//
// WHAT IT PRINTS. One `<path>\t<line>\t<why>` record per line of the package that
// constructs or obtains a *tabwriter.Writer, sorted, one record per line even when
// a line does it twice: the unit of this gate is an output PATH, and the caller's
// baseline counts lines.
//
// HOW IT RESOLVES (b), and why this half and not the other. The gate could instead
// REFUSE any command-layer file that receives a *tabwriter.Writer from outside the
// package — but at the call site `tw := core.Columns(w)` the type is never written
// down, so that refusal needs type inference to know which call sites to refuse. It
// would be a guess. Reading the workspace is not: `go.work` names the modules,
// each `go.mod` names its import path, so an import that resolves inside this
// workspace resolves to a DIRECTORY this program can read, and an exported function
// there whose signature returns *tabwriter.Writer is a factory by exactly the same
// rule as a local one. One hop, deliberately: this gate counts the hand-built
// tables OF THE COMMAND LAYER, and a table built inside `core` is core's line.
//
// THE LIMIT THAT REMAINS, and it is stated here because the previous version of
// this rule claimed a limit its header never wrote down:
//
//   - A factory in a module OUTSIDE this workspace — a third-party dependency in
//     the module cache — is not read. Its import resolves to no directory here.
//   - A *tabwriter.Writer reached through an INTERFACE or a func-typed struct
//     field whose value is assigned in another package: the signature at the call
//     site names the interface, not the writer.
//   - A local variable that shadows a factory's name is counted as the factory.
//     Counting one line too many is the direction this gate errs in on purpose.
//
// THREE ANSWERS: 0 scanned · 2 CANNOT LOOK. It never returns 1: the verdict is the
// caller's, and this program only reports what it saw.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	pkg := flag.String("pkg", "cmd/olivares", "package directory to scan")
	root := flag.String("root", ".", "repository root, for go.work and go.mod resolution")
	skip := flag.String("skip", "", "directory whose files are never counted (the renderer's own package)")
	flag.Parse()

	if err := run(os.Stdout, *root, *pkg, *skip); err != nil {
		fmt.Fprintf(os.Stderr, "cli-render-tabwriter: NO HE PODIDO MIRAR: %v\n", err)
		os.Exit(2)
	}
}

// parsed is one file of the scanned package, with its syntax tree.
type parsed struct {
	path string
	file *ast.File
}

// occurrence is one line that builds or obtains a column writer.
type occurrence struct {
	path string
	line int
	why  string
}

func run(out *os.File, root, pkg, skip string) error {
	files, err := goFiles(pkg, skip)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no .go files under %s", pkg)
	}

	mods, err := workspaceModules(root)
	if err != nil {
		return err
	}

	fset := token.NewFileSet()
	// The scan is by DIRECTORY, because a bare identifier resolves inside its own
	// Go package and nowhere else. Treating `cmd/olivares` and its internal/
	// subpackages as one namespace would charge a file for a name it cannot call.
	byDir := map[string][]string{}
	for _, f := range files {
		d := filepath.Dir(f)
		byDir[d] = append(byDir[d], f)
	}

	var found []occurrence
	for _, dir := range sortedKeys(byDir) {
		occ, err := scanDir(fset, byDir[dir], mods)
		if err != nil {
			return err
		}
		found = append(found, occ...)
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].path != found[j].path {
			return found[i].path < found[j].path
		}
		return found[i].line < found[j].line
	})
	w := bufio.NewWriter(out)
	defer w.Flush()
	seen := map[string]bool{}
	for _, o := range found {
		key := fmt.Sprintf("%s\t%d", o.path, o.line)
		if seen[key] {
			continue
		}
		seen[key] = true
		fmt.Fprintf(w, "%s\t%d\t%s\n", o.path, o.line, o.why)
	}
	return nil
}

// scanDir does the two passes one Go package needs: the first learns which
// functions hand out a column writer, the second counts the lines that ask for one.
func scanDir(fset *token.FileSet, files []string, mods []module) ([]occurrence, error) {
	var trees []parsed
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		// EVERY file of the scanned package is parsed, with no prefilter, and that
		// is a correction and not an oversight: door (b) is a file that calls
		// `core.Columns(w)` and never writes the word `tabwriter` anywhere. A
		// prefilter on the bytes of this package would keep exactly that file out
		// of the walk, which is the door it exists to close. The prefilter lives
		// where it is sound instead — over the IMPORTED packages, below, where a
		// factory DECLARATION must name the tabwriter package to have its type.
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		trees = append(trees, parsed{path: path, file: f})
	}

	// PASS 1 — the factories of THIS package, by signature.
	local := map[string]bool{}
	for _, t := range trees {
		tw := tabwriterNames(t.file)
		if len(tw) == 0 {
			continue
		}
		for _, decl := range t.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil {
				continue
			}
			if returnsTabwriter(fn.Type, tw) {
				local[fn.Name.Name] = true
			}
		}
	}

	// PASS 1-bis — the factories of the workspace packages this directory imports.
	imported, err := importedFactories(trees, mods)
	if err != nil {
		return nil, err
	}

	// PASS 2 — the lines.
	var found []occurrence
	for _, t := range trees {
		found = append(found, scanFile(fset, t.path, t.file, local, imported)...)
	}
	return found, nil
}

// scanFile walks one file and reports every line that constructs or obtains a
// column writer.
func scanFile(fset *token.FileSet, path string, file *ast.File, local map[string]bool, imported map[string]map[string]bool) []occurrence {
	tw := tabwriterNames(file)
	// alias -> import path, for the selector half of the cross-package rule.
	alias := map[string]string{}
	for _, spec := range file.Imports {
		p := importPath(spec)
		if p == "" {
			continue
		}
		if imported[p] == nil {
			continue
		}
		alias[importLocalName(spec, p)] = p
	}

	declNames := map[*ast.Ident]bool{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name != nil {
			declNames[fn.Name] = true
		}
	}

	var found []occurrence
	at := func(pos token.Pos, why string) {
		found = append(found, occurrence{path: path, line: fset.Position(pos).Line, why: why})
	}

	// calls records the Fun expression of every call, so the value rule below can
	// tell `newTabWriter(w)` from `var f = newTabWriter`.
	calls := map[ast.Expr]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			calls[c.Fun] = true
		}
		return true
	})

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			if declNames[node] || !local[node.Name] {
				return true
			}
			if calls[node] {
				at(node.Pos(), "factory-call")
			} else {
				// DOOR (c): the factory handed around as a value. It is counted
				// where it is assigned or passed, because that is the last line
				// at which its name is still written down.
				at(node.Pos(), "factory-value")
			}
		case *ast.SelectorExpr:
			x, ok := node.X.(*ast.Ident)
			if !ok {
				// A method whose signature returns a column writer, called on
				// some receiver expression: `b.columns(w)`.
				if local[node.Sel.Name] && calls[node] {
					at(node.Sel.Pos(), "factory-call")
				}
				return true
			}
			if tw[x.Name] && node.Sel.Name == "NewWriter" {
				at(node.Pos(), "tabwriter-new")
				return true
			}
			if p := alias[x.Name]; p != "" && imported[p][node.Sel.Name] {
				// DOOR (b): a factory in another package of this workspace.
				if calls[node] {
					at(node.Pos(), "factory-call:"+p)
				} else {
					at(node.Pos(), "factory-value:"+p)
				}
				return true
			}
			if local[node.Sel.Name] && calls[node] {
				at(node.Sel.Pos(), "factory-call")
			}
		}
		return true
	})
	return found
}

// tabwriterNames is the set of identifiers THIS FILE uses for text/tabwriter. It
// is a set and not a string because a file may import the package twice under two
// names, and because a dot import spells the constructor with no qualifier at all.
func tabwriterNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, spec := range file.Imports {
		if importPath(spec) != "text/tabwriter" {
			continue
		}
		name := importLocalName(spec, "text/tabwriter")
		if name == "_" {
			continue
		}
		names[name] = true
	}
	return names
}

// importPath unquotes an import spec's path.
func importPath(spec *ast.ImportSpec) string {
	if spec.Path == nil {
		return ""
	}
	return strings.Trim(spec.Path.Value, `"`)
}

// importLocalName is the identifier a file uses for an import: its alias when it
// has one, otherwise the last element of the path. A dot import is reported as "."
// and the caller treats a bare `NewWriter` accordingly.
func importLocalName(spec *ast.ImportSpec, path string) string {
	if spec.Name != nil {
		return spec.Name.Name
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// returnsTabwriter reports whether a signature hands out a *tabwriter.Writer under
// any of the names this file gave the package. A dot import makes it a bare
// *Writer, which is why the bare form is accepted only when "." is in the set.
func returnsTabwriter(sig *ast.FuncType, tw map[string]bool) bool {
	if sig == nil || sig.Results == nil {
		return false
	}
	for _, field := range sig.Results.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		switch t := star.X.(type) {
		case *ast.SelectorExpr:
			if x, ok := t.X.(*ast.Ident); ok && tw[x.Name] && t.Sel.Name == "Writer" {
				return true
			}
		case *ast.Ident:
			if tw["."] && t.Name == "Writer" {
				return true
			}
		}
	}
	return false
}

// module is one `use` entry of the workspace: the import path its go.mod declares
// and the directory it lives in.
type module struct {
	path string
	dir  string
}

// workspaceModules reads go.work, then each named module's go.mod. A repository
// with no go.work is a repository with no sibling modules, so an empty list is a
// complete answer there and not a missing one.
func workspaceModules(root string) ([]module, error) {
	work := filepath.Join(root, "go.work")
	src, err := os.ReadFile(work)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", work, err)
	}
	var dirs []string
	inBlock := false
	for _, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "use (":
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			dirs = append(dirs, line)
		case strings.HasPrefix(line, "use "):
			dirs = append(dirs, strings.TrimSpace(strings.TrimPrefix(line, "use ")))
		}
	}
	var mods []module
	for _, d := range dirs {
		gomod := filepath.Join(root, filepath.Clean(d), "go.mod")
		body, err := os.ReadFile(gomod)
		if err != nil {
			// A `use` entry whose module cannot be read is the one case this
			// program refuses: the workspace says a sibling is there, so silently
			// dropping it would reopen door (b) without saying a word.
			return nil, fmt.Errorf("go.work names %s and its go.mod is unreadable: %w", d, err)
		}
		path := modulePath(body)
		if path == "" {
			return nil, fmt.Errorf("%s declares no module path", gomod)
		}
		mods = append(mods, module{path: path, dir: filepath.Join(root, filepath.Clean(d))})
	}
	// Longest import path first, so `…/core/api` wins over `…/core`.
	sort.Slice(mods, func(i, j int) bool { return len(mods[i].path) > len(mods[j].path) })
	return mods, nil
}

func modulePath(gomod []byte) string {
	for _, raw := range strings.Split(string(gomod), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// importedFactories reads every workspace package these files import and returns,
// per import path, the EXPORTED functions whose signature hands out a column
// writer. An import that resolves to no directory in this workspace is an external
// dependency: outside this gate's reach, and named as such in the header.
func importedFactories(trees []parsed, mods []module) (map[string]map[string]bool, error) {
	if len(mods) == 0 {
		return nil, nil
	}
	paths := map[string]bool{}
	for _, t := range trees {
		for _, spec := range t.file.Imports {
			if p := importPath(spec); p != "" {
				paths[p] = true
			}
		}
	}
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, p := range sortedSet(paths) {
		dir := resolve(p, mods)
		if dir == "" {
			continue
		}
		names, err := exportedFactories(fset, dir)
		if err != nil {
			return nil, err
		}
		if len(names) > 0 {
			out[p] = names
		}
	}
	return out, nil
}

// resolve maps an import path to a directory of this workspace, or "" when the
// path belongs to a module this workspace does not contain.
func resolve(path string, mods []module) string {
	for _, m := range mods {
		if path == m.path {
			return m.dir
		}
		if strings.HasPrefix(path, m.path+"/") {
			return filepath.Join(m.dir, filepath.FromSlash(strings.TrimPrefix(path, m.path+"/")))
		}
	}
	return ""
}

func exportedFactories(fset *token.FileSet, dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A package that is imported and not on disk is a build failure, not this
		// gate's business; it reports nothing rather than refusing.
		return nil, nil
	}
	names := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if !bytes.Contains(src, []byte("tabwriter")) {
			continue
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		tw := tabwriterNames(f)
		if len(tw) == 0 {
			continue
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || !fn.Name.IsExported() {
				continue
			}
			if returnsTabwriter(fn.Type, tw) {
				names[fn.Name.Name] = true
			}
		}
	}
	return names, nil
}

// goFiles lists the non-test .go files under pkg, skipping one directory subtree.
func goFiles(pkg, skip string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(pkg, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip != "" && (path == skip || strings.HasPrefix(path, skip+string(filepath.Separator))) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", pkg, err)
	}
	sort.Strings(out)
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedSet(m map[string]bool) []string { return sortedKeys(m) }
