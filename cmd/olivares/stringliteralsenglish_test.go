// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// stringliteralsenglish_test.go is the CLASS half of the US-English user-facing text
// contract (#588): the per-command tests prove each known message reads in English; this
// one walks every non-test string literal under cmd/, core/, modules/ and connectors/
// so the next Spanish literal cannot quietly ship in a file nobody re-audits. The audit
// found three independent producers this way (the security-response refusal, the work
// validation hints of #506, and the plugin noexec hint); a per-file fix leaves the class
// open, this walk closes it.
//
// Density, not membership: a Spanish word list would flag every borrowed noun. What
// separates Spanish prose from an English string that happens to contain "de" or "y" is
// the COUNT of Spanish function words, so a literal is Spanish only at three or more.
// The known literals measure 6-20. Ceiling, stated: a Spanish snippet of two function
// words or fewer escapes (measured: the original loader's "su dueno" initializer alone reads 1),
// and a Spanish text built only of words that are also English ("no", "son", "sea",
// "era", "solo") is invisible — those words are not in the list on purpose, because
// keeping them would flag "no son of mine", "a new era" and "solo mode".

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// spanishFunctionWords: the load-bearing words of Spanish prose that are not ordinary
// standalone English words. The product's Spanish literals strip accents ("asi",
// "comun", "dueno"), so matching is plain lowercase token equality, no folding needed.
// Single-letter Spanish words ("y", "o") are NOT here on purpose, measured: an SSH
// command with three "-o" options and SQL with the alias "o.rolsuper" both reached the
// threshold on that letter alone, and no known Spanish literal needs either to pass it.
var spanishFunctionWords = map[string]struct{}{
	"aunque": {}, "ambas": {}, "ambos": {}, "al": {}, "como": {}, "cual": {}, "cuando": {},
	"cada": {}, "de": {}, "del": {}, "desde": {}, "donde": {}, "el": {}, "en": {},
	"entonces": {}, "entre": {}, "es": {}, "esa": {}, "ese": {}, "eso": {}, "esta": {},
	"estan": {}, "este": {}, "estos": {}, "hacia": {}, "hasta": {}, "la": {}, "las": {},
	"los": {}, "mas": {}, "mismo": {}, "misma": {}, "muy": {}, "nada": {}, "nunca": {},
	"otra": {}, "otro": {}, "otras": {}, "otros": {}, "para": {}, "pero": {},
	"por": {}, "porque": {}, "que": {}, "quien": {}, "se": {}, "segun": {}, "siempre": {},
	"sin": {}, "sobre": {}, "su": {}, "sus": {}, "tambien": {}, "tampoco": {}, "todos": {},
	"todas": {}, "un": {}, "una": {}, "unas": {}, "unos": {},
}

// spanishDensity counts Spanish function-word tokens in s, tokens being maximal runs of
// letters, so "no-root" is "no"+"root" and "--sign-key" contributes nothing.
func spanishDensity(s string) int {
	count := 0
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if _, ok := spanishFunctionWords[strings.ToLower(tok)]; ok {
			count++
		}
	}
	return count
}

// spanishDensityThreshold is the count at which a literal is judged Spanish prose.
const spanishDensityThreshold = 3

// addSegment is one maximal run of contiguously concatenated string literals, or a gap
// holding the non-literal operand that breaks the chain: in `"a"+x+"b"` the operator
// sees "a" and "b" as separate texts, and only in-order flattening knows that. The gap
// expression is kept because its own literals still have to be judged.
type addSegment struct {
	lits    []*ast.BasicLit
	gapExpr ast.Expr
}

// flattenAdd flattens an expression into ordered segments. Parentheses are transparent
// because a parenthesized group still contributes its text at that position.
func flattenAdd(e ast.Expr, out *[]addSegment) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			*out = append(*out, addSegment{lits: []*ast.BasicLit{v}})
		}
	case *ast.ParenExpr:
		flattenAdd(v.X, out)
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			flattenAdd(v.X, out)
			flattenAdd(v.Y, out)
			return
		}
		*out = append(*out, addSegment{gapExpr: e})
	default:
		*out = append(*out, addSegment{gapExpr: e})
	}
}

