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
// THE PUBLISHED PAGES. English plus every locale in docs-site/src/site-locales.mjs
// (LOCALES minus root) carry the same generated region (the binary's original
// English help). The wrapper import()s that module and hands this program a JSON
// roster; this program does not parse the .mjs and does not walk the docs tree
// (walking would pick up archived VERSIONS snapshots). A missing page or
// unreadable declaration is CANNOT LOOK, never "the copies I could read were
// in sync". A newly declared published locale is checked and written
// automatically.
//
// THREE ANSWERS: 0 clean / 1 the page and the binary disagree, every difference
// printed / 2 CANNOT LOOK. Never two: "I could not enumerate" is not "in sync".
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

	// localeRosterSchema is the contract with scripts/cli-ref-docs/published-locales.mjs,
	// which import()s docs-site/src/site-locales.mjs. An unrecognised schema is
	// CANNOT LOOK: a field whose meaning changed must not be read as the old one.
	localeRosterSchema = "olivares.cli-ref-locales/1"

	// writeStagingSuffix is the sibling temp file --write uses. The self-test
	// plants a directory at that path to fail the staging write without relying
	// on mode bits; the suffix is named here so the plant and the writer cannot
	// drift apart.
	writeStagingSuffix = ".cli-ref-docs.tmp"
)

func localeCLIPageRel(lang string) string {
	return "docs-site/src/content/docs/" + lang + "/reference/cli.md"
}

func allCLIPageRels(published []string) []string {
	rels := make([]string, 0, 1+len(published))
	rels = append(rels, pageRel)
	for _, lang := range published {
		rels = append(rels, localeCLIPageRel(lang))
	}
	return rels
}

// localeRoster is the JSON published-locales.mjs writes after importing
// site-locales.mjs. The generator never parses the .mjs and never walks the
// docs tree for locale directories: walking would pick up archived VERSIONS
// snapshots (docs-site/src/content/docs/2026-06/) and regenerate a freeze.
type localeRoster struct {
	Schema    string   `json:"schema"`
	Published []string `json:"published"`
	Archived  []string `json:"archived"`
}

var localeSegmentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func loadLocaleRoster(path string) (localeRoster, error) {
	var r localeRoster
	if strings.TrimSpace(path) == "" {
		return r, cannot("no -locales-file was given, so the published locale roster was never read")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, cannot("could not read the locale roster %s: %v", path, err)
	}
	if len(raw) == 0 {
		return r, cannot("the locale roster at %s is empty", path)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, cannot("the locale roster at %s is not readable JSON: %v", path, err)
	}
	if r.Schema != localeRosterSchema {
		return r, cannot("the locale roster declares schema %q but this gate speaks %q", r.Schema, localeRosterSchema)
	}
	if len(r.Published) == 0 {
		return r, cannot("the locale roster at %s declares ZERO published locales", path)
	}
	archived := make(map[string]bool, len(r.Archived))
	for _, a := range r.Archived {
		if !validLocaleSegment(a) {
			return r, cannot("archived slug %q is not a safe directory name", a)
		}
		if archived[a] {
			return r, cannot("archived slug %q is declared twice", a)
		}
		archived[a] = true
	}
	seen := make(map[string]bool, len(r.Published))
	for _, p := range r.Published {
		if !validLocaleSegment(p) {
			return r, cannot("published locale %q is not a safe directory name", p)
		}
		if p == "root" || p == "en" {
			return r, cannot("published locale %q is the English root, which is not a locale directory", p)
		}
		if archived[p] {
			return r, cannot("published locale %q is an archived VERSIONS slug; a frozen snapshot must not be regenerated", p)
		}
		if seen[p] {
			return r, cannot("published locale %q is declared twice", p)
		}
		seen[p] = true
	}
	return r, nil
}

func validLocaleSegment(s string) bool {
	return localeSegmentRe.MatchString(s) && s != "." && s != ".."
}

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
	localesFile := flag.String("locales-file", "", "JSON roster written by published-locales.mjs after importing site-locales.mjs")
	write := flag.Bool("write", false, "regenerate the page instead of checking it")
	list := flag.Bool("list", false, "print the enumerated command roster and exit")
	selfTest := flag.Bool("self-test", false, "run the red/green fixture battery and exit")
	flag.Parse()

	if *selfTest {
		os.Exit(runSelfTest())
	}

	rc := run(*root, *dumpPath, *localesFile, *write, *list)
	os.Exit(rc)
}

