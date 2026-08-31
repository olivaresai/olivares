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

package main

import (
	"go/ast"
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
	var out []moduleRoute
	for _, d := range dirs {
		rs, err := routesInPackage(d, root)
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
	files     map[*ast.FuncDecl]string   // decl → repo-relative file
	registrar map[*ast.FuncDecl][]string // decl → the RouteRegistrar parameter names
}

func routesInPackage(dir, root string) ([]moduleRoute, error) {
	p := &pkg{
		dir: dir, root: root, fset: token.NewFileSet(),
		consts: map[string]string{}, methods: map[string]*ast.FuncDecl{},
		funcs: map[string]*ast.FuncDecl{}, nsByRecv: map[string]string{},
		files: map[*ast.FuncDecl]string{}, registrar: map[*ast.FuncDecl][]string{},
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
		f, err := parser.ParseFile(p.fset, name, nil, parser.ParseComments)
		if err != nil {
			return nil, blind("%s does not parse: %v", name, err)
		}
		p.collect(f, p.rel(name))
	}
	if len(p.apiRoutes) == 0 {
		return nil, nil // not a module package
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
	return p.routes()
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
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
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

func (p *pkg) routes() ([]moduleRoute, error) {
	var out []moduleRoute
	reached := map[*ast.FuncDecl]bool{}
	for _, fd := range p.apiRoutes {
		recv := recvType(fd)
		ns, ok := p.nsByRecv[recv]
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
	return out, nil
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

// callsOn returns the reg.Handle / reg.HandleEntity calls in fd's body, in source
// order, made on one of the given registrar identifiers.
func (p *pkg) callsOn(fd *ast.FuncDecl, names []string) []*ast.CallExpr {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []*ast.CallExpr
	ast.Inspect(fd, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleEntity") {
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
	entity := call.Fun.(*ast.SelectorExpr).Sel.Name == "HandleEntity"
	wantArgs, handlerIdx := 4, 3
	if entity {
		wantArgs, handlerIdx = 5, 4
	}
	if len(call.Args) != wantArgs {
		return moduleRoute{}, blind("%s:%d: a route registration with %d arguments is not the shape this gate reads",
			pos.Filename, pos.Line, len(call.Args))
	}
	method, ok := stringLit(call.Args[0])
	if !ok {
		return moduleRoute{}, blind("%s:%d: the HTTP method is computed, so the operation key cannot be derived", pos.Filename, pos.Line)
	}
	pattern, ok := stringLit(call.Args[1])
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
