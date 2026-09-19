// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Golden tests for the CLI renderer. Each test names the design decision it pins.
//
// EVERY GOLDEN IS THE PLAIN FORM, and that is the point: the
// bytes a pipe receives are a published interface, so they are asserted
// literally. The rich form is not given a second golden — it is asserted to be
// the plain form plus SGR, which is a stronger statement than any pair of
// goldens, because it cannot drift in only one of them.

package termrender

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// env builds a LookupEnv over a map.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

// pipe is a renderer whose stream is not a terminal: no colour, unbounded width.
func pipe(w io.Writer) *Renderer {
	return New(w, Options{LookupEnv: env(nil), IsTerminal: func(io.Writer) bool { return false }})
}

// screen is a renderer on an n-column terminal with colour on.
func screen(w io.Writer, n int) *Renderer {
	on := true
	return New(w, Options{Color: &on, Width: n,
		LookupEnv: env(nil), IsTerminal: func(io.Writer) bool { return true }})
}

func render(t *testing.T, build func(*Renderer)) string {
	t.Helper()
	var b bytes.Buffer
	build(pipe(&b))
	return b.String()
}

func wantExact(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("plain form mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// --- Colour is decided by the stream and by NO_COLOR ----------------------

func TestColorMatrix(t *testing.T) {
	cases := []struct {
		name     string
		terminal bool
		envVars  map[string]string
		want     bool
	}{
		{"terminal, clean environment", true, nil, true},
		{"pipe, clean environment", false, nil, false},
		{"terminal, NO_COLOR=1", true, map[string]string{"NO_COLOR": "1"}, false},
		// The empty form is the one a naive `getenv(x) != ""` check gets wrong,
		// and it is the documented spelling of NO_COLOR.
		{"terminal, NO_COLOR= (declared empty)", true, map[string]string{"NO_COLOR": ""}, false},
		{"pipe, NO_COLOR=1", false, map[string]string{"NO_COLOR": "1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := New(io.Discard, Options{
				LookupEnv:  env(tc.envVars),
				IsTerminal: func(io.Writer) bool { return tc.terminal },
			})
			if r.Color() != tc.want {
				t.Fatalf("Color() = %v, want %v", r.Color(), tc.want)
			}
		})
	}
}

// TestColorRolesAreClosed pins the closed role set: four semantic roles, no theme.
func TestColorRolesAreClosed(t *testing.T) {
	styled := 0
	for role := RoleNone; role <= RoleMuted; role++ {
		if role.sgr() != "" {
			styled++
		}
	}
	if styled != 4 {
		t.Fatalf("styled roles = %d, want exactly 4 (ok, warn, fail, muted)", styled)
	}
	if RoleNone.sgr() != "" {
		t.Fatalf("RoleNone must be unstyled")
	}
}

// --- Rich is plain plus SGR; a pipe is never width-limited ----------------

// TestRichIsPlainPlusColor asserts, for every primitive, that stripping SGR from
// the terminal form yields the pipe form byte for byte. This is what makes the
// plain goldens below a contract and not a snapshot: colour cannot change a word,
// a space, or a column.
func TestRichIsPlainPlusColor(t *testing.T) {
	draw := func(r *Renderer) {
		r.Fields([]Field{{Key: "provider", Value: "prv_01", Role: RoleOK}, {Key: "state", Value: ""}})
		r.Table(Table{
			Header: []string{"check", "status"},
			Rows:   [][]string{{"livez", "pass"}, {"store", "fail"}},
			Roles:  [][]Role{{RoleNone, RoleOK}, {RoleNone, RoleFail}},
		})
		r.StatusLine(Status{Role: RoleWarn, Label: "hook pep", Detail: "not wired"})
		r.Summary(map[Role]int{RoleOK: 2, RoleFail: 1}, "run olivares doctor again")
		r.Error(Problem{What: "the engine refused the credential", Cause: "http 401", Next: "olivares provider rotate prv_01"})
		r.Next("olivares status")
	}
	var plain, rich bytes.Buffer
	draw(pipe(&plain))
	// A width wide enough that the table keeps its column form in both.
	draw(screen(&rich, 200))
	if got := StripANSI(rich.String()); got != plain.String() {
		t.Fatalf("rich minus SGR != plain\n--- rich stripped ---\n%s\n--- plain ---\n%s", got, plain.String())
	}
	if !strings.Contains(rich.String(), "\x1b[") {
		t.Fatalf("the terminal form carried no SGR at all; the comparison proved nothing")
	}
}

