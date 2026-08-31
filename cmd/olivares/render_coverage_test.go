// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// renderExemptMarker lets a command file that formats output on its own say why,
// in the source, next to the code.
//
// CONTRACT, widened on 2026-08-23 when this gate was rebuilt: the marker covers
// BOTH directions — a table built over a writer the command chose itself, and a
// JSON document hand-marshalled inside the text branch. It used to be documented
// as "formats output WITHOUT renderOut", which described only the first.
const renderExemptMarker = "render-exempt:"

// TestCommandFilesRenderThroughRenderOut is the E2 gate as a test: a command file
// that formats structured output must let renderOut decide the shape, so the
// global -o/--output the root help advertises actually reaches it.
//
// The rule is false in BOTH directions, which is why this checks two things and
// not one. Before some leaves ignored `-o json` and printed their table
// regardless (sources ls, dr ls, eventing egress status); others ignored `-o text`
// and printed JSON regardless (keys status, license status, license verify, fixed
// in 41382bdd0). Each was a leaf that built its own output instead of handing a
// value to the one renderer.
//
// ⛔ REBUILT 2026-08-23, AND THE OLD VERSION WAS WRONG IN BOTH DIRECTIONS AT ONCE.
// It looked for the literal strings `tabwriter.NewWriter` / `MarshalIndent` /
// `SetIndent` and then asked whether a renderer was mentioned within TWELVE LINES
// above. Measured the day it was replaced:
//
//   - ALL SIX of its offenders were FALSE POSITIVES. `renderOut` (render.go:160)
//     branches on `selectedOutput` BEFORE it calls textFn, so under `-o json` the
//     text closure never runs and what it contains cannot reach stdout. Three of
//     the six were the bodies of writeKillSwitchTable / writeBreakGlassTable /
//     writeApprovalTable, whose six call sites are all argument #1 of renderOut;
//     the other three sat 38, 17 and 18 lines inside their own renderOut closure.
//     Confirmed empirically, not only by reading: driving the built binary against
//     a stub control plane, all five governance/pdp verbs emit parseable JSON on
//     stdout under `-o json`, and cmd_sources_get_test.go already json.Unmarshals
//     the whole stdout of `sources get -o json`.
//
//   - It saw 51 of 99 table constructions. `newTabWriter` (cmd_compliance.go:370)
//     wraps tabwriter.NewWriter, and 60 of the tree's constructions spell it that
//     way — invisible to a check keyed on the spelling of the constructor.
//
//   - And the same window hid a REAL defect of the opposite polarity, because the
//     offending MarshalIndent sat ONE line below its renderOut and was therefore
//     absolved: cmd_protocol_binding.go marshalled JSON inside its own text branch,
//     so `-o text` printed JSON. Exactly the defect, three years of precedent,
//     and the gate written to catch it could not see it.
//
// ⛔ AND WIDENING THE WINDOW IS NOT THE REPAIR — it converts false positives into
// false NEGATIVES. The three helper bodies sit 18, 23 and 23 lines below a
// renderOut call, but those calls belong to governanceKillSwitchListCmd,
// governanceBreakGlassUsesCmd and governanceApprovalsDecisionsCmd — commands that
// never call the helper underneath them. A 24-line window would absolve them by
// FILE LAYOUT, and would equally absolve a genuinely non-compliant table dropped
// anywhere below any renderOut. That is a weaker gate wearing a green light.
//
// So the question is answered STRUCTURALLY instead of by proximity:
//
//	a table is compliant when its writer is an io.Writer PARAMETER of the
//	enclosing function or closure — i.e. the destination was handed in, which is
//	what renderOut does at render.go:166 — and an offender when it is built over
//	cmd.OutOrStdout(), os.Stdout or any writer the code picked for itself.
//
// That distinction is precisely what separates the six false positives from the
// real defects, and it does not care how the constructor is spelled.
func TestCommandFilesRenderThroughRenderOut(t *testing.T) {
	files, err := filepath.Glob("cmd_*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var sources []string
	for _, name := range files {
		if !strings.HasSuffix(name, "_test.go") {
			sources = append(sources, name)
		}
	}
	if len(sources) < 40 {
		t.Fatalf("globbed only %d command files; the scan is not seeing the package", len(sources))
	}

	fset := token.NewFileSet()
	var offenders []string
	tables, jsonInText := 0, 0

	for _, name := range sources {
		raw, rerr := os.ReadFile(name) //nolint:gosec // fixed package-local path
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		lines := strings.Split(string(raw), "\n")

		file, perr := parser.ParseFile(fset, name, raw, parser.ParseComments)
		if perr != nil {
			// A command file this package compiles must parse. Refusing here is the
			// third answer: "I could not look" is not "clean".
			t.Fatalf("parse %s: %v", name, perr)
		}

		textFns := renderTextClosures(file)
		var stack []renderFrame

		var visit func(ast.Node) bool
		visit = func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.FuncDecl:
				stack = append(stack, renderFrame{writers: renderWriterParams(v.Type)})
				if v.Body != nil {
					ast.Inspect(v.Body, visit)
				}
				stack = stack[:len(stack)-1]
				return false
			case *ast.FuncLit:
				stack = append(stack, renderFrame{
					writers:  renderWriterParams(v.Type),
					isTextFn: textFns[v],
				})
				ast.Inspect(v.Body, visit)
				stack = stack[:len(stack)-1]
				return false
			case *ast.CallExpr:
				line := fset.Position(v.Pos()).Line
				if writer, ok := renderTableWriter(v); ok {
					tables++
					if !renderHandedWriter(stack, writer) && !renderExempt(lines, line) {
						offenders = append(offenders, fmt.Sprintf(
							"%s:%d: table built over a writer this command chose itself (%s), "+
								"so -o json cannot suppress it", name, line, renderExprText(writer)))
					}
				}
				if renderJSONTell(v) && renderInTextClosure(stack) {
					jsonInText++
					if !renderExempt(lines, line) {
						offenders = append(offenders, fmt.Sprintf(
							"%s:%d: JSON marshalled inside the TEXT branch renderOut invoked, "+
								"so -o text prints JSON", name, line))
					}
				}
			}
			return true
		}
		ast.Inspect(file, visit)
	}

	// NON-VACUITY, both halves. A scan that resolved nothing would otherwise pass
	// exactly like a clean tree — the failure mode this file exists to end.
	if tables == 0 {
		t.Fatalf("scanned %d files and found NO table construction; the detector is not resolving", len(sources))
	}
	if jsonInText == 0 {
		t.Fatalf("scanned %d files and found NO JSON inside a render text closure, not even an "+
			"exempt one; the -o text direction of this gate is not resolving", len(sources))
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("%d place(s) format structured output without letting renderOut decide the shape. "+
			"Hand the value to renderOut/renderListOut/renderReportOut/renderStatusOut and write the "+
			"text form over the io.Writer it passes, or state why not with a `// %s <reason>` comment "+
			"above it:\n  %s", len(offenders), renderExemptMarker, strings.Join(offenders, "\n  "))
	}
}

