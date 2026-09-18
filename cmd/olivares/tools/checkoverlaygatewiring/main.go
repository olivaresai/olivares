// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command checkoverlaygatewiring judges where the live overlay reader is called, and where
// it is not.
//
// Contract: 0 wiring intact · 1 finding · 2 could not look.
//
// The judged placement: one `task lint:overlay-live-facts` in .githooks/pre-push
// immediately after the second same-act `task lint:overlay-seal` and before the two
// Community add-on tasks, with no live read reachable from lint:addon-sets. The reader
// requires a freshly sealed private origin/main, which exists only at the paired dev
// boundary; a Community-only lane can answer nothing but 2 there.
//
// The grammar is closed. Both judged files name these tasks and scripts in prose, banners
// and quoted arguments, so a judge that reads a mention as a call reports CLEAN on a hook
// that calls nothing. Execution-bearing syntax is examined before it is discarded: a
// command substitution stays inside the word it is written in, with balanced parentheses;
// a redirection target is kept rather than skipped; a stripped assignment prefix is still
// scanned; and a command string is lexed line by line, so a comment ends at its own
// newline instead of swallowing the next command. Only these forms are admitted:
//
//	hook      [VAR=value ...] task <name>
//	Taskfile  bash <script> [args ...]
//	          bash scripts/hub-leg.sh <task> <script> [args ...]
//	          bash scripts/hub-only-gate.sh <leg> <subject> <task> [args ...]
//	          bash scripts/edition-split-gate.sh <leg> <subject> <public-task> <private-task>
//	          declared deps: / cmds: - task: <task>   (the only delegation followed)
//
// A reached task admits only the fields in admittedTaskFields, with their YAML kinds.
// Anything that decides whether or how a command runs — status, preconditions, platforms,
// dir, env, vars — is refused rather than ignored. A judged program named outside the
// position an admitted form explains, or inside a command substitution, is refused too: an
// unmodelled form is not evidence that the program does not run. Single-quoted text and
// comments are data.
//
// This is a bounded source judge. It does not execute the shell, model arbitrary Bash or
// prove a script's body. The complete hook replay in scripts/test-prepush-refclass.sh is
// the separate execution control for the hook's actual branches.
//
// It lives in cmd/olivares because that module already depends on gopkg.in/yaml.v3, as
// checkciports and checkcienvreach do; no new dependency is introduced and nothing here is
// imported by the CLI. Its bench is scripts/test-overlay-gate-wiring.sh, which builds this
// tool once and runs it over disposable mutants of the two real files.
package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	rcOK      = 0
	rcFinding = 1
	rcBlind   = 2
)

// The judged subjects. Names, not patterns: a word equals one of these or it does not.
const (
	liveTask    = "lint:overlay-live-facts"
	sealTask    = "lint:overlay-seal"
	aggregate   = "lint:addon-sets"
	battery     = "lint:addon-sets-gate"
	liveScript  = "scripts/check-overlay-live-facts.sh"
	readerBench = "scripts/test-overlay-live-facts.sh"
	legWrapper  = "scripts/hub-leg.sh"
	gateWrapper = "scripts/hub-only-gate.sh"
	splitGate   = "scripts/edition-split-gate.sh"
)

// A task name as this repository writes them, anchored: an unanchored class would accept
// a shell expansion and let the judge describe a call it cannot resolve.
var plainTask = regexp.MustCompile(`^[a-z0-9][a-z0-9:_.\-]*$`)

// The names whose execution this judgment turns on. A command that names one of these in a
// form outside the admitted grammar is refused: absence of a modelled edge is not evidence
// that the program does not run.
var judgedNames = []string{liveTask, sealTask, aggregate, battery, liveScript, readerBench}

// Heads whose arguments are printed, not executed. They are the only unmodelled commands
// allowed to name a judged program, and only in words that carry no substitution.
var nonExecutingHeads = map[string]bool{
	"echo": true, "printf": true, ":": true, "true": true, "false": true,
}

// The Taskfile fields this judge models, with the YAML kind each must have. Everything else
// in a reached task definition is refused: `status`, `preconditions`, `platforms`, `dir`,
// `env`, `vars`, `sources` and their kin decide whether or how a command runs, and a judge
// that ignores them reports a read that may never happen.
var admittedTaskFields = map[string]yaml.Kind{
	"cmds":    yaml.SequenceNode,
	"deps":    yaml.SequenceNode,
	"desc":    yaml.ScalarNode,
	"summary": yaml.ScalarNode,
}

