// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestHelpDoesNotAdvertiseUnavailableAddOns pins the E6 gate: the root help of
// the artifact that is actually distributed must not offer a command group whose
// every verb refuses in that artifact.
func TestHelpDoesNotAdvertiseUnavailableAddOns(t *testing.T) {
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs([]string{"--help"})
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	help := out.String()
	for _, name := range addOnOnlyCommands {
		listed := strings.Contains(help, "\n  "+name+" ")
		if thisEdition.addOnsLinked && !listed {
			t.Errorf("the enterprise build must list %q in the root help", name)
		}
		if !thisEdition.addOnsLinked && listed {
			t.Errorf("the root help offers %q, but every verb under it refuses in this build", name)
		}
	}
}

// TestUnavailableAddOnsStayInvocable is the other half, and the reason the fix
// hides rather than removes: an operator's existing script must keep getting the
// add-on's own explanation, not "unknown command".
func TestUnavailableAddOnsStayInvocable(t *testing.T) {
	root := newRootCmd()
	for _, name := range addOnOnlyCommands {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("%q is no longer invocable: %v", name, err)
		}
		if strings.TrimSpace(cmd.Long) == "" {
			t.Errorf("%q is hidden from the help, so its own --help is the only "+
				"explanation left; it must have a Long description", name)
		}
	}
}

// TestNoSourceOffersTheEnterpriseBuildTag stops the repaired instruction coming
// back. `go build -tags enterprise ./cmd/olivares/` fails on this repository
// with undefined symbols — the commercial tree moved to its own distribution in
// so any user-facing text that tells an operator to build with that tag
// is sending them to a compile error.
//
// Comments are exempt: the tag is still the real name of the seam, and the
// wiring files legitimately describe it. This scans STRING LITERALS.
func TestNoSourceOffersTheEnterpriseBuildTag(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	var offenders []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, rerr := os.ReadFile(name) //nolint:gosec // fixed package-local path
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, "-tags enterprise") {
				continue
			}
			// Only a quoted occurrence can reach an operator.
			if !strings.Contains(line, `"`) {
				continue
			}
			offenders = append(offenders, name+" line "+strings.TrimSpace(line[:min(len(line), 60)])+
				" (line "+strconv.Itoa(i+1)+")")
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d user-facing string(s) tell an operator to build with -tags enterprise, "+
			"which does not compile from this repository:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// enterpriseBuildWords is the vocabulary of a build users cannot make: "rebuild with
// the enterprise tag", "the enterprise build", "(enterprise add-on)", "builds compiled
// with the `enterprise` tag". The paid capabilities are named Business, with their
// family (docs/editions.md). The --enterprise flag, the `enterprise` command group and
// the Enterprise plan are published names and are not matched.
var enterpriseBuildWords = regexp.MustCompile("(?i)" +
	`(?:the|an?|this)\s+enterprise\b` +
	`|\benterprise[\s-]+(?:builds?|tags?|binar(?:y|ies)|editions?|supersets?|add-?ons?|capabilit(?:y|ies)|modules?|verifiers?|depth|downloads?|upgrades?)\b` +
	`|\(enterprise[;) ]` +
	`|^enterprise\s+(?:governance|risk|evidence)\b` +
	"|`enterprise`(?:\\s+and\\s+`addon_\\w+`)?\\s+tags?" +
	`|≡enterprise|enterprise≡` +
	`|(?:^|\n|# )enterprise:\s`)

// publishedEnterpriseNames are the edition names that stay: Enterprise is a real third
// edition. The match is case sensitive on purpose: the old "enterprise edition" that
// described the Business download is still caught.
var publishedEnterpriseNames = strings.NewReplacer("Enterprise plan", "", "Enterprise-only", "", "Enterprise edition", "")

func enterpriseBuildWording(text string) string {
	return enterpriseBuildWords.FindString(publishedEnterpriseNames.Replace(text))
}

