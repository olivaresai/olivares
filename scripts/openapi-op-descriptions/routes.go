// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Route enumeration. The beta document is built by core/api/openapi_modules.go from
// the routes each module registers through the RouteRegistrar seam; this file reads
// the SAME registrations from the source with go/parser and resolves each one to the
// handler it mounts, so the description can come from the prose next to the handler.
//
// WHY AN AST AND NOT A GREP. The question here is "which function does this route
// mount, and what does its doc comment say" — a grep can find the string
// `reg.Handle("GET", "/spend"` but cannot tell `m.handleSpend` on *Module in
// modules/finops from `m.handleSpend` on *AgentsConsole in modules/governance, and
// four types in one package (governance: Module, AgentsConsole, PolicyConsole,
// IdentityConsole) is not a hypothetical here.
//
// AND IT FAILS CLOSED, TWICE OVER: a registration this file cannot read (a computed
// method, a computed pattern, a registrar handed to a helper) is reported by name
// instead of skipped, and compose() then requires the set it produced to EQUAL the
// operation set of the committed beta document.
//
// IT READS THE BUILD THE DOCUMENTS DESCRIBE. A file the build excludes registers nothing,
// so files are selected by their `//go:build` line for linux/amd64 without cgo plus the
// tags the caller gives (-tags, OLIVARES_BUILD_TAGS). With none that is the Community
// binary, which is what the committed documents are dumped from; an assembled Business
// tree holds both editions of a method side by side, and reading both made whichever
// sorted last hide the other. A provider that wraps a module (`struct{ *governance.Module }`)
// mounts its extra routes under the namespace Go promotes from it: that one level is
// read, and any other shape is still refused by name.
//
// IT READS EVERY DOOR THE RECORDING REGISTRAR ANSWERS. core/api's recording registrar
// publishes Handle, HandleEntity and the two governed doors, HandlePolicy and
// HandleSealed, which a module reaches through a registrar found by type assertion
// (`door, ok := reg.(sealedRegistrar)`). Reading only the first two left a governed
// route in the document and out of this roster, so the gate stopped at "the document
// and the routes disagree" instead of naming the handler whose prose was missing.

package main

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// moduleRoute is one route a module registers: the facts the beta document is built
// from, plus the doc comment of the handler it mounts.
type moduleRoute struct {
	ns      string
	method  string
	pattern string // module-relative chi pattern, e.g. "/spend" or "/{id}"
	file    string // repo-relative, for messages
	line    int

	handler     string // "(*Module).handleSpend", "" when it could not be resolved
	handlerName string // "handleSpend" — the name a Go doc comment must open with
	handlerPos  string // "modules/finops/api.go:117"
	doc         string // the handler's Go doc comment, "" when it has none
}

// key is the operation key of this route: the same "<METHOD> <spec path>" the
// published beta document uses. specPath mirrors moduleSpecPath +
// canonicalRoutePattern in core/api — the two must agree or compose() fails closed.
func (r moduleRoute) key() string { return r.method + " " + r.specPath() }

