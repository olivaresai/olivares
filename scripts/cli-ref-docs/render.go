// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── loading ────────────────────────────────────────────────────────────────────────────

// loadDump reads the JSON the in-package walk wrote. Every failure here is
// CANNOT LOOK, including "the file is not there": the dump is produced by a
// `go test` run that the wrapper invokes immediately before this program, so its
// absence means that run did not happen or did not finish — which is precisely
// the state that must never read as "the docs are in sync".
func loadDump(path string) (cliDump, error) {
	var d cliDump
	if strings.TrimSpace(path) == "" {
		return d, cannot("no -dump path was given, so the command tree was never enumerated")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return d, cannot("the command-tree dump was not written to %s (%v); the walk in "+
			"cmd/olivares/clirefdump_test.go did not run or did not finish", path, err)
	}
	if len(raw) == 0 {
		return d, cannot("the command-tree dump at %s is empty", path)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, cannot("the command-tree dump at %s is not readable JSON: %v", path, err)
	}
	if d.Schema != dumpSchema {
		return d, cannot("the dump declares schema %q but this gate speaks %q; a field whose "+
			"meaning changed must not be read by a gate that still assumes the old one", d.Schema, dumpSchema)
	}
	if len(d.Commands) < populationFloor {
		return d, cannot("the dump lists %d commands, below the floor of %d; the tree cannot have "+
			"shrunk that far, so the walk is broken and the page must not be regenerated from it",
			len(d.Commands), populationFloor)
	}
	if d.Root == "" {
		return d, cannot("the dump names no root command")
	}
	// The walk sorts by path; depending on that ordering without checking it is how
	// a page starts churning on a reordering nobody sees.
	for i := 1; i < len(d.Commands); i++ {
		if d.Commands[i-1].Path >= d.Commands[i].Path {
			return d, cannot("the dump is not sorted by command path (%q then %q), so the rendered "+
				"page would depend on walk order", d.Commands[i-1].Path, d.Commands[i].Path)
		}
	}
	return d, nil
}

// exitCodeDocRe strips the "Name — " lead-in the package's doc comments use, so
// the published cell is the meaning rather than a repetition of the name.
var exitCodeDocRe = regexp.MustCompile(`^\s*\w+\s*(—|--|-)\s*`)