// Keys admitted inside a cmds:/deps: mapping entry. A mapping that also carries an
// execution attribute is not accepted merely because a recognized key is present.
var admittedCommandKeys = map[string]bool{"cmd": true, "task": true}
var admittedDepKeys = map[string]bool{"task": true}

// The only non-name argument the hook is allowed to hand `task`. It is the readability
// probe run before any leg, not a leg.
var supportedTaskFlags = map[string]bool{"--list-all": true}

// Words that may precede a command in the hook without changing which program runs.
var leadingKeywords = map[string]bool{
	"!": true, "if": true, "elif": true, "while": true, "until": true,
	"then": true, "do": true, "else": true, "{": true, "time": true,
}

func blind(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "checkoverlaygatewiring: COULD NOT LOOK: "+format+"\n", a...)
	os.Exit(rcBlind)
}

func main() {
	if len(os.Args) != 3 {
		blind("usage: checkoverlaygatewiring <hook-path> <taskfile-path>. Both paths are " +
			"explicit so the bench judges its fixtures with the same program as the real tree.")
	}
	hookPath, taskfilePath := os.Args[1], os.Args[2]

	hookRaw, err := os.ReadFile(hookPath)
	if err != nil {
		blind("cannot read the hook at %s (%v).", hookPath, err)
	}
	taskfileRaw, err := os.ReadFile(taskfilePath)
	if err != nil {
		blind("cannot read the Taskfile at %s (%v).", taskfilePath, err)
	}

	calls := hookCalls(string(hookRaw))
	tasks := loadTasks(taskfileRaw, taskfilePath)
	for _, required := range []string{liveTask, sealTask, aggregate, battery} {
		if _, ok := tasks[required]; !ok {
			blind("the Taskfile declares no task %s: the subject of this judgment is absent.", required)
		}
	}

	var findings []string

	// 1 · the dedicated task really runs the live reader. Naming a leg that reads nothing
	//     is worse than not naming it: observation would report a read that never happened.
	if !reach(tasks, liveTask).runs(liveScript) {
		findings = append(findings, fmt.Sprintf(
			"task %s does not run %s: the hook would name a leg that reads nothing.",
			liveTask, liveScript))
	}

	// 2 · exactly one dedicated live invocation, so one act performs one live read.
	liveAt := indexesOf(calls, liveTask)
	sealAt := indexesOf(calls, sealTask)
	switch {
	case len(liveAt) == 0:
		findings = append(findings, fmt.Sprintf(
			"the hook never calls `task %s`: the live overlay facts have no executable caller.",
			liveTask))
	case len(liveAt) > 1:
		findings = append(findings, fmt.Sprintf(
			"the hook calls `task %s` %d times (hook lines %s): one act, one dedicated read.",
			liveTask, len(liveAt), linesOf(calls, liveAt)))
	}

	if len(liveAt) == 1 {
		// 3 · it follows the SECOND same-act seal, immediately. The first seal is refreshed
		//     before the add-on lane because the preceding lints can outlive the reader's
		//     freshness ceiling; a read placed before that refresh judges the older evidence
		//     the refresh exists to replace.
		switch {
		case len(sealAt) < 2:
			findings = append(findings, fmt.Sprintf(
				"the hook makes %d `task %s` call(s): the dedicated live read has no second "+
					"same-act seal to follow.", len(sealAt), sealTask))
		case liveAt[0] != sealAt[1]+1:
			after := "nothing"
			if sealAt[1]+1 < len(calls) {
				after = calls[sealAt[1]+1].name
			}
			findings = append(findings, fmt.Sprintf(
				"`task %s` does not immediately follow the second `task %s` (hook line %d, "+
					"followed by %s instead).",
				liveTask, sealTask, calls[sealAt[1]].line, after))
		}

		// 4 · it precedes both Community add-on tasks, which the seal and the read protect.
		for _, community := range []string{aggregate, battery} {
			at := indexesOf(calls, community)
			switch {
			case len(at) == 0:
				findings = append(findings, fmt.Sprintf("the hook never calls `task %s`.", community))
			case liveAt[0] > at[0]:
				findings = append(findings, fmt.Sprintf(
					"`task %s` runs AFTER `task %s` (hook line %d): the add-on lane would derive "+
						"before the live read.", liveTask, community, calls[at[0]].line))
			}
		}
	}

	// 5 · the Community derivation aggregate performs no live read, directly or through a
	//     declared delegation. This is the path Community-only CI runs with no private
	//     source, where the reader can only answer COULD NOT LOOK.
	if where := reach(tasks, aggregate).who(liveScript); len(where) > 0 {
		findings = append(findings, fmt.Sprintf(
			"task %s reaches the live reader %s again (via %s): Community-only CI has no "+
				"private source or seal there.", aggregate, liveScript, strings.Join(where, ", ")))
	}

	// 6 · the hermetic reader battery stays wired. Moving the live read out of the Community
	//     lane must not quietly take the controls that prove the reader itself with it.
	if !reach(tasks, battery).runs(readerBench) {
		findings = append(findings, fmt.Sprintf(
			"task %s no longer reaches %s: the hermetic live-reader battery lost its caller.",
			battery, readerBench))
	}

	if len(findings) > 0 {
		fmt.Println("checkoverlaygatewiring: FINDING")
		for _, f := range findings {
			fmt.Printf("  - %s\n", f)
		}
		os.Exit(rcFinding)
	}
	fmt.Printf("checkoverlaygatewiring: clean — %d hook task invocation(s); one dedicated live "+
		"read, after the second seal, before the add-on lane.\n", len(calls))
	os.Exit(rcOK)
}