// renderFrame is one function or closure on the way down to a formatting call.
type renderFrame struct {
	writers  map[string]bool // io.Writer parameters this frame was handed
	isTextFn bool            // this closure IS the text branch renderOut invokes
}

// renderRenderers are the calls that decide text-vs-JSON. renderListOut takes its
// per-row text function in a different position, which is why the arg index is
// resolved per name rather than assumed.
var renderRenderers = map[string]int{
	"renderOut":       1,
	"renderReportOut": 1,
	"renderStatusOut": 1,
	"renderListOut":   3,
}

// renderTextClosures collects every function literal handed to a renderer as its
// text branch. That set is what makes "inside the text branch" a structural fact
// instead of a distance in lines.
func renderTextClosures(file *ast.File) map[*ast.FuncLit]bool {
	out := map[*ast.FuncLit]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		idx, ok := renderRenderers[name.Name]
		if !ok || len(call.Args) <= idx {
			return true
		}
		if lit, ok := call.Args[idx].(*ast.FuncLit); ok {
			out[lit] = true
		}
		return true
	})
	return out
}

// renderWriterParams names the io.Writer parameters of a signature.
func renderWriterParams(ft *ast.FuncType) map[string]bool {
	out := map[string]bool{}
	if ft == nil || ft.Params == nil {
		return out
	}
	for _, field := range ft.Params.List {
		sel, ok := field.Type.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "io" || sel.Sel.Name != "Writer" {
			continue
		}
		for _, ident := range field.Names {
			out[ident.Name] = true
		}
	}
	return out
}

// renderTableWriter reports the writer a table is being built over, for either
// spelling of the constructor. `newTabWriter` is not a nicety: 60 of the tree's
// 99 table constructions use it, and keying on `tabwriter.NewWriter` alone left
// every one of them unchecked.
func renderTableWriter(call *ast.CallExpr) (ast.Expr, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		if ok && pkg.Name == "tabwriter" && fn.Sel.Name == "NewWriter" && len(call.Args) > 0 {
			return call.Args[0], true
		}
	case *ast.Ident:
		if fn.Name == "newTabWriter" && len(call.Args) > 0 {
			return call.Args[0], true
		}
	}
	return nil, false
}

// renderJSONTell reports a hand-built JSON document.
func renderJSONTell(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "MarshalIndent" || sel.Sel.Name == "SetIndent"
}

// renderHandedWriter reports whether the writer was handed to some frame on the
// way in, rather than chosen here. Walking the whole stack — not just the nearest
// frame — is what lets a helper legitimately take `out io.Writer` and a closure
// legitimately use the one renderOut passed it.
func renderHandedWriter(stack []renderFrame, writer ast.Expr) bool {
	ident, ok := writer.(*ast.Ident)
	if !ok {
		return false
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].writers[ident.Name] {
			return true
		}
	}
	return false
}

// renderInTextClosure reports whether we are lexically inside the text branch a
// renderer invoked.
func renderInTextClosure(stack []renderFrame) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].isTextFn {
			return true
		}
	}
	return false
}

// renderExempt reports a written reason above the formatting. The reason has to
// have some substance: an empty marker would be a way to switch the gate off
// without saying anything.
func renderExempt(lines []string, line int) bool {
	for i := line - 1; i >= 0 && i > line-1-8; i-- {
		if i >= len(lines) {
			continue
		}
		pos := strings.Index(lines[i], renderExemptMarker)
		if pos < 0 {
			continue
		}
		if len(strings.TrimSpace(lines[i][pos+len(renderExemptMarker):])) >= 10 {
			return true
		}
	}
	return false
}

// renderExprText renders an expression compactly enough to name it in a failure.
func renderExprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return renderExprText(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return renderExprText(v.Fun) + "()"
	case *ast.UnaryExpr:
		return "&" + renderExprText(v.X)
	}
	return "that expression"
}
