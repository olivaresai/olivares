// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// The battery that proves this gate can fail.
//
// Every trap the generator exists to catch is planted here on a throwaway tree
// and must produce the RIGHT answer — not merely a non-zero one. The three
// verdicts are distinct on purpose (0 in sync / 1 drift / 2 CANNOT LOOK), so a
// case asserts the exact code AND, for the red cases, that the named offender
// appears in the output. "It went red" is not evidence: a gate that fails for
// the wrong reason passes the whole red column while checking nothing.
//
// The GREEN cases are not filler. Without them a gate that failed unconditionally
// would satisfy every red case above and be indistinguishable from a working one.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fixture is one throwaway repository: a dump, an exitcode.go and a page.
type fixture struct {
	dir      string
	dumpPath string
}

const fixtureExitCodes = `package exitcode

// The exit-code contract (documented in the root command's help).
const (
	// OK — the command succeeded.
	OK = 0
	// Err — generic failure with no more specific classification.
	Err = 1
	// Usage — the invocation itself is wrong.
	Usage = 2
)
`

const fixtureRootLong = `olivares is the test root.

Exit codes:
  0  success
  1  generic error
  2  usage error (unknown flag or bad arguments)
`

// newFixtureDump builds a synthetic tree above the population floor. The names
// are generated so a case can add, remove or mutate one command without hand
// maintaining 200 of them.
func newFixtureDump() cliDump {
	d := cliDump{Schema: dumpSchema, Root: "olivares"}
	d.Commands = append(d.Commands, dumpCommand{
		Path: "olivares", Name: "olivares", Use: "olivares [command]",
		Short: "the test root", Long: fixtureRootLong, Depth: 0,
		Runnable: true, HasSubcommands: true, HasHelpFlag: true,
		Flags: []dumpFlag{
			{Name: "help", Type: "bool", Default: "false", Usage: "help for olivares"},
			{Name: "output", Shorthand: "o", Type: "string", Default: "text",
				Usage: "global output format: text or json", Persistent: true},
		},
	})
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("cmd%03d", i)
		c := dumpCommand{
			Path: "olivares " + name, Name: name, Parent: "olivares",
			Use:   name + " [flags]",
			Short: fmt.Sprintf("summary of %s", name), Depth: 1,
			Runnable: true, HasHelpFlag: true,
			Flags: []dumpFlag{{Name: "help", Type: "bool", Default: "false", Usage: "help for " + name}},
		}
		// A third of them declare a flag, one has an alias, one is hidden: the
		// shapes the renderer takes different branches for must all be exercised
		// by the GREEN case, not only by the red ones.
		if i%3 == 0 {
			c.Flags = append(c.Flags, dumpFlag{
				Name: "tenant", Type: "string", Default: "", Usage: "tenant id to address",
				Required: i%9 == 0,
			})
		}
		if i == 7 {
			c.Aliases = []string{"seven"}
		}
		if i == 11 {
			c.Hidden = true
		}
		d.Commands = append(d.Commands, c)
	}
	sortDumpByPath(&d)
	return d
}

func sortDumpByPath(d *cliDump) {
	// insertion sort: the fixtures are small and this keeps the battery free of
	// the sort package's stability questions.
	for i := 1; i < len(d.Commands); i++ {
		for j := i; j > 0 && d.Commands[j-1].Path > d.Commands[j].Path; j-- {
			d.Commands[j-1], d.Commands[j] = d.Commands[j], d.Commands[j-1]
		}
	}
}

// writeFixture lays a fixture down on disk with the page already in sync, unless
// the caller mutates the dump afterwards.
func writeFixture(base string, d cliDump) (fixture, error) {
	f := fixture{dir: base}
	if err := os.MkdirAll(filepath.Join(base, "cmd", "olivares", "exitcode"), 0o755); err != nil {
		return f, err
	}
	if err := os.MkdirAll(filepath.Join(base, filepath.Dir(pageRel)), 0o755); err != nil {
		return f, err
	}
	if err := os.WriteFile(filepath.Join(base, exitCodeRel), []byte(fixtureExitCodes), 0o600); err != nil {
		return f, err
	}
	f.dumpPath = filepath.Join(base, "dump.json")
	if err := writeDump(f.dumpPath, d); err != nil {
		return f, err
	}
	page := "---\ntitle: fixture\n---\n\nHuman prose above.\n\n" + beginMarker + "\n" + endMarker + "\n\nHuman prose below.\n"
	if err := os.WriteFile(filepath.Join(base, pageRel), []byte(page), 0o600); err != nil {
		return f, err
	}
	// Generate once so the fixture starts in sync.
	if rc := runQuiet(base, f.dumpPath, true, false); rc != 0 {
		return f, fmt.Errorf("could not put the fixture in sync: rc=%d", rc)
	}
	return f, nil
}

