// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"bufio"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
)

// downProduct reports the product as measured and not answering.
type downProduct struct{}

func (downProduct) MeasureProduct() auth.ProductMeasurement {
	return auth.ProductMeasurement{Measured: true}
}

// brokenStack is the host's sign-in stack as an appliance finds it when the portal's own
// PAM configuration is missing or unusable. It refuses to be measured as usable and fails
// the case if anything asks it for a transaction or for a group.
type brokenStack struct{ t *testing.T }

func (s brokenStack) Measure() auth.PAMMeasurement {
	return auth.PAMMeasurement{Reason: "This console's sign-in stack is unusable."}
}

func (s brokenStack) Start(string) (auth.Transaction, error) {
	s.t.Error("an unusable stack was asked to open a transaction")
	return nil, errors.New("unusable")
}

func (s brokenStack) LocalGroups(string) ([]string, error) {
	s.t.Error("an unusable stack was asked for a login's groups")
	return nil, errors.New("unusable")
}

// writingIdentifiers lists, per import path, the identifiers that create, write or remove a
// file, or start a process. This console offers verbs and performs none, so it uses none.
var writingIdentifiers = map[string][]string{
	"os":      {"Create", "CreateTemp", "WriteFile", "OpenFile", "Mkdir", "MkdirAll", "MkdirTemp", "Remove", "RemoveAll", "Rename", "Symlink", "Link", "Chmod", "Chown", "Truncate"},
	"os/exec": {"Command", "CommandContext", "LookPath"},
	"syscall": {"Write", "Creat", "Mknod", "Unlink", "Setuid", "Setgid"},
}

// writeSites returns each place in the parsed files that creates, writes or removes a file,
// or starts a process.
func writeSites(files map[string]*ast.File) []string {
	var sites []string
	for path, syntax := range files {
		imported := map[string]string{}
		for _, spec := range syntax.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if _, watched := writingIdentifiers[importPath]; err != nil || !watched {
				continue
			}
			name := importPath[strings.LastIndex(importPath, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "." {
				sites = append(sites, path+": dot import of "+importPath)
				continue
			}
			imported[name] = importPath
		}
		ast.Inspect(syntax, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok {
				if importPath, ok := imported[pkg.Name]; ok && slices.Contains(writingIdentifiers[importPath], sel.Sel.Name) {
					sites = append(sites, path+": "+importPath+"."+sel.Sel.Name)
				}
			}
			return true
		})
	}
	return sites
}

// consoleSources parses every non-test Go source of this console, including its command.
func consoleSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	files := map[string]*ast.File{}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		syntax, parseErr := parser.ParseFile(token.NewFileSet(), path, data, 0)
		if parseErr != nil {
			return parseErr
		}
		files[path] = syntax
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no non-test sources found")
	}
	return files
}

