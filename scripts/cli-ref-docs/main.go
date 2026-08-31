// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command cli-ref-docs generates the public CLI reference from the olivares
// command tree and fails when the published page and the binary disagree.
//
// THE REGRESSION IT FORBIDS. Measured 2026-08-16 on this branch: the binary
// registers 700 command nodes carrying 2209 flags, and
// docs-site/src/content/docs/reference/cli.md documented FOUR of them
// (`version`, `serve`, `collector`, `openapi`) with a hand-written flag table
// that listed 7 of `serve`'s 19 flags. Nothing was red. A hand-maintained
// reference for a surface this size is stale the day the 701st command lands,
// so the roster is ENUMERATED FROM THE BINARY and the page is REGENERATED from
// that enumeration; this gate fails when the two disagree, and names what moved.
//
// WHERE THE ENUMERATION COMES FROM, and why not from the sources. The tree is
// built by RUNNING code — newRootCmd() adds groups conditionally
// (enterpriseRootCommands, hideUnavailableAddOns) — and the data a reference
// needs (every flag's name, shorthand, type, default, usage, hidden/required
// status) lives in pflag structs, not in text. No parse of the sources and no
// grep can enumerate that. cmd/olivares/clirefdump_test.go walks the real tree
// and writes it as JSON; this program only ever reads that JSON. The one thing
// it does parse is cmd/olivares/exitcode/exitcode.go, because the exit-code
// contract IS a set of declared constants, and that is an AST question.
//
// THE CROSS-CHECK THAT MAKES THE EXIT-CODE TABLE MORE THAN A COPY. Package
// exitcode's own doc comment says the contract is "documented in the root
// command's help". Two statements of one contract can drift, so this gate
// requires that the set of codes declared in the package and the set listed in
// the root command's help are EQUAL, and names any code that appears in one and
// not the other.
//
// THREE ANSWERS: 0 clean / 1 the page and the binary disagree, every difference
// printed / 2 CANNOT LOOK. Never two: "I could not enumerate" is not "in sync".
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// dumpSchema is the contract with cmd/olivares/clirefdump_test.go. An
	// unrecognised schema is CANNOT LOOK, never "no drift": a field whose meaning
	// changed under a gate that kept reading it is how a gate certifies instead of
	// checking.
	dumpSchema = "olivares.cli-ref/1"

	beginMarker = "<!-- BEGIN GENERATED olivares-cli-reference -->"
	endMarker   = "<!-- END GENERATED olivares-cli-reference -->"

	pageRel     = "docs-site/src/content/docs/reference/cli.md"
	exitCodeRel = "cmd/olivares/exitcode/exitcode.go"

	// populationFloor is the anti-vacuity floor. If the walk breaks and reports a
	// handful of commands, a tiny generated region would match a tiny page and the
	// gate would go green on an empty reference. The tree held 700 nodes on
	// 2026-08-16; 200 is far below any plausible pruning and far above a collapse.
	populationFloor = 200
)

// ── the dump, exactly as cmd/olivares/clirefdump_test.go writes it ──────────────────────

type dumpFlag struct {
	Name       string `json:"name"`
	Shorthand  string `json:"shorthand"`
	Type       string `json:"type"`
	Default    string `json:"default"`
	Usage      string `json:"usage"`
	Persistent bool   `json:"persistent"`
	Hidden     bool   `json:"hidden"`
	Required   bool   `json:"required"`
	Deprecated string `json:"deprecated"`
}

type dumpCommand struct {
	Path           string     `json:"path"`
	Name           string     `json:"name"`
	Parent         string     `json:"parent"`
	Use            string     `json:"use"`
	Short          string     `json:"short"`
	Long           string     `json:"long"`
	Example        string     `json:"example"`
	Aliases        []string   `json:"aliases"`
	GroupID        string     `json:"group_id"`
	Depth          int        `json:"depth"`
	Hidden         bool       `json:"hidden"`
	Deprecated     string     `json:"deprecated"`
	Runnable       bool       `json:"runnable"`
	HasSubcommands bool       `json:"has_subcommands"`
	HasHelpFlag    bool       `json:"has_help_flag"`
	Flags          []dumpFlag `json:"flags"`
}