func (r moduleRoute) specPath() string {
	p := betaPathPrefix + r.ns + r.pattern
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// where names the handler for an error message, or the registration when the handler
// could not be resolved.
func (r moduleRoute) where() string {
	if r.handler == "" {
		return "the handler registered at " + r.file + ":" + strconv.Itoa(r.line)
	}
	return r.handler + " (" + r.handlerPos + ")"
}

// registrationShapes is every door a module registers a route through, with its
// argument count and the index of the handler it mounts. The method and the pattern
// are always the first two arguments.
var registrationShapes = map[string]struct{ args, handler int }{
	"Handle":       {args: 4, handler: 3},
	"HandleSystem": {args: 3, handler: 2},
	"HandleEntity": {args: 5, handler: 4},
	"HandlePolicy": {args: 5, handler: 4},
	"HandleSealed": {args: 5, handler: 4},
}

// governedDoors are the doors a module finds by type assertion. Their names mean a
// route registration and nothing else, so a call to one that no APIRoutes registrar
// reaches is refused rather than skipped.
var governedDoors = map[string]bool{"HandlePolicy": true, "HandleSealed": true, "HandleSystem": true}

// enumerateModuleRoutes reads every module package under dir and returns the routes
// its APIRoutes methods register.
func enumerateModuleRoutes(root string) ([]moduleRoute, error) {
	dir := filepath.Join(root, modulesDirRel)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, blind("%s is not a directory (%v), so no module route could be enumerated", dir, err)
	}
	dirs, err := packageDirs(dir)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return nil, blind("%s contains no Go package", dir)
	}
	// Composition-root modules can own deployment-wide routes while retaining
	// the same API module seam. Discover their packages without naming features.
	cmdDir := filepath.Join(root, "cmd")
	if info, err := os.Stat(cmdDir); err == nil && info.IsDir() {
		additional, err := packageDirs(cmdDir)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, additional...)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, blind("listing composition-root packages: %v", err)
	}
	// Every package is read before any is resolved: a provider mounts routes under the
	// namespace of the module it embeds, and that module is another package.
	peers := map[string]*pkg{}
	var loaded []*pkg
	for _, d := range dirs {
		p, err := loadPackage(d, root)
		if err != nil {
			return nil, err
		}
		peers[filepath.ToSlash(p.rel(d))] = p
		loaded = append(loaded, p)
	}
	var out []moduleRoute
	for _, p := range loaded {
		p.peers = peers
		if len(p.apiRoutes) == 0 {
			continue // not a module package
		}
		rs, err := p.routes()
		if err != nil {
			return nil, err
		}
		out = append(out, rs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out, nil
}

func packageDirs(root string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Vendored or generated trees are not module packages; testdata is not
			// compiled into the binary either.
			if name := d.Name(); name == "testdata" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			seen[filepath.Dir(p)] = true
		}
		return nil
	})
	if err != nil {
		return nil, blind("walking %s: %v", root, err)
	}
	var out []string
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

// pkg is one parsed module package: what a route registration has to be resolved
// against.
type pkg struct {
	dir       string
	root      string
	fset      *token.FileSet
	consts    map[string]string          // package-level string constants
	methods   map[string]*ast.FuncDecl   // "recvType.Name"
	funcs     map[string]*ast.FuncDecl   // package-level funcs
	apiRoutes []*ast.FuncDecl            // the APIRoutes methods
	apiNS     []*ast.FuncDecl            // the APINamespace methods
	nsByRecv  map[string]string          // receiver type name → API namespace
	embeds    map[string][]embedded      // struct type name → its anonymous fields
	topConsts map[*ast.ValueSpec]bool    // package-level const specs
	peers     map[string]*pkg            // every walked package by repo-relative directory
	files     map[*ast.FuncDecl]string   // decl → repo-relative file
	registrar map[*ast.FuncDecl][]string // decl → the RouteRegistrar parameter names
}

// embedded is one anonymous field of a struct: the type its method set is promoted from.
type embedded struct {
	importPath string // "" for a type of the same package
	name       string
}

// selected reports whether the build the published documents describe compiles the
// file: linux/amd64 without cgo (the product is one static binary) plus buildTags. A
// file outside that build registers nothing, so two files that declare the same method
// for different editions are never both read.
func selected(dir, name string) (bool, error) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = "linux", "amd64", false
	ctx.BuildTags = buildTags
	return ctx.MatchFile(dir, name)
}

