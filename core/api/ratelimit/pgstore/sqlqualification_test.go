// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package pgstore_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
)

// TestProductionSQLIsFullyQualified is this package's OWN static ratchet against
// hostile name resolution. sqlstore's equivalent walks only from
// core/internal/store and its doc comment states it does not cover pgstore, so
// without this a dropped pg_catalog. prefix survives every behavioral test that
// happens to run with a benign search_path — measured.
//
// SCOPE, stated so nobody mistakes what this is: a lint against ACCIDENTAL
// regression by a future editor, not a boundary that resists someone who is
// deliberately evading it. The security property — that a hostile search_path
// cannot capture this package's names — is carried by the PIN and proven
// behaviourally against live PostgreSQL with mutation-verified legs. A
// hand-rolled SQL lexer will always lose to a determined evader (dollar-quoting,
// E-strings, nested comments), and someone able to commit such an edit has
// easier attacks available. What this test MUST catch is the ordinary mistake:
// a forgotten prefix, a new statement, a shadowed name. Those are closed
// structurally below rather than by lexer patches.
//
// Two design decisions, both from evasions that were measured against weaker
// versions of this test:
//
//   - It resolves the SQL from the CALL SITES via the AST, not from a
//     hand-maintained list. A statement simply not added to such a list was
//     invisible; here a new Exec/Query is covered the moment it is written, and
//     an argument this resolver cannot render is a failure rather than a skip.
//   - The rule is INVERTED versus a name blacklist, which only ever protects
//     names somebody already thought of: every call-shaped token must be
//     pg_catalog-qualified, SQL grammar, or an object this package defines.
//     `NOW()` (PostgreSQL down-folds unquoted identifiers) and
//     `statement_timestamp()` both slipped past the blacklist version.
func TestProductionSQLIsFullyQualified(t *testing.T) {
	t.Parallel()
	for _, lit := range sqlStatementsSent(t) {
		checkQualified(t, lit.where, lit.sql)
	}
}

type sqlLit struct {
	where string
	sql   string
}

// sqlStatementsSent returns every SQL string this package hands to database/sql.
func sqlStatementsSent(t *testing.T) []sqlLit {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package sources: %v", err)
	}
	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	// Package-level string vars/consts. Resolved twice so a definition may refer
	// to another one declared after it.
	// Seeded from the SAME source of truth the package uses: engineSchema is
	// mustSafeIdent(dialect.EngineSchema), a call the AST cannot evaluate, and
	// every object name concatenates it.
	// The AST cannot fold mustSafeIdent(dialect.EngineSchema), so the seed comes
	// from the same constant — and is then CHECKED against what the package really
	// renders with, or a tampered initializer would build hostile SQL while this
	// scanner happily reconstructed the innocent version.
	if got := pgstore.EngineSchemaForTest(); got != dialect.EngineSchema {
		t.Fatalf("the package renders SQL with schema %q but dialect.EngineSchema is %q: the ratchet's reconstruction would not be the SQL that runs", got, dialect.EngineSchema)
	}
	consts := map[string]string{"engineSchema": dialect.EngineSchema}
	for range 2 {
		for _, f := range files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
					continue
				}
				for _, sp := range gd.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
						continue
					}
					if v, ok := renderString(vs.Values[0], consts); ok {
						consts[vs.Names[0].Name] = v
					}
				}
			}
		}
	}

	execFns := map[string]bool{
		"Exec": true, "ExecContext": true, "Query": true, "QueryContext": true,
		"QueryRow": true, "QueryRowContext": true,
		"Prepare": true, "PrepareContext": true,
	}

	// A local declaration or assignment that reuses a package-level SQL name makes
	// the AST render the ORIGINAL initializer while production executes something
	// else — measured: `gaugeSQL := "SELECT count(*) …"` inside a function left
	// this test green. Rather than teach the resolver full scope analysis, forbid
	// the construct: these names are package statements and must stay that way.
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range v.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && consts[id.Name] != "" {
						t.Errorf("%s: %q is a package-level SQL statement and must not be shadowed or reassigned locally: the ratchet would inspect the original text while production executed this one", fset.Position(id.Pos()), id.Name)
					}
				}
			case *ast.RangeStmt:
				// `for gaugeSQL := range …` rebinds the name for the loop body.
				for _, k := range []ast.Expr{v.Key, v.Value} {
					if id, ok := k.(*ast.Ident); ok && consts[id.Name] != "" {
						t.Errorf("%s: range variable %q shadows a package-level SQL statement", fset.Position(id.Pos()), id.Name)
					}
				}
			case *ast.DeclStmt:
				// `var gaugeSQL = "…"` and `const gaugeSQL = "…"` inside a function
				// shadow exactly like `:=` — measured: both left this test green
				// while production executed the local string.
				if gd, ok := v.Decl.(*ast.GenDecl); ok && (gd.Tok == token.VAR || gd.Tok == token.CONST) {
					for _, sp := range gd.Specs {
						vs, ok := sp.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, id := range vs.Names {
							if consts[id.Name] != "" {
								t.Errorf("%s: local var %q shadows a package-level SQL statement; the ratchet would inspect the package text while production executed the local one", fset.Position(id.Pos()), id.Name)
							}
						}
					}
				}
			case *ast.FuncType:
				// A PARAMETER or a NAMED RESULT of the same name shadows it for the
				// whole body.
				for _, fl := range []*ast.FieldList{v.Params, v.Results} {
					if fl == nil {
						continue
					}
					for _, fld := range fl.List {
						for _, id := range fld.Names {
							if consts[id.Name] != "" {
								t.Errorf("%s: %q shadows a package-level SQL statement for the whole function body", fset.Position(id.Pos()), id.Name)
							}
						}
					}
				}
			case *ast.FuncDecl:
				// A method named like a database/sql entry point would be collected
				// as if it carried SQL, or worse, mask one.
				if v.Recv != nil && execFns[v.Name.Name] {
					t.Errorf("%s: this package defines a method %q, colliding with a database/sql statement entry point the ratchet keys on", fset.Position(v.Pos()), v.Name.Name)
				}
			}
			return true
		})
	}

	var out []sqlLit
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !execFns[sel.Sel.Name] || len(call.Args) == 0 {
				return true
			}
			arg := call.Args[0]
			if strings.HasSuffix(sel.Sel.Name, "Context") && len(call.Args) > 1 {
				arg = call.Args[1]
			}
			where := fset.Position(arg.Pos()).String()
			sqlText, ok := renderString(arg, consts)
			if !ok {
				t.Errorf("%s: the SQL argument of %s could not be resolved statically, so this ratchet cannot inspect it; keep SQL in a package-level string rather than building it dynamically", where, sel.Sel.Name)
				return true
			}
			out = append(out, sqlLit{where: where, sql: sqlText})
			return true
		})
	}
	if len(out) < 8 {
		t.Fatalf("only %d SQL call sites found; the scanner is not seeing the package's statements", len(out))
	}
	return out
}