// loadExitCodes parses cmd/olivares/exitcode/exitcode.go and returns the declared
// contract. This is the one input taken from the sources rather than from the
// running binary, because the contract IS a set of declared constants: an AST
// tells a declaration from prose about one, and a grep cannot.
func loadExitCodes(path string) ([]exitCode, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, cannot("could not parse the exit-code contract at %s: %v", path, err)
	}
	var out []exitCode
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				continue
			}
			value, err := strconv.Atoi(lit.Value)
			if err != nil {
				continue
			}
			meaning := ""
			if vs.Doc != nil {
				meaning = collapse(vs.Doc.Text())
				meaning = exitCodeDocRe.ReplaceAllString(meaning, "")
			}
			if meaning == "" {
				return nil, cannot("exit code %s (%d) in %s has no doc comment, so the reference "+
					"has nothing truthful to publish for it", vs.Names[0].Name, value, exitCodeRel)
			}
			out = append(out, exitCode{Name: vs.Names[0].Name, Value: value, Meaning: meaning})
		}
	}
	if len(out) == 0 {
		return nil, cannot("found no exit-code constants in %s; the contract cannot be empty, so "+
			"this is a parse that failed rather than a contract that is missing", exitCodeRel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	for i := 1; i < len(out); i++ {
		if out[i].Value == out[i-1].Value {
			return nil, cannot("exit codes %s and %s in %s share the value %d",
				out[i-1].Name, out[i].Name, exitCodeRel, out[i].Value)
		}
	}
	return out, nil
}

// ── the checks that run before anything is rendered ─────────────────────────────────────

// bannedProse is the vocabulary the canon forbids on a public surface. The
// reference publishes prose that was written for `--help`, where nobody was
// thinking about the public-claims rules, so it is checked HERE — at the command
// and flag that carries it — instead of being discovered as an unattributed hit
// in a 300 KB generated page by a whole-tree lint.
var bannedProse = []struct {
	pattern *regexp.Regexp
	why     string
}{
	{regexp.MustCompile(`(?i)\b(impossible|infallible|unhackable|foolproof)\b`),
		"an absolute claim the canon forbids on a public surface"},
	{regexp.MustCompile(`(?i)\b(tamper|bullet)[- ]?proof\b`),
		"an absolute claim the canon forbids on a public surface"},
	{regexp.MustCompile(`(?i)\b100\s*%\s*(secure|safe|reliable|accurate)\b`),
		"an absolute claim the canon forbids on a public surface"},
	{regexp.MustCompile(`(?i)SLSA[- ]?(L(evel)?[- ]?3|Build[- ]?Level[- ]?3)`),
		"an unnormalized supply-chain level claim (scripts/check-docs-honesty.sh)"},
	{regexp.MustCompile(`(?i)FIPS[- ]?(140-3[- ]?)?(validated|certified)`),
		"an unnormalized cryptographic-validation claim (scripts/check-docs-honesty.sh)"},
}

// internalRefs is the second half of the same job, and it exists because this
// generator was measured LAUNDERING one.
//
// THE DEFECT, measured 2026-08-17. The export gate scrubs internal-reference
// provenance out of COMMENTS in the export copy, and reports "comments: 0" after
// rewriting thousands of tokens. But loadExitCodes below reads vs.Doc.Text() — a
// Go doc comment — and this generator writes it into the published page, which is
// COMMITTED. By the time the scrubber runs it is no longer a comment: it is
// shipped prose, which the scrubber does not touch and the raw leak check
// correctly refuses. One session id in one doc comment therefore turned into an
// export leak, and `lint:export` exits 1 on the whole branch, so nothing can be
// pushed at all until somebody finds it. Generating documentation from doc
// comments defeats comment sanitisation BY CONSTRUCTION, and the only place that
// can see it coming is here, before the text is written.
//
// WHY THESE ARE SHAPES AND NOT THE CURATION LIST. The authoritative list of what
// does not ship lives in the export curation script, in bash arrays, with KEEP
// subtracted. Restating it here would be exactly the "two statements of one
// contract drift" defect that the exit-code cross-check below exists to prevent,
// and this module builds with GOWORK=off precisely so it depends on nothing. So
// this is a deliberate SUBSET that fires early and names the offending flag;
// `lint:export` stays authoritative for the whole tree.
//
// AND WHY THERE IS NO BARE `sessions/` RULE, which is the rule anyone writing
// this would reach for first: `superadmin disable` says "revokes its
// an internal design note (not shipped)", meaning sessions and tokens. That is product vocabulary, not
// a path, and the export gate's own curation KEEPs it for the same reason. A
// pattern that matched it would redden a correct page, so every rule here is
// path-shaped or a token that cannot be ordinary prose.
var internalRefs = []struct {
	pattern *regexp.Regexp
	why     string
}{
	{regexp.MustCompile(`\bdesign/[A-Za-z0-9._-]+`),
		"a citation of the internal design tree, which does not ship, so a reader cannot follow it"},
	{regexp.MustCompile(`\bdocs/[0-9]{2}\b`),
		"a citation of the numbered internal docs series, which the export curation blocks"},
	{regexp.MustCompile(`\b(ESTADO-PROYECTO|PLAN-DESARROLLO|CLAUDE\.md)\b`),
		"a citation of an internal coordination file, which does not ship"},
	{regexp.MustCompile(`\bS[0-9]{3,4}\b`),
		"a session id, which is internal provenance and means nothing to a reader"},
}

// pathCharset is what a command path may contain if the anchor derived from it is
// to be predictable. Every path in the tree is lowercase ASCII today; a path that
// left this set would produce a heading slug this generator cannot compute, and
// the index links would land nowhere.
var pathCharset = regexp.MustCompile(`^[a-z0-9 _-]+$`)

// clockDerivedDefault reports the date string a flag's default embeds, if it
// embeds today's.
//
// THE DEFECT IT CATCHES, measured 2026-08-16. `support bundle --out` defaulted to
// fmt.Sprintf("olivares-support-%s.tar.gz", time.Now()…) evaluated when the
// command was CONSTRUCTED. Its advertised default therefore changed every second:
// `--help` printed a different value on every run, and this page could never be
// byte-stable, so the gate would have flapped for everyone forever — one flag out
// of 2209 able to keep the whole reference permanently red.
//
// The in-process double walk in TestCLIRefDump cannot see this: both walks happen
// in the same second, so both produce the same string. A default that embeds
// today's date, however, is clock-derived on any day it is checked.
//
// Known limit, stated rather than papered over: a default carrying a time with no
// date (a bare "150405") would pass both checks. Nothing in the tree does that
// today, and the fix for one is the fix for the other — compute the value when
// the command RUNS and describe it as prose in the usage string.
func clockDerivedDefault(def string, now time.Time) string {
	if def == "" {
		return ""
	}
	for _, layout := range []string{"20060102", "2006-01-02", "2006/01/02"} {
		if s := now.UTC().Format(layout); strings.Contains(def, s) {
			return s
		}
	}
	return ""
}

// inspect returns everything wrong with the tree's own prose, all of it, so one
// run names every offender instead of one per push.
func inspect(d cliDump, codes []exitCode) ([]string, error) {
	var problems []string
	now := time.Now()
	for _, c := range d.Commands {
		if !pathCharset.MatchString(c.Path) {
			problems = append(problems, fmt.Sprintf("%s: the command path contains characters outside "+
				"[a-z0-9 _-], so its heading anchor cannot be computed", c.Path))
		}
		if strings.TrimSpace(c.Short) == "" {
			problems = append(problems, fmt.Sprintf("%s: has no Short description, so the reference "+
				"would publish a blank summary (cmd_help_completeness_test.go guards this too)", c.Path))
		}
		problems = append(problems, checkProse(c.Path, "summary", c.Short)...)
		if strings.Count(c.Short, "`")%2 != 0 {
			problems = append(problems, fmt.Sprintf("%s: its summary has an odd number of backticks, "+
				"which would open a code span that never closes", c.Path))
		}
		for _, f := range c.Flags {
			if f.Name == "help" {
				continue
			}
			problems = append(problems, checkProse(c.Path, "--"+f.Name, f.Usage)...)
			if stamp := clockDerivedDefault(f.Default, now); stamp != "" {
				problems = append(problems, fmt.Sprintf("%s --%s: its default %q embeds today's date "+
					"(%s), so it is computed from the clock when the command is built and this page "+
					"could never be byte-stable. Default the flag to empty, compute the value when the "+
					"command runs, and describe it as prose in the usage string",
					c.Path, f.Name, f.Default, stamp))
			}
			if strings.Count(f.Usage, "`")%2 != 0 {
				problems = append(problems, fmt.Sprintf("%s --%s: its usage string has an odd number of "+
					"backticks, which would open a code span that never closes", c.Path, f.Name))
			}
		}
	}

	// The cross-check package exitcode's own doc comment asks for: the contract is
	// declared in one place and RESTATED in the root command's help, and two
	// statements of one contract drift.
	declared := map[int]string{}
	for _, c := range codes {
		declared[c.Value] = c.Name
		// The exit-code cell is the ONE published string that comes from a Go doc
		// comment rather than from a `--help` usage string, and until 2026-08-17 it
		// was the one published string nothing prose-checked. That is not a detail:
		// a comment is written for the next maintainer, so it is the likeliest place
		// in the whole tree for internal provenance to sit, and this is the only
		// route by which a comment reaches a public page. It gets the same check as
		// every other cell.
		problems = append(problems, checkProse(exitCodeRel, fmt.Sprintf("exit code %d (%s)", c.Value, c.Name), c.Meaning)...)
	}
	var rootLong string
	for _, c := range d.Commands {
		if c.Path == d.Root {
			rootLong = c.Long
		}
	}
	if strings.TrimSpace(rootLong) == "" {
		return problems, cannot("the root command has no Long help, so the exit-code contract it is " +
			"supposed to restate could not be read")
	}
	listed, err := exitCodesInHelp(rootLong)
	if err != nil {
		return problems, err
	}
	for value, name := range declared {
		if !listed[value] {
			problems = append(problems, fmt.Sprintf("exit code %d (%s) is declared in %s but the root "+
				"command's help does not list it; the package doc says the contract is documented there",
				value, name, exitCodeRel))
		}
	}
	for value := range listed {
		if _, ok := declared[value]; !ok {
			problems = append(problems, fmt.Sprintf("the root command's help lists exit code %d, which "+
				"no constant in %s declares", value, exitCodeRel))
		}
	}
	sort.Strings(problems)
	return problems, nil
}

func checkProse(path, where, text string) []string {
	var out []string
	for _, b := range bannedProse {
		if m := b.pattern.FindString(text); m != "" {
			out = append(out, fmt.Sprintf("%s %s: %q is %s", path, where, m, b.why))
		}
	}
	for _, b := range internalRefs {
		if m := b.pattern.FindString(text); m != "" {
			out = append(out, fmt.Sprintf("%s %s: %q is %s", path, where, m, b.why))
		}
	}
	return out
}

var helpCodeRe = regexp.MustCompile(`^\s+(\d+)\s`)

// exitCodesInHelp reads the "Exit codes:" block out of the root command's help.
// Continuation lines of a wrapped entry start with spaces but not with a digit,
// so they are skipped rather than mistaken for codes.
func exitCodesInHelp(long string) (map[int]bool, error) {
	lines := strings.Split(long, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "Exit codes:") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, cannot("the root command's help has no \"Exit codes:\" block, so the contract " +
			"could not be cross-checked against the exitcode package")
	}
	out := map[int]bool{}
	for _, l := range lines[start+1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		m := helpCodeRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out[n] = true
	}
	if len(out) == 0 {
		return nil, cannot("the root command's \"Exit codes:\" block listed no codes this gate could " +
			"read, which is a parse failure rather than an empty contract")
	}
	return out, nil
}