func loadPackage(dir, root string) (*pkg, error) {
	p := &pkg{
		dir: dir, root: root, fset: token.NewFileSet(),
		consts: map[string]string{}, methods: map[string]*ast.FuncDecl{},
		funcs: map[string]*ast.FuncDecl{}, nsByRecv: map[string]string{},
		embeds:    map[string][]embedded{},
		topConsts: map[*ast.ValueSpec]bool{},
		files:     map[*ast.FuncDecl]string{}, registrar: map[*ast.FuncDecl][]string{},
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, blind("listing %s: %v", dir, err)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		ok, err := selected(dir, filepath.Base(name))
		if err != nil {
			return nil, blind("%s: reading its build constraint: %v", p.rel(name), err)
		}
		if !ok {
			continue
		}
		f, err := parser.ParseFile(p.fset, name, nil, parser.ParseComments)
		if err != nil {
			return nil, blind("%s does not parse: %v", name, err)
		}
		p.collect(f, p.rel(name))
	}
	// Namespaces are resolved only after EVERY file of the package is parsed: the
	// constant an APINamespace returns is routinely declared in another file of the
	// same package (modules/capabilities/api.go returns Namespace, declared in
	// capabilities.go), and resolving it as each file is read makes the answer depend
	// on which file came first alphabetically.
	for _, fd := range p.apiNS {
		if ns, ok := p.namespaceOf(fd); ok {
			p.nsByRecv[recvType(fd)] = ns
		}
	}
	return p, nil
}

// rel renders a path relative to the repository root, so every message this gate
// prints is a path a reader can paste into an editor.
func (p *pkg) rel(name string) string {
	if r, err := filepath.Rel(p.root, name); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return name
}

func (p *pkg) collect(f *ast.File, file string) {
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					p.collectEmbeds(f, ts)
				}
			}
			continue
		}
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				p.topConsts[vs] = true
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if s, ok := stringLit(vs.Values[i]); ok {
							p.consts[name.Name] = s
						}
					}
				}
			}
			continue
		}
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		p.files[fd] = file
		if fd.Recv == nil || len(fd.Recv.List) != 1 {
			p.funcs[fd.Name.Name] = fd
		} else {
			p.methods[recvType(fd)+"."+fd.Name.Name] = fd
		}
		if names := registrarParams(fd.Type); len(names) > 0 {
			p.registrar[fd] = names
		}
		if fd.Name.Name == "APIRoutes" && fd.Recv != nil {
			p.apiRoutes = append(p.apiRoutes, fd)
		}
		if fd.Name.Name == "APINamespace" && fd.Recv != nil {
			p.apiNS = append(p.apiNS, fd)
		}
	}
}

// namespaceOf reads the literal a module's APINamespace returns, following one level
// of package constant (every module in the tree spells it `return Namespace`).
func (p *pkg) namespaceOf(fd *ast.FuncDecl) (string, bool) {
	if fd.Body == nil || len(fd.Body.List) != 1 {
		return "", false
	}
	ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	if s, ok := stringLit(ret.Results[0]); ok {
		return s, true
	}
	if id, ok := ret.Results[0].(*ast.Ident); ok {
		if s, ok := p.consts[id.Name]; ok {
			return s, true
		}
	}
	return "", false
}

// collectEmbeds records the anonymous fields of a struct type, with the import path of
// the package each one is written against.
func (p *pkg) collectEmbeds(f *ast.File, ts *ast.TypeSpec) {
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return
	}
	for _, field := range st.Fields.List {
		if len(field.Names) != 0 {
			continue
		}
		t := field.Type
		if star, ok := t.(*ast.StarExpr); ok {
			t = star.X
		}
		switch t := t.(type) {
		case *ast.Ident:
			p.embeds[ts.Name.Name] = append(p.embeds[ts.Name.Name], embedded{name: t.Name})
		case *ast.SelectorExpr:
			alias, ok := t.X.(*ast.Ident)
			if !ok {
				continue
			}
			for _, imp := range f.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				local := path[strings.LastIndex(path, "/")+1:]
				if imp.Name != nil {
					local = imp.Name.Name
				}
				if local == alias.Name {
					p.embeds[ts.Name.Name] = append(p.embeds[ts.Name.Name], embedded{importPath: path, name: t.Sel.Name})
				}
			}
		}
	}
}

// peer returns the walked package an import path names: the one whose directory, under
// the module path its go.mod declares, spells exactly that path. A path that merely ends
// in the same directory names another module's package and is not resolved.
func (p *pkg) peer(importPath string) *pkg {
	for rel, q := range p.peers {
		if want, ok := importPathOf(p.root, rel); ok && want == importPath {
			return q
		}
	}
	return nil
}