// renderString evaluates a string expression built from literals, package-level
// string identifiers and + concatenation. Anything else is unresolvable, which
// the caller treats as a failure.
func renderString(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, ok1 := renderString(v.X, consts)
		r, ok2 := renderString(v.Y, consts)
		return l + r, ok1 && ok2
	case *ast.ParenExpr:
		return renderString(v.X, consts)
	}
	return "", false
}

// grammar is the closed set of tokens that LOOK like calls but are SQL syntax:
// qualifying them does not parse. Extending it is a deliberate act — which is
// the property the inverted rule buys.
// grammar is the MEASURED minimal set of tokens that appear before a
// parenthesis in this package's SQL and are syntax rather than resolvable
// functions. It was derived by emptying it and reading what the scanner
// reported — not guessed. Keeping it minimal matters: every extra name is one a
// real PostgreSQL function could occupy, and listing it would blind the scanner
// to a genuine unqualified call (an earlier hand-written list carried ~48).
var grammar = map[string]bool{
	"coalesce": true, "greatest": true, "least": true, "extract": true,
	"exists": true, "in": true, "any": true, "values": true,
	"table": true, "from": true, "conflict": true,
}

var (
	callRE    = regexp.MustCompile(`(?i)([a-z_][a-z0-9_]*)\s*\(`)
	castRE    = regexp.MustCompile(`(?i)::\s*([a-z_][a-z0-9_.]*)`)
	eStringRE = regexp.MustCompile(`(^|[^a-zA-Z0-9_.])[eE]'`)
	// Object names this package creates; always addressed schema-qualified.
	ownObjects = []string{"ratelimit_buckets", "olivares_ratelimit_take"}
)