// TestHelpNamesBusinessNotTheEnterpriseBuild walks every command, hidden ones
// included, and fails on build vocabulary in what `--help` prints. The Community
// binary hides the paid groups from the root help but their own --help stays
// reachable, and the generated CLI reference is built from this same tree.
func TestHelpNamesBusinessNotTheEnterpriseBuild(t *testing.T) {
	var offenders []string
	visited := map[string]bool{}
	walkCommands(newRootCmd(), func(c *cobra.Command) {
		visited[commandPathWithoutBinary(c)] = true
		check := func(where, text string) {
			if m := enterpriseBuildWording(text); m != "" {
				offenders = append(offenders, c.CommandPath()+" "+where+": "+strconv.Quote(m))
			}
		}
		check("Short", c.Short)
		check("Long", c.Long)
		check("Example", c.Example)
		c.LocalFlags().VisitAll(func(f *pflag.Flag) { check("--"+f.Name, f.Usage) })
	})
	// A walk that did not reach the paid groups proves nothing about them.
	for _, path := range []string{"hooks", "threatintel", "license install", "upgrade", "reporting enterprise", "compliance dora", "migrate manifest"} {
		if !visited[path] {
			t.Errorf("the command walk never reached %q: the guard is not looking at it", path)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d help text(s) name the enterprise build instead of Business and its family:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// concatText flattens a "a" + "b" + x + "c" chain into one text: adjacent literals join,
// anything that is not a string literal becomes a NUL.
func concatText(t *testing.T, fset *token.FileSet, e ast.Expr, sb *strings.Builder) {
	t.Helper()
	switch x := e.(type) {
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			concatText(t, fset, x.X, sb)
			concatText(t, fset, x.Y, sb)
			return
		}
	case *ast.ParenExpr:
		concatText(t, fset, x.X, sb)
		return
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, err := strconv.Unquote(x.Value)
			if err != nil {
				t.Fatalf("unquote %s: %v", fset.Position(x.Pos()), err)
			}
			sb.WriteString(s)
			return
		}
	}
	sb.WriteByte(0)
}

// TestNoStringNamesTheEnterpriseBuild is the same rule for the strings that are not
// help: the errors and hints a refused boot, login or upgrade prints. It reads the
// string literals (not comments) of this package, core/auth, compliance and
// capabilities and reporting, joining literals split across a "+" chain.
func TestNoStringNamesTheEnterpriseBuild(t *testing.T) {
	seen := map[string]bool{}
	var offenders []string
	literals := 0
	for _, dir := range []string{".", "../../core/auth", "../../modules/compliance", "../../modules/capabilities", "../../modules/reporting"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			t.Fatalf("no Go files in %s: the scan would see nothing", dir)
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, name, nil, 0)
			if perr != nil {
				t.Fatalf("parse %s: %v", name, perr)
			}
			check := func(pos token.Pos, text string) {
				literals++
				if m := enterpriseBuildWording(text); m != "" {
					msg := fset.Position(pos).String() + ": " + strconv.Quote(m)
					if !seen[msg] {
						seen[msg] = true
						offenders = append(offenders, msg)
					}
				}
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if x.Kind != token.STRING {
						return true
					}
					text, uerr := strconv.Unquote(x.Value)
					if uerr != nil {
						t.Fatalf("unquote %s: %v", fset.Position(x.Pos()), uerr)
					}
					check(x.Pos(), text)
				case *ast.BinaryExpr:
					if x.Op == token.ADD {
						var sb strings.Builder
						concatText(t, fset, x, &sb)
						check(x.Pos(), sb.String())
					}
				}
				return true
			})
		}
	}
	// Catalog prose feeds live OpenAPI, the committed snapshots and web types.
	catalog := "../../scripts/openapi-op-catalog.tsv"
	raw, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatalf("read %s: %v", catalog, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			t.Fatalf("%s:%d: malformed catalog row", catalog, i+1)
		}
		if m := enterpriseBuildWording(fields[2]); m != "" {
			offenders = append(offenders, catalog+":"+strconv.Itoa(i+1)+": "+strconv.Quote(m))
		}
	}
	if literals == 0 {
		t.Fatal("the scan saw no string literal")
	}
	if len(offenders) > 0 {
		t.Fatalf("%d string(s) name the enterprise build instead of Business:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