func run(root, dumpPath, localesFile string, write, list bool) int {
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

	roster, err := loadLocaleRoster(localesFile)
	if err != nil {
		return report(err)
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

	pages, lookErrs := loadCLIPages(root, roster.Published)
	if len(lookErrs) > 0 {
		return reportLook(lookErrs)
	}

	if write {
		if err := writePreparedPages(root, pages, region); err != nil {
			return report(err)
		}
		fmt.Printf("cli-ref-docs: wrote %d pages — %d commands, %d flags\n",
			len(pages), len(dump.Commands), countFlags(dump))
		for _, p := range pages {
			fmt.Printf("    %s\n", p.rel)
		}
		return 0
	}

	var drifted []preparedPage
	for _, p := range pages {
		if p.published != region {
			drifted = append(drifted, p)
		}
	}
	if len(drifted) == 0 {
		fmt.Printf("cli-ref-docs: OK — generated CLI reference matches the binary (%d pages, %d commands, %d flags)\n",
			len(pages), len(dump.Commands), countFlags(dump))
		return 0
	}

	rels := make([]string, 0, len(drifted))
	for _, p := range drifted {
		rels = append(rels, p.rel)
	}
	fmt.Fprintf(os.Stderr, "cli-ref-docs: %s %s out of date with the command tree.\n",
		strings.Join(rels, ", "), driftVerb(len(rels)))
	// When every drifted page still carries the same stale region (the usual
	// case: the dump moved and nobody regenerated), describe it once.
	same := true
	for i := 1; i < len(drifted); i++ {
		if drifted[i].published != drifted[0].published {
			same = false
			break
		}
	}
	if same {
		for _, line := range describeDrift(drifted[0].published, region, dump) {
			fmt.Fprintf(os.Stderr, "    %s\n", line)
		}
	} else {
		for _, p := range drifted {
			fmt.Fprintf(os.Stderr, "    %s:\n", p.rel)
			for _, line := range describeDrift(p.published, region, dump) {
				fmt.Fprintf(os.Stderr, "      %s\n", line)
			}
		}
	}
	fmt.Fprintln(os.Stderr, "  Regenerate with: bash scripts/check-cli-ref-docs.sh --write")
	return 1
}

func driftVerb(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

type preparedPage struct {
	rel       string
	before    string
	after     string
	published string
}

func loadCLIPages(root string, published []string) ([]preparedPage, []error) {
	rels := allCLIPageRels(published)
	out := make([]preparedPage, 0, len(rels))
	var errs []error
	for _, rel := range rels {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			errs = append(errs, cannot("could not read the published page %s: %v", rel, err))
			continue
		}
		before, region, after, err := splitRegion(string(raw))
		if err != nil {
			if cl, ok := err.(cannotLook); ok {
				errs = append(errs, cannot("%s: %s", rel, cl.msg))
			} else {
				errs = append(errs, cannot("%s: %v", rel, err))
			}
			continue
		}
		out = append(out, preparedPage{rel: rel, before: before, after: after, published: region})
	}
	return out, errs
}

// writePreparedPages writes every page's new bytes to a sibling temp file first,
// then renames. A late locale that cannot be loaded never reaches this function
// (loadCLIPages collected it as CANNOT LOOK); a late write error still removes
// temps that did not rename, so a half-finished --write does not look like a
// successful regeneration of the pages that happened to come first.
func writePreparedPages(root string, pages []preparedPage, region string) error {
	type pending struct {
		rel, path, tmp string
	}
	items := make([]pending, 0, len(pages))
	defer func() {
		for _, it := range items {
			if it.tmp != "" {
				_ = os.Remove(it.tmp)
			}
		}
	}()
	for _, p := range pages {
		path := filepath.Join(root, p.rel)
		tmp := path + writeStagingSuffix
		out := p.before + region + p.after
		if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil { //nolint:gosec // a published docs page
			return cannot("could not write %s: %v", p.rel, err)
		}
		items = append(items, pending{rel: p.rel, path: path, tmp: tmp})
	}
	for i := range items {
		if err := os.Rename(items[i].tmp, items[i].path); err != nil {
			return cannot("could not replace %s: %v", items[i].rel, err)
		}
		items[i].tmp = ""
	}
	return nil
}

func reportLook(errs []error) int {
	for _, err := range errs {
		if cl, ok := err.(cannotLook); ok {
			fmt.Fprintf(os.Stderr, "cli-ref-docs: CANNOT LOOK — %s\n", cl.msg)
		} else {
			fmt.Fprintf(os.Stderr, "cli-ref-docs: %v\n", err)
		}
	}
	fmt.Fprintln(os.Stderr, "  A gate that could not enumerate the CLI is not a gate that passed.")
	return 2
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