// blankNonCode replaces SQL line/block comments and double-quoted identifiers
// with spaces, preserving length so reported offsets stay meaningful. A comment
// can otherwise hide code from a reader while an apostrophe inside one flips the
// literal detector for everything after it. Double-quoted identifiers keep their
// CONTENT — only the quotes are blanked — because quoting is how a name evades
// case-folding, not a reason to stop looking at it.
func blankNonCode(sql string) string {
	b := []byte(sql)
	blank := func(i, j int) {
		for k := i; k < j && k < len(b); k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	for i := 0; i < len(b); i++ {
		switch {
		case b[i] == '\'':
			j := i + 1
			for j < len(b) && b[j] != '\'' {
				j++
			}
			i = j
		case b[i] == '"':
			// Blank only the QUOTES, never the name inside them. A quoted
			// identifier is still a name reference — and a more dangerous one,
			// because quoting bypasses PostgreSQL's down-folding, so `"now"()`
			// would otherwise vanish from the scan entirely (measured).
			j := i + 1
			for j < len(b) && b[j] != '"' {
				j++
			}
			b[i] = ' '
			if j < len(b) {
				b[j] = ' '
			}
			i = j
		case b[i] == '-' && i+1 < len(b) && b[i+1] == '-':
			j := i
			for j < len(b) && b[j] != '\n' {
				j++
			}
			blank(i, j)
			i = j
		case b[i] == '/' && i+1 < len(b) && b[i+1] == '*':
			j := i + 2
			for j+1 < len(b) && !(b[j] == '*' && b[j+1] == '/') {
				j++
			}
			blank(i, j+2)
			i = j + 1
		}
	}
	return string(b)
}

// qualifiedBy reports whether the token starting at `at` is prefixed by exactly
// `schema.` — checking the character BEFORE the prefix too, so a hostile
// `evilpg_catalog.now()` or `myschema.public.t` cannot pass by suffix match.
func qualifiedBy(sql string, at int, schema string) bool {
	want := schema + "."
	if at < len(want) {
		return false
	}
	if !strings.EqualFold(sql[at-len(want):at], want) {
		return false
	}
	if at-len(want) == 0 {
		return true
	}
	c := sql[at-len(want)-1]
	return !(c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'))
}

func checkQualified(t *testing.T, where, rawSQL string) {
	t.Helper()
	// E'' escape strings are legal SQL a contributor could reach for in good
	// faith, and they change literal parsing (backslash escapes) in a way this
	// scanner does not model — an E-string can blind the rest of the statement.
	// This package has no need for one, so the honest close is to forbid the
	// form rather than to grow the lexer until it handles every corner.
	if m := eStringRE.FindStringIndex(rawSQL); m != nil {
		t.Errorf("%s: E'' escape string in production SQL — this package must not use them: backslash-escape parsing is not modeled here, so an E-string can hide the rest of the statement from the qualification scan:\n  %s", where, excerptAround(rawSQL, m[0]))
	}
	sql := blankNonCode(rawSQL)
	own := map[string]bool{}
	for _, o := range ownObjects {
		own[o] = true
	}

	for _, m := range callRE.FindAllStringSubmatchIndex(sql, -1) {
		name := strings.ToLower(sql[m[2]:m[3]])
		if grammar[name] || insideLiteral(sql, m[3]) {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(strings.ToLower(sql[maxInt0(0, m[2]-14):m[2]])), " as") {
			continue // a table alias: INSERT INTO t AS b (...)
		}
		if qualifiedBy(sql, m[2], "pg_catalog") {
			continue
		}
		if own[name] && qualifiedBy(sql, m[2], "public") {
			continue
		}
		t.Errorf("%s: unqualified call %q — qualify it with pg_catalog. (an exact-signature shadow wins resolution regardless of search_path order), or classify it as grammar:\n  %s", where, name, excerptAround(sql, m[2]))
	}

	for _, m := range castRE.FindAllStringSubmatchIndex(sql, -1) {
		typ := strings.ToLower(sql[m[2]:m[3]])
		// The regex anchors at `::`, so the captured text IS the type reference:
		// a hostile `::evilpg_catalog.float8` captures "evilpg_catalog.float8"
		// and fails this prefix test, which is what makes it sufficient here.
		if insideLiteral(sql, m[3]) || strings.HasPrefix(typ, "pg_catalog.") {
			continue
		}
		t.Errorf("%s: unqualified cast %q — resolution covers TYPES: a DOMAIN of that name in a writable schema is selected by an unqualified cast, and its CHECK runs with the connecting role's privileges:\n  %s", where, typ, excerptAround(sql, m[2]))
	}

	for _, name := range ownObjects {
		re := regexp.MustCompile(`(?i)(^|[^.\w])` + regexp.QuoteMeta(name) + `\b`)
		for _, loc := range re.FindAllStringIndex(sql, -1) {
			if insideLiteral(sql, loc[1]) {
				continue // a NAME compared as a value, e.g. p.proname = 'x'
			}
			start := loc[1] - len(name)
			if qualifiedBy(sql, start, "public") || qualifiedBy(sql, start, "pg_catalog") {
				continue
			}
			t.Errorf("%s: unqualified relation %q — pg_temp is searched FIRST for relations even under a single trusted-schema path:\n  %s", where, name, excerptAround(sql, loc[0]))
		}
	}
}

// insideLiteral reports whether a position falls inside a single-quoted SQL
// string (odd number of quotes before it). Dollar-quoted bodies ($fn$…$fn$) are
// deliberately NOT treated as literals: that IS the function body, and it is the
// most important thing to scan.
func insideLiteral(s string, at int) bool { return strings.Count(s[:at], "'")%2 == 1 }

func maxInt0(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func excerptAround(s string, at int) string {
	start := maxInt0(0, at-60)
	end := at + 60
	if end > len(s) {
		end = len(s)
	}
	return "..." + strings.ReplaceAll(s[start:end], "\n", " ") + "..."
}
