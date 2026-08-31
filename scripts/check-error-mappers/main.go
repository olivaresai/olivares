// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command check-error-mappers fails when a product module answers a store or
// license error on its own authority instead of the product's.
//
// WHAT IT ENFORCES. Every function with the shape
//
//	func <name>(w http.ResponseWriter, err error)
//
// under the scanned tree must REACH api.StoreErrorStatus — directly, or through
// another function of its own package. That is the whole rule, and it is a
// reachability question rather than a coverage one on purpose: coverage would mean
// re-listing the canonical sentinel set in this gate, and then a sentinel added to
// core/api tomorrow needs an edit here AND in every module before anything goes
// green. Reachability needs neither.
//
// WHY IT EXISTS, measured on 2026-08-12 over modules/. The tree held THIRTY-SIX
// copies of one error mapper, no gate watched them, and they had drifted where
// nobody was looking: four sentinels core/api has long mapped —
// store.ErrTenantSuspended (423), store.ErrTenantNotInService (423),
// store.ErrNotLeader (503) and store.ErrResidencyViolation (403) — were present in
// AT MOST TWO copies each, so a suspended tenant was answered 423 by /v1/orgs and
// 500 "internal error" by every /v1/m/ route in the product for the same refusal.
// Delegation fixed the thirty-six that exist; this gate is what stops the
// thirty-seventh from arriving without it.
//
// THE ROSTER IS BEHAVIOURAL, NOT NOMINAL, and that is not a preference — it is the
// correction scripts/check-json-decoders.sh had to make to itself hours after it
// landed (see its own comment at :43), and the same error had already been made
// about THIS family: the census that opened this work counted thirty-three members
// by matching the name write*Error, and missed three that spell it differently —
// deploy/lifecycle.go execUnavailable, knowledge/dataproduct.go
// handleDataProductErr and an internal design note (not shipped) writeRunErr. What a mapper is
// CALLED is a convention a newcomer need not know; the signature is what the defect
// needs to exist.
//
// THERE ARE NO EXEMPTIONS AND THERE IS NO ALLOWLIST. Two members are in the family
// by signature and not by subject — execUnavailable maps executor failures, and
// writeWorkError answers on the sessions work vocabulary. Both consult the shared
// mapping anyway, in a branch that changes nothing about what they already answer,
// precisely so this file needs no list of names. An exemption whose reason has
// expired is a hole with a comment on it (scripts/test-pg-test-env.sh:2150), and a
// premise about which errors reach a function is exactly the kind that expires
// quietly.
//
// WHAT THIS GATE'S DISCOVERY MECHANISM DOES NOT REACH, named here because a gate
// says what it DISCOVERS and not what it checks. It finds mappers by SIGNATURE, so a
// handler that classifies a sentinel INLINE — switch on errors.Is, write the status
// straight out, no separate writer — is invisible to it by construction.
//
// Measured on 2026-08-12, that is exactly ONE module in the tree: modules/reporting
// has no member of the family at all and classifies in the handler
// (modules/reporting/api.go:112-117). It is CORRECT there today — it answers
// ErrTenantSuspended and ErrTenantNotInService 423 and ErrResidencyViolation 403,
// which is what the shared mapping says — and that is the interesting part rather
// than a footnote: a previous session hit this same drift, fixed it in the one module
// it was looking at, and had no way to make the answer general. That is how thirty-six
// copies come to disagree.
//
// That module was brought into the family the same day, after another lane showed the
// closed overlay gates it: reporting now has a mapper and this gate watches it. What
// stays true is the general shape of the blind spot — a handler that classifies inline
// has nothing with the family signature, and this gate cannot see it.
//
// ⛔ WHAT IT CANNOT DO, and it is written here because the first version of this comment
// claimed the opposite. An external contrast (an internal design note (not shipped))
// refuted "cannot be fooled" with six working evasions. Five are now RED cases in the
// self-test — a build-tag variant that does not delegate, a call whose results are all
// discarded, a call inside `if false`, a local shadowing the api package, and an aliased
// net/http import. Two remain OPEN and are residue, not oversight:
//
//   - A package-level `var writeErr = func(http.ResponseWriter, error) {…}` is not an
//     ast.FuncDecl, so it is not in the roster.
//   - A mapper can CALL the shared function, use its result, and still write something
//     else. Catching that needs the written status to be shown to derive from the
//     returned one — dataflow over go/types, not a syntactic walk.
//
// Closing them properly means go/packages + go/types with one load per build-tag set,
// which is a different instrument and a bigger one. A gate that says what it cannot do
// is worth more than one that is believed to do more than it does.
//
// THREE ANSWERS: clean / offenders named / CANNOT LOOK.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// canonicalPkg is the import path of the package that owns the mapping, and
// canonicalFunc the function every member must reach.
const (
	canonicalPkg  = "github.com/olivaresai/olivares/core/api"
	canonicalFunc = "StoreErrorStatus"
)