type dumpGroup struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type cliDump struct {
	Schema   string        `json:"schema"`
	Root     string        `json:"root"`
	Groups   []dumpGroup   `json:"groups"`
	Commands []dumpCommand `json:"commands"`
}

// exitCode is one constant of the CLI's exit-code contract.
type exitCode struct {
	Name    string
	Value   int
	Meaning string
}

// ── failure vocabulary ─────────────────────────────────────────────────────────────────
//
// cannotLook is exit 2 and drift is exit 1, and they are never mixed: the first
// says the gate could not establish an answer, the second says it established
// one and it is wrong.

type cannotLook struct{ msg string }

func (e cannotLook) Error() string { return e.msg }

func cannot(format string, a ...any) error { return cannotLook{fmt.Sprintf(format, a...)} }

func main() {
	root := flag.String("root", ".", "repository root")
	dumpPath := flag.String("dump", "", "path to the JSON command-tree dump written by TestCLIRefDump")
	write := flag.Bool("write", false, "regenerate the page instead of checking it")
	list := flag.Bool("list", false, "print the enumerated command roster and exit")
	selfTest := flag.Bool("self-test", false, "run the red/green fixture battery and exit")
	flag.Parse()

	if *selfTest {
		os.Exit(runSelfTest())
	}

	rc := run(*root, *dumpPath, *write, *list)
	os.Exit(rc)
}

func run(root, dumpPath string, write, list bool) int {
	dump, err := loadDump(dumpPath)
	if err != nil {
		return report(err)
	}
	codes, err := loadExitCodes(filepath.Join(root, exitCodeRel))
	if err != nil {
		return report(err)
	}

	if list {
		for _, c := range dump.Commands {
			fmt.Println(c.Path)
		}
		return 0
	}

	problems, err := inspect(dump, codes)
	if err != nil {
		return report(err)
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "cli-ref-docs: the command tree carries prose the public reference must not publish:")
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "    %s\n", p)
		}
		return 1
	}

	region, err := render(dump, codes)
	if err != nil {
		return report(err)
	}

	pagePath := filepath.Join(root, pageRel)
	page, err := os.ReadFile(pagePath)
	if err != nil {
		return report(cannot("could not read the published page %s: %v", pageRel, err))
	}
	before, after, err := splitRegion(string(page))
	if err != nil {
		return report(err)
	}

	if write {
		out := before + region + after
		if err := os.WriteFile(pagePath, []byte(out), 0o644); err != nil { //nolint:gosec // a published docs page
			return report(cannot("could not write %s: %v", pageRel, err))
		}
		fmt.Printf("cli-ref-docs: wrote %s — %d commands, %d flags\n", pageRel, len(dump.Commands), countFlags(dump))
		return 0
	}

	published := publishedRegion(string(page))
	if published == region {
		fmt.Printf("cli-ref-docs: OK — %s matches the binary (%d commands, %d flags)\n",
			pageRel, len(dump.Commands), countFlags(dump))
		return 0
	}

	fmt.Fprintf(os.Stderr, "cli-ref-docs: %s is out of date with the command tree.\n", pageRel)
	for _, line := range describeDrift(published, region, dump) {
		fmt.Fprintf(os.Stderr, "    %s\n", line)
	}
	fmt.Fprintln(os.Stderr, "  Regenerate with: bash scripts/check-cli-ref-docs.sh --write")
	return 1
}

func report(err error) int {
	if cl, ok := err.(cannotLook); ok {
		fmt.Fprintf(os.Stderr, "cli-ref-docs: CANNOT LOOK — %s\n", cl.msg)
		fmt.Fprintln(os.Stderr, "  A gate that could not enumerate the CLI is not a gate that passed.")
		return 2
	}
	fmt.Fprintf(os.Stderr, "cli-ref-docs: %v\n", err)
	return 1
}

func countFlags(d cliDump) int {
	n := 0
	for _, c := range d.Commands {
		for _, f := range c.Flags {
			if f.Name != "help" {
				n++
			}
		}
	}
	return n
}