// ── the hook: an ordered inventory of the task invocations it writes ──────────────────

type invocation struct {
	line int
	name string
}

func indexesOf(calls []invocation, name string) []int {
	var out []int
	for i, c := range calls {
		if c.name == name {
			out = append(out, i)
		}
	}
	return out
}

func linesOf(calls []invocation, idx []int) string {
	parts := make([]string, 0, len(idx))
	for _, i := range idx {
		parts = append(parts, fmt.Sprint(calls[i].line))
	}
	return strings.Join(parts, ", ")
}

// hookCalls returns the task invocations the hook makes, in file order. It refuses any
// form it cannot resolve rather than describing a placement it did not read.
func hookCalls(text string) []invocation {
	var calls []invocation
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		logical, consumed := joinContinuations(lines, i)
		lineNo := i + 1
		i += consumed

		cmds, heredocs, err := lexLine(logical, lineNo)
		if err != nil {
			blind("%v", err)
		}
		for _, cmd := range cmds {
			name, stripped, accounted, ok := taskInvocation(cmd, lineNo)
			if ok {
				calls = append(calls, invocation{line: lineNo, name: name})
			}
			head := ""
			if len(stripped) > 0 {
				head = stripped[0].text
			}
			if !ok && nonExecutingHeads[head] {
				accounted = len(stripped)
			}
			hookMentions(lineNo, head, cmd, stripped, accounted)
		}
		// A heredoc body is data to this judge, and data must not supply a call.
		for _, delim := range heredocs {
			body, end := heredocBody(lines, i+1, delim)
			for _, judged := range []string{liveTask, aggregate, battery} {
				if strings.Contains(body, judged) {
					blind("hook line %d opens a heredoc (%s) whose body names %s: this judge "+
						"reads commands, not embedded documents.", lineNo, delim, judged)
				}
			}
			i = end
		}
	}
	if len(calls) == 0 {
		blind("no `task` invocation was recognized in the hook. Its shape changed, and an " +
			"extractor that matches nothing must not report a wiring it cannot see.")
	}
	return calls
}

