// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Unit controls for the grammar this judge admits. They cover the discriminations a
// substring search gets wrong — an inline comment, a quoted sentence, a redirection, a
// heredoc body, the folded wrapper's quoted reason — at the level where they are decided.
// The end-to-end verdicts (0/1/2) belong to scripts/test-overlay-gate-wiring.sh, which
// runs the compiled binary over disposable mutants of the two real files; the refusal
// paths here call os.Exit by design and are exercised there, through a process.
package main

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func words(t *testing.T, line string) []word {
	t.Helper()
	cmds, _, err := lexLine(line, 1)
	if err != nil {
		t.Fatalf("lexLine(%q) failed: %v", line, err)
	}
	if len(cmds) != 1 {
		t.Fatalf("lexLine(%q) produced %d commands, want 1", line, len(cmds))
	}
	return cmds[0].words
}

func texts(ws []word) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.text)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLexLineWords(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{"plain invocation", "task lint:overlay-live-facts", []string{"task", "lint:overlay-live-facts"}},
		{"env prefixed", "OLIVARES_NETWORK_ADVISORY=1 task lint:session-numbers",
			[]string{"OLIVARES_NETWORK_ADVISORY=1", "task", "lint:session-numbers"}},
		{"redirection is not an argument", "bash scripts/addon-sets.sh > /dev/null",
			[]string{"bash", "scripts/addon-sets.sh"}},
		{"file descriptor and target dropped", "task --list-all 2>&1", []string{"task", "--list-all"}},
		{"quoted sentence is one word", `bash scripts/hub-only-gate.sh lint:addon-sets-gate "a, b (c)" lint:addon-sets-gate:legs`,
			[]string{"bash", "scripts/hub-only-gate.sh", "lint:addon-sets-gate", "a, b (c)", "lint:addon-sets-gate:legs"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := texts(words(t, c.line))
			if !equal(got, c.want) {
				t.Fatalf("lexLine(%q) = %q, want %q", c.line, got, c.want)
			}
		})
	}
}

// An inline comment must end the line. This is the third false CLEAN measured against the
// previous implementation: `: # ; task lint:overlay-live-facts` executed nothing and was
// read as an invocation.
func TestInlineCommentEndsTheLine(t *testing.T) {
	cmds, _, err := lexLine(": # ; task lint:overlay-live-facts", 1)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	if len(cmds) != 1 || len(cmds[0].words) != 1 || cmds[0].words[0].text != ":" {
		t.Fatalf("got %d command(s), want the single command `:`", len(cmds))
	}
	if _, _, _, ok := taskInvocation(cmds[0], 1); ok {
		t.Fatal("a commented-out call was read as an invocation")
	}
}

func TestFullLineCommentProducesNoCommand(t *testing.T) {
	cmds, _, err := lexLine("# task lint:overlay-live-facts", 1)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	if len(cmds) != 0 {
		t.Fatalf("got %d command(s), want none", len(cmds))
	}
}

// A separator inside quotes does not split a command, and the resulting word is data: the
// hook's own FAST-lint banner names these tasks in exactly this shape.
func TestQuotedSeparatorsStayInsideOneDataWord(t *testing.T) {
	ws := words(t, `echo "pre-push: overlay-seal + overlay-live-facts; task lint:addon-sets"`)
	if len(ws) != 2 || ws[0].text != "echo" {
		t.Fatalf("got %q, want two words headed by echo", texts(ws))
	}
	if !ws[1].isData() {
		t.Fatalf("word %q is not classified as data", ws[1].text)
	}
	if ws[1].bare {
		t.Fatalf("word %q was marked as carrying an unquoted part", ws[1].text)
	}
}

// A command substitution inside quotes is not data: its text is not the program that runs,
// so a judged name found there must reach the refusal path rather than be ignored.
func TestSubstitutionInsideQuotesIsNotData(t *testing.T) {
	ws := words(t, `X="$(task lint:overlay-live-facts)"`)
	if len(ws) != 1 {
		t.Fatalf("got %q, want one word", texts(ws))
	}
	if ws[0].isData() {
		t.Fatal("a quoted command substitution was classified as data")
	}
}