// fn is one top-level function: whether it has the family signature, whether it
// calls the canonical mapper itself, and which same-package functions it calls.
type fn struct {
	file       string
	line       int
	name       string
	constraint string // the //go:build line, when the file carries one
	family     bool
	direct     bool     // calls <api>.StoreErrorStatus USEFULLY in its own body
	calls      []string // package-scoped names of same-package funcs it calls
	receiver   bool
}

// where renders a finding's position, naming the build constraint when there is
// one. Without it, two variants of the same function are indistinguishable in the
// report — which is precisely the case this gate got wrong first time round.
func (e *fn) where() string {
	if e.constraint == "" {
		return fmt.Sprintf("%s:%d: %s", e.file, e.line, e.name)
	}
	return fmt.Sprintf("%s:%d: %s  [%s]", e.file, e.line, e.name, e.constraint)
}

// buildConstraint returns the file's //go:build expression, or "".
func buildConstraint(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//go:build ") {
			return strings.TrimSpace(strings.TrimPrefix(t, "//go:build "))
		}
		if t != "" && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "package") {
			return ""
		}
	}
	return ""
}

func main() {
	root := "modules"
	if v := os.Getenv("OLIVARES_ERROR_MAPPER_SCAN"); v != "" {
		root = v
	}
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--self-test" {
		os.Exit(selfTest())
	}
	if len(args) == 1 {
		root = args[0]
	}
	rc, report := check(root)
	fmt.Print(report)
	os.Exit(rc)
}