// taskInvocation reports the task a command runs, when the command is one of the two
// accepted hook forms: `task <name>` and `VAR=value ... task <name>`. It also returns the
// command with its keyword and assignment prefix removed, and how many of those remaining
// words the accepted form explains, so the caller can judge the rest.
func taskInvocation(cmd command, lineNo int) (name string, stripped []word, accounted int, ok bool) {
	words := cmd.words
	for len(words) > 0 && leadingKeywords[words[0].text] && !words[0].quoted {
		words = words[1:]
	}
	for len(words) > 0 && isAssignment(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 || words[0].text != "task" {
		return "", words, 0, false
	}
	if len(words) == 1 {
		blind("hook line %d runs `task` with no argument.", lineNo)
	}
	arg := words[1]
	if strings.HasPrefix(arg.text, "-") {
		if !supportedTaskFlags[arg.text] {
			blind("hook line %d runs `task %s`: an option this judge does not support.", lineNo, arg.text)
		}
		return "", words, 2, false
	}
	if !plainTask.MatchString(arg.text) {
		blind("hook line %d runs `task %s`: not a resolvable task name.", lineNo, arg.text)
	}
	return arg.text, words, 2, true
}

// hookMentions is relevantWords for the hook: a judged program named inside a command
// substitution — in an argument, in a stripped assignment prefix or in a redirection
// target — or in a command form this judge does not model, is refused rather than counted
// as absent. echo/printf and their kin print their arguments, so they may name one.
func hookMentions(lineNo int, head string, cmd command, stripped []word, accounted int) {
	running := make([]word, 0, len(cmd.words)+len(cmd.redirects))
	running = append(running, cmd.words...)
	running = append(running, cmd.redirects...)
	for _, w := range running {
		if !w.subst {
			continue
		}
		for _, judged := range judgedNames {
			if strings.Contains(w.text, judged) {
				blind("hook line %d names %s inside a command substitution (%q): a "+
					"substitution runs wherever it is written, and this judge does not execute it.",
					lineNo, judged, w.text)
			}
		}
	}
	for _, w := range unexplained(cmd.words, stripped, accounted) {
		for _, judged := range judgedNames {
			if strings.Contains(w.text, judged) {
				blind("hook line %d names %s in `%s`, a command form this judge does not "+
					"model. Refusing rather than reporting a placement it did not read.",
					lineNo, judged, headOrEmpty(head))
			}
		}
	}
}

func isAssignment(w word) bool {
	if !w.bare {
		return false
	}
	eq := strings.Index(w.text, "=")
	if eq <= 0 {
		return false
	}
	for i, r := range w.text[:eq] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// joinContinuations returns the logical line starting at i and how many extra physical
// lines it swallowed, so a command split with a trailing backslash is lexed whole.
func joinContinuations(lines []string, i int) (string, int) {
	logical := lines[i]
	extra := 0
	for endsWithContinuation(logical) && i+extra+1 < len(lines) {
		extra++
		logical = strings.TrimSuffix(logical, "\\") + " " + lines[i+extra]
	}
	return logical, extra
}

func endsWithContinuation(s string) bool {
	if !strings.HasSuffix(s, "\\") {
		return false
	}
	n := 0
	for j := len(s) - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// heredocBody returns the body text between `start` and the closing delimiter, plus the
// index of the delimiter line. An unterminated heredoc consumes the rest of the file,
// which is the fail-closed reading.
func heredocBody(lines []string, start int, delim string) (string, int) {
	var body []string
	for j := start; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == delim {
			return strings.Join(body, "\n"), j
		}
		body = append(body, lines[j])
	}
	return strings.Join(body, "\n"), len(lines) - 1
}

// ── the Taskfile: declared commands and the delegation actually written ───────────────

type taskDef struct {
	cmds       []string
	delegates  []string
	unmodelled []string
}

func loadTasks(raw []byte, path string) map[string]*taskDef {
	var doc struct {
		Tasks map[string]yaml.Node `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		blind("the Taskfile at %s does not parse (%v).", path, err)
	}
	if len(doc.Tasks) == 0 {
		blind("the Taskfile at %s declares no tasks.", path)
	}
	out := make(map[string]*taskDef, len(doc.Tasks))
	for name := range doc.Tasks {
		node := doc.Tasks[name]
		out[name] = parseTask(&node)
	}
	return out
}

func parseTask(node *yaml.Node) *taskDef {
	def := &taskDef{}
	if node.Kind != yaml.MappingNode {
		def.unmodelled = append(def.unmodelled, "its body is not a mapping")
		return def
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		kind, admitted := admittedTaskFields[key]
		if !admitted {
			def.unmodelled = append(def.unmodelled,
				"it declares "+key+":, a field this judge does not model")
			continue
		}
		if value.Kind != kind {
			def.unmodelled = append(def.unmodelled,
				"its "+key+": is not the YAML type this judge models")
			continue
		}
		switch key {
		case "deps":
			def.readSequence(value, "deps", false)
		case "cmds":
			def.readSequence(value, "cmds", true)
		}
	}
	return def
}

// readSequence models the two shapes this repository writes: a scalar (a command string
// under cmds:, a task name under deps:) and a mapping whose ONLY keys are the recognized
// ones. A mapping that also carries an execution attribute is unmodelled even though a
// recognized key is present, because that attribute decides whether the command runs.
func (d *taskDef) readSequence(node *yaml.Node, key string, commands bool) {
	admitted := admittedDepKeys
	if commands {
		admitted = admittedCommandKeys
	}
	for _, item := range node.Content {
		switch item.Kind {
		case yaml.ScalarNode:
			if commands {
				d.cmds = append(d.cmds, item.Value)
			} else {
				d.delegates = append(d.delegates, item.Value)
			}
		case yaml.MappingNode:
			recognized := 0
			for i := 0; i+1 < len(item.Content); i += 2 {
				k, v := item.Content[i].Value, item.Content[i+1]
				if !admitted[k] {
					d.unmodelled = append(d.unmodelled,
						"a "+key+" entry declares "+k+":, an attribute this judge does not model")
					continue
				}
				if v.Kind != yaml.ScalarNode {
					d.unmodelled = append(d.unmodelled,
						"a "+key+" entry declares a non-scalar "+k+":")
					continue
				}
				switch k {
				case "task":
					d.delegates = append(d.delegates, v.Value)
					recognized++
				case "cmd":
					d.cmds = append(d.cmds, v.Value)
					recognized++
				}
			}
			if recognized == 0 {
				d.unmodelled = append(d.unmodelled,
					"a "+key+" entry declares none of the recognized keys")
			}
		default:
			d.unmodelled = append(d.unmodelled,
				"a "+key+" entry is neither a scalar nor a mapping")
		}
	}
}

// lexProgram lexes a Taskfile command string as the shell program it is: line by line,
// joining only real backslash continuations, so a comment ends at its own newline. A
// heredoc is a form this judge does not model and is refused rather than flattened.
func lexProgram(owner, raw string) []command {
	var out []command
	lines := strings.Split(raw, "\n")
	for i := 0; i < len(lines); i++ {
		logical, consumed := joinContinuations(lines, i)
		i += consumed
		cmds, heredocs, err := lexLine(logical, i+1)
		if err != nil {
			blind("task %s declares a command this judge cannot lex: %v", owner, err)
		}
		if len(heredocs) > 0 {
			blind("task %s declares a command with a heredoc, a form this judge does not model.", owner)
		}
		out = append(out, cmds...)
	}
	return out
}

// reachSet is what a task actually executes: the scripts, and which task ran each one.
type reachSet struct {
	scripts map[string][]string
}

func (r reachSet) runs(script string) bool { return len(r.scripts[script]) > 0 }
func (r reachSet) who(script string) []string {
	return r.scripts[script]
}

// reach walks a task and the tasks it DECLARES it delegates to — deps:, cmds: - task:,
// and the explicit hub-only-gate.sh / edition-split-gate.sh wrapper arguments (both
// branches of the split gate). It never follows a word merely
// because it happens to equal a task name: that is how an unrelated caller of the same
// script gets reported as this wiring.
func reach(tasks map[string]*taskDef, root string) reachSet {
	out := reachSet{scripts: map[string][]string{}}
	seen := map[string]bool{}
	frontier := []string{root}
	for len(frontier) > 0 {
		name := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		if seen[name] {
			continue
		}
		seen[name] = true

		def, ok := tasks[name]
		if !ok {
			blind("walking %s reaches task %s, which the Taskfile does not declare: an "+
				"unresolvable edge must not pass for an absent one.", root, name)
		}
		if len(def.unmodelled) > 0 {
			blind("task %s is not fully modelled by this judge (%s).", name, strings.Join(def.unmodelled, "; "))
		}
		frontier = append(frontier, def.delegates...)

		for _, raw := range def.cmds {
			// A command string is a shell PROGRAM, and a YAML literal scalar keeps its
			// newlines. Flattening them would put the next physical line inside the previous
			// line's comment, so each line is lexed on its own and only a backslash
			// continuation joins two.
			for _, cmd := range lexProgram(name, raw) {
				script, delegates := commandRuns(name, cmd)
				if script != "" {
					out.scripts[script] = append(out.scripts[script], name)
				}
				// A wrapper may name both sides of a run-time branch; every named task is
				// walked, so the reachable set is a superset of any one tree's execution.
				frontier = append(frontier, delegates...)
			}
		}
	}
	return out
}

// commandRuns resolves ONE Taskfile command to the script it executes and/or the tasks it
// delegates to. Only the wrapper forms this repository writes are accepted. A command that
// names a judged program in any other executable position is refused with 2: an unmodelled
// form is not evidence that the program does not run.
//
// A wrapper may delegate to MORE THAN ONE task. edition-split-gate.sh picks one of two at
// run time from a classification this judge does not perform, so both are returned and both
// are walked. That is deliberately a SUPERSET of what any single tree executes: the judged
// invariant is that no live read is reachable from lint:addon-sets AT ALL, because that lane
// also runs in a Community tree. Walking one branch and calling the other unreachable would
// redefine the invariant into "not reachable in the tree I guessed".
func commandRuns(owner string, cmd command) (script string, delegates []string) {
	words := cmd.words
	for len(words) > 0 && leadingKeywords[words[0].text] && !words[0].quoted {
		words = words[1:]
	}
	for len(words) > 0 && isAssignment(words[0]) {
		words = words[1:]
	}

	// accounted is how many leading words of `words` the admitted form explains. The
	// stripped prefix and everything past `accounted` are scanned below, because a judged
	// program named there may still be executed.
	head, accounted := "", 0
	if len(words) > 0 {
		head = words[0].text
		switch {
		case head == "task":
			blind("task %s delegates through a bare `task` command, a form this judge does not "+
				"model. Declared deps: or cmds: - task: are the delegation it reads.", owner)

		case head == "bash" || head == "sh":
			if len(words) < 2 {
				blind("task %s runs `%s` with no script argument.", owner, head)
			}
			target := words[1]
			if strings.HasPrefix(target.text, "-") || opaque(target) {
				blind("task %s runs `%s %s`: this judge cannot resolve which program that executes.",
					owner, head, target.text)
			}
			switch target.text {
			case legWrapper:
				if len(words) < 4 {
					blind("task %s invokes %s with %d argument(s); its contract is "+
						"<task> <script> [args ...].", owner, legWrapper, len(words)-2)
				}
				if opaque(words[3]) {
					blind("task %s invokes %s with an unresolvable script argument %q.",
						owner, legWrapper, words[3].text)
				}
				script, accounted = words[3].text, 4
			case gateWrapper:
				// hub-only-gate.sh <leg-name> <what-it-checks> <task> [args ...]: the subject is
				// a quoted sentence and the delegated task is the argument AFTER it. Reading the
				// sentence would let quoted prose supply a task edge.
				if len(words) < 5 {
					blind("task %s invokes %s with %d argument(s); its contract is "+
						"<leg-name> <what-it-checks> <task> [args ...].", owner, gateWrapper, len(words)-2)
				}
				if !plainTask.MatchString(words[4].text) {
					blind("task %s invokes %s delegating to %q, which is not a resolvable task name.",
						owner, gateWrapper, words[4].text)
				}
				delegates, accounted = []string{words[4].text}, 5
			case splitGate:
				// edition-split-gate.sh <leg-name> <what-is-not-checked> <public-task>
				// <private-task>: the script itself refuses any other count (`$# -eq 4`),
				// so an off-contract call is a form this judge must not guess at. The leg
				// name and the subject sentence are DATA — the script only prints them —
				// but they are accounted for here so a judged name written there is not
				// mistaken for a call; a substitution in any word is still refused above.
				if len(words) != 6 {
					blind("task %s invokes %s with %d argument(s); its contract is exactly "+
						"4: <leg-name> <what-is-not-checked> <public-task> <private-task>.",
						owner, splitGate, len(words)-2)
				}
				for _, at := range []int{4, 5} {
					if !plainTask.MatchString(words[at].text) {
						blind("task %s invokes %s delegating to %q, which is not a resolvable "+
							"task name.", owner, splitGate, words[at].text)
					}
				}
				// BOTH branches, not the one a classifier would pick here: see commandRuns.
				delegates, accounted = []string{words[4].text, words[5].text}, 6
			default:
				script, accounted = target.text, 2
			}

		case strings.HasSuffix(head, ".sh") && !opaque(words[0]):
			script, accounted = head, 1

		case nonExecutingHeads[head]:
			// echo/printf/: print their arguments. They are the only unmodelled commands
			// allowed to name a judged program, and only in words that run nothing.
			accounted = len(words)
		}
	}

	relevantWords(owner, head, cmd, words, accounted)
	return script, delegates
}