func TestOperatorsSplitCommands(t *testing.T) {
	cmds, _, err := lexLine("if ! command -v task >/dev/null 2>&1; then", 1)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	if len(cmds) != 2 {
		t.Fatalf("got %d command(s), want 2", len(cmds))
	}
	if _, _, _, ok := taskInvocation(cmds[0], 1); ok {
		t.Fatal("`command -v task` was read as a task invocation")
	}
}

func TestHeredocDelimiterIsRecognized(t *testing.T) {
	_, heredocs, err := lexLine("python3 - <<'PY'", 1)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	if len(heredocs) != 1 || heredocs[0] != "PY" {
		t.Fatalf("got %q, want [PY]", heredocs)
	}
}

func TestHeredocBodyStopsAtItsDelimiter(t *testing.T) {
	lines := []string{"cat <<'EOF'", "task lint:overlay-live-facts", "EOF", "task lint:addon-sets"}
	body, end := heredocBody(lines, 1, "EOF")
	if body != "task lint:overlay-live-facts" {
		t.Fatalf("body = %q", body)
	}
	if end != 2 {
		t.Fatalf("end = %d, want 2", end)
	}
}

func TestJoinContinuations(t *testing.T) {
	logical, extra := joinContinuations([]string{"task \\", "lint:overlay-live-facts", "next"}, 0)
	if extra != 1 {
		t.Fatalf("extra = %d, want 1", extra)
	}
	if got := texts(words(t, logical)); !equal(got, []string{"task", "lint:overlay-live-facts"}) {
		t.Fatalf("joined line lexed to %q", got)
	}
}

