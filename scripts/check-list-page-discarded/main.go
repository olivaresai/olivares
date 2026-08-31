// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command check-list-page-discarded fails when a caller throws away the
// model.Page a List returns AND asks for no page size, so a truncated list
// reaches the operator as if it were the whole list.
//
// WHAT IT ENFORCES, and it is one rule with two halves:
//
//	recs, _, err := repo.List(ctx, q)   // the page is discarded
//	q has no Limit                      // so the store's default caps it
//
// Either half alone is fine. Discarding the page after asking for `Limit: 1` is
// a point query. Omitting the Limit while KEEPING the page leaves the evidence of
// truncation in the caller's hands — what they do with it is their business, and
// this gate has no opinion. It is the CONJUNCTION that destroys the evidence:
// core/internal/store/sqlstore/generic.go:26-30 caps an unbounded query at
// defaultLimit = 100, and a caller who also drops the Page cannot know it
// happened, cannot say so, and cannot page.
//
// WHY IT EXISTS, measured 2026-08-28 over the whole tree with go/ast:
//
//	.List( calls with assignment .................. 549
//	  discard the page ............................ 259
//	  carry no Limit .............................. 110
//	  BOTH — the blind intersection ...............   3
//
// The three were `olivares eventing subscriptions ls`, `deliveries ls` and
// `dead-letters ls` (cmd/olivares/cmd_eventing.go:148, :1111, :1194). A tenant with
// 101 subscriptions was shown 100 and told nothing; an operator reading the
// dead-letter list to decide what to redeliver acted on a list that had been cut
// behind them. Zero gates watched for it.
//
// IT IS STRUCTURAL, NOT NOMINAL. It matches the SHAPE of the assignment and the
// query, never the name of a variable, a method or a package. The census that
// opened this work started as a `grep` and gave four different answers — 143, 228,
// 374 and 539 — depending on the pattern, because most of these calls span several
// lines and 36 % of them pass the query in a variable. What a caller is called is a
// convention; the shape is what the defect needs in order to exist.
//
// THERE IS NO ALLOWLIST, and none is needed: after the three fixes the tree holds
// ZERO violations. An exemption whose reason has expired is a hole with a comment
// on it, and a gate that starts at zero can be held at zero.
//
// WHAT THIS GATE'S DISCOVERY MECHANISM DOES NOT REACH, named here because a gate
// says what it DISCOVERS and not what it checks. It decides the cap question from a
// model.Query literal at the call site, or from an assignment of one to the query
// variable WITHIN the enclosing function. Measured 2026-08-29, that leaves 172 of the
// 549 calls undecided, and the numbers are re-derivable with `--json`:
//
//	121  the query identifier is never assigned a model.Query literal in this
//	     function — a parameter, a closure capture, or a value from a helper
//	     (108 `q`, 7 `cursor`, 4 `query`, 1 `frameQ`, 1 `selector`)
//	 51  the argument is neither a composite literal nor a plain identifier
//
// Those 172 are reported as UNDECIDED and do not fail the gate. Failing them closed
// would make it red on arrival and therefore useless; passing them silently would let
// it claim a coverage it does not have. Deciding them needs interprocedural analysis:
// a helper that builds the query may well set the cap inside, and this gate cannot
// see that. A caller who wants certainty should pass the query as a literal at the
// call site, which is also the shape a reader can check without chasing a variable.
//
// ⛔ AND THE NUMBER THIS REPLACED IS THE LESSON. An earlier version of this checker
// counted an identifier as "a query that never gets a cap" as soon as it saw it
// assigned ANYTHING, so `q := buildQuery()` was reported capless. That produced 110
// "no Limit" where the honest count is 3 decided plus 172 undecided — and 110 was
// published as confirming another lane's independent census of ~105. Two numbers
// from two methods landing near each other is arithmetic, not verification. What
// held under both classifiers, and what this gate is actually for, is the
// intersection: 3.
package main

import (
	"encoding/json"
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

// finding is one call whose page is discarded and whose query declares no cap.
type finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Via  string `json:"via"`
}

