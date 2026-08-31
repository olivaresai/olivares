// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const upgradeRegionID = "olivares-upgrade-channels"

// THE UPGRADE GUIDE IS WRITTEN, NOT GENERATED, AND THAT IS THE HONEST ANSWER.
//
// An upgrade runbook is judgement: when to take the security channel, what to do about a
// half-finished swap, how to decide that a rollback is the right call. None of that has a
// denominator, so generating it would mean inventing prose and calling it derived. What
// this file does instead is make the page's CHECKABLE claims checkable:
//
//	the channel roster is GENERATED from core/release.Channels — add a fourth channel
//	and the page goes red until it is regenerated;
//
//	every flag the page prints on an `olivares upgrade` command line must be a flag
//	cmd_upgrade.go actually registers. A runbook that tells an operator to run
//	`--dry-run` on a command with no such flag fails in front of them, at the worst
//	possible moment, and nothing in this repository could see it before today.
//
// And the check refuses to be vacuous: a page with no `olivares upgrade` command line at
// all is a finding, because a flag check over zero command lines passes forever.

type upgradeSurface struct {
	Channels []channelDecl
	// flags registered by newUpgradeCmd, long name -> usage
	flags map[string]upgradeFlag
	// claimed are the flags the published page prints on an upgrade command line.
	claimed     []string
	preFindings []string
	notes       []string
}

type channelDecl struct {
	Name string // "stable"
	Sym  string // "ChannelStable"
}

type upgradeFlag struct {
	Name      string
	Shorthand string
	Usage     string
}

func loadUpgrade(root string) (*upgradeSurface, error) {
	u := &upgradeSurface{flags: map[string]upgradeFlag{}}

	fset := token.NewFileSet()
	rel, err := parser.ParseFile(fset, filepath.Join(root, releaseRel), nil, parser.ParseComments)
	if err != nil {
		return nil, cannot("could not parse %s: %v", releaseRel, err)
	}
	values := map[string]string{}
	var order []string
	for _, decl := range rel.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gen.Tok == token.CONST {
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if !strings.HasPrefix(vs.Names[0].Name, "Channel") {
					continue
				}
				if v := strLit(vs.Values[0]); v != "" {
					values[vs.Names[0].Name] = v
				}
			}
		}
		if gen.Tok == token.VAR {
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 || vs.Names[0].Name != "Channels" {
					continue
				}
				lit, ok := vs.Values[0].(*ast.CompositeLit)
				if !ok {
					return nil, cannot("%s declares Channels but not as a slice literal", releaseRel)
				}
				for _, e := range lit.Elts {
					id, ok := e.(*ast.Ident)
					if !ok {
						return nil, cannot("%s lists a channel that is not a declared constant", releaseRel)
					}
					order = append(order, id.Name)
				}
			}
		}
	}
	// The ORDER is the declaration's own ("escalating-stability order"), not alphabetical:
	// re-sorting it here would publish a different meaning than the code states.
	for _, sym := range order {
		v, ok := values[sym]
		if !ok {
			return nil, cannot("%s lists %s in Channels and declares no value for it", releaseRel, sym)
		}
		u.Channels = append(u.Channels, channelDecl{Name: v, Sym: sym})
	}
	if len(u.Channels) < channelFloor {
		return nil, cannot("%s yields %d release channel(s), below the floor of %d; the roster was not read", releaseRel, len(u.Channels), channelFloor)
	}

	// The flags of `olivares upgrade`, from the cobra registration itself.
	cmdFile, err := parser.ParseFile(fset, filepath.Join(root, cmdUpgradeRel), nil, 0)
	if err != nil {
		return nil, cannot("could not parse %s: %v", cmdUpgradeRel, err)
	}
	ast.Inspect(cmdFile, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newUpgradeCmd" {
			return true
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasSuffix(sel.Sel.Name, "Var") && !strings.HasSuffix(sel.Sel.Name, "VarP") {
				return true
			}
			// pflag's registrars are (target, name, [shorthand,] value, usage).
			if len(call.Args) < 4 {
				return true
			}
			name := strLit(call.Args[1])
			if name == "" {
				return true
			}
			f := upgradeFlag{Name: name, Usage: strLit(call.Args[len(call.Args)-1])}
			if strings.HasSuffix(sel.Sel.Name, "VarP") && len(call.Args) >= 5 {
				f.Shorthand = strLit(call.Args[2])
			}
			u.flags[name] = f
			return true
		})
		return false
	})
	if len(u.flags) < upgradeFlagFloor {
		return nil, cannot("%s registers %d flag(s) on `olivares upgrade`, below the floor of %d; newUpgradeCmd was not read", cmdUpgradeRel, len(u.flags), upgradeFlagFloor)
	}

	u.notes = append(u.notes, fmt.Sprintf("upgrade: %d release channel(s) generated from %s; %d flag(s) of `olivares upgrade` available to check the page's claims against",
		len(u.Channels), releaseRel, len(u.flags)))
	return u, nil
}