// unexplained returns the words an admitted form does not account for: the keyword and
// assignment prefix that was stripped off the front, and everything past the accounted
// position. A prefix is not harmless because it was stripped —
// `CAPTURE="$(reader)" echo ok` is not `echo ok`.
func unexplained(all, stripped []word, accounted int) []word {
	prefix := len(all) - len(stripped)
	out := append([]word{}, all[:prefix]...)
	if accounted < len(stripped) {
		out = append(out, stripped[accounted:]...)
	}
	return out
}

// relevantWords refuses a command that names a judged program outside the position the
// admitted form explains. Two ways a name can still run: a command substitution, which
// executes wherever it is written — in an argument, in a stripped assignment prefix, or in
// a redirection target — and an unmodelled head such as `env bash <script>`, whose
// arguments this judge does not resolve. Single-quoted text and comments are data and
// never trigger; so is a plain pathname used as a redirection target, which denotes a file
// rather than an execution.
func relevantWords(owner, head string, cmd command, stripped []word, accounted int) {
	running := make([]word, 0, len(cmd.words)+len(cmd.redirects))
	running = append(running, cmd.words...)
	running = append(running, cmd.redirects...)
	for _, w := range running {
		if !w.subst {
			continue
		}
		for _, judged := range judgedNames {
			if strings.Contains(w.text, judged) {
				blind("task %s names %s inside a command substitution (%q): a substitution "+
					"runs wherever it is written, and this judge does not execute it.",
					owner, judged, w.text)
			}
		}
	}
	for _, w := range unexplained(cmd.words, stripped, accounted) {
		for _, judged := range judgedNames {
			if strings.Contains(w.text, judged) {
				blind("task %s names %s in `%s`, a command form this judge does not model: "+
					"an unmodelled form is not evidence that the program does not run.",
					owner, judged, headOrEmpty(head))
			}
		}
	}
}