type counts struct {
	Calls     int `json:"calls"`
	Discard   int `json:"discard_page"`
	NoLimit   int `json:"no_limit"`
	Undecided int `json:"undecided"`
	// UndecidedBy breaks the undecided total down by reason, so the doc comment's
	// numbers can be re-derived by running the gate instead of being trusted.
	UndecidedBy map[string]int `json:"undecided_by,omitempty"`
}

func main() {
	var root string
	var selfTest bool
	var asJSON bool
	flag.StringVar(&root, "root", ".", "tree to scan")
	flag.BoolVar(&selfTest, "self-test", false, "run the built-in controls and exit")
	flag.BoolVar(&asJSON, "json", false, "emit the census as JSON")
	flag.Parse()
	// A positional directory wins over --root so this reads like its sibling
	// scripts/check-error-mappers, which its wrapper is copied from.
	if flag.NArg() >= 1 {
		root = flag.Arg(0)
	}

	if selfTest {
		if err := runSelfTest(); err != nil {
			fmt.Fprintf(os.Stderr, "check-list-page-discarded: SELF-TEST FAILED — %v\n", err)
			os.Exit(1)
		}
		fmt.Println("check-list-page-discarded: self-test CLEAN — the checker fires on the defect and stays quiet on each legitimate shape.")
		return
	}

	found, c, err := scan(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-list-page-discarded: COULD NOT LOOK — %v\n", err)
		os.Exit(2)
	}
	// ⛔ WHEN --json IS ON, asJSON is the only writer on stdout. This gate exists
	// because three CLI verbs put a truncated list in front of an operator; shipping
	// it with a human sentence appended AFTER a JSON document would break `| jq` for
	// exactly the reason cmd_observeplane.go:452 states and the fix above honours —
	// a notice must not put a second document in front of the bytes a pipe reads.
	out := os.Stdout
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"counts": c, "findings": found})
		out = os.Stderr
	}
	if len(found) > 0 {
		fmt.Fprintf(os.Stderr, "check-list-page-discarded: FAIL — %d call(s) discard model.Page AND declare no Limit:\n", len(found))
		for _, f := range found {
			fmt.Fprintf(os.Stderr, "  %s:%d  (query %s)\n", f.File, f.Line, f.Via)
		}
		fmt.Fprintf(os.Stderr, "\nEither keep the page and report the truncation, or declare a Limit.\n")
		fmt.Fprintf(os.Stderr, "cmd/olivares/cmd_eventing.go shows the shape this gate was written for.\n")
		os.Exit(1)
	}
	// The wording is deliberate. It counts only the calls this gate can DECIDE; the
	// undecided ones are neither capped nor uncapped as far as it knows, and a green
	// line reading "0 carry no Limit" would be read as "every call is capped" — a
	// claim about 172 calls it has not made.
	// TWO DENOMINATORS, kept apart on purpose. Whether the page is discarded is a
	// syntactic fact about the assignment and is known for EVERY call. Whether a cap
	// was declared is a dataflow question and is not. Reporting both against one
	// total would overstate the reach of the half that has less.
	fmt.Fprintf(out, "check-list-page-discarded: CLEAN — %d List calls, %d of them discard the page. Of the %d whose cap it can decide, %d declare none, and 0 calls do both. %d undecided (see the package doc).\n",
		c.Calls, c.Discard, c.Calls-c.Undecided, c.NoLimit, c.Undecided)
}

