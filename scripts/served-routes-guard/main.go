// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command served-routes-guard prints the routes the control-plane binary actually
// serves, read with Go's own parser: the http.Server whose Addr is cfg.ListenAddr in
// func main, the variable it is given as Handler, and — following exactly one
// delegation hop into a function of the same module — the ServeMux that function
// RETURNS. It exists because the first reader (2026-09-06, independent review of
// cf45f7f699) credited every ServeMux declared in the router function and searched a
// comment-stripped text; and the second (review of 9b2a26baa1) visited every call node
// below the body, so `if false { mux.Handle(...) }` and an uncalled
// `func() { mux.Handle(...) }` still counted as served while a compiled httptest got 404.
//
// The supported execution model is deliberately small and is a straight line:
//   - only TOP-LEVEL statements of the setup body are read, in source order;
//   - the mux is defined once, at top level, by a bare `x := http.NewServeMux()` (or
//     `var x = http.NewServeMux()`);
//   - a registration is a top-level statement that is exactly `x.Handle(...)` or
//     `x.HandleFunc(...)`, after the definition and before the boundary, whose only
//     mention of x is the receiver; its pattern must be a string literal;
//   - the boundary is, in a router function, the terminating `return x` that must be the
//     LAST top-level statement (any other return is a second path); in main, the top-level
//     statement `s := &http.Server{...}` that binds x as the Handler of the selected server;
//   - control transfers are read BEFORE any mux reasoning, on every statement: a `goto`
//     or a label anywhere outside function literals (a jump can skip a top-level
//     registration without naming the mux — the root review of ee185bda96 showed
//     `goto afterHealth` around the health registration passing as served), and an
//     unconditional top-level `panic(...)` / `os.Exit(...)` before the boundary, put the
//     body OUTSIDE the model. `break`, `continue` and `fallthrough` cannot leave the loop
//     or switch that contains them, and a registration inside one is already outside;
//   - the selected server is a tracked variable, not a literal seen once: the one
//     `http.Server` literal whose Addr is cfg.ListenAddr must be bound by a top-level
//     `s := &http.Server{...}` (or `s := http.Server{...}`, `var s = ...`), and EVERY
//     later use of s must be a method call on s (`s.ListenAndServe()`, `s.Shutdown(ctx)`,
//     also inside a goroutine or an if-init). A field assignment (`s.Handler = ...`), a
//     reassignment, a copy, an argument to another call, or a literal built inside an
//     if/func literal is outside the model — the same root review replaced the Handler
//     after the literal and the reader still credited the mux;
//   - statements that never mention x are skipped ONLY after the transfer check (they
//     cannot register on x); a statement that mentions x in any other way — inside an if,
//     for, switch, select, defer, go, block, label, a function literal (defining one
//     executes nothing), a reassignment, an argument to another call, a registration
//     after the boundary or before the definition — puts the body OUTSIDE the model: the
//     helper answers UNSUPPORTED naming the line and the context, never a route list.
//
// Defining a function literal does not run it; a conditional does not run its body;
// text in comments or strings has no AST node; a jump is not proven taken or not taken.
// None of those is a registration here, and none is a proof of absence either. What the
// model does NOT claim: termination of main, TLS or middleware semantics, or that a
// method of *http.Server cannot alter the handler — net/http's methods do not, and that
// is stated as the limit the "method call" allowance rests on.
// The helper never decides. The caller turns facts into a verdict.
//
// Usage: served-routes-guard <repo-root> <module-dir> <main.go>   (paths under repo-root)
// stdout, tab-separated:
//
//	WHERE     <file>:<func>          where the served registrations were read
//	RETURNED  <mux var> | fresh       the mux the router returns ("fresh" = a bare http.NewServeMux())
//	ROUTE     <pattern>  <handler>    one per registration on the straight-line path, in source order
//	OPAQUE    <n>                     registrations whose pattern is not a string literal
//	TLS       0|1                     ListenAndServeTLS or X509KeyPair referenced in main.go
//
// exit 0 facts printed · 2 unreadable or outside the supported model (reason on stderr) · 3 usage
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var fset = token.NewFileSet()