func headOrEmpty(head string) string {
	if head == "" {
		return "a command that is only assignments"
	}
	return head
}

// opaque reports a word this judge cannot resolve statically: it carries an expansion or
// a command substitution, so its text is not the program that will run.
func opaque(w word) bool {
	return strings.Contains(w.text, "$") || strings.Contains(w.text, "`")
}

// ── the lexer ────────────────────────────────────────────────────────────────────────

type word struct {
	text   string
	quoted bool // some part of it came from inside quotes
	bare   bool // some part of it came from outside quotes
	subst  bool // it carries $( … ) or a backquote OUTSIDE single quotes: it runs something
}

// isData reports a word that runs nothing: quoted through and through, with no command
// substitution. Single-quoted text is always data; a double-quoted argument holding
// $( … ) is not, because that substitution executes before the command does.
func (w word) isData() bool {
	return w.quoted && !w.bare && !w.subst
}

type command struct {
	line  int
	words []word
	// Targets of redirections. They are not arguments, but a target carrying a
	// substitution still runs a program, so they are kept for the relevance scan instead
	// of being skipped as bytes.
	redirects []word
}

func (c command) empty() bool { return len(c.words) == 0 && len(c.redirects) == 0 }

// separators end a word wherever they appear unquoted.
const wordBreak = " \t;&|()<>"