// ── markdown helpers ───────────────────────────────────────────────────────────────────

var spaceRun = regexp.MustCompile(`[ \t\r\n]+`)

func collapse(s string) string { return strings.TrimSpace(spaceRun.ReplaceAllString(s, " ")) }

// cell makes a string safe inside a markdown table cell. Three real hazards,
// all measured in the tree on 2026-08-16: 19 usage strings contain a pipe (an
// alternation like `low|medium|high`), which would silently start a new column;
// 28 contain angle brackets (`"<alg>:<base64>"`), which markdown hands to the
// HTML parser and which then vanish from the rendered page; and any newline
// would end the row. Backticks are deliberately NOT escaped — usage strings use
// them to mark code and they are balanced (inspect fails the run if one is not).
func cell(s string) string {
	s = collapse(s)
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

// slug reproduces the heading id github-slugger derives, for the restricted
// alphabet inspect() guarantees: lowercase, drop the colon, spaces to hyphens.
// The index links are checked against the headings by scripts/check-docs-anchors.mjs,
// which is the independent oracle for this function.
func slug(path string) string {
	return "command-" + strings.ReplaceAll(strings.ToLower(path), " ", "-")
}

// linkable reports whether a command's heading anchor can be predicted with
// confidence.
//
// An underscore in a command name is ambiguous in a markdown heading, and the two
// authorities disagree about it. `olivares __extract` is the one such command in
// the tree. Read as text, `__extract` is an unclosed strong-emphasis marker that
// remark leaves literal, so github-slugger keeps the underscores and the id is
// `command-olivares-__extract`; scripts/check-docs-anchors.mjs, which is the gate
// that will actually run, strips `__` as an emphasis marker first and computes
// `command-olivares-extract`. Escaping the underscores does not reconcile them —
// it changes the input to both and they still disagree.
//
// So this generator does not bet on either. A command whose name contains an
// emphasis character keeps its section and its full entry in the index; only the
// LINK is withheld, because a link is the one part that can be silently wrong.
// Nothing is excluded and no gate is relaxed: the reference still covers every
// command, and the anchor lint has nothing to dangle.
func linkable(path string) bool { return !strings.ContainsAny(path, "_*") }

// indexEntry renders a command in the index: linked when its anchor is
// predictable, plain when it is not.
func indexEntry(path string) string {
	if linkable(path) {
		return fmt.Sprintf("[`%s`](#%s)", path, slug(path))
	}
	return fmt.Sprintf("`%s`", path)
}

func heading(path string) string { return "#### Command: " + path }

// ── rendering ──────────────────────────────────────────────────────────────────────────

func render(d cliDump, codes []exitCode) (string, error) {
	var b strings.Builder
	groups, hidden := 0, 0
	for _, c := range d.Commands {
		if c.HasSubcommands {
			groups++
		}
		if c.Hidden {
			hidden++
		}
	}

	b.WriteString(beginMarker)
	b.WriteString("\n")
	b.WriteString("<!-- Generated from the olivares command tree by `bash scripts/check-cli-ref-docs.sh --write`.\n")
	b.WriteString("     Do not edit inside this region: the push gate compares it against the binary. -->\n\n")

	b.WriteString("## Complete command reference\n\n")
	b.WriteString(fmt.Sprintf(
		"This section is generated from the command tree of the community (AGPL) build of the `%s` "+
			"binary at this commit. It covers %d command nodes — the root command and %d subcommands, "+
			"of which %d are groups that carry subcommands and %d are hidden diagnostics — together with "+
			"the %d flags they declare. It is regenerated from the binary rather than kept by hand, so a "+
			"command or flag added without a documentation change fails the push gate.\n\n",
		d.Root, len(d.Commands), len(d.Commands)-1, groups, hidden, countFlags(d)))
	b.WriteString("Nothing here is a stability promise: see [Stability](#stability) below for what may " +
		"still change.\n\n")

	// Exit codes.
	b.WriteString("### Exit codes\n\n")
	b.WriteString("Every command in the tree exits with one of these codes. Scripts and CI pipelines " +
		"branch on them, so an existing code is never renumbered — only appended to.\n\n")
	b.WriteString("| Code | Name | Meaning |\n|---|---|---|\n")
	for _, c := range codes {
		b.WriteString(fmt.Sprintf("| `%d` | `%s` | %s |\n", c.Value, c.Name, cell(c.Meaning)))
	}
	b.WriteString("\n")

	// Flags shared by the whole tree.
	var rootCmd dumpCommand
	for _, c := range d.Commands {
		if c.Path == d.Root {
			rootCmd = c
		}
	}
	b.WriteString("### Flags every command accepts\n\n")
	b.WriteString("`-h`, `--help` prints the command's own help and exits `0`. The flags below are " +
		"declared on the root command and inherited by every command in the tree.\n\n")
	b.WriteString(flagTable(rootCmd.Flags))
	b.WriteString("\n")
	b.WriteString("Command groups declare further flags that their own subcommands inherit. A flag " +
		"marked **inherited** in any table below is declared there and taken by everything under it, " +
		"so it is listed once, at the command that declares it, rather than repeated on each of its " +
		"subcommands.\n\n")

	// Index.
	b.WriteString("### Command index\n\n")
	b.WriteString(fmt.Sprintf("All %d commands, in alphabetical order.\n\n", len(d.Commands)))
	b.WriteString("| Command | Summary |\n|---|---|\n")
	for _, c := range d.Commands {
		note := ""
		if c.Hidden {
			note = " _(hidden)_"
		}
		if c.Deprecated != "" {
			note += " _(deprecated)_"
		}
		b.WriteString(fmt.Sprintf("| %s | %s%s |\n", indexEntry(c.Path), cell(c.Short), note))
	}
	b.WriteString("\n")

	// Detail.
	b.WriteString("### Command detail\n\n")
	for _, c := range d.Commands {
		b.WriteString(heading(c.Path))
		b.WriteString("\n\n")
		if c.Hidden {
			b.WriteString("Hidden diagnostic: it does not appear in `--help` output and is not part of " +
				"the supported surface.\n\n")
		}
		if c.Deprecated != "" {
			b.WriteString(fmt.Sprintf("**Deprecated.** %s\n\n", cell(c.Deprecated)))
		}
		b.WriteString(cell(c.Short))
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf("```\n%s\n```\n\n", usageLine(c)))
		if len(c.Aliases) > 0 {
			quoted := make([]string, 0, len(c.Aliases))
			for _, a := range c.Aliases {
				quoted = append(quoted, "`"+a+"`")
			}
			b.WriteString("Aliases: " + strings.Join(quoted, ", ") + "\n\n")
		}
		own := ownFlags(c)
		if len(own) == 0 {
			if c.Parent != "" {
				b.WriteString(fmt.Sprintf("Declares no flags of its own; it takes those of "+
					"%s and the root command.\n\n", indexEntry(c.Parent)))
			} else {
				b.WriteString("Declares no flags of its own.\n\n")
			}
			continue
		}
		b.WriteString(flagTable(own))
		b.WriteString("\n")
	}

	b.WriteString(endMarker)
	b.WriteString("\n")
	return b.String(), nil
}

