// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

// errorRemedy is the Community code table: an enterprise build overlays more handlers,
// and their codes, into core/ and modules/.

package api

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

type remedyEnvelope struct {
	Error struct {
		Code    string  `json:"code"`
		Message string  `json:"message"`
		Remedy  *string `json:"remedy"`
	} `json:"error"`
}

func writeErrorEnvelope(t *testing.T, err error) (int, remedyEnvelope) {
	t.Helper()
	rec := httptest.NewRecorder()
	(&Server{log: slog.Default()}).writeError(rec, httptest.NewRequest(http.MethodPost, "/x", nil), err)
	var env remedyEnvelope
	if jerr := json.Unmarshal(rec.Body.Bytes(), &env); jerr != nil {
		t.Fatalf("error body is not JSON (%v): %s", jerr, rec.Body.String())
	}
	return rec.Code, env
}

func errorBodyEnvelope(t *testing.T, code, message string) remedyEnvelope {
	t.Helper()
	raw, err := json.Marshal(ErrorBody(code, message))
	if err != nil {
		t.Fatal(err)
	}
	var env remedyEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env
}

// A Business route asked of a Community build, and a refused step-up, keep their
// status, code and message and add the one next step: the edition and where to get
// it, and the factor to enrol.
func TestErrorRemedyNamesTheNextStep(t *testing.T) {
	const msg = "Session recording requires Olivares Business."
	env := errorBodyEnvelope(t, "business_required", msg)
	if env.Error.Code != "business_required" || env.Error.Message != msg {
		t.Fatalf("code or message changed: %+v", env.Error)
	}
	if env.Error.Remedy == nil || !strings.Contains(*env.Error.Remedy, "Olivares Business") ||
		!strings.Contains(*env.Error.Remedy, "https://olivares.ai/pricing") {
		t.Errorf("business_required remedy = %v, want the edition and the pricing link", env.Error.Remedy)
	}

	status, env := writeErrorEnvelope(t, auth.ErrStepUpRequired)
	if status != http.StatusForbidden || env.Error.Code != "step_up_required" || env.Error.Message != auth.ErrStepUpRequired.Error() {
		t.Fatalf("step-up refusal changed: %d %+v", status, env.Error)
	}
	if env.Error.Remedy == nil || !strings.Contains(*env.Error.Remedy, "passkey") {
		t.Errorf("step_up_required remedy = %v, want the factor to enrol", env.Error.Remedy)
	}

	text := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	// One code, two causes: the remedy must hold for an account that has no
	// authenticator yet, not only for one that must enter its code.
	if _, env := writeErrorEnvelope(t, auth.ErrTOTPEnrolmentRequired); env.Error.Code != "totp_required" ||
		env.Error.Remedy == nil || !strings.Contains(*env.Error.Remedy, "set it up") {
		t.Errorf("totp_required (enrolment) remedy = %q, want the setup step too", text(env.Error.Remedy))
	}
	// The engine reads its license from --license, OLIVARES_LICENSE_PATH and OLIVARES_LICENSE.
	if env := errorBodyEnvelope(t, "license_managed_externally", "x"); env.Error.Remedy == nil ||
		!strings.Contains(*env.Error.Remedy, "OLIVARES_LICENSE_PATH") {
		t.Errorf("license_managed_externally remedy = %q, want every place the license is set", text(env.Error.Remedy))
	}
}

// The remedy is fixed text chosen by the code alone. A refusal whose own text carries
// a secret, a path, a host name or identifiers puts none of it in error.remedy; a code
// with nothing to do next, or an unknown one, carries no remedy at all.
func TestErrorRemedyIsFixedTextChosenByCodeOnly(t *testing.T) {
	sensitive := []string{
		"olst_" + strings.Repeat("x", 24), "/var/lib/olivares/tls.key", "db.internal.example",
		"tenant 0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "session 0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5c",
	}
	leaky := fmt.Errorf("%w: %s", auth.ErrStepUpRequired, strings.Join(sensitive, " "))

	_, clean := writeErrorEnvelope(t, auth.ErrStepUpRequired)
	_, env := writeErrorEnvelope(t, leaky)
	if clean.Error.Remedy == nil || env.Error.Remedy == nil || *env.Error.Remedy != *clean.Error.Remedy {
		t.Fatalf("remedy = %v, want the same text as the plain refusal's %v", env.Error.Remedy, clean.Error.Remedy)
	}
	plain := errorBodyEnvelope(t, "business_required", "This capability requires Olivares Business.")
	body := errorBodyEnvelope(t, "business_required", strings.Join(sensitive, " "))
	if plain.Error.Remedy == nil || body.Error.Remedy == nil || *body.Error.Remedy != *plain.Error.Remedy {
		t.Fatalf("remedy = %v, want the same text as the plain refusal's %v", body.Error.Remedy, plain.Error.Remedy)
	}
	for _, s := range sensitive {
		for _, r := range []*string{env.Error.Remedy, body.Error.Remedy} {
			if strings.Contains(*r, s) {
				t.Errorf("remedy %q carries %q from the failure", *r, s)
			}
		}
	}

	if _, env := writeErrorEnvelope(t, fmt.Errorf("broken: %s", sensitive[1])); env.Error.Code != "internal" || env.Error.Remedy != nil {
		t.Errorf("an internal error carries a remedy: %+v", env.Error)
	}
	if env := errorBodyEnvelope(t, "no_such_code", "x"); env.Error.Remedy != nil {
		t.Errorf("an unknown code carries a remedy: %+v", env.Error)
	}
}