func TestTaskInvocationAcceptsOnlyModelledForms(t *testing.T) {
	cases := []struct {
		line string
		want string
		ok   bool
	}{
		{"task lint:overlay-live-facts", "lint:overlay-live-facts", true},
		{"OLIVARES_NETWORK_ADVISORY=1 task lint:hub-web-fidelity", "lint:hub-web-fidelity", true},
		{`GOFLAGS="-p=1" GOMAXPROCS=2 task test`, "test", true},
		{"then task lint:addon-sets", "lint:addon-sets", true},
		{"task --list-all", "", false},
		{"echo task lint:overlay-live-facts", "", false},
		{"alias task", "", false},
	}
	for _, c := range cases {
		cmds, _, err := lexLine(c.line, 1)
		if err != nil {
			t.Fatalf("lexLine(%q) failed: %v", c.line, err)
		}
		if len(cmds) == 0 {
			t.Fatalf("lexLine(%q) produced no command", c.line)
		}
		got, _, _, ok := taskInvocation(cmds[0], 1)
		if ok != c.ok || got != c.want {
			t.Fatalf("taskInvocation(%q) = (%q, %v), want (%q, %v)", c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestCommandRunsResolvesTheWrapperForms(t *testing.T) {
	cases := []struct {
		name          string
		line          string
		wantScript    string
		wantDelegates []string
	}{
		{"direct bash", "bash scripts/check-c03-grants-seam.sh", "scripts/check-c03-grants-seam.sh", nil},
		{"leg wrapper names the script it runs",
			"bash scripts/hub-leg.sh lint:overlay-live-facts scripts/check-overlay-live-facts.sh",
			"scripts/check-overlay-live-facts.sh", nil},
		{"gate wrapper delegates to the task after its quoted reason",
			`bash scripts/hub-only-gate.sh lint:addon-sets-gate "the pricing canon (design/PRICING-CANON.md, commercial/module-slug-package.json)" lint:addon-sets-gate:legs`,
			"", []string{"lint:addon-sets-gate:legs"}},
		// THE REAL DISPATCHER, copied from Taskfile.yml lint:addon-sets. Its first argument is
		// the leg label — a judged name written as DATA — and the last two are the branches.
		{"split gate delegates to BOTH branches and reads its label as data",
			`bash scripts/edition-split-gate.sh lint:addon-sets "the pricing canon and the commercial programme derived from it (design/PRICING-CANON.md, commercial/, cloud/control-plane)" lint:addon-sets:public lint:addon-sets:legs`,
			"", []string{"lint:addon-sets:public", "lint:addon-sets:legs"}},
		{"a printed path executes nothing", "echo scripts/test-overlay-live-facts.sh", "", nil},
		{"a commented command string executes nothing",
			"# bash scripts/hub-leg.sh lint:overlay-live-facts scripts/check-overlay-live-facts.sh", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmds, _, err := lexLine(c.line, 0)
			if err != nil {
				t.Fatalf("lexLine failed: %v", err)
			}
			if len(cmds) == 0 {
				if c.wantScript != "" || len(c.wantDelegates) != 0 {
					t.Fatalf("no command lexed, want script %q delegates %q", c.wantScript, c.wantDelegates)
				}
				return
			}
			script, delegates := commandRuns("lint:under-test", cmds[0])
			if script != c.wantScript || !equal(delegates, c.wantDelegates) {
				t.Fatalf("commandRuns = (%q, %q), want (%q, %q)", script, delegates, c.wantScript, c.wantDelegates)
			}
		})
	}
}

// The wrapper's second argument is a sentence. A task name written inside it is data and
// must not become an edge of the call graph.
func TestQuotedReasonIsNotATaskEdge(t *testing.T) {
	line := `bash scripts/hub-only-gate.sh lint:addon-sets-gate "runs lint:addon-sets-gate:legs" lint:overlay-live-facts`
	cmds, _, err := lexLine(line, 0)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	_, delegates := commandRuns("lint:addon-sets-gate", cmds[0])
	if !equal(delegates, []string{"lint:overlay-live-facts"}) {
		t.Fatalf("delegates = %q, want the unquoted argument lint:overlay-live-facts", delegates)
	}
}

// The split gate's subject is a sentence too, and it is the argument a judged task name is
// most likely to appear in. Neither it nor the leg label may become an edge: the branches
// are the third and fourth arguments, by POSITION, and nothing else.
func TestSplitGateReasonAndLabelAreNotTaskEdges(t *testing.T) {
	line := `bash scripts/edition-split-gate.sh lint:addon-sets "also runs lint:overlay-live-facts and scripts/check-overlay-live-facts.sh" lint:addon-sets:public lint:addon-sets:legs`
	cmds, _, err := lexLine(line, 0)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	script, delegates := commandRuns("lint:addon-sets", cmds[0])
	if script != "" {
		t.Fatalf("script = %q, want none: the dispatcher runs tasks, not a script argument", script)
	}
	if !equal(delegates, []string{"lint:addon-sets:public", "lint:addon-sets:legs"}) {
		t.Fatalf("delegates = %q, want exactly the two positional branches", delegates)
	}
}

func TestIsAssignment(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"FOO=bar", true},
		{"F1=bar", true},
		{`GOFLAGS="-p=1"`, true},
		{`"FOO=bar"`, false},
		{"1FOO=bar", false},
		{"=bar", false},
		{"foo", false},
		{"scripts/x.sh", false},
	}
	for _, c := range cases {
		ws := words(t, c.line)
		if got := isAssignment(ws[0]); got != c.want {
			t.Fatalf("isAssignment(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestOpaqueWords(t *testing.T) {
	for _, line := range []string{"task ${leg}", "bash $SCRIPT", "bash `echo x`"} {
		ws := words(t, line)
		if !opaque(ws[1]) {
			t.Fatalf("%q: word %q not classified as opaque", line, ws[1].text)
		}
	}
	if opaque(words(t, "bash scripts/x.sh")[1]) {
		t.Fatal("a literal script path was classified as opaque")
	}
}

// ── the closed grammar the correction added ──────────────────────────────────────────

// A substitution runs wherever it is written, including inside double quotes; single
// quotes suppress it and an escape makes it literal.
func TestSubstitutionIsTrackedOutsideSingleQuotes(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{`X="$(scripts/check-overlay-live-facts.sh)"`, true},
		{"X=`scripts/check-overlay-live-facts.sh`", true},
		{`X='$(scripts/check-overlay-live-facts.sh)'`, false},
		{`X="\$(scripts/check-overlay-live-facts.sh)"`, false},
		{`X="${LEG}"`, false},
	}
	for _, c := range cases {
		ws := words(t, c.line)
		if got := ws[0].subst; got != c.want {
			t.Fatalf("subst(%s) = %v, want %v (text %q)", c.line, got, c.want, ws[0].text)
		}
	}
}

// An unquoted $( … ) belongs to the word it is written in, with its parentheses balanced,
// so the executable text stays inspectable instead of being split away or discarded.
func TestUnquotedSubstitutionStaysInsideItsWord(t *testing.T) {
	ws := words(t, `CAPTURE=$(scripts/check-overlay-live-facts.sh) echo ok`)
	if len(ws) != 3 {
		t.Fatalf("got %q, want three words", texts(ws))
	}
	if ws[0].text != "CAPTURE=$(scripts/check-overlay-live-facts.sh)" || !ws[0].subst {
		t.Fatalf("assignment word = %q subst=%v", ws[0].text, ws[0].subst)
	}
	if !isAssignment(ws[0]) {
		t.Fatal("the assignment prefix was not recognized as one")
	}
}

// Balanced nesting: an inner ) does not end the substitution early.
func TestNestedSubstitutionIsBalanced(t *testing.T) {
	ws := words(t, `X=$(a $(b) c)d`)
	if len(ws) != 1 || ws[0].text != "X=$(a $(b) c)d" {
		t.Fatalf("got %q, want one balanced word", texts(ws))
	}
	if !ws[0].subst {
		t.Fatal("a nested substitution was not marked as running something")
	}
}

// A redirection target is kept, because `>$(reader)` runs the reader before it opens
// anything; an ordinary descriptor redirection must not become an argument.
func TestRedirectionTargetsArePreserved(t *testing.T) {
	cmds, _, err := lexLine(`bash scripts/x.sh >/dev/null 2>&1`, 1)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	if len(cmds) != 1 {
		t.Fatalf("got %d command(s), want 1", len(cmds))
	}
	if got := texts(cmds[0].words); !equal(got, []string{"bash", "scripts/x.sh"}) {
		t.Fatalf("words = %q, want the command without its redirections", got)
	}
	if got := texts(cmds[0].redirects); !equal(got, []string{"/dev/null", "1"}) {
		t.Fatalf("redirects = %q, want the two targets", got)
	}
	for _, w := range cmds[0].redirects {
		if w.subst {
			t.Fatalf("plain target %q was marked as running something", w.text)
		}
	}

	cmds, _, err = lexLine(`echo ok >$(scripts/check-overlay-live-facts.sh)`, 1)
	if err != nil {
		t.Fatalf("lexLine failed: %v", err)
	}
	if len(cmds) != 1 || len(cmds[0].redirects) != 1 {
		t.Fatalf("got %d command(s) with %d redirect(s)", len(cmds), len(cmds[0].redirects))
	}
	target := cmds[0].redirects[0]
	if target.text != "$(scripts/check-overlay-live-facts.sh)" || !target.subst {
		t.Fatalf("target = %q subst=%v", target.text, target.subst)
	}
}

// A YAML literal scalar is a program: a comment ends at its own newline, so the next line
// is a second command and not part of that comment.
func TestLexProgramKeepsPhysicalLineBoundaries(t *testing.T) {
	cmds := lexProgram("lint:under-test",
		"echo ok # harmless\nbash scripts/check-overlay-live-facts.sh\n")
	if len(cmds) != 2 {
		t.Fatalf("got %d command(s), want 2", len(cmds))
	}
	script, _ := commandRuns("lint:under-test", cmds[1])
	if script != "scripts/check-overlay-live-facts.sh" {
		t.Fatalf("second line resolved to %q", script)
	}
}

// A backslash continuation still joins two physical lines into one command.
func TestLexProgramJoinsContinuations(t *testing.T) {
	cmds := lexProgram("lint:under-test", "bash \\\n  scripts/x.sh\n")
	if len(cmds) != 1 {
		t.Fatalf("got %d command(s), want 1", len(cmds))
	}
	if script, _ := commandRuns("lint:under-test", cmds[0]); script != "scripts/x.sh" {
		t.Fatalf("continued command resolved to %q", script)
	}
}

// unexplained returns exactly the words an admitted form does not account for, prefix
// included: that prefix is where a stripped assignment would otherwise disappear.
func TestUnexplainedKeepsTheStrippedPrefix(t *testing.T) {
	ws := words(t, `CAPTURE=x bash scripts/hub-leg.sh lint:a scripts/b.sh extra`)
	rest := unexplained(ws, ws[1:], 4)
	if got := texts(rest); !equal(got, []string{"CAPTURE=x", "extra"}) {
		t.Fatalf("unexplained = %q, want the prefix and the trailing argument", got)
	}
}

func TestIsDataFollowsSubstitutionNotText(t *testing.T) {
	quoted := words(t, `echo '$(scripts/check-overlay-live-facts.sh)'`)[1]
	if !quoted.isData() {
		t.Fatal("single-quoted text carrying a substitution shape was not classified as data")
	}
	live := words(t, `echo "$(scripts/check-overlay-live-facts.sh)"`)[1]
	if live.isData() {
		t.Fatal("a double-quoted command substitution was classified as data")
	}
}

func taskBody(t *testing.T, body string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	if len(doc.Content) == 0 {
		t.Fatal("fixture is empty")
	}
	return doc.Content[0]
}

// A reached definition is admitted field by field, with its YAML kind. Fields that decide
// whether or how a command runs are refused rather than ignored.
func TestParseTaskAdmitsOnlyModelledFields(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		unmodelled bool
	}{
		{"the shape this repository writes", "desc: a leg\ncmds:\n  - bash scripts/x.sh\n", false},
		{"declared delegation", "cmds:\n  - task: lint:other\n", false},
		{"deps", "deps:\n  - lint:other\n", false},
		{"status can suppress the commands", "status:\n  - test -f x\ncmds:\n  - bash scripts/x.sh\n", true},
		{"dir changes where they run", "dir: /tmp\ncmds:\n  - bash scripts/x.sh\n", true},
		{"vars feed expansion", "vars:\n  A: b\ncmds:\n  - bash scripts/x.sh\n", true},
		{"preconditions gate the task", "preconditions:\n  - sh: test -f x\ncmds:\n  - bash scripts/x.sh\n", true},
		{"cmds must be a sequence", "cmds: bash scripts/x.sh\n", true},
		{"desc must be a scalar", "desc:\n  - a\ncmds:\n  - bash scripts/x.sh\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			def := parseTask(taskBody(t, c.body))
			if got := len(def.unmodelled) > 0; got != c.unmodelled {
				t.Fatalf("unmodelled = %v (%v), want %v", got, def.unmodelled, c.unmodelled)
			}
		})
	}
}

// A recognized cmd:/task: does not admit the mapping on its own.
func TestCommandMapRejectsExecutionAttributes(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		unmodelled bool
	}{
		{"cmd alone", "cmds:\n  - cmd: bash scripts/x.sh\n", false},
		{"cmd with platforms", "cmds:\n  - cmd: bash scripts/x.sh\n    platforms: [linux]\n", true},
		{"cmd with ignore_error", "cmds:\n  - cmd: bash scripts/x.sh\n    ignore_error: true\n", true},
		{"task with vars", "cmds:\n  - task: lint:other\n    vars: {A: b}\n", true},
		{"dep with vars", "deps:\n  - task: lint:other\n    vars: {A: b}\n", true},
		{"neither key", "cmds:\n  - defer: bash scripts/x.sh\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			def := parseTask(taskBody(t, c.body))
			if got := len(def.unmodelled) > 0; got != c.unmodelled {
				t.Fatalf("unmodelled = %v (%v), want %v", got, def.unmodelled, c.unmodelled)
			}
		})
	}
}

// The heads whose arguments are printed are the only unmodelled commands allowed to name a
// judged program. Everything else reaches the refusal path, which is exercised end to end
// by scripts/test-overlay-gate-wiring.sh because it exits the process.
func TestNonExecutingHeadsNameAJudgedProgramWithoutRunningIt(t *testing.T) {
	for _, line := range []string{
		"echo scripts/test-overlay-live-facts.sh",
		"printf %s scripts/check-overlay-live-facts.sh",
	} {
		cmds, _, err := lexLine(line, 0)
		if err != nil {
			t.Fatalf("lexLine(%q) failed: %v", line, err)
		}
		script, delegates := commandRuns("lint:under-test", cmds[0])
		if script != "" || len(delegates) != 0 {
			t.Fatalf("commandRuns(%q) = (%q, %q), want no edge", line, script, delegates)
		}
	}
	if !nonExecutingHeads["echo"] || nonExecutingHeads["env"] || nonExecutingHeads["command"] {
		t.Fatal("the non-executing head set does not hold the boundary it is for")
	}
}