// verifyClaims reads the published upgrade guide and checks every flag it tells an
// operator to type.
func (u *upgradeSurface) verifyClaims(page string) []string {
	var findings []string
	lines := strings.Split(page, "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$ "))
		// A command line is one that starts with the invocation. Prose that MENTIONS a
		// flag ("the --check flag") is deliberately out of scope: this checks what a
		// reader would copy and run, and widening it to every hyphenated token in the
		// page would report `--now` from a systemctl example as an upgrade flag.
		if !strings.HasPrefix(trimmed, "olivares upgrade") {
			continue
		}
		for _, tok := range strings.Fields(trimmed) {
			if !strings.HasPrefix(tok, "--") {
				continue
			}
			name := strings.TrimPrefix(tok, "--")
			if k := strings.IndexByte(name, '='); k >= 0 {
				name = name[:k]
			}
			name = strings.Trim(name, "`\"',.;:)")
			if name == "" {
				continue
			}
			if _, ok := u.flags[name]; !ok {
				findings = append(findings, fmt.Sprintf(
					"%s:%d tells the reader to run `olivares upgrade --%s` and %s registers no such flag; the command would fail in front of the operator",
					upgradePageRel, i+1, name, cmdUpgradeRel))
				continue
			}
			if !seen[name] {
				seen[name] = true
				u.claimed = append(u.claimed, name)
			}
		}
	}
	sort.Strings(u.claimed)
	// ANTI-VACUITY. A flag check that inspected no command line passes on an empty page,
	// on a page whose examples were deleted, and on a page whose fences stopped being
	// recognised. It must say so instead of reporting clean.
	if len(u.claimed) == 0 {
		findings = append(findings, fmt.Sprintf(
			"%s prints no `olivares upgrade` command line, so the flag-claim check verified nothing at all", upgradePageRel))
	}
	return findings
}

func (u *upgradeSurface) print(w io.Writer) {
	fmt.Fprintf(w, "# upgrade — %d channels, %d flags\n", len(u.Channels), len(u.flags))
	for _, c := range u.Channels {
		fmt.Fprintf(w, "channel\t%s\t%s\n", c.Name, c.Sym)
	}
	names := make([]string, 0, len(u.flags))
	for n := range u.flags {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(w, "flag\t--%s\t%s\n", n, u.flags[n].Usage)
	}
}

func (u *upgradeSurface) region() string {
	var b strings.Builder
	fmt.Fprintf(&b, "`olivares upgrade` follows a release **channel**. There are **%d**, and they are declared in\n", len(u.Channels))
	fmt.Fprintf(&b, "`%s` in escalating-stability order:\n\n", releaseRel)
	b.WriteString("| `--channel` value | Declared as |\n")
	b.WriteString("|---|---|\n")
	for _, c := range u.Channels {
		// The value is published verbatim. An earlier version title-cased it for looks and
		// printed "Lts", which is not what an operator types and not what the code calls it:
		// a display name invented by the renderer is a third name for one thing.
		fmt.Fprintf(&b, "| `%s` | `release.%s` |\n", c.Name, c.Sym)
	}
	b.WriteString("\nA value outside this table is rejected before anything is downloaded (`release.ValidChannel`).\n")
	return b.String()
}

func applyUpgrade(root string, u *upgradeSurface, write bool) (*surface, error) {
	s := &surface{name: "upgrade", findings: append([]string{}, u.preFindings...), notes: u.notes}
	if len(s.findings) > 0 && write {
		return s, nil
	}
	keys := make([]string, 0, len(u.Channels))
	for _, c := range u.Channels {
		keys = append(keys, "`"+c.Name+"`")
	}
	f, err := applyRegion(root, upgradePageRel, upgradeRegionID, u.region(), write, &keySpec{
		noun: "release channel", want: keys, col: 0,
		fix: "Regenerate with `bash scripts/check-guide-docs.sh --write`.",
	})
	if err != nil {
		return nil, err
	}
	s.findings = append(s.findings, f...)

	// Read the page AFTER a --write pass, so the claims are checked against what is now
	// published rather than against what was there when the run started.
	page, err := readPage(root, upgradePageRel)
	if err != nil {
		return nil, err
	}
	s.findings = append(s.findings, u.verifyClaims(page)...)
	s.notes = append(s.notes, fmt.Sprintf("upgrade: %d distinct flag claim(s) in %s checked against the binary's own registration", len(u.claimed), upgradePageRel))
	return s, nil
}
