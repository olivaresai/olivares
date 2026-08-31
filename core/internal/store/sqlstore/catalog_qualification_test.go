// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// riskyCatalogFunctions are the pg_catalog functions this engine calls whose BUILTIN
// signature is not an exact match for the call site — because that is precisely the
// condition under which a user-defined function captures the call.
//
// The distinction is measured, not assumed (PostgreSQL 15.18):
//
//	public.pg_roles view over pg_catalog.pg_roles   -> pg_catalog wins (relations are safe)
//	public.current_setting(text)                    -> pg_catalog wins (identical signature)
//	public.hashtextextended(text, integer)          -> CAPTURES
//	public.format(text, text)                       -> CAPTURES  (builtin is variadic)
//
// The hashtextextended case is worth stating precisely, because an earlier revision of
// this comment gave the right conclusion for the wrong reason. The builtin is
// `hashtextextended(text, bigint)`, NOT `("any", bigint)` — verified against pg_proc. It
// is captured because the call site passes the literal `0`, which is `integer`, so a
// user-defined `(text, integer)` is the more exact match.
//
// Relations and identically-signed functions are protected by pg_catalog's implicit
// precedence. Overload resolution is NOT: an exact match anywhere on the search path
// beats a variadic or "any"-typed builtin, whatever the schema order, so pinning
// search_path does not close this. Only qualification does.
//
// Functions with identical signatures are listed anyway. They are safe today, but the
// safety depends on a subtlety no reader should have to re-derive, and qualifying costs
// nothing.
var riskyCatalogFunctions = []string{
	"current_schema",
	"current_setting",
	"format",
	"has_schema_privilege",
	"has_table_privilege",
	"hashtextextended",
	"pg_advisory_lock",
	"pg_advisory_unlock",
	"pg_advisory_xact_lock",
	"pg_get_userbyid",
	"pg_has_role",
	"pg_terminate_backend",
	"pg_try_advisory_lock",
	"set_config",
}

// TestNoUnqualifiedCatalogFunctionsInProductionSQL is the DURABLE half of a fix that
// otherwise depends on everyone remembering a rule. It earned its place immediately: the
// hand-written sweep that ADDED it had omitted set_config and pg_terminate_backend
// (they had been qualified by hand earlier and were ticked off from memory), which left
// the RLS tenant binding unqualified. This test found that within minutes.
//
// WHAT IT DOES NOT DO — stated because a guard that is trusted for more than it checks is
// worse than no guard. It does NOT close the class. It catches the listed function names,
// written literally, adjacent to their paren, in non-test Go under core/internal/store.
// It is blind to:
//   - function names not on the list, and names that reach SQL via concatenation,
//     fmt.Sprintf fragments, or any SQL assembled at runtime;
//   - RELATIONS, TYPES, CASTS and OPERATORS, which are also search_path-resolved — the
//     provisioning path is protected from those by running on a trusted search_path
//     (trustedProvisioningPath), not by this test;
//   - .sql migration files, and every other Go module (a known live example outside this
//     scope: core/api/ratelimit/pgstore, which is another lane's file).
//
// It scans only string literals, via the Go parser, so comments explaining the attack are
// not mistaken for call sites — and so a name assembled outside a literal is invisible.
//
// So: it is a ratchet against regression in the engine's own SQL, not a proof of absence.
func TestNoUnqualifiedCatalogFunctionsInProductionSQL(t *testing.T) {
	t.Parallel()

	pats := make(map[string]*regexp.Regexp, len(riskyCatalogFunctions))
	for _, fn := range riskyCatalogFunctions {
		// Capture any qualifier so a WRONG one is caught too: an earlier version only
		// required "not preceded by a dot", which quietly accepted `public.format(` —
		// the very shape of the attack. The paren must be ADJACENT: SQL calls are
		// written that way, and allowing whitespace matched English prose in comments
		// ("pg_terminate_backend (PostgreSQL 14+)").
		pats[fn] = regexp.MustCompile(`(?:^|[^\w.])((?:[a-zA-Z_][\w]*\.)?)` + regexp.QuoteMeta(fn) + `\(`)
	}

	// The engine's whole SQL surface: this package plus the dialects that render its
	// statements. Relative to the package directory, which is where `go test` runs.
	root := ".."
	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Parse rather than grep the raw text: SQL reaches the server only through
		// STRING LITERALS, and scanning the whole file flags the comments that explain
		// this very attack ("a same-signature other.set_config(…) returns the expected
		// value"). Prose is not a call site.
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			for fn, re := range pats {
				for _, m := range re.FindAllStringSubmatchIndex(lit.Value, -1) {
					qualifier := lit.Value[m[2]:m[3]]
					if qualifier == "pg_catalog." {
						continue
					}
					what := "unqualified"
					if qualifier != "" {
						what = "qualified as " + qualifier + " instead of pg_catalog."
					}
					// The literal's own start line plus the newlines before the match:
					// exact for raw strings spanning many lines.
					line := fset.Position(lit.Pos()).Line + strings.Count(lit.Value[:m[0]], "\n")
					offenders = append(offenders,
						filepath.ToSlash(path)+":"+itoa(line)+": "+what+" "+fn+"(")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(offenders) > 0 {
		t.Errorf(`%d unqualified pg_catalog call(s) in engine SQL.

Qualify them as pg_catalog.<name>(. An unqualified catalog call is resolved against the
connection's search_path, and a user-defined function that matches the call site more
exactly than the builtin captures it — measured: a public.format(text,text) intercepts
SELECT format($1::text,$2::text) even with pg_catalog implicitly first. Where the result
is then EXECUTEd, that is arbitrary SQL running with the caller's privileges.

%s`, len(offenders), strings.Join(offenders, "\n"))
	}
}

// itoa avoids pulling strconv in for one call in one test.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