// TestEveryErrorCodeIsInTheRemedyTable reads core/, modules/ and cmd/olivares/internal
// (the trees that serve the REST API) and collects every literal error code that can
// reach {"error":{"code":…}}: statusFor's answers; the code argument of ErrorBody and
// of every function that passes its own `code string` parameter on to an envelope
// (found to a fixpoint); a hand-built "error" map; an X.Error.Code assignment; and the
// typed carriers below. A package constant naming the code counts as the literal. The
// table must hold exactly those codes: a new code fails until it has a row, and a row
// no handler returns fails until it is removed. The sessions work plane answers its own
// structured result (verdict, code, evidence_ref; docs/api-errors.md) and is not read.
func TestEveryErrorCodeIsInTheRemedyTable(t *testing.T) {
	found := errorCodeCensus(t, filepath.Join("..", ".."))
	for code, at := range found {
		if _, ok := errorRemedy[code]; !ok {
			t.Errorf("%s returns error code %q, which is not in errorRemedy (core/api/errorremedy.go)", at, code)
		}
	}
	for code := range errorRemedy {
		if _, ok := found[code]; !ok {
			t.Errorf("errorRemedy lists %q, which no handler returns: remove its row", code)
		}
	}
}

// errorCarriers are the typed errors whose code a handler later writes into the
// envelope: "<dir> <constructor>" with the code's argument index, or -1 for the last
// positional field of a composite literal.
var errorCarriers = map[string]int{
	"modules/gitpublish refuse":    0,
	"modules/skills refuse":        0,
	"modules/sessions codedRunErr": -1,
}