// TestPipeIsUnbounded pins that a pipe is never width-limited, so a wide
// table keeps its column form and no value is reshaped by where it was run.
func TestPipeIsUnbounded(t *testing.T) {
	wide := strings.Repeat("x", 300)
	got := render(t, func(r *Renderer) {
		r.Table(Table{Header: []string{"a", "b"}, Rows: [][]string{{wide, "1"}}})
	})
	if !strings.Contains(got, wide) {
		t.Fatalf("a piped table dropped or reshaped its value")
	}
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("a piped table used the record form; got %d lines:\n%s", strings.Count(got, "\n"), got)
	}
}

// --- One width model for every primitive ----------------------------------

func TestVisibleWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"\x1b[32mabc\x1b[0m", 3},
		{"\x1b[2mok\x1b[0m!", 3},
		{"é", 1},                     // e + combining acute is one column
		{"\x1b]8;;https://x\x07a", 1}, // OSC 8 hyperlink open is zero-width
	}
	for _, tc := range cases {
		if got := visibleWidth(tc.in); got != tc.want {
			t.Fatalf("visibleWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestAlignmentIgnoresColor pins the width model where it matters: two cells of
// equal visible width must occupy equal columns whether or not they are painted.
func TestAlignmentIgnoresColor(t *testing.T) {
	var b bytes.Buffer
	r := screen(&b, 200)
	r.Table(Table{
		Header: []string{"check", "status"},
		Rows:   [][]string{{"aaa", "pass"}, {"bbb", "fail"}},
		Roles:  [][]Role{{RoleNone, RoleOK}, {RoleNone, RoleFail}},
	})
	// Every row's second column must begin at the same offset as the header's,
	// whether or not the cell before it was painted.
	lines := strings.Split(strings.TrimRight(StripANSI(b.String()), "\n"), "\n")
	want := strings.Index(lines[0], "STATUS")
	if want <= 0 {
		t.Fatalf("header did not carry a second column: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if got := strings.IndexAny(l, "pf"); got != want {
			t.Fatalf("second column starts at %d, want %d: %q", got, want, l)
		}
	}
}

// --- Degenerate input is clamped, never a failure -------------------------

// TestNoPrimitivePanicsOnDegenerateInput pins that degenerate input is clamped
// and never a failure. The render hot path must not be the reason a command
// fails, so the degenerate shapes are exercised deliberately: short rows, a nil
// role matrix, an empty header, zero fields.
func TestNoPrimitivePanicsOnDegenerateInput(t *testing.T) {
	var b bytes.Buffer
	r := screen(&b, 10) // narrower than anything below
	r.Fields(nil)
	r.Fields([]Field{{Key: "", Value: ""}})
	r.Table(Table{Rows: [][]string{{"a"}, {"a", "b", "c"}}})
	r.Table(Table{Header: []string{"one", "two"}, Rows: [][]string{{"only"}}})
	r.StatusLine(Status{})
	r.Summary(nil, "")
	r.Error(Problem{})
	r.Next("")
	if b.Len() == 0 {
		t.Fatalf("the degenerate run wrote nothing; it proved nothing")
	}
}

// --- The table and its record fallback ------------------------------------

func TestTableColumnFormGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Table(Table{
			Header: []string{"provider", "kind", "state"},
			Rows: [][]string{
				{"prv_01a0b3ec-2c7f-785c-a362-36f2cb27d3f0", "anthropic", "active"},
				{"prv_02", "openai", "revoked"},
			},
		})
	})
	wantExact(t, got, ""+
		"PROVIDER                                  KIND       STATE\n"+
		"prv_01a0b3ec-2c7f-785c-a362-36f2cb27d3f0  anthropic  active\n"+
		"prv_02                                    openai     revoked\n")
}

// TestTableRecordFallbackGolden pins the decision the walk forced: on a terminal
// too narrow for the columns, every byte is kept and the SHAPE is what gives way.
// Nothing is truncated and no column is dropped.
func TestTableRecordFallbackGolden(t *testing.T) {
	var b bytes.Buffer
	off := false
	r := New(&b, Options{Color: &off, Width: 40,
		LookupEnv: env(nil), IsTerminal: func(io.Writer) bool { return true }})
	r.Table(Table{
		Header: []string{"provider", "kind", "state"},
		Rows: [][]string{
			{"prv_01a0b3ec-2c7f-785c-a362-36f2cb27d3f0", "anthropic", "active"},
			{"prv_02", "openai", "revoked"},
		},
	})
	wantExact(t, b.String(), ""+
		"PROVIDER  prv_01a0b3ec-2c7f-785c-a362-36f2cb27d3f0\n"+
		"KIND      anthropic\n"+
		"STATE     active\n"+
		"\n"+
		"PROVIDER  prv_02\n"+
		"KIND      openai\n"+
		"STATE     revoked\n")
}