func TestTty1_OffersTheRepairVerbsWhenPamIsUnusable(t *testing.T) {
	// The state this console exists for: the product does not answer and the portal's own
	// sign-in stack cannot be used.
	selector := auth.NewSelector(downProduct{}, auth.NewPAM(brokenStack{t: t}))
	state := selector.Select()
	if state.Mode != auth.Unavailable {
		t.Fatalf("product down and the stack unusable is %q, want %q", state.Mode, auth.Unavailable)
	}

	t.Run("the network path offers nothing", func(t *testing.T) {
		if verbs := state.NetworkVerbs(); len(verbs) != 0 {
			t.Errorf("the network offers %v, want nothing at all", verbs)
		}
		if state.OffersEverything() {
			t.Error("the network offers everything")
		}
		offer, err := selector.SignIn("ada")
		if !errors.Is(err, auth.ErrConsoleRemains) {
			t.Errorf("a network sign-in answered %v, want the console on this machine", err)
		}
		if offer.Mode != "" || len(offer.Verbs) != 0 {
			t.Errorf("a refused network sign-in was offered %+v", offer)
		}
		if statement := state.Statement(); !strings.Contains(statement, "tty1") {
			t.Errorf("the page states %q, which does not name the remaining door", statement)
		}
	})

	t.Run("the console carries the same repair verbs, and the one only it can carry", func(t *testing.T) {
		want := append(auth.RepairVerbs(), auth.VerbRepairPortalPAM)
		if got := Verbs(); !slices.Equal(got, want) {
			t.Fatalf("the console offers %v, want %v", got, want)
		}
		for _, v := range auth.RepairVerbs() {
			if !slices.Contains(Verbs(), v) {
				t.Errorf("the console does not carry the repair verb %q", v)
			}
		}
		if !slices.Contains(Verbs(), auth.VerbRepairPortalPAM) {
			t.Errorf("the console does not carry %q, which no other surface can", auth.VerbRepairPortalPAM)
		}
		// Writing to the returned list must not widen the next one.
		Verbs()[0] = auth.Verb("anything")
		if got := Verbs(); !slices.Equal(got, want) {
			t.Errorf("the verb list was widened by writing to it: %v", got)
		}
	})

	console := NewConsole(state)

	t.Run("the console states what it is and how it authenticates", func(t *testing.T) {
		header := strings.Join(console.Header(), "\n")
		for _, want := range []string{ConsoleName, "last door", "its own sign-in", "never by access to this machine", "tty1"} {
			if !strings.Contains(header, want) {
				t.Errorf("the header does not state %q:\n%s", want, header)
			}
		}
		if !strings.Contains(header, string(auth.Unavailable)) {
			t.Errorf("the header does not state the network sign-in mode:\n%s", header)
		}
	})

	t.Run("every verb is offered and none is performed without the console's own sign-in", func(t *testing.T) {
		for _, v := range Verbs() {
			answer := console.Select(v)
			if !strings.Contains(answer, string(v)) {
				t.Errorf("selecting %q answers %q, which does not name it", v, answer)
			}
			if !strings.Contains(answer, "needs its qualified sign-in") {
				t.Errorf("selecting %q answers %q, which does not say the act is refused without the sign-in", v, answer)
			}
		}
		if answer := console.Select(auth.Verb("delete-everything")); strings.Contains(answer, "needs its qualified sign-in") {
			t.Errorf("a verb this console does not offer answers %q", answer)
		}
	})

	t.Run("a session states the mode, lists the verbs and answers a selection", func(t *testing.T) {
		var out strings.Builder
		selections := []string{}
		for i := range Verbs() {
			selections = append(selections, strconv.Itoa(i+1))
		}
		selections = append(selections, "0", "q")
		var diagnostics strings.Builder
		in := strings.NewReader(strings.Join(selections, "\n") + "\n")
		if code := console.Run(in, &out, &diagnostics); code != 0 {
			t.Errorf("the console exited %d, want 0", code)
		}
		if diagnostics.Len() != 0 {
			t.Errorf("a session that left wrote a diagnostic: %q", diagnostics.String())
		}
		session := out.String()
		for _, want := range append([]string{ConsoleName, "tty1"}, verbNames()...) {
			if !strings.Contains(session, want) {
				t.Errorf("the session does not state %q:\n%s", want, session)
			}
		}
		if strings.Count(session, "needs its qualified sign-in") < len(Verbs()) {
			t.Errorf("a selection did not say the act is refused without the sign-in:\n%s", session)
		}
	})

	t.Run("the console writes no file and starts no process: power leaves through its helper", func(t *testing.T) {
		for _, site := range writeSites(consoleSources(t)) {
			t.Errorf("this console writes to the host or starts a process: %s", site)
		}
		// Control: the same scan finds a planted helper call and a planted write.
		for _, src := range []string{
			`package p; import "os/exec"; func f() { exec.Command("olivares-portal-power", "reboot") }`,
			`package p; import "os"; func f() { os.WriteFile("state", nil, 0o600) }`,
		} {
			syntax, err := parser.ParseFile(token.NewFileSet(), "planted.go", src, 0)
			if err != nil {
				t.Fatal(err)
			}
			if sites := writeSites(map[string]*ast.File{"planted.go": syntax}); len(sites) == 0 {
				t.Fatalf("control: the scan does not find %s", src)
			}
		}
	})
}

// errTerminal is the failure a broken terminal gives its reader or writer.
var errTerminal = errors.New("the terminal was hung up")

// failingReader yields what it holds and then fails, as the input of a terminal that is
// hung up in the middle of a session does.
type failingReader struct{ held *strings.Reader }