// usageLine renders the invocation. cobra's Use field already carries the
// positional arguments for the commands that take them (`get <id>`), and it is
// the command's own statement of its shape, so it is used verbatim rather than
// reconstructed.
func usageLine(c dumpCommand) string {
	use := strings.TrimSpace(c.Use)
	if c.Parent == "" {
		return use
	}
	// Use begins with the command's own name; the path already ends with it.
	rest := strings.TrimSpace(strings.TrimPrefix(use, c.Name))
	if rest == "" {
		return c.Path
	}
	return c.Path + " " + rest
}

func ownFlags(c dumpCommand) []dumpFlag {
	var out []dumpFlag
	for _, f := range c.Flags {
		if f.Name == "help" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func flagTable(flags []dumpFlag) string {
	own := make([]dumpFlag, 0, len(flags))
	for _, f := range flags {
		if f.Name != "help" {
			own = append(own, f)
		}
	}
	if len(own) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| Flag | Type | Default | Description |\n|---|---|---|---|\n")
	for _, f := range own {
		name := "`--" + f.Name + "`"
		if f.Shorthand != "" {
			name = "`-" + f.Shorthand + "`, " + name
		}
		def := "—"
		if f.Default != "" {
			def = "`" + cell(f.Default) + "`"
		}
		desc := cell(f.Usage)
		var notes []string
		if f.Required {
			notes = append(notes, "**required**")
		}
		if f.Persistent {
			notes = append(notes, "**inherited**")
		}
		if f.Hidden {
			notes = append(notes, "_hidden_")
		}
		if f.Deprecated != "" {
			notes = append(notes, "_deprecated: "+cell(f.Deprecated)+"_")
		}
		if len(notes) > 0 {
			desc = strings.Join(notes, ", ") + ". " + desc
		}
		b.WriteString(fmt.Sprintf("| %s | `%s` | %s | %s |\n", name, f.Type, def, desc))
	}
	return b.String()
}

// ── the published region ───────────────────────────────────────────────────────────────

func splitRegion(page string) (before, after string, err error) {
	i := strings.Index(page, beginMarker)
	j := strings.Index(page, endMarker)
	if i < 0 || j < 0 || j < i {
		return "", "", cannot("the published page has lost its %s / %s markers, so the generated "+
			"region could not be located; a page without them is not a page without drift",
			beginMarker, endMarker)
	}
	return page[:i], page[j+len(endMarker)+1:], nil
}

func publishedRegion(page string) string {
	i := strings.Index(page, beginMarker)
	j := strings.Index(page, endMarker)
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return page[i : j+len(endMarker)+1]
}

// describeDrift turns "these two strings differ" into the names of what moved.
// A diff of a 300 KB region is unreadable; the commands and flags that appeared
// or vanished are the answer, so they are what gets printed.
func describeDrift(published, want string, d cliDump) []string {
	var out []string

	pubHeads := headingSet(published)
	for _, c := range d.Commands {
		if !pubHeads[heading(c.Path)] {
			out = append(out, fmt.Sprintf("MISSING from the page: %s (%s)", c.Path, collapse(c.Short)))
		}
	}
	wantHeads := headingSet(want)
	for h := range pubHeads {
		if !wantHeads[h] {
			out = append(out, fmt.Sprintf("PUBLISHED but no longer in the binary: %s",
				strings.TrimPrefix(h, "#### Command: ")))
		}
	}
	sort.Strings(out)
	if len(out) > 40 {
		extra := len(out) - 40
		out = out[:40]
		out = append(out, fmt.Sprintf("… and %d more", extra))
	}
	if len(out) == 0 {
		// Same command set: the difference is inside the rows. Name the first lines
		// that differ so the author sees which flag or summary moved.
		out = append(out, "the command set matches, so the difference is in the detail (a flag, a "+
			"default, a summary or an exit code changed):")
		out = append(out, firstDifference(published, want)...)
	}
	return out
}

func headingSet(region string) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(region, "\n") {
		if strings.HasPrefix(l, "#### Command: ") {
			out[l] = true
		}
	}
	return out
}

func firstDifference(published, want string) []string {
	p := strings.Split(published, "\n")
	w := strings.Split(want, "\n")
	for i := 0; i < len(p) && i < len(w); i++ {
		if p[i] != w[i] {
			return []string{
				fmt.Sprintf("  page  (line %d of the region): %s", i+1, truncate(p[i])),
				fmt.Sprintf("  binary(line %d of the region): %s", i+1, truncate(w[i])),
			}
		}
	}
	return []string{fmt.Sprintf("  the region is %d lines on the page and %d from the binary", len(p), len(w))}
}

func truncate(s string) string {
	if len(s) <= 160 {
		return s
	}
	return s[:157] + "..."
}