func die(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(code)
}

func printExpr(e ast.Node) string {
	if e == nil {
		return "<nil>"
	}
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, e); err != nil {
		return "<unprintable>"
	}
	return b.String()
}

// canon removes whitespace outside string literals so a call laid out over several
// lines prints the same as its one-line form.
func canon(s string) string {
	var b strings.Builder
	inStr, inRaw, esc := false, false, false
	for _, r := range s {
		switch {
		case inRaw:
			b.WriteRune(r)
			if r == '`' {
				inRaw = false
			}
		case inStr:
			b.WriteRune(r)
			if esc {
				esc = false
			} else if r == '\\' {
				esc = true
			} else if r == '"' {
				inStr = false
			}
		case r == '"':
			inStr = true
			b.WriteRune(r)
		case r == '`':
			inRaw = true
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isSel(e ast.Expr, x, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == x && s.Sel.Name == sel
}

func isCall(e ast.Expr, x, sel string) bool {
	c, ok := e.(*ast.CallExpr)
	return ok && len(c.Args) == 0 && isSel(c.Fun, x, sel)
}

func funcDecl(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

type route struct{ pattern, handler string }

// use is one occurrence of the tracked identifier as a variable, with its context.
type use struct {
	id        *ast.Ident
	parent    ast.Node
	grand     ast.Node
	inFuncLit bool
}

// usesOf lists every occurrence of the variable `name` inside node. A selector member
// or a struct key spelled the same is not the variable.
func usesOf(node ast.Node, name string) []use {
	var stack []ast.Node
	var out []use
	funcLits := 0
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			if _, ok := stack[len(stack)-1].(*ast.FuncLit); ok {
				funcLits--
			}
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if _, ok := n.(*ast.FuncLit); ok {
			funcLits++
		}
		id, ok := n.(*ast.Ident)
		if !ok || id.Name != name {
			return true
		}
		var parent, grand ast.Node
		if len(stack) >= 2 {
			parent = stack[len(stack)-2]
		}
		if len(stack) >= 3 {
			grand = stack[len(stack)-3]
		}
		if sel, ok := parent.(*ast.SelectorExpr); ok && sel.Sel == id {
			return true
		}
		if kv, ok := parent.(*ast.KeyValueExpr); ok && kv.Key == id {
			return true
		}
		out = append(out, use{id, parent, grand, funcLits > 0})
		return true
	})
	return out
}

func line(n ast.Node) int { return fset.Position(n.Pos()).Line }

// short prints a statement on one line, whitespace collapsed outside strings, for a message.
func short(n ast.Node) string {
	var b strings.Builder
	inStr, inRaw, esc, space := false, false, false, false
	for _, r := range printExpr(n) {
		switch {
		case inRaw:
			b.WriteRune(r)
			if r == '`' {
				inRaw = false
			}
		case inStr:
			b.WriteRune(r)
			if esc {
				esc = false
			} else if r == '\\' {
				esc = true
			} else if r == '"' {
				inStr = false
			}
		case r == '"':
			inStr, space = true, false
			b.WriteRune(r)
		case r == '`':
			inRaw, space = true, false
			b.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if !space && b.Len() > 0 {
				b.WriteRune(' ')
				space = true
			}
		default:
			space = false
			b.WriteRune(r)
		}
	}
	t := strings.TrimSpace(b.String())
	if len(t) > 90 {
		t = t[:90] + "…"
	}
	return t
}

// definitionOf reports whether st is exactly `mux := http.NewServeMux()` or
// `var mux = http.NewServeMux()`.
func definitionOf(st ast.Stmt, mux string) bool {
	switch s := st.(type) {
	case *ast.AssignStmt:
		if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
			if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name == mux && isCall(s.Rhs[0], "http", "NewServeMux") {
				return true
			}
		}
	case *ast.DeclStmt:
		if gd, ok := s.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR && len(gd.Specs) == 1 {
			if vs, ok := gd.Specs[0].(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == mux &&
				len(vs.Values) == 1 && isCall(vs.Values[0], "http", "NewServeMux") {
				return true
			}
		}
	}
	return false
}

// registrationOf returns the call when st is exactly `mux.Handle(...)` / `mux.HandleFunc(...)`.
func registrationOf(st ast.Stmt, mux string) *ast.CallExpr {
	es, ok := st.(*ast.ExprStmt)
	if !ok {
		return nil
	}
	c, ok := es.X.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
		return nil
	}
	if id, ok := sel.X.(*ast.Ident); !ok || id.Name != mux {
		return nil
	}
	return c
}

func stmtKind(st ast.Stmt) string {
	switch st.(type) {
	case *ast.IfStmt:
		return "if"
	case *ast.ForStmt:
		return "for"
	case *ast.RangeStmt:
		return "for range"
	case *ast.SwitchStmt, *ast.TypeSwitchStmt:
		return "switch"
	case *ast.SelectStmt:
		return "select"
	case *ast.DeferStmt:
		return "defer"
	case *ast.GoStmt:
		return "go"
	case *ast.BlockStmt:
		return "bloque anidado"
	case *ast.LabeledStmt:
		return "etiqueta"
	case *ast.AssignStmt:
		return "asignacion"
	case *ast.DeclStmt:
		return "declaracion"
	case *ast.ExprStmt:
		return "expresion"
	case *ast.ReturnStmt:
		return "return"
	}
	return fmt.Sprintf("%T", st)
}

// firstReturn finds a return statement inside st outside function literals.
func firstReturn(st ast.Stmt) *ast.ReturnStmt {
	var found *ast.ReturnStmt
	ast.Inspect(st, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if r, ok := n.(*ast.ReturnStmt); ok {
			found = r
			return false
		}
		return true
	})
	return found
}