func errorCodeCensus(t *testing.T, root string) map[string]string {
	t.Helper()
	type file struct {
		dir string
		f   *ast.File
	}
	fset := token.NewFileSet()
	var files []file
	for _, tree := range []string{"core", "modules", filepath.Join("cmd", "olivares", "internal")} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if n := d.Name(); n == "testdata" || n == "node_modules" || strings.HasPrefix(n, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			dir, _ := filepath.Rel(root, filepath.Dir(p))
			files = append(files, file{filepath.ToSlash(dir), f})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	str := func(e ast.Expr) (string, bool) {
		b, ok := e.(*ast.BasicLit)
		if !ok || b.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(b.Value)
		return s, err == nil
	}
	consts := map[string]string{}
	for _, pf := range files {
		for _, d := range pf.f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, s := range gd.Specs {
					vs := s.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if v, ok := str(vs.Values[i]); ok {
								consts[pf.dir+" "+n.Name] = v
							}
						}
					}
				}
			}
		}
	}
	callee := func(e ast.Expr) string {
		switch f := e.(type) {
		case *ast.Ident:
			return f.Name
		case *ast.SelectorExpr:
			return f.Sel.Name
		}
		return ""
	}
	// isEnvelopeCode matches the X.Error.Code of an errorBody being filled in.
	isEnvelopeCode := func(e ast.Expr) bool {
		se, ok := e.(*ast.SelectorExpr)
		if !ok || se.Sel.Name != "Code" {
			return false
		}
		in, ok := se.X.(*ast.SelectorExpr)
		return ok && in.Sel.Name == "Error"
	}
	// errorMapCode is the code of a hand-built {"error": {"code": X, …}}.
	errorMapCode := func(kv *ast.KeyValueExpr) ast.Expr {
		if k, ok := str(kv.Key); !ok || k != "error" {
			return nil
		}
		cl, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return nil
		}
		for _, el := range cl.Elts {
			if inner, ok := el.(*ast.KeyValueExpr); ok {
				if k, ok := str(inner.Key); ok && k == "code" {
					return inner.Value
				}
			}
		}
		return nil
	}
	// envelopes maps a constructor to the index of its code argument. An unexported
	// name is keyed by its directory, so two packages' helpers never alias.
	envelopes := map[string]int{"ErrorBody": 0}
	key := func(dir, name string) string {
		if ast.IsExported(name) {
			return name
		}
		return dir + " " + name
	}
	isCode := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == "code" }
	for changed := true; changed; {
		changed = false
		for _, pf := range files {
			for _, d := range pf.f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				if _, done := envelopes[key(pf.dir, fd.Name.Name)]; done {
					continue
				}
				idx, i := -1, 0
				for _, field := range fd.Type.Params.List {
					typ, isIdent := field.Type.(*ast.Ident)
					for _, n := range field.Names {
						if n.Name == "code" && isIdent && typ.Name == "string" {
							idx = i
						}
						i++
					}
					if len(field.Names) == 0 {
						i++
					}
				}
				if idx < 0 {
					continue
				}
				forwards := false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.CallExpr:
						if at, ok := envelopes[key(pf.dir, callee(x.Fun))]; ok && at < len(x.Args) && isCode(x.Args[at]) {
							forwards = true
						}
					case *ast.KeyValueExpr:
						if c := errorMapCode(x); c != nil && isCode(c) {
							forwards = true
						}
					case *ast.AssignStmt:
						for j, l := range x.Lhs {
							if isEnvelopeCode(l) && j < len(x.Rhs) && isCode(x.Rhs[j]) {
								forwards = true
							}
						}
					}
					return true
				})
				if forwards {
					envelopes[key(pf.dir, fd.Name.Name)] = idx
					changed = true
				}
			}
		}
	}
	found := map[string]string{}
	add := func(dir string, e ast.Expr) {
		code, ok := str(e)
		if id, isIdent := e.(*ast.Ident); !ok && isIdent {
			code, ok = consts[dir+" "+id.Name]
		}
		if ok {
			if _, seen := found[code]; !seen {
				found[code] = fset.Position(e.Pos()).String()
			}
		}
	}
	for _, pf := range files {
		ast.Inspect(pf.f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if pf.dir == "core/api" && x.Recv == nil && x.Name.Name == "statusFor" {
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if r, ok := m.(*ast.ReturnStmt); ok && len(r.Results) == 2 {
							add(pf.dir, r.Results[1])
						}
						return true
					})
				}
			case *ast.CallExpr:
				name := callee(x.Fun)
				if at, ok := envelopes[key(pf.dir, name)]; ok && at < len(x.Args) {
					add(pf.dir, x.Args[at])
				}
				if at, ok := errorCarriers[pf.dir+" "+name]; ok && at >= 0 && at < len(x.Args) {
					add(pf.dir, x.Args[at])
				}
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); ok && len(x.Elts) > 0 {
					if at, ok := errorCarriers[pf.dir+" "+id.Name]; ok && at < 0 {
						add(pf.dir, x.Elts[len(x.Elts)-1])
					}
				}
			case *ast.KeyValueExpr:
				if c := errorMapCode(x); c != nil {
					add(pf.dir, c)
				}
			case *ast.AssignStmt:
				for j, l := range x.Lhs {
					if isEnvelopeCode(l) && j < len(x.Rhs) {
						add(pf.dir, x.Rhs[j])
					}
				}
			}
			return true
		})
	}
	return found
}

const (
	errorDocsBegin = "<!-- BEGIN GENERATED olivares-api-error-codes — regenerate with `OLIVARES_UPDATE_GOLDEN=1 go test ./core/api -run '^TestErrorCodeDocsMatchTheTable$'`; do not edit by hand -->"
	errorDocsEnd   = "<!-- END GENERATED olivares-api-error-codes -->"
)

// TestErrorCodeDocsMatchTheTable holds the code table of docs/api-errors.md and of the
// docs-site API errors page to errorRemedy, so both list every code with its remedy.
// OLIVARES_UPDATE_GOLDEN=1 rewrites the two generated regions.
func TestErrorCodeDocsMatchTheTable(t *testing.T) {
	var b strings.Builder
	b.WriteString(errorDocsBegin + "\n\n| Code | Remedy |\n| --- | --- |\n")
	for _, code := range slices.Sorted(maps.Keys(errorRemedy)) {
		fmt.Fprintf(&b, "| `%s` | %s |\n", code, errorRemedy[code])
	}
	b.WriteString("\n" + errorDocsEnd)
	want := b.String()
	for _, path := range []string{
		filepath.Join("..", "..", "docs", "api-errors.md"),
		filepath.Join("..", "..", "docs-site", "src", "content", "docs", "reference", "api-errors.md"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc := string(raw)
		i, j := strings.Index(doc, errorDocsBegin), strings.Index(doc, errorDocsEnd)
		if i < 0 || j < i {
			t.Fatalf("%s has no generated error code region", path)
		}
		if doc[i:j+len(errorDocsEnd)] == want {
			continue
		}
		if os.Getenv("OLIVARES_UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, []byte(doc[:i]+want+doc[j+len(errorDocsEnd):]), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Errorf("%s: the error code table differs from errorRemedy; regenerate with OLIVARES_UPDATE_GOLDEN=1 go test ./core/api -run '^TestErrorCodeDocsMatchTheTable$'", path)
	}
}