// check returns 0 clean, 1 offenders, 2 could-not-look.
func check(root string) (int, string) {
	var out strings.Builder
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return 2, fmt.Sprintf("check-error-mappers: CANNOT LOOK — no tree at %s.\n"+
			"  Nothing was examined, so nothing is approved.\n", root)
	}

	fset := token.NewFileSet()
	// EVERY DECLARATION IS ITS OWN ENTRY, and that is the correction the external
	// contrast forced. The first version indexed by "<pkgdir>\x00<funcname>", so two
	// build-tag variants of one function — which is EXACTLY this repository's
	// architecture, an open tree plus a closed enterprise overlay — collapsed into one
	// entry and the later file silently replaced the earlier. Measured by the contrast:
	// a //go:build enterprise writeStoreError that answered 418 and never delegated
	// went GREEN because the !enterprise sibling had already been credited.
	//
	// So: decls are judged one by one, and NAME RESOLUTION IS SEPARATE and fail-closed
	// — a delegation target only counts when EVERY declaration of that name in the
	// package reaches the mapper. One variant that does not is one build that does not.
	all := []*fn{}
	byName := map[string][]*fn{} // "<pkgdir>\x00<funcname>" -> every declaration
	parseFailed := ""

	walkErr := filepath.Walk(root, func(p string, i os.FileInfo, err error) error {
		if err != nil || i.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			parseFailed = fmt.Sprintf("%s: %v", p, rerr)
			return filepath.SkipAll
		}
		f, perr := parser.ParseFile(fset, p, src, 0)
		if perr != nil {
			// A file that would not parse is a file this gate did not read. It is not
			// "clean": go build would have caught it, but this gate must not be the one
			// that says OK about a tree it could not open.
			parseFailed = fmt.Sprintf("%s: %v", p, perr)
			return filepath.SkipAll
		}
		dir := filepath.Dir(p)
		alias := canonicalAlias(f)
		httpAlias := importAlias(f, "net/http")
		constraint := buildConstraint(src)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			e := &fn{file: p, line: fset.Position(fd.Pos()).Line, name: fd.Name.Name,
				constraint: constraint, family: isFamily(fd, httpAlias), receiver: fd.Recv != nil}
			scanBody(fd.Body, fd.Name.Name, dir, alias, e)
			all = append(all, e)
			if !e.receiver {
				byName[dir+"\x00"+fd.Name.Name] = append(byName[dir+"\x00"+fd.Name.Name], e)
			}
		}
		return nil
	})
	if parseFailed != "" {
		return 2, fmt.Sprintf("check-error-mappers: CANNOT LOOK — a source file under %s did not parse.\n  %s\n"+
			"  Nothing was examined past it, so nothing is approved.\n", root, parseFailed)
	}
	if walkErr != nil {
		return 2, fmt.Sprintf("check-error-mappers: CANNOT LOOK — walking %s failed: %v\n", root, walkErr)
	}

	// reaches is fail-closed at every step. A delegation target counts only when EVERY
	// declaration of that name in the package reaches the mapper, so a build-tag variant
	// that does not delegate cannot be laundered by its sibling that does.
	var reaches func(e *fn, seen map[*fn]bool) bool
	var allReach func(key string, seen map[*fn]bool) bool
	reaches = func(e *fn, seen map[*fn]bool) bool {
		if e == nil || seen[e] {
			return false
		}
		seen[e] = true
		if e.direct {
			return true
		}
		for _, c := range e.calls {
			if allReach(c, seen) {
				return true
			}
		}
		return false
	}
	allReach = func(key string, seen map[*fn]bool) bool {
		decls := byName[key]
		if len(decls) == 0 {
			return false
		}
		for _, d := range decls {
			if !reaches(d, seen) {
				return false
			}
		}
		return true
	}

	var family, offenders []string
	for _, e := range all {
		if !e.family {
			continue
		}
		family = append(family, e.where())
		if !reaches(e, map[*fn]bool{}) {
			offenders = append(offenders, e.where())
		}
	}

	if len(family) == 0 {
		return 2, fmt.Sprintf("check-error-mappers: CANNOT LOOK — zero func(http.ResponseWriter, error) found under %s.\n"+
			"  This tree has always had some (thirty-seven at the time of writing). Finding none\n"+
			"  means the scan stopped matching, not that the mappers stopped existing.\n", root)
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		out.WriteString("check-error-mappers: FAIL — these error mappers answer on their own authority:\n\n")
		for _, o := range offenders {
			out.WriteString("  " + o + "\n")
		}
		out.WriteString("\n" +
			"  A func(http.ResponseWriter, error) must reach api.StoreErrorStatus, directly or\n" +
			"  through another function of its own package, so the product answers a store or\n" +
			"  license sentinel the same way everywhere. Handle whatever is genuinely yours\n" +
			"  first, then let the shared mapping have the rest:\n\n" +
			"      default:\n" +
			"          status, msg, _ := api.StoreErrorStatus(err)\n" +
			"          writeJSON(w, status, errorBody(msg))\n\n" +
			"  The call must USE what it returns and must not sit in a branch the compiler\n" +
			"  drops; a call whose results go nowhere delegates nothing. Every build-tag\n" +
			"  variant is judged on its own, and the offender line names its constraint.\n\n" +
			"  If this writer is in the family by signature and not by subject, it still\n" +
			"  consults the mapping — see modules/deploy/lifecycle.go execUnavailable for the\n" +
			"  shape and for why there is no allowlist here.\n\n")
		out.WriteString(fmt.Sprintf("  (%d mappers examined, %d offending)\n", len(family), len(offenders)))
		return 1, out.String()
	}

	out.WriteString(fmt.Sprintf("check-error-mappers: OK — %d error mappers examined under %s; every one reaches api.%s\n",
		len(family), root, canonicalFunc))
	return 0, out.String()
}

// canonicalAlias returns the identifier this file uses for the canonical package,
// or "" when the file does not import it. Reading the import block rather than
// assuming the name "api" is what makes an aliased import (or a same-named package
// from somewhere else) impossible to confuse for it.
func canonicalAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != canonicalPkg {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				// A blank import cannot be called through; a dot import would make the
				// call a bare Ident, which this gate deliberately does not credit
				// because it cannot tell it from a local function of the same name.
				return ""
			}
			return imp.Name.Name
		}
		return "api"
	}
	return ""
}