// TestTableEmptyGolden pins the defect renderListOut already names for lists and
// nothing enforced for tables: an empty result must say so.
func TestTableEmptyGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Table(Table{Header: []string{"a"}, Empty: "No providers registered."})
		r.Next("olivares provider add --kind anthropic --name \"Anthropic\" < key.txt")
	})
	wantExact(t, got, ""+
		"No providers registered.\n"+
		"next: olivares provider add --kind anthropic --name \"Anthropic\" < key.txt\n")
}

// TestTableRaggedRowsGolden pins what the walk found in `agent tool detect`: two
// rows of different length under no header. The short row gets empty cells, the
// columns still line up, and nothing silently disappears.
func TestTableRaggedRowsGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Table(Table{
			Header: []string{"path", "origin", "match", "version"},
			Rows: [][]string{
				{"/home/op/.grok/bin/grok", "path", "unregistered-observed"},
				{"/srv/tools/grok/1.0.34/bin/grok", "managed", "registered", "1.0.34"},
			},
		})
	})
	wantExact(t, got, ""+
		"PATH                             ORIGIN   MATCH                  VERSION\n"+
		"/home/op/.grok/bin/grok          path     unregistered-observed\n"+
		"/srv/tools/grok/1.0.34/bin/grok  managed  registered             1.0.34\n")
}

// --- Fields, status, summary, error ---------------------------------------

func TestFieldsGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Fields([]Field{
			{Key: "provider", Value: "prv_01"},
			{Key: "connection", Value: "tested", Role: RoleOK},
			{Key: "guard warning", Value: ""},
		})
	})
	// The third line is the measured defect: `olivares status` ends a line with
	// `guard_warning=` and nothing tells an empty value from a cut line.
	wantExact(t, got, ""+
		"PROVIDER       prv_01\n"+
		"CONNECTION     tested\n"+
		"GUARD WARNING  -\n")
}

func TestStatusLineGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.StatusLine(Status{Role: RoleOK, Label: "livez", Detail: "status=200"})
		r.StatusLine(Status{Role: RoleWarn, Label: "hook pep", Detail: "not wired"})
		r.StatusLine(Status{Role: RoleFail, Label: "configuration", Detail: "missing"})
		r.StatusLine(Status{Role: RoleNone, Label: "audit chain", Detail: "not requested"})
		r.StatusLine(Status{Role: RoleOK, Label: "binary"})
	})
	// [--] is NOT [ok]: an unmeasured check that reads as a passing one is how a
	// SKIP becomes a PASS.
	wantExact(t, got, ""+
		"[ok]   livez - status=200\n"+
		"[warn] hook pep - not wired\n"+
		"[fail] configuration - missing\n"+
		"[--]   audit chain - not requested\n"+
		"[ok]   binary\n")
}

func TestSummaryGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Summary(map[Role]int{RoleOK: 12, RoleWarn: 2, RoleFail: 1}, "run olivares doctor --data-dir DIR")
		r.Summary(map[Role]int{RoleOK: 3}, "")
		r.Summary(nil, "")
	})
	wantExact(t, got, ""+
		"12 ok, 2 warn, 1 fail - run olivares doctor --data-dir DIR\n"+
		"3 ok\n"+
		"nothing to report\n")
}

// TestErrorGolden pins the error shape. The reference CLI ends with "Run
// `<binary> <cmd> --help` for details"; this ends with the command to run,
// because the walk showed the CLI usually knows it.
func TestErrorGolden(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Error(Problem{
			What:  "the control plane refused this credential",
			Cause: "http 401 unauthenticated",
			Next:  "olivares auth bootstrap --setup-token-file ./setup.token --email you@example.com --password-file ./pw --save-context",
		})
		r.Error(Problem{What: "no provider reference was given"})
	})
	wantExact(t, got, ""+
		"error: the control plane refused this credential\n"+
		"cause: http 401 unauthenticated\n"+
		"next: olivares auth bootstrap --setup-token-file ./setup.token --email you@example.com --password-file ./pw --save-context\n"+
		"error: no provider reference was given\n")
}

// --- Progress: nothing in a pipe ------------------------------------------