// firstTransfer finds, outside function literals, a goto or a label inside st.
func firstTransfer(st ast.Stmt) (ast.Node, string) {
	var found ast.Node
	var what string
	ast.Inspect(st, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if x.Tok == token.GOTO {
				found, what = x, "goto "+x.Label.Name
				return false
			}
		case *ast.LabeledStmt:
			found, what = x, "etiqueta "+x.Label.Name+":"
			return false
		}
		return true
	})
	return found, what
}

// terminates reports whether st is an unconditional top-level `panic(...)` or `os.Exit(...)`.
func terminates(st ast.Stmt) bool {
	es, ok := st.(*ast.ExprStmt)
	if !ok {
		return false
	}
	c, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "panic" {
		return true
	}
	return isSel(c.Fun, "os", "Exit")
}

// serverBinding recognizes a top-level `s := &http.Server{...}` / `s := http.Server{...}` /
// `var s = ...` and returns the variable and the literal.
func serverBinding(st ast.Stmt) (string, *ast.CompositeLit) {
	var name string
	var rhs ast.Expr
	switch s := st.(type) {
	case *ast.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
			return "", nil
		}
		id, ok := s.Lhs[0].(*ast.Ident)
		if !ok {
			return "", nil
		}
		name, rhs = id.Name, s.Rhs[0]
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR || len(gd.Specs) != 1 {
			return "", nil
		}
		vs, ok := gd.Specs[0].(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
			return "", nil
		}
		name, rhs = vs.Names[0].Name, vs.Values[0]
	default:
		return "", nil
	}
	if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.AND {
		rhs = u.X
	}
	cl, ok := rhs.(*ast.CompositeLit)
	if !ok || !isSel(cl.Type, "http", "Server") {
		return "", nil
	}
	return name, cl
}

// serverHandler returns the Handler value of an http.Server literal, or nil.
func serverHandler(cl *ast.CompositeLit) ast.Expr {
	for _, e := range cl.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Handler" {
				return kv.Value
			}
		}
	}
	return nil
}