// judgeSpanishLiterals reports every expression in n whose concatenated string-literal
// text reaches the Spanish density threshold, as file:line plus the offending text.
// An expression whose top level is a literal-bearing + chain is judged at that level and
// recursed only through its gap operands, so no literal is judged twice and none hides
// inside an operand the chain judgement skipped.
func judgeSpanishLiterals(n ast.Node, fset *token.FileSet, findings *[]string) {
	ast.Inspect(n, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		var segs []addSegment
		flattenAdd(e, &segs)
		hasLit := false
		for i := range segs {
			if segs[i].lits != nil {
				hasLit = true
				break
			}
		}
		if !hasLit {
			return true // nothing to judge here: descend
		}
		for i := 0; i < len(segs); i++ {
			if segs[i].gapExpr != nil {
				judgeSpanishLiterals(segs[i].gapExpr, fset, findings)
				continue
			}
			text := ""
			for j := i; j < len(segs) && segs[j].lits != nil; j++ {
				for _, lit := range segs[j].lits {
					decoded, err := strconv.Unquote(lit.Value)
					if err != nil {
						*findings = append(*findings, fset.Position(lit.Pos()).String()+" invalid string literal: "+err.Error())
						continue
					}
					text += decoded
				}
			}
			if spanishDensity(text) >= spanishDensityThreshold {
				*findings = append(*findings, fset.Position(segs[i].lits[0].Pos()).String()+" "+text)
			}
			for i < len(segs) && segs[i].lits != nil {
				i++
			}
			i--
		}
		return false // the literals here were judged; do not re-judge them singly
	})
}

// englishLiteralRoots are the trees whose runtime strings reach users or operators.
// testdata/ is skipped: those .go files are fixtures executed by tests, test material
// like _test.go itself. cmd/olivares/tools/ is skipped: separate main packages for
// contributor tooling, never linked into the product binary; their prose is the
// contributor-tooling lane (#594), not the product contract this gate guards.
var englishLiteralRoots = []string{"../../cmd", "../../core", "../../modules", "../../connectors"}

func TestProductStringLiteralsAreEnglish(t *testing.T) {
	// SELF-CHECK first, the tenantconfigclass lesson: a detector that silently stopped
	// matching anything would report a clean tree whatever the walk saw. The synthetic
	// pair below proves parse -> flatten -> density still fires on Spanish, spares
	// English, and keeps a Spanish literal visible inside a chain's gap operand.
	const src = `package p

const english = "the plugin runs under the dedicated uid and the mount is writable"
const chain = "prefix: " + explain("el plugin se lanza bajo un uid dedicado, sin permiso")
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "selfcheck.go", src, 0)
	if err != nil {
		t.Fatalf("parse self-check: %v", err)
	}
	var got []string
	judgeSpanishLiterals(f, fset, &got)
	if len(got) != 1 || !strings.Contains(got[0], "el plugin") {
		t.Fatalf("self-check: the detector no longer discriminates; findings: %q", got)
	}

	var findings []string
	for _, root := range englishLiteralRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				switch {
				case d.Name() == "testdata":
					return filepath.SkipDir
				case path == filepath.Clean("../../cmd/olivares/tools"):
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			judgeSpanishLiterals(f, fset, &findings)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	for _, f := range findings {
		t.Errorf("Spanish-density string literal (threshold %d Spanish function words): %s", spanishDensityThreshold, f)
	}
}

func TestSpanishLiteralDetectorReadsRuntimeText(t *testing.T) {
	for _, src := range []string{
		`package p; const text = "el\nplugin\nse\nlanza\nbajo\nun\nuid"`,
		`package p; const text = "e" + "l s" + "e u" + "n"`,
	} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "runtime-text.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		var findings []string
		judgeSpanishLiterals(f, fset, &findings)
		if len(findings) != 1 {
			t.Errorf("expected one Spanish literal in %s; got %q", src, findings)
		}
	}
}