// lexLine splits ONE logical line into commands, resolving quoting, substitution nesting,
// inline comments, redirections and heredoc introducers. It returns the heredoc delimiters
// the line opens so the caller can treat their bodies as data.
func lexLine(line string, lineNo int) ([]command, []string, error) {
	var (
		cmds     []command
		heredocs []string
		cur      = command{line: lineNo}
	)
	flushCmd := func() {
		if !cur.empty() {
			cmds = append(cmds, cur)
		}
		cur = command{line: lineNo}
	}

	r := []rune(line)
	i := 0
	for i < len(r) {
		c := r[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '#':
			// A word never starts with an unquoted #, so reaching one here is a comment
			// that runs to the end of the line.
			i = len(r)
		case c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			flushCmd()
			i++
		case c == '<' || c == '>':
			next, delim, err := lexRedirection(&cur, r, i, lineNo)
			if err != nil {
				return nil, nil, err
			}
			if delim != "" {
				heredocs = append(heredocs, delim)
			}
			i = next
		default:
			w, next, err := scanWord(r, i, lineNo)
			if err != nil {
				return nil, nil, err
			}
			cur.words = append(cur.words, w)
			i = next
		}
	}
	flushCmd()
	return cmds, heredocs, nil
}

// lexRedirection consumes one redirection: its file-descriptor digit, its operator and its
// target. The target is KEPT on the command rather than skipped, because
// `echo ok >$(scripts/check-overlay-live-facts.sh)` runs the substitution before it opens
// anything. A heredoc introducer returns its delimiter instead of a target.
func lexRedirection(cur *command, r []rune, i, lineNo int) (int, string, error) {
	if n := len(cur.words); n > 0 && allDigits(cur.words[n-1].text) && !cur.words[n-1].quoted {
		cur.words = cur.words[:n-1] // `2>&1`: the 2 is a descriptor, not an argument
	}
	if r[i] == '<' && i+1 < len(r) && r[i+1] == '<' {
		delim, next, err := heredocDelimiter(r, i, lineNo)
		return next, delim, err
	}
	i++
	for i < len(r) && (r[i] == '>' || r[i] == '&') {
		i++
	}
	for i < len(r) && (r[i] == ' ' || r[i] == '\t') {
		i++
	}
	if i >= len(r) || strings.ContainsRune(";&|()", r[i]) {
		return i, "", nil // a redirection with no target: the shell's problem, not a program
	}
	w, next, err := scanWord(r, i, lineNo)
	if err != nil {
		return i, "", err
	}
	cur.redirects = append(cur.redirects, w)
	return next, "", nil
}