// importPathOf derives the import path of the package at repo-relative directory rel from
// the nearest go.mod at or above it, inside root.
func importPathOf(root, rel string) (string, bool) {
	rel = filepath.ToSlash(rel)
	for dir := rel; ; dir = pathDir(dir) {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
					mod := strings.Trim(f[1], `"`)
					if dir == "." {
						return mod + "/" + rel, true
					}
					return mod + strings.TrimPrefix(rel, dir), true
				}
			}
			return "", false
		}
		if dir == "." {
			return "", false
		}
	}
}

func pathDir(d string) string {
	if i := strings.LastIndex(d, "/"); i >= 0 {
		return d[:i]
	}
	return "."
}

// namespace returns the API namespace a receiver mounts under: the one it declares, or
// else the one of the single module type it embeds. Go promotes APINamespace through an
// embedded field and the engine asks the outer value, so a provider that wraps a module
// to add routes mounts them beside the module's own. One level, and only a namespace
// the embedded type declares itself; two candidates or none stay unread and are refused.
func (p *pkg) namespace(recv string) (string, bool) {
	typ := strings.TrimPrefix(recv, "*")
	// The type's own APINamespace shadows any embedded one (either receiver form is in
	// its method set), so a declaration this gate could not read stays unread.
	for _, fd := range p.apiNS {
		if strings.TrimPrefix(recvType(fd), "*") == typ {
			ns, ok := p.nsByRecv[recvType(fd)]
			return ns, ok
		}
	}
	var found []string
	for _, e := range p.embeds[typ] {
		q := p
		if e.importPath != "" {
			if q = p.peer(e.importPath); q == nil {
				continue
			}
		}
		for _, key := range []string{"*" + e.name, e.name} {
			if ns, ok := q.nsByRecv[key]; ok {
				found = append(found, ns)
				break
			}
		}
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// stringValue folds what a registration spells for a method or a pattern: a literal, a
// package constant, or a `+` of those (`cbAPIPath+"/rules"`). Anything else is computed.
func (p *pkg) stringValue(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		return stringLit(x)
	case *ast.ParenExpr:
		return p.stringValue(x.X)
	case *ast.Ident:
		if x.Obj != nil {
			// Resolved inside this file: only a package-level const spec is the constant
			// p.consts holds. A parameter, a variable or a const local to a function that
			// shadows the package name is a computed value.
			if vs, ok := x.Obj.Decl.(*ast.ValueSpec); !ok || !p.topConsts[vs] || x.Obj.Kind != ast.Con {
				return "", false
			}
		}
		s, ok := p.consts[x.Name]
		return s, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := p.stringValue(x.X)
		r, rok := p.stringValue(x.Y)
		return l + r, lok && rok
	}
	return "", false
}

func (p *pkg) routes() ([]moduleRoute, error) {
	var out []moduleRoute
	reached := map[*ast.FuncDecl]bool{}
	read := map[*ast.CallExpr]bool{}
	for _, fd := range p.apiRoutes {
		recv := recvType(fd)
		ns, ok := p.namespace(recv)
		if !ok {
			return nil, blind("%s:%d: %s.APIRoutes registers routes but this gate could not read %s.APINamespace()",
				p.files[fd], p.fset.Position(fd.Pos()).Line, recv, recv)
		}
		names := registrarParams(fd.Type)
		if len(names) == 0 {
			// `func (m *Module) APIRoutes(api.RouteRegistrar) {}` — the parameter is
			// unnamed, so the module mounts nothing. Two modules do this on purpose.
			continue
		}
		calls, err := p.gather(fd, names, recv, reached)
		if err != nil {
			return nil, err
		}
		for _, c := range calls {
			r, err := p.route(ns, recvName(c.owner), recv, c.call)
			if err != nil {
				return nil, err
			}
			r.file, r.line = p.files[c.owner], p.fset.Position(c.call.Pos()).Line
			out = append(out, r)
			read[c.call] = true
		}
	}

	// A registration this walk did not reach is a route the beta document would
	// publish and this gate would never see. Nothing in the tree does it today
	// (measured 2026-08-16: 686 of 686 registrations are reachable from an APIRoutes
	// body, 13 of them through modules/reporting's one helper); if one appears, say
	// so instead of silently enumerating 686 of 687.
	for fd, names := range p.registrar {
		if reached[fd] {
			continue
		}
		if found := len(p.callsOn(fd, names)); found > 0 {
			return nil, blind("%s:%d: %s registers %d route(s) that no APIRoutes body reaches, so this gate cannot tell which namespace they mount under",
				p.files[fd], p.fset.Position(fd.Pos()).Line, fd.Name.Name, found)
		}
	}

	// A governed door called on anything but a registrar an APIRoutes body holds
	// (a door handed to a helper, stored, or declared with var) is a route the
	// document publishes and this walk did not read.
	if pos, door, ok := p.unreadGovernedCall(read); ok {
		return nil, blind("%s:%d: a route is registered through %s on a registrar no APIRoutes body holds, so this gate cannot tell which namespace it mounts under",
			pos.Filename, pos.Line, door)
	}
	return out, nil
}

// unreadGovernedCall returns the first call to a governed door, in file and line
// order, that the walk from the APIRoutes bodies did not read.
func (p *pkg) unreadGovernedCall(read map[*ast.CallExpr]bool) (token.Position, string, bool) {
	var first token.Position
	var door string
	for fd := range p.files {
		ast.Inspect(fd, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok || read[ce] {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || !governedDoors[sel.Sel.Name] {
				return true
			}
			pos := p.fset.Position(ce.Pos())
			pos.Filename = p.rel(pos.Filename)
			if door == "" || pos.Filename < first.Filename || (pos.Filename == first.Filename && pos.Line < first.Line) {
				first, door = pos, sel.Sel.Name
			}
			return true
		})
	}
	return first, door, door != ""
}

// registration is one route registration together with the function it was written
// in — which is not always APIRoutes: modules/reporting mounts 13 of its routes from a
// helper method that APIRoutes hands the registrar to.
type registration struct {
	call  *ast.CallExpr
	owner *ast.FuncDecl
}

// gather returns the route registrations reachable from fd, following a call to a
// method of the SAME receiver that is handed the registrar. It follows only that
// shape: a registrar that leaves the receiver (stored in a field, closed over by a
// value returned to a caller, passed to another package) is not followed, and the
// unreached-registration check above then refuses to certify the enumeration.
func (p *pkg) gather(fd *ast.FuncDecl, names []string, recv string, reached map[*ast.FuncDecl]bool) ([]registration, error) {
	if reached[fd] {
		return nil, nil // already walked: a helper called twice, or a cycle
	}
	reached[fd] = true
	var out []registration
	for _, c := range p.callsOn(fd, names) {
		out = append(out, registration{call: c, owner: fd})
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var ferr error
	ast.Inspect(fd, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok || ferr != nil {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); !ok || id.Name != recvName(fd) {
			return true
		}
		helper, ok := p.methods[recv+"."+sel.Sel.Name]
		if !ok {
			return true
		}
		// Only follow when the registrar itself is what is handed over.
		handed := false
		for _, a := range ce.Args {
			if id, ok := a.(*ast.Ident); ok && want[id.Name] {
				handed = true
			}
		}
		if !handed {
			return true
		}
		inner := registrarParams(helper.Type)
		if len(inner) == 0 {
			ferr = blind("%s:%d: %s is handed the route registrar through an unnamed parameter, so its registrations cannot be read",
				p.files[helper], p.fset.Position(helper.Pos()).Line, sel.Sel.Name)
			return false
		}
		more, err := p.gather(helper, inner, recv, reached)
		if err != nil {
			ferr = err
			return false
		}
		out = append(out, more...)
		return true
	})
	if ferr != nil {
		return nil, ferr
	}
	return out, nil
}

// callsOn returns the route registrations in fd's body, in source order, made on one
// of the given registrar identifiers or on a registrar asserted from one of them
// (`door, ok := reg.(sealedRegistrar)`), through any door in registrationShapes.
func (p *pkg) callsOn(fd *ast.FuncDecl, names []string) []*ast.CallExpr {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	ast.Inspect(fd, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) == 0 || len(as.Rhs) != 1 {
			return true
		}
		ta, ok := as.Rhs[0].(*ast.TypeAssertExpr)
		if !ok {
			return true
		}
		if from, ok := ta.X.(*ast.Ident); ok && want[from.Name] {
			if to, ok := as.Lhs[0].(*ast.Ident); ok && to.Name != "_" {
				want[to.Name] = true
			}
		}
		return true
	})
	var out []*ast.CallExpr
	ast.Inspect(fd, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, door := registrationShapes[sel.Sel.Name]; !door {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && want[id.Name] {
			out = append(out, ce)
		}
		return true
	})
	return out
}

