// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command suspend-fence-guard prints, with Go's own parser, the facts the C05-09
// suspend-fence gate pins: the parameter lists of the *Manager methods, the calls and
// identifiers their bodies contain, and — for every call the billing handler makes on
// its manager — each argument with a structural classification of how it relates to
// the handler's own selected provider. It exists because the previous reader
// (2026-09-06, independent review of cf45f7f699) searched comment-stripped text, so a
// call quoted inside a string satisfied a body obligation and any expression that
// merely mentioned h.provider — including one that discarded it — passed as "bound".
//
// The provider grammar is bounded on purpose. An argument in the provider position is
//
//	provider-direct       R.provider, where R is the receiver of the enclosing method;
//	provider-via-helper   F(R.provider) or (func literal)(R.provider), where the callee
//	                      takes exactly one WebhookProvider, returns one, and its body is
//	                      zero or more `if p == "" { return <x> }` defaults followed by a
//	                      final return — detail "identity" when that return is the
//	                      parameter, "returns:<x>" when it is something else, or
//	                      "unsupported:<why>" when the body has another shape;
//	provider-not-bound    a readable value that is not the handler's provider (a literal,
//	                      another identifier or field, a helper fed something else);
//	provider-unsupported  anything else. The caller must not treat it as bound.
//
// It never decides. The caller turns facts into a verdict.
//
// Usage: suspend-fence-guard <manager.go> <polar.go>
// stdout, tab-separated:
//
//	METHOD   <name> <receiver> <param>|<param>…       one per *Manager method Suspend/Reactivate/UpdatePlan
//	CALL     <method> <call>                          every call expression in that body, whitespace-free
//	IDENT    <method> <identifier>                    every identifier used in that body, once
//	HANDLER  <method> <k> <receiver> <argc>           the k-th call R.manager.<method>(…) in polar.go
//	ARG      <method> <k> <i> <class> <printed> <detail>   printed as gofmt would, on one line
//
// exit 0 facts printed · 2 unreadable (reason on stderr) · 3 usage
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
	"sort"
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