func (r failingReader) Read(p []byte) (int, error) {
	if r.held.Len() > 0 {
		return r.held.Read(p)
	}
	return 0, errTerminal
}

// failingWriter accepts its first accepted writes and fails every one after them.
type failingWriter struct {
	accepted int
	writes   int
	wrote    strings.Builder
}

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.writes >= w.accepted {
		return 0, errTerminal
	}
	w.writes++
	return w.wrote.Write(p)
}

// unreadInput fails the case if the console reads it: a console whose output has failed
// must stop, not wait on the operator for a selection it cannot answer.
type unreadInput struct{ t *testing.T }

func (r unreadInput) Read([]byte) (int, error) {
	r.t.Error("the console read its input after its output failed")
	return 0, io.EOF
}

func TestTty1_RunDistinguishesLeavingFromATerminalFailure(t *testing.T) {
	console := NewConsole(auth.State{Mode: auth.Unavailable})
	first := console.Select(Verbs()[0])

	// How many writes a whole session that leaves with "q" makes, so that a writer can be
	// made to fail on exactly its last one.
	counted := &failingWriter{accepted: 1 << 20}
	if code := console.Run(strings.NewReader("q\n"), counted, io.Discard); code != 0 {
		t.Fatalf("control: a session that leaves with q exited %d", code)
	}

	for _, c := range []struct {
		name string
		in   io.Reader
		out  io.Writer
		// code is the exit code, and diagnostic the whole of what the console states on
		// its diagnostics: nothing when it left, one fixed sentence when it failed.
		code       int
		diagnostic string
		// says and never are what the console output must and must not contain.
		says, never []string
	}{{
		name: "the end of the input leaves",
		in:   strings.NewReader("1\n"),
		code: 0,
		says: []string{first, "The input ended"},
	}, {
		name:  "q leaves, and nothing after it is read",
		in:    strings.NewReader("q\n1\n"),
		code:  0,
		says:  []string{"Leaving this console"},
		never: []string{first},
	}, {
		name:       "a read that fails is not the end of the input",
		in:         failingReader{held: strings.NewReader("1\n")},
		code:       2,
		diagnostic: inputFailed,
		says:       []string{first},
		never:      []string{"The input ended", "Leaving this console"},
	}, {
		name:       "a line longer than the console can read is not the end of the input",
		in:         strings.NewReader(strings.Repeat("1", bufio.MaxScanTokenSize+1) + "\n"),
		code:       2,
		diagnostic: inputFailed,
		never:      []string{"The input ended", "That selected nothing"},
	}, {
		name:       "output that fails at once stops the console before it reads",
		in:         unreadInput{t: t},
		out:        &failingWriter{},
		code:       2,
		diagnostic: outputFailed,
	}, {
		name:       "output that fails on the last line is not leaving",
		in:         strings.NewReader("q\n"),
		out:        &failingWriter{accepted: counted.writes - 1},
		code:       2,
		diagnostic: outputFailed,
	}} {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			var w io.Writer = &out
			if c.out != nil {
				w = c.out
			}
			var diagnostics strings.Builder
			if code := console.Run(c.in, w, &diagnostics); code != c.code {
				t.Errorf("the console exited %d, want %d", code, c.code)
			}
			want := ""
			if c.diagnostic != "" {
				want = c.diagnostic + "\n"
			}
			if got := diagnostics.String(); got != want {
				t.Errorf("the console's diagnostics are %q, want %q", got, want)
			}
			session := out.String()
			for _, s := range c.says {
				if !strings.Contains(session, s) {
					t.Errorf("the session does not state %q:\n%s", s, session)
				}
			}
			for _, s := range c.never {
				if strings.Contains(session, s) {
					t.Errorf("the session states %q:\n%s", s, session)
				}
			}
		})
	}

	// The two diagnostics are different sentences. That each is exactly one fixed sentence,
	// with nothing of the failure or the input in it, is the exact comparison above.
	if inputFailed == outputFailed {
		t.Errorf("an input failure and an output failure state the same sentence: %q", inputFailed)
	}
}

// verbNames returns the console's verbs as the strings a session prints.
func verbNames() []string {
	var names []string
	for _, v := range Verbs() {
		names = append(names, string(v))
	}
	return names
}