// TestProgressWritesNothingToAPipe pins that progress never reaches a pipe. A
// carriage return in a log file is a line whose start a reader never sees; a
// spinner in CI is thousands of frames.
func TestProgressWritesNothingToAPipe(t *testing.T) {
	var b bytes.Buffer
	p := pipe(&b).Progress("downloading")
	p.Set("40 MB of 163 MB")
	p.Set("163 MB of 163 MB")
	p.Done()
	if b.Len() != 0 {
		t.Fatalf("progress wrote %q to a pipe; it must write nothing", b.String())
	}
}

func TestProgressRewritesOneLineOnATerminal(t *testing.T) {
	var b bytes.Buffer
	p := screen(&b, 80).Progress("downloading")
	p.Set("40 MB of 163 MB")
	p.Set("9 MB")
	p.Done()
	out := b.String()
	if strings.Contains(out, "\n") {
		t.Fatalf("progress emitted a newline; it must rewrite one line: %q", out)
	}
	if !strings.HasPrefix(out, "\rdownloading 40 MB of 163 MB") {
		t.Fatalf("first frame missing: %q", out)
	}
	// The shorter second frame must overwrite the tail of the longer first one,
	// or the terminal shows "downloading 9 MB of 163 MB" — a number that was
	// never reported.
	if !strings.Contains(out, "\rdownloading 9 MB     ") {
		t.Fatalf("a shorter frame did not clear the longer one it replaced: %q", out)
	}
	if !strings.HasSuffix(out, "\r") {
		t.Fatalf("Done() must leave the cursor at column 0 on a cleared line: %q", out)
	}
}

// --- determinism ----------------------------------------------------------

// TestSortedKeysIsDeterministic pins determinism: two runs of one command produce
// identical bytes, which a Go map range does not.
func TestSortedKeysIsDeterministic(t *testing.T) {
	m := map[string]int{"zulu": 1, "alpha": 2, "mike": 3}
	first := SortedKeys(m)
	for i := 0; i < 50; i++ {
		got := SortedKeys(m)
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("SortedKeys is not deterministic: %v then %v", first, got)
			}
		}
	}
	if strings.Join(first, ",") != "alpha,mike,zulu" {
		t.Fatalf("SortedKeys = %v, want sorted", first)
	}
}

// TestNoTrailingWhitespace pins line()'s trimming: trailing spaces are invisible
// on screen and a permanent diff hazard in the goldens above.
func TestNoTrailingWhitespace(t *testing.T) {
	got := render(t, func(r *Renderer) {
		r.Table(Table{Header: []string{"a", "b"}, Rows: [][]string{{"x", ""}, {"longer", "y"}}})
		r.Fields([]Field{{Key: "k", Value: "v"}})
		r.StatusLine(Status{Role: RoleOK, Label: "x"})
	})
	for _, l := range strings.Split(got, "\n") {
		if l != strings.TrimRight(l, " \t") {
			t.Fatalf("line has trailing whitespace: %q", l)
		}
	}
}

// --- WrappedFields --------------------------------------------------------

// wrappedFixture is one long sentence and one short value, which is the shape
// doctor's DETAIL block has: most checks say one clause and one says three.
func wrappedFixture() []Field {
	return []Field{
		{Key: "data-directory", Value: "/var/lib/olivares mode=0777 | fix: remove group/world write access; " +
			"user mode requires 0700 and system mode 0700 or 0750"},
		{Key: "license", Value: "source=none"},
	}
}

// TestWrappedFieldsFoldsUnderItsColumn is the primitive doctor's DETAIL block
// needed: the sentence folds at the terminal width and the folds hang under the
// value column, so a field reads as one paragraph.
func TestWrappedFieldsFoldsUnderItsColumn(t *testing.T) {
	var b bytes.Buffer
	off := false
	New(&b, Options{Color: &off, Width: 60, LookupEnv: env(nil),
		IsTerminal: func(io.Writer) bool { return true }}).WrappedFields(wrappedFixture())
	wantExact(t, b.String(), `DATA-DIRECTORY  /var/lib/olivares mode=0777 | fix: remove
                group/world write access; user mode requires
                0700 and system mode 0700 or 0750
LICENSE         source=none
`)
}

// TestWrappedFieldsUsesTheWidthItIsGiven is the half a fixed width fails. Folding
// at a constant would pass "nothing is too wide" and waste every terminal wider
// than the constant, which is exactly what doctor did before this primitive: its
// DETAIL block was byte-identical at 80 and at 120.
func TestWrappedFieldsUsesTheWidthItIsGiven(t *testing.T) {
	widest := func(n int) int {
		var b bytes.Buffer
		off := false
		New(&b, Options{Color: &off, Width: n, LookupEnv: env(nil),
			IsTerminal: func(io.Writer) bool { return true }}).WrappedFields(wrappedFixture())
		max := 0
		for _, line := range strings.Split(b.String(), "\n") {
			if w := visibleWidth(line); w > max {
				max = w
			}
		}
		return max
	}
	for _, n := range []int{60, 80, 120} {
		if got := widest(n); got > n {
			t.Errorf("at %d columns the widest line is %d", n, got)
		}
	}
	if narrow, wide := widest(80), widest(120); wide <= narrow {
		t.Errorf("120 columns produced no line wider than 80 (%d vs %d), so the width is "+
			"decorative and a wide terminal is wasted", wide, narrow)
	}
}