// isFamily reports the signature func(<pkg>.ResponseWriter, error) with no results,
// where <pkg> is whatever THIS FILE calls net/http. Comparing the literal text
// "http.ResponseWriter" let `import h "net/http"` slip a new member past the roster —
// found by the external contrast, and the same class of defect as counting by name.
// A method is included: it is the same writer with a receiver bolted on.
func isFamily(fd *ast.FuncDecl, httpAlias string) bool {
	if fd.Type.Results != nil || fd.Type.Params == nil || httpAlias == "" {
		return false
	}
	var types []string
	for _, f := range fd.Type.Params.List {
		n := 1
		if len(f.Names) > 0 {
			n = len(f.Names)
		}
		for i := 0; i < n; i++ {
			types = append(types, typeString(f.Type))
		}
	}
	return len(types) == 2 && types[0] == httpAlias+".ResponseWriter" && types[1] == "error"
}

// importAlias returns the identifier this file uses for path, or "" if it does not
// import it. A blank or dot import yields "" — neither can be written as a selector
// this gate could recognise, so crediting one would be a guess.
func importAlias(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		return path[strings.LastIndex(path, "/")+1:]
	}
	return ""
}

// scanBody records what this function calls, and credits a call to the canonical
// mapper ONLY when its result is actually consumed and the call is not in a branch
// the compiler will drop. Both refinements came from the external contrast, which
// wrote mappers that called StoreErrorStatus and then answered 418 anyway — once by
// discarding all three results, once from inside `if false`. A gate that credits a
// call it can see but whose value goes nowhere is checking for a token, not for
// delegation.
//
// A local variable that shadows the API package's name also disqualifies the call:
// `api := somethingElse` makes `api.StoreErrorStatus` a method on something this gate
// cannot resolve, and crediting it on the strength of the identifier is the same
// mistake one level down.
func scanBody(body *ast.BlockStmt, self, dir, apiAlias string, e *fn) {
	shadowed := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == apiAlias {
					shadowed = true
				}
			}
		case *ast.ValueSpec:
			for _, id := range v.Names {
				if id.Name == apiAlias {
					shadowed = true
				}
			}
		}
		return true
	})

	var walk func(n ast.Node, live bool)
	walk = func(n ast.Node, live bool) {
		switch v := n.(type) {
		case nil:
			return
		case *ast.IfStmt:
			// `if false { … }` is compiled away; a call inside it delegates nothing.
			cond := live
			if id, ok := v.Cond.(*ast.Ident); ok && id.Name == "false" {
				cond = false
			}
			if v.Init != nil {
				walk(v.Init, live)
			}
			walk(v.Body, cond)
			if v.Else != nil {
				walk(v.Else, live)
			}
			return
		case *ast.ExprStmt:
			// A bare call statement discards every result. For the canonical mapper
			// that means the status and the message went nowhere.
			if c, ok := v.X.(*ast.CallExpr); ok {
				noteCall(c, self, dir, apiAlias, e, false, shadowed)
				for _, a := range c.Args {
					walk(a, live)
				}
				return
			}
		case *ast.CallExpr:
			noteCall(v, self, dir, apiAlias, e, live, shadowed)
		}
		for _, c := range children(n) {
			walk(c, live)
		}
	}
	walk(body, true)
}

// noteCall records one call. resultUsed=false means the call statement threw its
// results away, which disqualifies it as delegation but still counts as a call for
// the package-local graph.
func noteCall(c *ast.CallExpr, self, dir, apiAlias string, e *fn, resultUsed, shadowed bool) {
	switch callee := c.Fun.(type) {
	case *ast.SelectorExpr:
		if id, ok := callee.X.(*ast.Ident); ok && apiAlias != "" && !shadowed &&
			id.Name == apiAlias && callee.Sel.Name == canonicalFunc && resultUsed {
			e.direct = true
		}
	case *ast.Ident:
		if callee.Name != self {
			e.calls = append(e.calls, dir+"\x00"+callee.Name)
		}
	}
}

// children yields a node's sub-nodes, so walk can carry its own liveness flag
// instead of using ast.Inspect (which cannot).
func children(n ast.Node) []ast.Node {
	var out []ast.Node
	ast.Inspect(n, func(c ast.Node) bool {
		if c == nil || c == n {
			return c == n
		}
		out = append(out, c)
		return false
	})
	return out
}

func typeString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return "*" + typeString(v.X)
	case *ast.SelectorExpr:
		return typeString(v.X) + "." + v.Sel.Name
	}
	return "?"
}