func writeDump(path string, d cliDump) error {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// runQuiet runs the gate with stdout/stderr captured, and returns only the code.
func runQuiet(root, dumpPath string, write, list bool) int {
	rc, _ := runCapturing(root, dumpPath, write, list)
	return rc
}

// runCapturing runs the gate and returns its exit code together with everything
// it printed, so a red case can assert WHICH offender was named.
func runCapturing(root, dumpPath string, write, list bool) (int, string) {
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		return -1, ""
	}
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				b.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	rc := run(root, dumpPath, write, list)
	_ = w.Close()
	out := <-done
	_ = r.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return rc, out
}

type caseResult struct {
	name   string
	wantRC int
	gotRC  int
	needle string
	out    string
}

func (c caseResult) ok() bool {
	return c.gotRC == c.wantRC && (c.needle == "" || strings.Contains(c.out, c.needle))
}

func runSelfTest() int {
	base, err := os.MkdirTemp("", "cli-ref-docs-selftest-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli-ref-docs self-test: CANNOT LOOK — no scratch dir: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(base) }()

	var results []caseResult
	n := 0
	// each case gets its own tree
	newTree := func(mutate func(*cliDump)) (fixture, error) {
		n++
		d := newFixtureDump()
		dir := filepath.Join(base, fmt.Sprintf("t%02d", n))
		f, err := writeFixture(dir, d)
		if err != nil {
			return f, err
		}
		if mutate != nil {
			mutate(&d)
			sortDumpByPath(&d)
			if err := writeDump(f.dumpPath, d); err != nil {
				return f, err
			}
		}
		return f, nil
	}
	add := func(name string, wantRC int, needle string, mutate func(*cliDump)) {
		f, err := newTree(mutate)
		if err != nil {
			results = append(results, caseResult{name: name, wantRC: wantRC, gotRC: -1, out: err.Error()})
			return
		}
		rc, out := runCapturing(f.dir, f.dumpPath, false, false)
		results = append(results, caseResult{name: name, wantRC: wantRC, gotRC: rc, needle: needle, out: out})
	}

	// bespoke regenerates a mutated fixture and asserts something about the PAGE
	// itself, for the properties that are invisible in an exit code.
	bespoke := func(name string, mutate func(*cliDump), check func(string) error) {
		f, err := newTree(mutate)
		if err != nil {
			results = append(results, caseResult{name: name, wantRC: 0, gotRC: -1, out: err.Error()})
			return
		}
		if rc := runQuiet(f.dir, f.dumpPath, true, false); rc != 0 {
			results = append(results, caseResult{name: name, wantRC: 0, gotRC: rc,
				out: "regeneration failed"})
			return
		}
		raw, err := os.ReadFile(filepath.Join(f.dir, pageRel))
		if err != nil {
			results = append(results, caseResult{name: name, wantRC: 0, gotRC: -1, out: err.Error()})
			return
		}
		if err := check(string(raw)); err != nil {
			results = append(results, caseResult{name: name, wantRC: 0, gotRC: 1, out: err.Error()})
			return
		}
		results = append(results, caseResult{name: name, wantRC: 0, gotRC: 0})
	}

	// ── GREEN ────────────────────────────────────────────────────────────────────
	add("in-sync-tree", 0, "matches the binary", nil)

	// Regenerating twice must produce identical bytes: a page that churns on every
	// run makes the gate useless as a diff.
	n++
	if f, err := writeFixture(filepath.Join(base, fmt.Sprintf("t%02d", n)), newFixtureDump()); err == nil {
		first, _ := os.ReadFile(filepath.Join(f.dir, pageRel))
		runQuiet(f.dir, f.dumpPath, true, false)
		second, _ := os.ReadFile(filepath.Join(f.dir, pageRel))
		rc := 0
		if string(first) != string(second) {
			rc = 1
		}
		results = append(results, caseResult{name: "regeneration-is-deterministic", wantRC: 0, gotRC: rc})
	} else {
		results = append(results, caseResult{name: "regeneration-is-deterministic", wantRC: 0, gotRC: -1, out: err.Error()})
	}

	// A rendered page is only useful if the strings the tree carries survive markdown.
	// Measured in the real tree on 2026-08-16: 19 usage strings contain a pipe and 28
	// contain angle brackets. Unescaped, the first silently starts a new table column
	// and the second is eaten by the HTML parser — both fail SILENTLY, which is why
	// they get a case rather than a comment.
	bespoke("hazardous-strings-are-escaped", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd000" {
				for j := range d.Commands[i].Flags {
					if d.Commands[i].Flags[j].Name == "tenant" {
						d.Commands[i].Flags[j].Usage = `low|medium|high, pinned as "<alg>:<base64>"`
					}
				}
			}
		}
	}, func(page string) error {
		if !strings.Contains(page, `low\|medium\|high`) {
			return fmt.Errorf("the pipe was not escaped; the row would gain a column")
		}
		if !strings.Contains(page, "&lt;alg&gt;") {
			return fmt.Errorf("the angle brackets were not escaped; markdown would drop them")
		}
		return nil
	})

	// The one command whose anchor cannot be predicted keeps its section and its index
	// entry, and loses only the link. Without this case, "withhold the link" and
	// "drop the command" are the same to the battery.
	bespoke("ambiguous-anchor-keeps-entry-loses-link", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd005" {
				d.Commands[i].Path = "olivares __probe"
				d.Commands[i].Name = "__probe"
			}
		}
	}, func(page string) error {
		if !strings.Contains(page, "#### Command: olivares __probe") {
			return fmt.Errorf("the command lost its section")
		}
		if !strings.Contains(page, "| `olivares __probe` |") {
			return fmt.Errorf("the command lost its index entry")
		}
		if strings.Contains(page, "](#command-olivares-__probe)") {
			return fmt.Errorf("a link was emitted for an anchor whose slug two sluggers disagree about")
		}
		return nil
	})

	// ── RED: drift between the page and the tree ─────────────────────────────────
	add("added-command-not-regenerated", 1, "olivares zzz-new", func(d *cliDump) {
		d.Commands = append(d.Commands, dumpCommand{
			Path: "olivares zzz-new", Name: "zzz-new", Parent: "olivares", Use: "zzz-new [flags]",
			Short: "a command that landed after the page was written", Depth: 1,
			Runnable: true, HasHelpFlag: true,
			Flags: []dumpFlag{{Name: "help", Type: "bool", Default: "false", Usage: "help"}},
		})
	})
	add("removed-command-still-published", 1, "no longer in the binary", func(d *cliDump) {
		d.Commands = d.Commands[:len(d.Commands)-1]
	})
	add("added-flag-not-regenerated", 1, "detail", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd000" {
				d.Commands[i].Flags = append(d.Commands[i].Flags, dumpFlag{
					Name: "brand-new", Type: "bool", Default: "false", Usage: "a flag nobody documented",
				})
			}
		}
	})
	add("changed-default-not-regenerated", 1, "detail", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares" {
				for j := range d.Commands[i].Flags {
					if d.Commands[i].Flags[j].Name == "output" {
						d.Commands[i].Flags[j].Default = "json"
					}
				}
			}
		}
	})
	add("changed-summary-not-regenerated", 1, "detail", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd001" {
				d.Commands[i].Short = "a summary somebody rewrote in the code only"
			}
		}
	})

	// ── RED: prose the public surface must not carry ─────────────────────────────
	add("banned-absolute-in-flag-usage", 1, "is an absolute claim the canon forbids", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd000" {
				for j := range d.Commands[i].Flags {
					if d.Commands[i].Flags[j].Name == "tenant" {
						d.Commands[i].Flags[j].Usage = "write to the tamper-proof ledger"
					}
				}
			}
		}
	})
	add("banned-claim-in-summary", 1, "unnormalized cryptographic-validation claim", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd002" {
				// Split exactly as scripts/check-docs-honesty.sh splits its own patterns:
				// that gate scans scripts/ too, so a fixture spelling the claim in one
				// piece makes THIS file the unanchored claim it exists to catch.
				d.Commands[i].Short = "run the FIPS-valid" + "ated cipher suite"
			}
		}
	})
	add("empty-summary", 1, "no Short description", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd003" {
				d.Commands[i].Short = ""
			}
		}
	})
	add("unbalanced-backtick-in-usage", 1, "odd number of backticks", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd000" {
				for j := range d.Commands[i].Flags {
					if d.Commands[i].Flags[j].Name == "tenant" {
						d.Commands[i].Flags[j].Usage = "see `keys status for the list"
					}
				}
			}
		}
	})
	add("clock-derived-flag-default", 1, "embeds today's date", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd000" {
				for j := range d.Commands[i].Flags {
					if d.Commands[i].Flags[j].Name == "tenant" {
						d.Commands[i].Flags[j].Default = "bundle-" + time.Now().UTC().Format("20060102") + ".tar.gz"
					}
				}
			}
		}
	})
	add("command-path-breaks-anchors", 1, "outside", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares cmd004" {
				d.Commands[i].Path = "olivares CMD/004"
			}
		}
	})

	// ── RED: the exit-code contract disagrees with itself ────────────────────────
	add("code-declared-but-not-in-help", 1, "does not list it", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares" {
				d.Commands[i].Long = strings.Replace(d.Commands[i].Long,
					"  2  usage error (unknown flag or bad arguments)\n", "", 1)
			}
		}
	})
	add("code-in-help-but-not-declared", 1, "no constant in", func(d *cliDump) {
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares" {
				d.Commands[i].Long += "  7  degraded\n"
			}
		}
	})

	// ── CANNOT LOOK ──────────────────────────────────────────────────────────────
	cannotCase := func(name, needle string, prepare func(f fixture) error) {
		n++
		dir := filepath.Join(base, fmt.Sprintf("t%02d", n))
		f, err := writeFixture(dir, newFixtureDump())
		if err != nil {
			results = append(results, caseResult{name: name, wantRC: 2, gotRC: -1, out: err.Error()})
			return
		}
		if err := prepare(f); err != nil {
			results = append(results, caseResult{name: name, wantRC: 2, gotRC: -1, out: err.Error()})
			return
		}
		rc, out := runCapturing(f.dir, f.dumpPath, false, false)
		results = append(results, caseResult{name: name, wantRC: 2, gotRC: rc, needle: needle, out: out})
	}

	cannotCase("dump-missing", "did not run or did not finish", func(f fixture) error {
		return os.Remove(f.dumpPath)
	})
	cannotCase("dump-empty", "is empty", func(f fixture) error {
		return os.WriteFile(f.dumpPath, nil, 0o600)
	})
	cannotCase("dump-unreadable-json", "not readable JSON", func(f fixture) error {
		return os.WriteFile(f.dumpPath, []byte("{not json"), 0o600)
	})
	cannotCase("dump-unknown-schema", "this gate speaks", func(f fixture) error {
		d := newFixtureDump()
		d.Schema = "olivares.cli-ref/99"
		return writeDump(f.dumpPath, d)
	})
	cannotCase("tree-below-population-floor", "below the floor", func(f fixture) error {
		d := newFixtureDump()
		d.Commands = d.Commands[:5]
		return writeDump(f.dumpPath, d)
	})
	cannotCase("dump-not-sorted", "not sorted by command path", func(f fixture) error {
		d := newFixtureDump()
		d.Commands[3], d.Commands[9] = d.Commands[9], d.Commands[3]
		return writeDump(f.dumpPath, d)
	})
	cannotCase("page-lost-its-markers", "lost its", func(f fixture) error {
		return os.WriteFile(filepath.Join(f.dir, pageRel), []byte("---\ntitle: x\n---\n\nno markers here\n"), 0o600)
	})
	cannotCase("exitcode-source-missing", "could not parse the exit-code contract", func(f fixture) error {
		return os.Remove(filepath.Join(f.dir, exitCodeRel))
	})
	cannotCase("exitcode-source-has-no-constants", "found no exit-code constants", func(f fixture) error {
		return os.WriteFile(filepath.Join(f.dir, exitCodeRel), []byte("package exitcode\n"), 0o600)
	})
	cannotCase("exitcode-constant-undocumented", "has no doc comment", func(f fixture) error {
		src := "package exitcode\n\nconst (\n\tOK = 0\n)\n"
		return os.WriteFile(filepath.Join(f.dir, exitCodeRel), []byte(src), 0o600)
	})
	cannotCase("root-help-has-no-exit-code-block", "no \"Exit codes:\" block", func(f fixture) error {
		d := newFixtureDump()
		for i := range d.Commands {
			if d.Commands[i].Path == "olivares" {
				d.Commands[i].Long = "olivares is the test root, with no contract block."
			}
		}
		return writeDump(f.dumpPath, d)
	})

	// ── verdict ──────────────────────────────────────────────────────────────────
	failed := 0
	for _, r := range results {
		status := "ok  "
		if !r.ok() {
			status = "FAIL"
			failed++
		}
		fmt.Printf("  %s %-36s want rc=%d got rc=%d\n", status, r.name, r.wantRC, r.gotRC)
		if !r.ok() {
			if r.needle != "" && !strings.Contains(r.out, r.needle) {
				fmt.Printf("       expected the output to name %q\n", r.needle)
			}
			for _, l := range strings.Split(strings.TrimSpace(r.out), "\n") {
				fmt.Printf("       | %s\n", l)
			}
		}
	}
	green, red, cannotN := 0, 0, 0
	for _, r := range results {
		switch r.wantRC {
		case 0:
			green++
		case 1:
			red++
		case 2:
			cannotN++
		}
	}
	fmt.Printf("cli-ref-docs self-test: %d cases (%d green, %d drift, %d CANNOT LOOK), %d failed\n",
		len(results), green, red, cannotN, failed)
	if failed > 0 {
		return 1
	}
	return 0
}