// scanWord reads ONE word: quoting, escapes and command substitutions resolved, stopping
// at the first unquoted separator. A substitution is kept inside the word it belongs to,
// with its parentheses balanced, so its text can be inspected rather than discarded.
func scanWord(r []rune, i, lineNo int) (word, int, error) {
	var (
		b strings.Builder
		w word
	)
scan:
	for i < len(r) {
		c := r[i]
		switch {
		case strings.ContainsRune(wordBreak, c):
			break scan
		case c == '\\':
			if i+1 < len(r) {
				b.WriteRune(r[i+1]) // escaped: literal, never a substitution
				w.bare = true
				i += 2
				continue
			}
			i++
		case c == '\'':
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				j++
			}
			if j >= len(r) {
				return w, i, fmt.Errorf("line %d: unterminated single quote", lineNo)
			}
			b.WriteString(string(r[i+1 : j]))
			w.quoted = true
			i = j + 1
		case c == '"':
			j := i + 1
			for j < len(r) && r[j] != '"' {
				if r[j] == '\\' && j+1 < len(r) {
					b.WriteRune(r[j+1])
					j += 2
					continue
				}
				// $( … ) and ` … ` still run inside double quotes.
				text, next, ok, err := scanSubstitutionAt(r, j, lineNo)
				if err != nil {
					return w, i, err
				}
				if ok {
					b.WriteString(text)
					w.subst = true
					j = next
					continue
				}
				b.WriteRune(r[j])
				j++
			}
			if j >= len(r) {
				return w, i, fmt.Errorf("line %d: unterminated double quote", lineNo)
			}
			w.quoted = true
			i = j + 1
		default:
			text, next, ok, err := scanSubstitutionAt(r, i, lineNo)
			if err != nil {
				return w, i, err
			}
			if ok {
				b.WriteString(text)
				w.bare, w.subst = true, true
				i = next
				continue
			}
			b.WriteRune(c)
			w.bare = true
			i++
		}
	}
	w.text = b.String()
	return w, i, nil
}

// scanSubstitutionAt reads a $( … ) or ` … ` starting at i, if one starts there. The
// parentheses are balanced and quoted spans inside are skipped, so a nested ) does not end
// it early and the whole executable text stays in one word.
func scanSubstitutionAt(r []rune, i, lineNo int) (string, int, bool, error) {
	if r[i] == '`' {
		j := i + 1
		for j < len(r) && r[j] != '`' {
			j++
		}
		if j >= len(r) {
			return "", i, false, fmt.Errorf("line %d: unterminated backquote", lineNo)
		}
		return string(r[i : j+1]), j + 1, true, nil
	}
	if r[i] != '$' || i+1 >= len(r) || r[i+1] != '(' {
		return "", i, false, nil
	}
	start := i
	i += 2
	depth := 1
	for i < len(r) && depth > 0 {
		switch r[i] {
		case '\'':
			j := i + 1
			for j < len(r) && r[j] != '\'' {
				j++
			}
			if j >= len(r) {
				return "", i, false, fmt.Errorf("line %d: unterminated single quote in a substitution", lineNo)
			}
			i = j + 1
			continue
		case '"':
			j := i + 1
			for j < len(r) && r[j] != '"' {
				if r[j] == '\\' && j+1 < len(r) {
					j += 2
					continue
				}
				j++
			}
			if j >= len(r) {
				return "", i, false, fmt.Errorf("line %d: unterminated double quote in a substitution", lineNo)
			}
			i = j + 1
			continue
		case '(':
			depth++
		case ')':
			depth--
		}
		i++
	}
	if depth != 0 {
		return "", i, false, fmt.Errorf("line %d: unterminated command substitution", lineNo)
	}
	return string(r[start:i]), i, true, nil
}

func heredocDelimiter(r []rune, i, lineNo int) (string, int, error) {
	i += 2
	if i < len(r) && r[i] == '-' {
		i++
	}
	for i < len(r) && (r[i] == ' ' || r[i] == '\t') {
		i++
	}
	var d strings.Builder
	for i < len(r) && !strings.ContainsRune(" \t;&|)", r[i]) {
		if r[i] == '\'' || r[i] == '"' {
			i++
			continue
		}
		d.WriteRune(r[i])
		i++
	}
	if d.Len() == 0 {
		return "", i, fmt.Errorf("line %d: a heredoc is opened with no delimiter", lineNo)
	}
	return d.String(), i, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