func (p *pkg) route(ns, recvName, recvType string, call *ast.CallExpr) (moduleRoute, error) {
	pos := p.fset.Position(call.Pos())
	pos.Filename = p.rel(pos.Filename)
	shape := registrationShapes[call.Fun.(*ast.SelectorExpr).Sel.Name]
	wantArgs, handlerIdx := shape.args, shape.handler
	if len(call.Args) != wantArgs {
		return moduleRoute{}, blind("%s:%d: a route registration with %d arguments is not the shape this gate reads",
			pos.Filename, pos.Line, len(call.Args))
	}
	method, ok := p.stringValue(call.Args[0])
	if !ok {
		return moduleRoute{}, blind("%s:%d: the HTTP method is computed, so the operation key cannot be derived", pos.Filename, pos.Line)
	}
	pattern, ok := p.stringValue(call.Args[1])
	if !ok {
		return moduleRoute{}, blind("%s:%d: the route pattern is computed, so the operation key cannot be derived", pos.Filename, pos.Line)
	}
	r := moduleRoute{ns: ns, method: strings.ToUpper(method), pattern: pattern}

	// Resolve the handler. An unresolvable handler is NOT an error: it simply has no
	// doc comment to publish, and compose() then requires a catalog row for it by
	// name. Refusing here would make one odd registration block the whole gate.
	switch h := call.Args[handlerIdx].(type) {
	case *ast.SelectorExpr:
		if id, ok := h.X.(*ast.Ident); ok && id.Name == recvName {
			if fd, ok := p.methods[recvType+"."+h.Sel.Name]; ok {
				r.handler = "(" + recvType + ")." + h.Sel.Name
				r.handlerName = h.Sel.Name
				r.handlerPos = p.files[fd] + ":" + strconv.Itoa(p.fset.Position(fd.Pos()).Line)
				r.doc = docText(fd)
			}
		}
	case *ast.Ident:
		if fd, ok := p.funcs[h.Name]; ok {
			r.handler = h.Name
			r.handlerName = h.Name
			r.handlerPos = p.files[fd] + ":" + strconv.Itoa(p.fset.Position(fd.Pos()).Line)
			r.doc = docText(fd)
		}
	}
	return r, nil
}

func docText(fd *ast.FuncDecl) string {
	if fd.Doc == nil {
		return ""
	}
	return fd.Doc.Text()
}

// registrarParams returns the names of ft's parameters of type RouteRegistrar (the
// seam every module route is registered through).
func registrarParams(ft *ast.FuncType) []string {
	var out []string
	if ft.Params == nil {
		return nil
	}
	for _, f := range ft.Params.List {
		if !isRouteRegistrar(f.Type) {
			continue
		}
		for _, n := range f.Names {
			if n.Name != "_" {
				out = append(out, n.Name)
			}
		}
	}
	return out
}

func isRouteRegistrar(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.SelectorExpr:
		return t.Sel.Name == "RouteRegistrar"
	case *ast.Ident:
		return t.Name == "RouteRegistrar"
	}
	return false
}

func recvType(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return ""
	}
	switch t := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return "*" + id.Name
		}
	case *ast.Ident:
		return t.Name
	}
	return ""
}

func recvName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 || len(fd.Recv.List[0].Names) != 1 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

func stringLit(e ast.Expr) (string, bool) {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