// enclosingTop returns the top-level statement of body that contains pos.
func enclosingTop(body *ast.BlockStmt, pos token.Pos) ast.Stmt {
	for _, st := range body.List {
		if st.Pos() <= pos && pos < st.End() {
			return st
		}
	}
	return nil
}

// mainPathReason checks the selected server's setup path before dispatching to
// either the inline-mux or delegated-router reader. Transfers do not need to name
// the handler to make that path unsupported. Conditional termination and function
// literal execution remain outside this deliberately small model.
func mainPathReason(body *ast.BlockStmt, boundary ast.Stmt) string {
	for _, st := range body.List {
		if n, what := firstTransfer(st); n != nil {
			return fmt.Sprintf("transferencia de control en la linea %d (%s) dentro de main: el camino lineal no se puede seguir, se registre o no el mux", line(n), what)
		}
		if st.Pos() >= boundary.Pos() {
			continue
		}
		_, returns := st.(*ast.ReturnStmt)
		if returns || terminates(st) {
			return fmt.Sprintf("main termina en la linea %d (%s) antes de la frontera del servidor seleccionado", line(st), short(st))
		}
	}
	return ""
}

// associates reports whether st is a top-level server binding whose Handler is exactly
// the mux identifier and whose only mention of the mux is that Handler value.
func associates(st ast.Stmt, mux string, uses []use) bool {
	_, cl := serverBinding(st)
	if cl == nil || len(uses) != 1 {
		return false
	}
	h, ok := serverHandler(cl).(*ast.Ident)
	return ok && h == uses[0].id
}

// walkStraight reads, over the TOP-LEVEL statements of body in order, the registrations
// the tracked mux receives on the straight-line path. mode "router": the path ends with
// the terminating return, which must be the last statement (the caller has already read
// what it returns). mode "main": the path ends where an http.Server literal first takes
// the mux as its Handler. Any other mention of the mux puts the body outside the model.
func walkStraight(fn string, body *ast.BlockStmt, mux, mode string) (routes []route, opaque int, defined bool, reason string) {
	associated := 0
	last := len(body.List) - 1
	for i, st := range body.List {
		if n, what := firstTransfer(st); n != nil {
			return nil, 0, defined, fmt.Sprintf("transferencia de control en la linea %d (%s) dentro de %s: el camino lineal no se puede seguir, se registre o no el mux", line(n), what, fn)
		}
		if terminates(st) && (mode == "router" || associated == 0) {
			return nil, 0, defined, fmt.Sprintf("%s termina en la linea %d (%s) antes de la frontera", fn, line(st), short(st))
		}
		uses := usesOf(st, mux)
		if _, ok := st.(*ast.ReturnStmt); ok {
			if mode == "router" {
				if i != last {
					return nil, 0, defined, fmt.Sprintf("func %s tiene un return en la linea %d que no es su ultima sentencia: el orden de registro no es lineal", fn, line(st))
				}
				continue
			}
			if associated == 0 {
				return nil, 0, defined, fmt.Sprintf("main termina en la linea %d antes de asociar %s a un http.Server", line(st), mux)
			}
			continue
		}
		if definitionOf(st, mux) {
			if defined {
				return nil, 0, defined, fmt.Sprintf("%s se define dos veces en %s (linea %d)", mux, fn, line(st))
			}
			defined = true
			continue
		}
		if c := registrationOf(st, mux); c != nil && len(uses) == 1 {
			if !defined {
				return nil, 0, defined, fmt.Sprintf("registro en la linea %d antes de definir %s", line(st), mux)
			}
			if associated != 0 {
				return nil, 0, defined, fmt.Sprintf("registro en la linea %d despues de asociar %s a un http.Server (linea %d): orden fuera del modelo lineal", line(st), mux, associated)
			}
			if len(c.Args) == 0 {
				opaque++
				continue
			}
			lit, ok := c.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				opaque++
				continue
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				opaque++
				continue
			}
			h := ""
			if len(c.Args) > 1 {
				h = canon(printExpr(c.Args[1]))
			}
			routes = append(routes, route{pattern, h})
			continue
		}
		if len(uses) == 0 {
			if mode == "router" {
				if r := firstReturn(st); r != nil {
					return nil, 0, defined, fmt.Sprintf("func %s devuelve por mas de un camino (return en la linea %d dentro de un %s): ambiguo para este gate", fn, line(r), stmtKind(st))
				}
			}
			continue
		}
		if mode == "main" && associates(st, mux, uses) {
			if !defined {
				return nil, 0, defined, fmt.Sprintf("%s se asocia a un http.Server en la linea %d antes de definirse", mux, line(st))
			}
			if associated == 0 {
				associated = line(st)
			}
			continue
		}
		u := uses[0]
		ctx := stmtKind(st)
		if u.inFuncLit {
			ctx = "funcion anonima (definirla no la ejecuta)"
		}
		return nil, 0, defined, fmt.Sprintf("el mux `%s` se escapa del modelo de ejecucion lineal en la linea %d, contexto %s (%s); no se acredita como servido", mux, line(u.id), ctx, short(st))
	}
	if mode == "main" && associated == 0 {
		return nil, 0, defined, fmt.Sprintf("%s nunca se asocia a un http.Server como Handler en el camino lineal de main", mux)
	}
	return routes, opaque, defined, ""
}