// oneLine collapses whitespace outside string literals to single spaces, so a printed
// expression reads as gofmt would print it on one line.
func oneLine(s string) string {
	var b strings.Builder
	inStr, inRaw, esc, space := false, false, false, false
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
	return strings.TrimSpace(b.String())
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

func params(ft *ast.FuncType) []string {
	var out []string
	if ft.Params == nil {
		return out
	}
	for _, f := range ft.Params.List {
		typ := canon(printExpr(f.Type))
		if len(f.Names) == 0 {
			out = append(out, typ)
			continue
		}
		for _, n := range f.Names {
			out = append(out, n.Name+" "+typ)
		}
	}
	return out
}

func receiverName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 || len(fd.Recv.List[0].Names) != 1 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

func isManagerMethod(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	st, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := st.X.(*ast.Ident)
	return ok && id.Name == "Manager"
}

func isRecvField(e ast.Expr, recv, field string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != field {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == recv
}

// analyzeProviderFn applies the bounded grammar to a helper's signature and body.
func analyzeProviderFn(ft *ast.FuncType, body *ast.BlockStmt) (kind, detail string) {
	if ft.Params == nil || len(ft.Params.List) != 1 || len(ft.Params.List[0].Names) != 1 || typeName(ft.Params.List[0].Type) != "WebhookProvider" {
		return "unsupported", "signature does not take exactly one WebhookProvider"
	}
	if ft.Results == nil || len(ft.Results.List) != 1 || len(ft.Results.List[0].Names) > 1 || typeName(ft.Results.List[0].Type) != "WebhookProvider" {
		return "unsupported", "signature does not return exactly one WebhookProvider"
	}
	p := ft.Params.List[0].Names[0].Name
	if body == nil || len(body.List) == 0 {
		return "unsupported", "empty body"
	}
	last, ok := body.List[len(body.List)-1].(*ast.ReturnStmt)
	if !ok || len(last.Results) != 1 {
		return "unsupported", "last statement is not a single-value return"
	}
	for i, st := range body.List[:len(body.List)-1] {
		ifs, ok := st.(*ast.IfStmt)
		if !ok || ifs.Init != nil || ifs.Else != nil || len(ifs.Body.List) != 1 {
			return "unsupported", fmt.Sprintf("statement %d is not an `if p == \"\" { return … }` default", i+1)
		}
		bin, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.EQL {
			return "unsupported", fmt.Sprintf("statement %d is not an `if p == \"\" { return … }` default", i+1)
		}
		x, xok := bin.X.(*ast.Ident)
		y, yok := bin.Y.(*ast.BasicLit)
		if !xok || !yok {
			y, yok = bin.X.(*ast.BasicLit)
			x, xok = bin.Y.(*ast.Ident)
		}
		if !xok || !yok || x.Name != p || y.Kind != token.STRING || y.Value != `""` {
			return "unsupported", fmt.Sprintf("statement %d is not an `if p == \"\" { return … }` default", i+1)
		}
		if r, ok := ifs.Body.List[0].(*ast.ReturnStmt); !ok || len(r.Results) != 1 {
			return "unsupported", fmt.Sprintf("statement %d is not an `if p == \"\" { return … }` default", i+1)
		}
	}
	if id, ok := last.Results[0].(*ast.Ident); ok && id.Name == p && p != "_" {
		return "identity", ""
	}
	return "returns", oneLine(printExpr(last.Results[0]))
}

func classify(arg ast.Expr, recv string, pkgFuncs map[string]*ast.FuncDecl) (class, detail string) {
	if isRecvField(arg, recv, "provider") {
		return "provider-direct", ""
	}
	switch e := arg.(type) {
	case *ast.CallExpr:
		if len(e.Args) != 1 {
			return "provider-unsupported", ""
		}
		fed := isRecvField(e.Args[0], recv, "provider")
		switch fn := e.Fun.(type) {
		case *ast.Ident:
			if !fed {
				return "provider-not-bound", ""
			}
			fd, ok := pkgFuncs[fn.Name]
			if !ok {
				return "provider-unsupported", fn.Name + " is not a function of this package"
			}
			kind, d := analyzeProviderFn(fd.Type, fd.Body)
			return "provider-via-helper", fn.Name + ":" + kind + ":" + d
		case *ast.FuncLit:
			if !fed {
				return "provider-not-bound", ""
			}
			kind, d := analyzeProviderFn(fn.Type, fn.Body)
			return "provider-via-helper", "func-literal:" + kind + ":" + d
		}
		return "provider-unsupported", ""
	case *ast.Ident, *ast.BasicLit, *ast.SelectorExpr:
		return "provider-not-bound", ""
	}
	return "provider-unsupported", ""
}

func main() {
	if len(os.Args) != 3 {
		die(3, "usage: suspend-fence-guard <manager.go> <polar.go>")
	}
	managerPath, polarPath := os.Args[1], os.Args[2]
	managerFile, err := parser.ParseFile(fset, managerPath, nil, 0)
	if err != nil {
		die(2, "cannot parse %s: %v", filepath.Base(managerPath), err)
	}
	polarFile, err := parser.ParseFile(fset, polarPath, nil, 0)
	if err != nil {
		die(2, "cannot parse %s: %v", filepath.Base(polarPath), err)
	}
	// The handler's package, without tests, for helper lookup by name.
	pkgFuncs := map[string]*ast.FuncDecl{}
	pkgs, err := parser.ParseDir(fset, filepath.Dir(polarPath), func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		die(2, "cannot parse the package of %s: %v", filepath.Base(polarPath), err)
	}
	for _, p := range pkgs {
		for _, f := range p.Files {
			for _, d := range f.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
					pkgFuncs[fd.Name.Name] = fd
				}
			}
		}
	}

	wanted := []string{"Suspend", "Reactivate", "UpdatePlan"}
	for _, name := range wanted {
		for _, d := range managerFile.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != name || !isManagerMethod(fd) {
				continue
			}
			fmt.Printf("METHOD\t%s\t%s\t%s\n", name, receiverName(fd), strings.Join(params(fd.Type), "|"))
			idents := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					fmt.Printf("CALL\t%s\t%s\n", name, canon(printExpr(x)))
				case *ast.Ident:
					idents[x.Name] = true
				}
				return true
			})
			names := make([]string, 0, len(idents))
			for k := range idents {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				fmt.Printf("IDENT\t%s\t%s\n", name, k)
			}
		}
	}

	for _, d := range polarFile.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		recv := receiverName(fd)
		if recv == "" {
			continue
		}
		counts := map[string]int{}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			s, ok := c.Fun.(*ast.SelectorExpr)
			if !ok || !isRecvField(s.X, recv, "manager") {
				return true
			}
			name := s.Sel.Name
			for _, w := range wanted {
				if w == name {
					counts[name]++
					k := counts[name]
					fmt.Printf("HANDLER\t%s\t%d\t%s\t%d\n", name, k, recv, len(c.Args))
					for i, a := range c.Args {
						class, detail := classify(a, recv, pkgFuncs)
						fmt.Printf("ARG\t%s\t%d\t%d\t%s\t%s\t%s\n", name, k, i, class, oneLine(printExpr(a)), detail)
					}
				}
			}
			return true
		})
	}
}