// TestWrappedFieldsPipeGetsEightyColumns: a stream with no width has to fold at
// SOMETHING, and picking the terminal floor keeps the plain form one set of bytes
// rather than the shell's guess. Unbounded is right for an identifier and wrong
// for a sentence.
func TestWrappedFieldsPipeGetsEightyColumns(t *testing.T) {
	piped := render(t, func(r *Renderer) { r.WrappedFields(wrappedFixture()) })
	var b bytes.Buffer
	off := false
	New(&b, Options{Color: &off, Width: 80, LookupEnv: env(nil),
		IsTerminal: func(io.Writer) bool { return true }}).WrappedFields(wrappedFixture())
	if piped != b.String() {
		t.Errorf("a pipe does not receive the eighty-column form.\npipe:\n%s\n80:\n%s", piped, b.String())
	}
	if !strings.Contains(piped, "\n                ") {
		t.Errorf("nothing folded, so this case compared two unfolded blocks:\n%s", piped)
	}
}

// TestWrappedFieldsHasAFloor: below sixty columns the hanging indent leaves a
// ribbon too narrow to read, so folding stops helping. The floor means a very
// narrow terminal soft-wraps, which it would have done anyway.
func TestWrappedFieldsHasAFloor(t *testing.T) {
	at := func(n int) string {
		var b bytes.Buffer
		off := false
		New(&b, Options{Color: &off, Width: n, LookupEnv: env(nil),
			IsTerminal: func(io.Writer) bool { return true }}).WrappedFields(wrappedFixture())
		return b.String()
	}
	if at(20) != at(60) {
		t.Errorf("a 20-column terminal folds differently from the floor.\n20:\n%s\n60:\n%s", at(20), at(60))
	}
	if at(60) == at(80) {
		t.Error("60 and 80 fold identically, so the floor above is indistinguishable from a constant")
	}
}

// TestWrappedFieldsNeverCutsAWord is the package's no-truncation rule at the one
// place folding could break it: an operator has to be able to copy a path.
func TestWrappedFieldsNeverCutsAWord(t *testing.T) {
	const path = "/home/claude/.local/share/olivares/install-manifest-with-a-very-long-name.json"
	got := render(t, func(r *Renderer) {
		r.WrappedFields([]Field{{Key: "install-manifest", Value: path}})
	})
	if !strings.Contains(got, path) {
		t.Fatalf("the path was cut to make it fit:\n%s", got)
	}
	// And it overflows rather than being hyphenated or dropped, which is the
	// stated consequence: the widest line here is WIDER than eighty columns.
	max := 0
	for _, line := range strings.Split(got, "\n") {
		if w := visibleWidth(line); w > max {
			max = w
		}
	}
	if max <= 80 {
		t.Fatalf("this fixture is meant to be unfoldable at eighty columns and its widest line "+
			"is %d, so the case proves nothing", max)
	}
}

// TestWrappedFieldsRichIsPlainPlusColour: the property the whole package rests on,
// asserted at a fold, where a reset sequence spanning a newline would break it.
func TestWrappedFieldsRichIsPlainPlusColour(t *testing.T) {
	var plain, rich bytes.Buffer
	off := false
	fields := []Field{{Key: "data-directory", Value: wrappedFixture()[0].Value, Role: RoleFail}}
	New(&plain, Options{Color: &off, Width: 80, LookupEnv: env(nil),
		IsTerminal: func(io.Writer) bool { return true }}).WrappedFields(fields)
	screen(&rich, 80).WrappedFields(fields)
	if got := StripANSI(rich.String()); got != plain.String() {
		t.Errorf("stripping colour did not give the plain form.\nplain:\n%q\nstripped:\n%q", plain.String(), got)
	}
	if !strings.Contains(rich.String(), "\x1b[") {
		t.Fatal("the coloured form carries no escape sequence, so the comparison proved nothing")
	}
	if !strings.Contains(plain.String(), "\n     ") {
		t.Fatal("nothing folded, so the property was not asserted at a fold")
	}
}