func mentions(f *ast.File, name string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func resolveImport(f *ast.File, pkg string) string {
	for _, s := range f.Imports {
		p, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if s.Name != nil {
			name = s.Name.Name
		}
		if name == pkg {
			return p
		}
	}
	return ""
}

func main() {
	if len(os.Args) != 4 {
		die(3, "usage: served-routes-guard <repo-root> <module-dir> <main.go>")
	}
	root, modRel, mainRel := os.Args[1], os.Args[2], os.Args[3]
	mainPath := filepath.Join(root, mainRel)
	mainFile, err := parser.ParseFile(fset, mainPath, nil, 0)
	if err != nil {
		die(2, "no puedo parsear %s: %v", mainRel, err)
	}
	mainFn := funcDecl(mainFile, "main")
	if mainFn == nil {
		die(2, "%s no declara func main", mainRel)
	}

	// 1. The one http.Server literal with Addr cfg.ListenAddr, wherever it is written.
	var lits []*ast.CompositeLit
	ast.Inspect(mainFn.Body, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok || !isSel(cl.Type, "http", "Server") {
			return true
		}
		for _, e := range cl.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Addr" && canon(printExpr(kv.Value)) == "cfg.ListenAddr" {
					lits = append(lits, cl)
				}
			}
		}
		return true
	})
	if len(lits) != 1 {
		die(2, "encuentro %d http.Server con Addr: cfg.ListenAddr en %s y esperaba exactamente uno", len(lits), mainRel)
	}
	lit := lits[0]
	// 2. It must be bound by a top-level simple statement `s := &http.Server{...}`: a literal
	//    built inside an if, a loop or a function literal is a conditional construction.
	var srvName string
	var srvDef ast.Stmt
	for _, st := range mainFn.Body.List {
		if name, cl := serverBinding(st); cl == lit {
			srvName, srvDef = name, st
		}
	}
	if srvDef == nil {
		top := enclosingTop(mainFn.Body, lit.Pos())
		ctx := "fuera de main"
		if top != nil {
			ctx = stmtKind(top) + " (" + short(top) + ")"
		}
		die(2, "el http.Server de cfg.ListenAddr (linea %d) no se construye en una asignacion simple del nivel superior de main, sino dentro de %s: construccion condicional, fuera del modelo lineal", line(lit), ctx)
	}
	if reason := mainPathReason(mainFn.Body, srvDef); reason != "" {
		die(2, "%s", reason)
	}
	hid, ok := serverHandler(lit).(*ast.Ident)
	if !ok {
		die(2, "el Handler del servidor en cfg.ListenAddr no es una variable simple (%s); este gate no sabe seguirlo", canon(printExpr(serverHandler(lit))))
	}
	// 3. The binding is kept: every other use of the server variable is a method call on it.
	//    net/http's methods do not replace Handler; a field assignment, a reassignment, a
	//    copy or an argument to another call could, and is outside the model.
	for _, u := range usesOf(mainFn.Body, srvName) {
		if u.id.Pos() >= srvDef.Pos() && u.id.Pos() < srvDef.End() {
			if _, ok := u.parent.(*ast.AssignStmt); ok {
				continue
			}
			if _, ok := u.parent.(*ast.ValueSpec); ok {
				continue
			}
		}
		if sel, ok := u.parent.(*ast.SelectorExpr); ok && sel.X == ast.Expr(u.id) {
			if c, ok := u.grand.(*ast.CallExpr); ok && c.Fun == ast.Expr(sel) {
				continue
			}
		}
		top := enclosingTop(mainFn.Body, u.id.Pos())
		ctx := stmtKind(top)
		if u.inFuncLit {
			ctx = "funcion anonima"
		}
		die(2, "el servidor `%s` se usa fuera del modelo en la linea %d, contexto %s (%s): la asociacion Handler del servidor seleccionado no se conserva", srvName, line(u.id), ctx, short(top))
	}
	tls := 0
	if mentions(mainFile, "ListenAndServeTLS") || mentions(mainFile, "X509KeyPair") {
		tls = 1
	}

	// 2. The single, top-level binding of that variable in main.
	var rhs ast.Expr
	binds := 0
	for _, u := range usesOf(mainFn.Body, hid.Name) {
		switch p := u.parent.(type) {
		case *ast.AssignStmt:
			for _, l := range p.Lhs {
				if l == ast.Expr(u.id) {
					binds++
				}
			}
		case *ast.ValueSpec:
			for _, n := range p.Names {
				if n == u.id {
					binds++
				}
			}
		}
	}
	for _, st := range mainFn.Body.List {
		switch s := st.(type) {
		case *ast.AssignStmt:
			if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok && id.Name == hid.Name {
					rhs = s.Rhs[0]
				}
			}
		case *ast.DeclStmt:
			if gd, ok := s.Decl.(*ast.GenDecl); ok && len(gd.Specs) == 1 {
				if vs, ok := gd.Specs[0].(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == hid.Name && len(vs.Values) == 1 {
					rhs = vs.Values[0]
				}
			}
		}
	}
	if binds != 1 || rhs == nil {
		die(2, "%s se asigna %d vez/veces en func main (esperaba una asignacion simple en el nivel superior)", hid.Name, binds)
	}

	var routes []route
	var opaque int
	var where, returned string
	switch {
	case isCall(rhs, "http", "NewServeMux"):
		where, returned = mainRel+":main", hid.Name
		var reason string
		routes, opaque, _, reason = walkStraight("main", mainFn.Body, hid.Name, "main")
		if reason != "" {
			die(2, "%s", reason)
		}
	default:
		// The delegated handler: in main it may only be defined and handed to a server.
		for _, st := range mainFn.Body.List {
			uses := usesOf(st, hid.Name)
			if len(uses) == 0 {
				continue
			}
			if as, ok := st.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(uses) == 1 && as.Lhs[0] == ast.Expr(uses[0].id) {
				continue
			}
			if ds, ok := st.(*ast.DeclStmt); ok && len(uses) == 1 {
				if gd, ok := ds.Decl.(*ast.GenDecl); ok && len(gd.Specs) == 1 {
					if vs, ok := gd.Specs[0].(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0] == uses[0].id {
						continue
					}
				}
			}
			if associates(st, hid.Name, uses) {
				continue
			}
			ctx := stmtKind(st)
			if uses[0].inFuncLit {
				ctx = "funcion anonima (definirla no la ejecuta)"
			}
			die(2, "el handler `%s` se usa fuera del modelo lineal de main en la linea %d, contexto %s (%s)", hid.Name, line(uses[0].id), ctx, short(st))
		}
		call, ok := rhs.(*ast.CallExpr)
		var sel *ast.SelectorExpr
		var pkgID *ast.Ident
		if ok {
			sel, ok = call.Fun.(*ast.SelectorExpr)
		}
		if ok {
			pkgID, ok = sel.X.(*ast.Ident)
		}
		if !ok {
			die(2, "%s se construye con %s en %s y este gate no sabe leer eso", hid.Name, canon(printExpr(rhs)), mainRel)
		}
		pkg, fn := pkgID.Name, sel.Sel.Name
		imp := resolveImport(mainFile, pkg)
		if imp == "" {
			die(2, "%s.%s construye el handler y %s no importa ningun paquete llamado %s", pkg, fn, mainRel, pkg)
		}
		modBytes, err := os.ReadFile(filepath.Join(root, modRel, "go.mod"))
		if err != nil {
			die(2, "las rutas viven en %s y no puedo resolverlo sin %s/go.mod", imp, modRel)
		}
		m := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(modBytes)
		if m == nil {
			die(2, "%s/go.mod no declara module", modRel)
		}
		modPath := string(m[1])
		if !strings.HasPrefix(imp, modPath+"/") {
			die(2, "el paquete %s no pertenece al modulo %s de %s; este gate no lo sigue", imp, modPath, modRel)
		}
		dirRel := filepath.Join(modRel, strings.TrimPrefix(imp, modPath+"/"))
		pkgs, err := parser.ParseDir(fset, filepath.Join(root, dirRel), func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil && !os.IsNotExist(err) {
			die(2, "no puedo parsear %s: %v", dirRel, err)
		}
		var fd *ast.FuncDecl
		files := 0
		for _, p := range pkgs {
			for path, f := range p.Files {
				files++
				if d := funcDecl(f, fn); d != nil {
					fd = d
					rel, _ := filepath.Rel(root, path)
					where = rel + ":" + fn
				}
			}
		}
		if fd == nil {
			die(2, "main.go construye el handler con %s.%s y no encuentro func %s en %s (%d fichero(s) .go sin _test)", pkg, fn, fn, dirRel, files)
		}
		// 3. What the function RETURNS as its LAST statement, which is the only thing the
		//    server gets; then the straight-line path that leads there.
		if fd.Body == nil || len(fd.Body.List) == 0 {
			die(2, "func %s no tiene cuerpo", fn)
		}
		lastStmt := fd.Body.List[len(fd.Body.List)-1]
		ret, ok := lastStmt.(*ast.ReturnStmt)
		if !ok {
			die(2, "func %s no termina en un return del handler (ultima sentencia: %s en la linea %d): camino no lineal", fn, stmtKind(lastStmt), line(lastStmt))
		}
		if len(ret.Results) != 1 {
			die(2, "func %s tiene un return con %d valores; este gate espera exactamente el handler", fn, len(ret.Results))
		}
		switch res := ret.Results[0].(type) {
		case *ast.Ident:
			returned = res.Name
		default:
			if !isCall(res, "http", "NewServeMux") {
				die(2, "func %s devuelve %s; este gate solo sigue un ServeMux declarado en la funcion o un http.NewServeMux() directo", fn, canon(printExpr(res)))
			}
			returned = "fresh"
		}
		if returned == "fresh" {
			break
		}
		var defined bool
		var reason string
		routes, opaque, defined, reason = walkStraight(fn, fd.Body, returned, "router")
		if reason != "" {
			die(2, "%s", reason)
		}
		if !defined {
			die(2, "func %s devuelve %s, que no es un http.NewServeMux() declarado una sola vez en el nivel superior de la funcion", fn, returned)
		}
	}

	fmt.Printf("WHERE\t%s\n", where)
	fmt.Printf("RETURNED\t%s\n", returned)
	for _, r := range routes {
		fmt.Printf("ROUTE\t%s\t%s\n", r.pattern, r.handler)
	}
	fmt.Printf("OPAQUE\t%d\n", opaque)
	fmt.Printf("TLS\t%d\n", tls)
}