// scan walks root and returns the violations plus the census that frames them.
func scan(root string) ([]finding, counts, error) {
	var out []finding
	var c counts
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			b := filepath.Base(p)
			if b == "vendor" || b == "node_modules" || b == "testdata" || (len(b) > 1 && strings.HasPrefix(b, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			// A file this gate cannot parse is not a clean file: say so.
			return fmt.Errorf("parse %s: %w", p, perr)
		}
		rel, _ := filepath.Rel(root, p)
		inspectFile(fset, f, rel, &out, &c)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, c, err
}

func inspectFile(fset *token.FileSet, f *ast.File, rel string, out *[]finding, c *counts) {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		limited, assigned := queryFacts(fn.Body)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "List" || len(call.Args) < 2 {
				return true
			}
			c.Calls++
			discards := len(as.Lhs) == 3 && isBlank(as.Lhs[1])
			if discards {
				c.Discard++
			}
			state, via := limitState(call.Args[1], limited, assigned)
			switch state {
			case limitAbsent:
				c.NoLimit++
			case limitUndecided:
				c.Undecided++
				if c.UndecidedBy == nil {
					c.UndecidedBy = map[string]int{}
				}
				c.UndecidedBy[via]++
			}
			if discards && state == limitAbsent {
				*out = append(*out, finding{File: rel, Line: fset.Position(as.Pos()).Line, Via: via})
			}
			return true
		})
	}
}

type limitVerdict int

const (
	limitPresent limitVerdict = iota
	limitAbsent
	limitUndecided
)

// limitState answers whether the query passed to List declares a page size.
func limitState(arg ast.Expr, limited, assigned map[string]bool) (limitVerdict, string) {
	switch a := arg.(type) {
	case *ast.CompositeLit:
		if !isQueryLit(a) {
			return limitUndecided, "query literal is not a model.Query"
		}
		if literalHasLimit(a) {
			return limitPresent, "literal with Limit"
		}
		return limitAbsent, "literal without Limit"
	case *ast.Ident:
		switch {
		case limited[a.Name]:
			return limitPresent, "var " + a.Name + " (Limit assigned)"
		case assigned[a.Name]:
			return limitAbsent, "var " + a.Name + " (never assigned a Limit)"
		default:
			return limitUndecided, "var " + a.Name + " (declared outside this function)"
		}
	default:
		return limitUndecided, "query is not a literal or an identifier"
	}
}

// queryFacts records, for one function body, which identifiers ever receive a
// Limit and which are assigned at all. Both halves matter: an identifier this
// function never assigns is a parameter or a capture, and the answer for it is
// UNDECIDED rather than "no Limit" — reporting it as a violation would accuse a
// caller whose cap may be set two frames up.
func queryFacts(body *ast.BlockStmt) (limited, assigned map[string]bool) {
	limited = map[string]bool{}
	assigned = map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, l := range as.Lhs {
			if se, ok := l.(*ast.SelectorExpr); ok && se.Sel.Name == "Limit" {
				if id, ok := se.X.(*ast.Ident); ok {
					limited[id.Name] = true
				}
				continue
			}
			id, ok := l.(*ast.Ident)
			if !ok || id.Name == "_" {
				continue
			}
			if i < len(as.Rhs) {
				cl, ok := as.Rhs[i].(*ast.CompositeLit)
				if !ok || !isQueryLit(cl) {
					// Assigned something this gate cannot vouch for: leave the
					// identifier UNDECIDED rather than call it a query without a cap.
					continue
				}
				assigned[id.Name] = true
				if literalHasLimit(cl) {
					limited[id.Name] = true
				}
			}
		}
		return true
	})
	return limited, assigned
}

// isQueryLit anchors the whole check to model.Query, so this gate can only ever
// accuse the family it is about.
//
// It matters even though the tree has no counter-example today: measured
// 2026-08-28, all 296 composite literals passed to a .List( are model.Query and
// none is any other type. But nothing STRUCTURAL stopped a future
// `foo.List(ctx, SomeOptions{})` with three returns and a discarded middle from
// being reported here, and a gate that can name the wrong subject gets ignored
// the first time it does — which is the failure that outlives the accusation.
func isQueryLit(cl *ast.CompositeLit) bool {
	switch t := cl.Type.(type) {
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && id.Name == "model" && t.Sel.Name == "Query"
	case *ast.Ident:
		// Inside package model itself the type is unqualified.
		return t.Name == "Query"
	default:
		return false
	}
}

func literalHasLimit(cl *ast.CompositeLit) bool {
	for _, el := range cl.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Limit" {
				return true
			}
		}
	}
	return false
}

func isBlank(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "_"
}
