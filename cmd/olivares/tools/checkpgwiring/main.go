// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command checkpgwiring enforces one invariant across the -race legs of mainline CI:
//
//	A module tree that needs PostgreSQL is raced BY A JOB THAT PROVIDES PostgreSQL.
//
// IT HAS TWO LAYERS, AND ONLY ONE OF THEM IS LOAD-BEARING (2026-08-02).
//
//  1. THE GUARANTEE, over the Taskfile — and it is REGIME-SCOPED, which the first draft of
//     this comment got wrong. Every target named `test:race*` — its recipe, its `deps`, its
//     `status` and its `preconditions`, and everything those delegate to — routes every `go test`
//     through scripts/with-pg-env.sh EXCEPT where the walk ends at a reviewed exemption. A
//     Taskfile is YAML: that set of targets is enumerable EXACTLY, so any invocation of any race
//     target — by any spelling, from any job, through any wrapper — enters that script.
//
//     TWO THINGS MAKE THAT TRUE RATHER THAN HOPEFUL, and both were bought on 2026-08-04 after
//     the fourteenth contrast measured them missing.
//
//     THE WRAPPER IS A FILE, NOT A NAME. Recognition used to be a substring in any argv word,
//     and it FAILED OPEN: `bash "$WRAPPER_ROOT/scripts/with-pg-env.sh" go test …` erased an
//     unresolvable prefix, left the basename visible, and a COUNTERFEIT wrapper elsewhere in the
//     tree ran while this program exited 0 — a fail-open in the identity check of the safeguard
//     itself. `dir:` did the same with no variable at all. Identity is now the resolved
//     canonical file under the directory the recipe really runs in, and a word this program
//     cannot resolve is never the wrapper.
//
//     EVERY COMMAND IS CLASSIFIED, or the answer is UNVERIFIED. Finding the commands that
//     visibly run `go test` and ignoring the rest is deny-closed over keys and wide open over
//     commands; five valid go-task shapes walked through that gap with an unwrapped test
//     executing. A command now either enters the wrapper — and then its ARGV is walked too, see
//     the next paragraph — or runs `go test`, or delegates through `task`, or sources a file, or
//     is inert, or is a reviewed entry of recipeHelpers. Anything else is UNVERIFIED, by name.
//     See recipeScan.shell.
//
//     AND EVERY HEAD IS CLASSIFIED TOO, which is the same sentence one level down and cost three
//     rounds to learn. Entering the wrapper was read as the end of the question — "nothing it
//     starts can escape the decision" — and it is not: the argv the wrapper execs can throw the
//     decision away, so the argv is walked. That walk then made the SAME mistake the key walk
//     had made twice, by stopping at the first head outside shellRunners and calling the rest
//     that program's own data. shellRunners enumerates what is DANGEROUS, and the sixteenth
//     contrast walked through it with one ordinary executable in front of an `env -u` of all
//     three DSNs: valid go-task, exit 0, tests running inside the wrapper with no posture. The
//     reviewed-helper premise had the same shape and the same hole. So the default is inverted
//     here as well: a head is a known runner, or a reviewed terminal (argvTerminals /
//     inertCommands), or a reviewed script the walk continues through and READS
//     (argvRunnerScripts), or a reviewed head of a helper (helperHeads) — and anything else is
//     UNVERIFIED. What a gate can enumerate is what is PERMITTED; enumerating the dangerous is a
//     blacklist wearing a whitelist's clothes, three times over in this one file.
//
//     What that script then does DEPENDS ON THE REGIME, and saying otherwise was an
//     overstatement the seventh contrast measured: with the DSNs unset and reachability forced
//     false, the default (fail-closed) regime exits 1 and never runs the child, PROMOTION exits
//     1, and LOCAL opt-in exits 0 AND RUNS IT — a real Postgres test then reports SKIP, PASS,
//     exit 0. LOCAL is deliberate: a developer with no server can still run the suite, and the
//     pre-push hook is its one sanctioned caller (.githooks/pre-push).
//
//     So the guarantee reads: in the regimes CI uses, a misconfigured job fails LOUDLY rather
//     than skipping in silence. checkNoJobEnablesLocalRegime is what keeps the premise of that
//     sentence — that CI uses those regimes — a checked fact instead of an assumption.
//
//  2. A NET, over the workflow — enumerated classes, with declared limits. It reports a job
//     that invokes a -race leg without a `postgres` service, checks that service's image is
//     pinned by digest, and checks every Postgres service agrees on one image. It catches the
//     ordinary mistake in a diff, where catching it is cheap. It does NOT promise completeness,
//     and its limits are written out at raceTargetsIn.
//
// WHY THE LAYERS ARE THIS WAY ROUND — measured, not argued. Three rules were written to
// establish the guarantee from the WORKFLOW and adversarial contrast broke all three: the token
// after `task` lost six spellings; any race-prefixed token in a task-invoking shell lost four
// more and invented a false positive; a real shell word scanner — quoting, escapes,
// continuations, $VAR against the env maps, operator boundaries — lost the classes enumerated at
// raceTargetsIn, among them
// shell functions, aliases, arrays, command substitution, printf and eval, xargs, make and npm
// wrappers, a Task alias, an included Taskfile, a matrix through bash -c, a composite action
// nested two deep, a sourced script, an extensionless executable, and two attacks on the depth
// bound itself. Completeness over arbitrary shell is not something a program can promise, and
// each round spent promising it was a round in which this header claimed more than the program
// delivered — the defect class this repository treats as first order, sitting in the very gate
// written to prevent it.
//
// WHY THIS EXISTS. On 2026-07-30 ./modules was measured Postgres-free, so its -race job
// was split out with no service and its task dropped the with-pg-env.sh wrapper. That
// premise was one commit from being false, so a fail-closed sweep was written the same
// day (scripts/test-pg-test-env.sh, task lint:pg-env). On 2026-08-01 unit H landed
// 12 writer-fence enforcement tests on real PostgreSQL under modules/eventing and the
// sweep fired — before those tests could run green while silently skipping, which is the
// whole failure mode. The premise flipped, the wiring flipped with it, and the assertion
// flipped from "./modules is Postgres-free" to the agreement this program checks.
//
// The protected property is NOT "modules has no Postgres". It is: no test skips in
// silence under a green job. That has two consistent worlds — a tree with no Postgres
// raced by a job with no server, and a tree with Postgres raced by a job with one — and
// exactly one broken one, the tree that acquires Postgres while its job does not.
//
// WHY A YAML DECODER AND NOT A GREP. Same argument the sibling tools make, with a sharper
// edge here: a line-oriented scan of mainline-ci.yml cannot tell which JOB a `services:`
// block belongs to, so asked about race-modules it would happily answer with race-rest's
// service — a FALSE GREEN in a fail-closed gate. checkciports's header records the same
// class of defect in text-scanning gates that passed this repository's own tree while
// accepting valid, dangerous YAML.
//
// WHY GO AND NOT PYTHON. The first implementation of this check was embedded Python in
// the battery and it worked, but it made scripts/test-pg-test-env.sh the only script in
// the repository that requires PyYAML — a gate that runs where its interpreter's optional
// dependency happens to be installed. lint:pg-env is a candidate for mainline CI, whose
// self-hosted runners have never been measured for PyYAML, and "an unverified 'cannot' in
// a gate is indistinguishable from a hole" (.githooks/pre-push). Go is present wherever
// any of these gates run, and this module already depends on gopkg.in/yaml.v3.
//
// THREE ANSWERS, NOT TWO. Exit 0 wired, exit 1 NOT wired, exit 2 COULD NOT LOOK — an
// unreadable or undecodable file, a job or task that is absent under the name given, a
// task whose recipe no longer runs `go test`. Exit 2 is never a pass: the caller
// (scripts/test-pg-test-env.sh) fails the row on anything but 0, and has rows that assert
// exit 2 happens where it should.
//
// It lives in the cmd/olivares module beside checkciports/checkcosignpins/checkcosignwiring
// for the same reason they do: that module already has yaml.v3, no new workspace module is
// introduced, and the CLI does not import it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// leg pairs a CI job with the Taskfile target it runs.
//
// THE TABLE NO LONGER DECIDES WHAT GETS CHECKED (2026-08-01). It used to, and the comment
// here claimed that adding a third -race leg without adding it below "is a decision somebody
// has to make in a diff, not an omission nobody sees". That claim was FALSE: nothing coupled
// the workflow to this table. A round-4 sol max contrast proved it by adding a valid YAML job
// `race-undetected` that ran `task test:race-hot:modules` with no Postgres service — this
// program exited 0 and printed the two inventoried rows. An assertion of enforcement that no
// mechanism provides is the defect this repository treats as first class, and it was sitting
// in the header of the gate written to prevent exactly that class.
//
// So the legs are DISCOVERED from the workflow: every job step invoking a `test:race*` target
// is checked, whether or not it appears below. The table now does two smaller and honest jobs
// — it supplies the human-readable `what` for the legs it knows, and it is asserted to still
// be PRESENT among the discovered ones, so a leg that silently disappears is a finding too.
// Coverage derives from the artifact; the table describes it. That is the same shape as the
// probe-coverage test in modules/eventing, and for the same reason.
//
// THE TABLE MUST THEREFORE BE COMPLETE, and on 2026-08-02 it was not. It listed two legs while
// the workflow invoked four, and argued in writing that completing it was unnecessary
// because all four discovered legs are checked and green. The fifth contrast refuted that by
// measurement rather than by argument: deleting the `test:race-hot:root` STEP from the workflow
// left this program at exit 0, because absence-detection only covers what the table declares.
// Every leg the workflow runs is listed below now. The disappearance guard is worth exactly the
// completeness of this list.
//
// The -job/-task flags bypass discovery for one pair, which is how the mutation matrix points
// this program at throwaway fixtures.
type leg struct {
	job  string
	task string
	what string
}

var legs = []leg{
	{job: "race-modules", task: "test:race-hot:modules", what: "./modules"},
	{job: "race-rest", task: "test:race-hot:workspace", what: "the workspace minus cmd/olivares and ./modules"},
	{job: "race-rest", task: "test:race-hot:root", what: "the root module's hot packages"},
	{job: "race-rest", task: "test:race-hot:hot", what: "the manifest's hot packages"},
}

// racePrefix is what makes a step a -race leg for discovery. Matching on the TARGET NAME and
// not on the literal `-race` in the recipe is deliberate: the recipe is in the other file, and
// a leg that stops passing -race while keeping the name is a different bug from the one this
// program guards. What it must never do is miss a leg that exists.
const racePrefix = "test:race"

// discoverLegs returns every (job, task) pair in the workflow whose step invokes a `test:race*`
// target, sorted for stable output. A job whose steps this program cannot read is UNVERIFIED
// rather than empty: an unreadable shape and an absent leg look identical from here, and only
// one of them is safe to report as clean.
func discoverLegs(wf map[string]any, jobs map[string]any, workflowPath, repoRoot string) []leg {
	var found []leg
	wfEnv := mapEnv(wf, nil)
	for jobID, raw := range jobs {
		jobMap, ok := raw.(map[string]any)
		if !ok {
			unverified(fmt.Sprintf("job %q in %s is not a mapping", jobID, workflowPath))
		}
		stepsRaw, ok := jobMap["steps"]
		if !ok {
			// "No steps" is NOT "no legs". A `uses:` job runs a whole other workflow this
			// program cannot read from here, and any other shape is the same fact: I could
			// not look. The doc above promised UNVERIFIED and the code said `continue`,
			// which is the fail-open answer — measured 2026-08-02, a `uses:` job racing an
			// unwired tree left this program at exit 0 while a forced -job/-task lookup on
			// the same fixture exited 1.
			if u, reusable := jobMap["uses"].(string); reusable {
				unverified(fmt.Sprintf("job %q in %s delegates to %s (`uses:`); its -race legs cannot be read from here", jobID, workflowPath, u))
			}
			unverified(fmt.Sprintf("job %q in %s declares neither `steps` nor `uses`", jobID, workflowPath))
		}
		steps, ok := stepsRaw.([]any)
		if !ok {
			unverified(fmt.Sprintf("job %q in %s has a `steps` key that is not a list", jobID, workflowPath))
		}
		jobEnv := mapEnv(jobMap, wfEnv)
		for _, s := range steps {
			stepMap, ok := s.(map[string]any)
			if !ok {
				unverified(fmt.Sprintf("job %q in %s has a step that is not a mapping", jobID, workflowPath))
			}
			stepEnv := mapEnv(stepMap, jobEnv)
			texts := stepTexts(stepMap, jobID, workflowPath, repoRoot)
			for _, target := range raceTargetsIn(texts, stepEnv, repoRoot, 0, map[string]bool{}) {
				found = append(found, leg{job: jobID, task: target, what: describe(jobID, target)})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].job != found[j].job {
			return found[i].job < found[j].job
		}
		return found[i].task < found[j].task
	})
	return found
}

// ---------------------------------------------------------------------------
// Finding the `task test:race*` invocations in a job
// ---------------------------------------------------------------------------
//
// THIS USED TO BE TOKEN MATCHING, TWICE, AND BOTH TIMES IT WAS WRONG.
//
// The first rule took the token immediately after a bare `task`. Six spellings of a valid
// Actions job racing a Postgres-needing tree with NO postgres service escaped it at exit 0
// (a go-task flag before the target, a backslash continuation, `bash -lc`, a job with no
// `steps` key). The second rule — any race-prefixed token anywhere in a shell that mentions
// `task` — closed those and opened two more holes of its own. The fifth contrast measured
// four further escapes, each verified by executing the shell against a stub and watching the
// exact argv `test:race-hot:modules` arrive: the target held in the job's `env`, the command
// written `\task`, the binary named by path as `"$HOME/go/bin/task"`, and the target
// concatenated by the shell as `test:"race"-hot:modules`. It also produced a FALSE positive:
// a job running `task lint:spdx` and then `echo "... test:race-hot:modules"` was reported as
// racing modules.
//
// Both failures have one cause: guessing at a shell instead of reading it. So this now scans
// shell words properly — quoting, escapes, `$VAR` against the env maps the workflow actually
// declares, and operator boundaries — and attributes a target only to the argv of a command
// whose resolved name really is `task`. That kills the false positive by construction and
// the four escapes by resolution.
//
// What a static reader cannot do is resolve a value that is not there. An argv word this
// scanner cannot resolve — an unknown variable, a substitution — makes the answer UNVERIFIED
// rather than clean, which is the whole discipline of this program: "I could not look" is a
// different fact from "I looked and it is clean".

// shellText is one readable shell body and a description of where it came from, so a finding
// or an UNVERIFIED can name the place rather than the symptom.
type shellText struct {
	body  string
	where string
}

// scanDepth bounds how far a shell is followed through `bash script.sh` hops. Beyond it the
// answer is UNVERIFIED: a chain this long is not something to guess about.
const scanDepth = 4

// shellWord is one word of a command after static resolution. `ok` false means the word's
// value depends on something this program cannot see.
type shellWord struct {
	value string
	ok    bool
}

// splitShell breaks a shell body into commands, and each command into resolved words. It
// honors single quotes (literal), double quotes (variables expand), backslash escapes and
// line continuations, and it ends a command at an unquoted operator. It is not a shell — it
// is the part of one needed to answer "which words are the argv of `task`".
func splitShell(body string, env map[string]string) [][]shellWord {
	var cmds [][]shellWord
	var cur []shellWord
	var w strings.Builder
	started, resolved := false, true

	flushWord := func() {
		if started {
			// A go-task template is not a resolved word. `vars: {GO_CMD: go}` with
			// `{{.GO_CMD}} test ./...` expands at run time to a real `go test`, and treating the
			// braces as a literal command head made the guard report success over a test that
			// ran unwrapped. Templating is go-task's, not the shell's, so the shell reader
			// cannot resolve it — and by this program's own rule, what it cannot resolve is
			// UNVERIFIED rather than assumed harmless.
			val := w.String()
			cur = append(cur, shellWord{value: val, ok: resolved && !strings.Contains(val, "{{")})
			w.Reset()
			started, resolved = false, true
		}
	}
	flushCmd := func() {
		flushWord()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
			cur = nil
		}
	}

	r := []rune(body)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch c {
		case '\\':
			if i+1 < len(r) {
				if r[i+1] == '\n' { // line continuation: the command keeps going
					i++
					continue
				}
				w.WriteRune(r[i+1]) // \task is task
				started = true
				i++
			}
		case '\'':
			started = true
			for i++; i < len(r) && r[i] != '\''; i++ {
				w.WriteRune(r[i]) // literal, no expansion
			}
		case '"':
			started = true
			for i++; i < len(r) && r[i] != '"'; i++ {
				if r[i] == '\\' && i+1 < len(r) {
					i++
					w.WriteRune(r[i])
					continue
				}
				if r[i] == '$' {
					v, ok, n := expand(r[i:], env)
					if n > 0 {
						w.WriteString(v)
						resolved = resolved && ok
						i += n - 1
						continue
					}
				}
				w.WriteRune(r[i])
			}
		case '$':
			// A COMMAND SUBSTITUTION IS ONE WORD, NOT A COMMAND BOUNDARY. `(` and `)` used to
			// end the command here, so `ROOT="$(cd "$(dirname "$X")/.." && pwd)"` — an ordinary
			// line of this repository's own reviewed helper — was chopped into pseudo-commands
			// whose heads are fragments like `/.. && pwd)`. That cost nothing while an
			// unrecognized head was ignored; it is a FALSE `UNVERIFIED` the moment a head has to
			// be on a reviewed list. What the substitution RUNS is not lost by this: every
			// caller that judges shell reads commandSubstitutions(body) as well, which is the
			// reader that was always meant to answer that question.
			if i+1 < len(r) && r[i+1] == '(' {
				end := spanParens(r, i+1)
				w.WriteString(string(r[i:end]))
				started = true
				resolved = false // its value is decided by running something
				i = end - 1
				continue
			}
			v, ok, n := expand(r[i:], env)
			if n > 0 {
				w.WriteString(v)
				started = true
				resolved = resolved && ok
				i += n - 1
				continue
			}
			w.WriteRune(c)
			started = true
		case '#':
			// An unquoted '#' that STARTS a word opens a comment to end of line. Without this,
			// `cd modules # cloud/control-plane` put the tree in the cd's argv and satisfied a
			// check that claims to read argv — measured by the tenth contrast, exit 0.
			if !started {
				//nolint:revive // scanning loop: the advance IS the post statement, so the body must be empty
				for i++; i < len(r) && r[i] != '\n'; i++ {
				}
				flushCmd()
				continue
			}
			w.WriteRune(c)
		case ' ', '\t':
			flushWord()
		case '<', '>':
			// A REDIRECTION IS NOT A COMMAND BOUNDARY, and treating it as one manufactured a
			// command out of the redirect's target: `echo x >/dev/null` produced a second
			// command whose head was `/dev/null`. That cost nothing while an unrecognized head
			// was silently ignored; it is a FALSE `UNVERIFIED` now that an unrecognized head
			// denies. So the operator, its companions (`>>`, `>&`, a leading fd as in `2>&1`)
			// and the word it redirects to are consumed and dropped, and the command carries on.
			// A PROCESS SUBSTITUTION IS A FILENAME, NOT ARGV. `mapfile -t M < <(go work edit
			// -json | sed -n '…')` is one line of this repository's own reviewed
			// scripts/go-work-each.sh, and chopping at `(` handed its insides out as
			// pseudo-commands — which is how a head of `sed` appeared in a script that runs no
			// sed of its own. Same class as the array literal and the command substitution
			// above, same treatment: consume it as one word. What it RUNS still executes in a
			// SUBSHELL, so it cannot alter the posture of the process that reads it.
			if i+1 < len(r) && r[i+1] == '(' {
				end := spanParens(r, i+1)
				flushWord()
				i = end - 1
				continue
			}
			if started && allDigits(w.String()) {
				w.Reset() // the `2` of `2>&1` belongs to the redirection, not to argv
				started, resolved = false, true
			}
			flushWord()
			for i+1 < len(r) && (r[i+1] == '<' || r[i+1] == '>' || r[i+1] == '&') {
				i++
			}
			for i+1 < len(r) && (r[i+1] == ' ' || r[i+1] == '\t') {
				i++
			}
			for i+1 < len(r) && !strings.ContainsRune(" \t\n;|&()<>", r[i+1]) {
				i++
			}
		case '(':
			// AN ARRAY ASSIGNMENT IS AN ASSIGNMENT, NOT A COMMAND. `HOT_GLOBS=( "a*_test.go" … )`
			// is one word of data; ending the command at `(` turned each element into a
			// pseudo-command whose head is a glob. Same reason as the substitution above: it was
			// harmless while an unrecognized head was ignored, and a false `UNVERIFIED` once a
			// head has to be on a reviewed list. Only an unmistakable assignment prefix triggers
			// it — `NAME=` or `NAME+=` glued to the paren — so a real subshell `( cmd )` still
			// ends the command exactly as before.
			if started && isAssignPrefix(w.String()) {
				end := spanParens(r, i)
				w.WriteString(string(r[i:end]))
				i = end - 1
				continue
			}
			// A FUNCTION DEFINITION IS ONE WORD TOO. `skip_match() {` arrived as the bare word
			// `skip_match`, indistinguishable from a CALL to it, so the reader could not tell
			// "this body defines it" from "this body runs something I have never read". Keeping
			// the `()` attached is what makes that difference visible, and it is the parse bash
			// itself uses.
			if started && isFunctionDefinition(w.String()+"()") {
				j := i + 1
				for j < len(r) && (r[j] == ' ' || r[j] == '\t') {
					j++
				}
				if j < len(r) && r[j] == ')' {
					w.WriteString("()")
					i = j
					continue
				}
			}
			flushCmd()
		case '\n', ';', '|', '&', ')':
			flushCmd()
		default:
			w.WriteRune(c)
			started = true
		}
	}
	flushCmd()
	return cmds
}

// spanParens returns the index just past the `)` that closes the `(` at r[open], counting nesting
// and ignoring parens inside single or double quotes. An unbalanced `(` spans to end of input:
// consuming the remainder is the answer that cannot invent a command boundary out of a fragment.
func spanParens(r []rune, open int) int {
	depth := 0
	for i := open; i < len(r); i++ {
		switch r[i] {
		case '\\':
			i++
		case '\'':
			//nolint:revive // scanning loop: the advance IS the post statement, so the body must be empty
			for i++; i < len(r) && r[i] != '\''; i++ {
			}
		case '"':
			for i++; i < len(r) && r[i] != '"'; i++ {
				if r[i] == '\\' {
					i++
				}
			}
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1
			}
		}
	}
	return len(r)
}

// isAssignPrefix reports whether s is exactly the left-hand side of an assignment with its `=`
// already written — `NAME=` or `NAME+=`. It is what distinguishes an array literal from a
// subshell, and it is deliberately strict: anything else keeps `(` as the command boundary it
// has always been.
func isAssignPrefix(s string) bool {
	s, ok := strings.CutSuffix(s, "=")
	if !ok {
		return false
	}
	s = strings.TrimSuffix(s, "+")
	if s == "" {
		return false
	}
	for i, c := range s {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// allDigits reports whether s is a non-empty run of digits — the shape of the file descriptor
// that may lead a redirection.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// commandSubstitutions returns the shell inside every `$( … )` and every backtick pair of a
// body. A SUBSTITUTION EXECUTES, and a recipe that computes a value by running a program is
// running a program: `RUN_REGEX="$(bash scripts/race-hot-tests.sh)"` is a real recipe of the
// Taskfile this gate guards. Leaving it unread was executable indirection with a different
// spelling — the same class the fourteenth contrast found under `defer:`, `includes:` and a
// helper script.
//
// IT IS DELIBERATELY NOT QUOTE-AWARE. A `$(` inside single quotes does not execute, so skipping
// single-quoted text would be correct — and getting the pairing wrong (an apostrophe in a shell
// comment is enough) would make this MISS a substitution, which is a false green. Reading one
// that never runs costs a loud UNVERIFIED somebody resolves in a line; missing one that does run
// is the defect this program exists to prevent. `$(( … ))` is arithmetic and executes nothing,
// so it is the one form skipped.
func commandSubstitutions(body string) []string {
	var out []string
	r := []rune(body)
	atWordStart := true
	for i := 0; i < len(r); i++ {
		start := atWordStart
		atWordStart = false
		switch r[i] {
		case ' ', '\t', '\n', ';', '|', '&', '(', ')':
			atWordStart = true
		case '#':
			// A COMMENT EXECUTES NOTHING, and it is the one thing this scanner must skip. The
			// repository's own scripts/race-hot-tests.sh documents its output as "emit the
			// ANCHORED `go test -run` regex" — backticks, in a comment — and reading that as a
			// substitution made a reviewed helper look like it ran the tests. The rule is
			// splitShell's, deliberately: a `#` that STARTS a word opens a comment to end of
			// line, and this program has one comment semantics rather than two.
			if start {
				//nolint:revive // scanning loop: the advance IS the post statement, so the body must be empty
				for i++; i < len(r) && r[i] != '\n'; i++ {
				}
				atWordStart = true
			}
		case '\\':
			i++ // an escaped character cannot open a substitution
		case '`':
			j := i + 1
			//nolint:revive // scanning loop: the advance IS the post statement, so the body must be empty
			for ; j < len(r) && r[j] != '`'; j++ {
			}
			if j < len(r) {
				out = append(out, string(r[i+1:j]))
				i = j
			}
		case '$':
			if i+1 >= len(r) || r[i+1] != '(' {
				continue
			}
			if i+2 < len(r) && r[i+2] == '(' {
				continue // $(( arithmetic ))
			}
			depth, j := 1, i+2
			for ; j < len(r) && depth > 0; j++ {
				switch r[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
			}
			if depth == 0 {
				out = append(out, string(r[i+2:j-1]))
				i = j - 1
			}
		}
	}
	return out
}

// expand resolves a $VAR or ${VAR} at the head of r. It returns the value, whether it could be
// resolved, and how many runes it consumed (0 when r does not start a variable reference).
// GitHub's own ${{ }} expressions are deliberately NOT resolved: their value is decided at run
// time, so a target that depends on one is exactly the unknowable case.
func expand(r []rune, env map[string]string) (string, bool, int) {
	if len(r) < 2 || r[0] != '$' {
		return "", true, 0
	}
	if r[1] == '{' {
		if len(r) > 2 && r[2] == '{' {
			return "", false, 2 // a ${{ }} expression: unknowable here
		}
		for j := 2; j < len(r); j++ {
			if r[j] == '}' {
				name := string(r[2:j])
				v, ok := env[name]
				return v, ok, j + 1
			}
		}
		return "", false, len(r)
	}
	j := 1
	for j < len(r) && (r[j] == '_' || (r[j] >= 'A' && r[j] <= 'Z') || (r[j] >= 'a' && r[j] <= 'z') || (r[j] >= '0' && r[j] <= '9')) {
		j++
	}
	if j == 1 {
		return "", true, 0
	}
	name := string(r[1:j])
	v, ok := env[name]
	return v, ok, j
}

// shellRunners are the commands that take another shell as an argument. `bash -lc "task X"`
// hides a task invocation from a scanner that only reads the outer argv, and it was one of the
// measured escapes.
//
// IT IS NOT, AND MUST NEVER AGAIN BE, THE BOUNDARY OF WHAT EXECUTES. This list enumerates what is
// dangerous, so a head absent from it means "not one of the shells I know", never "harmless": any
// ordinary program runs the words after it. Two rules read this table as if absence were safety —
// the wrapper-argv walk and the reviewed-helper premise — and the sixteenth contrast produced a
// false green out of each. Both now decide by a whitelist and treat an unknown head as UNVERIFIED;
// this table only says which heads are followed as SHELL.
var shellRunners = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "env": true}

// raceTargetsIn returns the race targets invoked by these shell bodies. It follows `bash -c`
// wrappers and local scripts, and calls unverified() rather than returning a clean answer
// whenever a `task` argv contains a word it cannot resolve.
//
// WHAT IT COVERS, AND WHAT IT DOES NOT. This is a net over ENUMERATED CLASSES, not a proof.
//
// Covered: a `task` invocation whose command word resolves — through quotes, escapes, line
// continuations, a leading VAR=VALUE and a basename — with its target resolved against the
// workflow's own env maps; `bash -c` and its siblings; repository scripts named by a `.sh` or
// `.bash` path, followed to a bounded depth; and the inline `run` steps of a local composite
// action. A target that cannot be resolved makes the answer UNVERIFIED rather than clean.
//
// NOT covered, and MEASURED as not covered on 2026-08-02: shell functions and aliases, arrays,
// command substitution, printf/eval indirection, xargs, make and npm wrappers, a Task alias or
// an included Taskfile, a matrix that hands the command to bash -c, a composite action nested
// two deep, a sourced script, an extensionless executable, and a `command` prefix.
//
// THAT LIST IS NOT A TODO. Every one of those forms still enters with-pg-env.sh through the
// target it runs, so layer 1 catches it at runtime. This layer exists to catch the ordinary
// mistake early; the guarantee does not rest on it. The comment is here so that nobody rebuilds
// a promise on top of a net — which is exactly what happened three times.
func raceTargetsIn(texts []shellText, env map[string]string, repoRoot string, depth int, seen map[string]bool) []string {
	if depth > scanDepth {
		unverified(fmt.Sprintf("a shell chain deeper than %d hops was not followed; the -race legs beyond it are unread", scanDepth))
	}
	var out []string
	for _, t := range texts {
		// A SUBSTITUTION EXECUTES, so the legs inside one are legs. This used to be true here
		// only BY ACCIDENT — splitShell chopped a command at `(`, which leaked the inside of
		// `$( … )` out as pseudo-commands, and a `task test:race*` in there was discovered as a
		// side effect of a parsing bug. The parse is correct now, so the reader that answers
		// this question is named instead of relied upon: the same commandSubstitutions() every
		// other judge of shell in this file already calls.
		for _, sub := range commandSubstitutions(t.body) {
			out = append(out, raceTargetsIn([]shellText{{body: sub, where: t.where + " (command substitution)"}}, env, repoRoot, depth+1, seen)...)
		}
		for _, cmd := range splitShell(t.body, env) {
			// Leading VAR=VALUE assignments belong to the command that follows them.
			local := env
			i := 0
			for ; i < len(cmd); i++ {
				k, v, isAssign := strings.Cut(cmd[i].value, "=")
				if !isAssign || k == "" || strings.ContainsAny(k, "/.-") {
					break
				}
				if local2 := map[string]string{}; true {
					for kk, vv := range local {
						local2[kk] = vv
					}
					local2[k] = v
					local = local2
				}
			}
			if i >= len(cmd) {
				continue
			}
			head, argv := cmd[i], cmd[i+1:]
			if !head.ok {
				// A command this program cannot name is only its business if something in
				// that command's own argv looks like a -race leg. Every CI shell is full of
				// runner-supplied variables, and answering "I could not look" about each of
				// them would be answering about the wrong thing — the UNVERIFIED that matters
				// is the one where a leg might really be hiding.
				for _, a := range argv {
					if a.ok && strings.HasPrefix(a.value, racePrefix) {
						unverified(fmt.Sprintf("%s passes %q to a command whose name this program cannot resolve statically; whether that races the tree is unknown", t.where, a.value))
					}
				}
				continue
			}
			name := filepath.Base(head.value)

			if name == "task" {
				for _, a := range argv {
					if !a.ok {
						unverified(fmt.Sprintf("%s invokes `task` with an argument this program cannot resolve statically; whether it names a -race leg is unknown", t.where))
					}
					if strings.HasPrefix(a.value, racePrefix) {
						out = append(out, a.value)
					}
				}
				continue
			}

			if shellRunners[name] {
				// `bash -c "<shell>"`, and `sh script.sh`: both hand a shell somewhere else.
				for j, a := range argv {
					if !a.ok {
						continue
					}
					if strings.HasPrefix(a.value, "-") && strings.Contains(a.value, "c") && j+1 < len(argv) {
						if next := argv[j+1]; next.ok {
							out = append(out, raceTargetsIn([]shellText{{body: next.value, where: t.where + " (via " + name + " -c)"}}, local, repoRoot, depth+1, seen)...)
						}
						break
					}
					out = append(out, followScript(a.value, t.where, local, repoRoot, depth, seen)...)
				}
				continue
			}
			out = append(out, followScript(head.value, t.where, local, repoRoot, depth, seen)...)
		}
	}
	return out
}

// followScript reads a repository script named by a shell word and scans it too. A composite
// action whose step runs a script that runs `task` was a measured escape: reading the action's
// inline `run` and stopping there is reading one link of a chain.
func followScript(word, where string, env map[string]string, repoRoot string, depth int, seen map[string]bool) []string {
	if !strings.HasSuffix(word, ".sh") && !strings.HasSuffix(word, ".bash") {
		return nil
	}
	path := word
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, filepath.Clean(word))
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil // not a repository script (a generated path, a fixture): nothing to read
	}
	if seen[path] {
		return nil
	}
	seen[path] = true
	return raceTargetsIn([]shellText{{body: string(body), where: where + " (via " + word + ")"}}, env, repoRoot, depth+1, seen)
}

// mapEnv reads an `env:` mapping. A value this program cannot render as a static string — a
// ${{ }} expression, a nested structure — is left OUT, which makes any word depending on it
// unresolvable and therefore UNVERIFIED rather than silently empty.
func mapEnv(m map[string]any, base map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	raw, ok := m["env"].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range raw {
		if sv, ok := v.(string); ok && !strings.Contains(sv, "${{") {
			out[k] = sv
		} else {
			delete(out, k) // shadowed by something unknowable: forget the outer value too
		}
	}
	return out
}

// stepTexts returns every shell body a step contributes: its own `run`, plus — for a step that
// delegates to a LOCAL composite action — the `run` of each of that action's steps.
//
// The local-action arm is not hypothetical tidiness. Before it existed the program's guarantee
// rested on an unmeasured premise: that no composite action in this repository invokes a -race
// target. The premise held on 2026-08-02, and "an unverified 'cannot' in a gate is
// indistinguishable from a hole" (.githooks/pre-push).
//
// A step that delegates to an EXTERNAL action is a stated limit: this program cannot read a
// third-party action, and the repository's convention is that -race legs are invoked from the
// workflow, a local action, or a repository script. Answering UNVERIFIED on every
// actions/checkout would make the gate unusable and would be answering about the wrong thing.
func stepTexts(step map[string]any, jobID, workflowPath, repoRoot string) []shellText {
	var out []shellText
	if run, ok := step["run"].(string); ok {
		out = append(out, shellText{body: run, where: fmt.Sprintf("job %q in %s", jobID, workflowPath)})
	}
	uses, ok := step["uses"].(string)
	if !ok || !strings.HasPrefix(uses, "./") {
		return out
	}
	dir := filepath.Join(repoRoot, filepath.Clean(uses))
	var actionPath string
	for _, name := range []string{"action.yml", "action.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			actionPath = filepath.Join(dir, name)
			break
		}
	}
	if actionPath == "" {
		unverified(fmt.Sprintf("job %q in %s uses local action %s, but no action.yml/action.yaml is readable under %s", jobID, workflowPath, uses, dir))
	}
	doc := loadMap(actionPath)
	runs, ok := doc["runs"].(map[string]any)
	if !ok {
		unverified(fmt.Sprintf("local action %s declares no `runs` mapping", actionPath))
	}
	steps, ok := runs["steps"].([]any)
	if !ok {
		// A JavaScript or Docker local action runs code this program cannot read. The header
		// used to claim such an action "cannot invoke the target without a shell"; it can —
		// through exec — so the honest answer is that it was not looked at.
		unverified(fmt.Sprintf("local action %s is `using: %v`, not a composite: its -race legs cannot be read from here", actionPath, runs["using"]))
	}
	for _, s := range steps {
		m, ok := s.(map[string]any)
		if !ok {
			unverified(fmt.Sprintf("local action %s has a step that is not a mapping", actionPath))
		}
		if run, ok := m["run"].(string); ok {
			out = append(out, shellText{body: run, where: fmt.Sprintf("local action %s (used by job %q)", actionPath, jobID)})
		}
	}
	return out
}

// describe gives a discovered leg the table's wording when the table knows it, and an honest
// placeholder when it does not. A leg is never skipped for want of a description.
func describe(job, task string) string {
	for _, l := range legs {
		if l.job == job && l.task == task {
			return l.what
		}
	}
	return "the tree run by " + task
}

const (
	exitOK         = 0
	exitFinding    = 1
	exitUnverified = 2
)

func main() {
	var (
		workflow = flag.String("workflow", "", "path to the workflow YAML to inspect (required)")
		taskfile = flag.String("taskfile", "", "path to the Taskfile YAML to inspect (required for -mode wiring)")
		mode     = flag.String("mode", "wiring", "wiring | image-agreement | print-exemptions | print-helpers")
		job      = flag.String("job", "", "check only this job (with -task); default is discovery from the workflow")
		task     = flag.String("task", "", "check only this task (with -job)")
		root     = flag.String("root", "", "repository root for resolving local `uses: ./...` actions (default: inferred from -workflow)")
	)
	flag.Parse()

	if *mode == "print-helpers" {
		// EMITTED, for the same reason the exemptions are: the premise each helper states — "it
		// runs no go test" — is checked by scripts/test-pg-test-env.sh against the paths this
		// program Honors, not against a spelling of its source that a semantic edit walks past.
		// No -workflow is needed to print a table, and demanding one would make the battery
		// build a fixture to ask a question about this file.
		names := make([]string, 0, len(recipeHelpers))
		for name := range recipeHelpers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("%s\t%s\n", name, recipeHelpers[name])
		}
		os.Exit(exitOK)
	}
	if *workflow == "" {
		unverified("no -workflow given; refusing to guess which file to inspect")
	}
	if *mode == "print-exemptions" {
		// The list is EMITTED so that a checker of this checker compares what the program
		// Honors, not a spelling of its source. The ninth contrast wrote a valid key as
		// `"test:" + "integration"`: gofmt accepted it, the program honored it, and the
		// battery's grep could not see it. A guard that reads a lexical shape can always be
		// walked around by a semantic one. Printing is the fix, and it is the same rule the
		// counts obey — the check emits it, nobody transcribes it.
		names := make([]string, 0, len(wrapperExempt))
		for name := range wrapperExempt {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("%s\t%s\n", name, wrapperExempt[name].tree)
		}
		os.Exit(exitOK)
	}
	switch *mode {
	case "wiring":
		if *taskfile == "" {
			unverified("no -taskfile given; refusing to guess which file to inspect")
		}
		if (*job == "") != (*task == "") {
			unverified("-job and -task must be given together, or neither")
		}
		checkWiring(*workflow, *taskfile, *job, *task, repoRootFor(*workflow, *root))
	case "image-agreement":
		checkImageAgreement(*workflow)
	default:
		unverified(fmt.Sprintf("unknown -mode %q", *mode))
	}
}

// unverified is the third answer. It goes to stderr and exits 2 — never 0, never 1: "I
// could not look" is a different fact from "I looked and it is clean", and a gate that
// conflates them is the fail-open pattern this repository removed from twenty files on
// 2026-08-01.
func unverified(msg string) {
	fmt.Fprintf(os.Stderr, "        UNVERIFIED: %s\n", msg)
	os.Exit(exitUnverified)
}

func loadMap(path string) map[string]any {
	raw, err := os.ReadFile(path)
	if err != nil {
		unverified(fmt.Sprintf("cannot read %s (%v)", path, err))
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		unverified(fmt.Sprintf("cannot parse %s (%v)", path, err))
	}
	if len(doc) == 0 {
		unverified(fmt.Sprintf("%s did not decode to a non-empty mapping", path))
	}
	return doc
}

func section(doc map[string]any, key, path string) map[string]any {
	sub, ok := doc[key].(map[string]any)
	if !ok || len(sub) == 0 {
		unverified(fmt.Sprintf("%s declares no `%s` mapping", path, key))
	}
	return sub
}

// pgServices returns the images of every service named exactly `postgres` in a job. Keyed
// by name on purpose: a renamed service is an ABSENCE, not a silent match on whatever else
// happens to be a database — the job's own env and provisioning steps name `postgres`.
func pgServices(job map[string]any) map[string]string {
	svcs, ok := job["services"].(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for name, spec := range svcs {
		if name != "postgres" {
			continue
		}
		m, ok := spec.(map[string]any)
		if !ok {
			continue
		}
		image, _ := m["image"].(string)
		out[name] = image
	}
	return out
}

// cmdTexts flattens a task's recipe. go-task accepts a plain string, a mapping with `cmd`,
// and a mapping with `task` (a call into another target); only the first two carry a shell
// command this gate can read, and a `task:` reference is followed by the caller declaring
// that other target as its own leg, not by recursing here.
func cmdTexts(task map[string]any) []string {
	raw, ok := task["cmds"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, c := range raw {
		switch v := c.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if s, ok := v["cmd"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// commandRunsGoTest reports whether this command's EXECUTABLE is `go` with `test` as its first
// argument, or whether one of its words is a nested shell that does so.
//
// It used to accept the words `go` and `test` adjacent ANYWHERE in the command, which is not the
// same question: the tenth contrast passed `echo go test cloud/control-plane >/dev/null` and got
// exit 0 out of a check whose comment said "the command that invokes go test". The executable is
// the first word after any leading VAR=VALUE assignments; a `bash -c "…"` argument is descended
// into, because that is how every real recipe here spells it.
func commandRunsGoTest(cmd []shellWord) bool {
	i := 0
	for ; i < len(cmd); i++ {
		k, _, isAssign := strings.Cut(cmd[i].value, "=")
		if !isAssign || k == "" || strings.ContainsAny(k, "/.-") {
			break
		}
	}
	if i >= len(cmd) {
		return false
	}
	// A command whose executable does NOT execute its arguments cannot be running anything,
	// however its argv reads. That is the whole difference between the real recipes here —
	// `bash scripts/with-pg-env.sh env … bash scripts/go-work-each.sh go test …`, where every
	// wrapper executes what follows — and the decoy `echo go test cloud/control-plane`, which
	// the previous rule accepted because it only looked for two adjacent words.
	if inertCommands[filepath.Base(cmd[i].value)] {
		return false
	}
	for j := i; j+1 < len(cmd); j++ {
		if filepath.Base(cmd[j].value) == "go" && cmd[j+1].value == "test" {
			return true
		}
	}
	// A nested shell handed over as one quoted argument: `bash -c "cd x && go test …"`.
	for _, w := range cmd[i+1:] {
		if !strings.Contains(w.value, "go test") {
			continue
		}
		for _, inner := range splitShell(w.value, nil) {
			if commandRunsGoTest(inner) {
				return true
			}
		}
	}
	return false
}

// inertCommands take their arguments as DATA. Anything else is treated as a wrapper that may
// execute what follows, which is the conservative direction: a wrapper wrongly believed inert
// would hide a real invocation, while a data command wrongly believed executing costs a loud
// finding somebody deletes.
// It is ALSO the list of what a race recipe may run outside the wrapper without a review, which
// is a second job for the same table and the reason it grew on 2026-08-04: an unrecognized head
// used to be ignored and now denies, so every command a real recipe or a fixture holds has to be
// classified here or be a finding. Each entry is one line and a reviewed claim; `sed` and `awk`
// are deliberately ABSENT — both can start a process — and so is `xargs`.
var inertCommands = map[string]bool{
	"echo": true, "printf": true, "true": true, "false": true, ":": true,
	"cat": true, "tee": true, "test": true, "[": true, "comm": true, "grep": true,
	// shell builtins and file plumbing that start nothing
	"cd": true, "pwd": true, "exit": true, "set": true, "unset": true, "shift": true,
	"read": true, "wait": true, "sleep": true, "mkdir": true, "rm": true, "rmdir": true,
	"touch": true, "mv": true, "cp": true, "ln": true, "chmod": true, "sync": true,
	// text tools that take their arguments as data
	"sort": true, "uniq": true, "head": true, "tail": true, "wc": true, "cut": true,
	"tr": true, "paste": true, "dirname": true, "basename": true, "date": true,
	// SHELL BUILTINS THAT BIND OR CONTROL, added 2026-08-05 when the walk started reading the
	// reviewed script scripts/go-work-each.sh instead of skimming it. Each binds a name or
	// steers a loop; none runs a word of its own argv.
	// RESIDUAL, named because this file does not get to have unnamed ones: `mapfile -C` names a
	// callback bash evaluates. That is an option nobody here writes and one a reviewer would see
	// being added — the same standing this list's other entries have, and NOT the standing `sed`
	// had, whose exposure was reachable by editing a script string the file already passed.
	"mapfile": true, "readarray": true, "local": true, "return": true,
	"continue": true, "break": true,
}

// underTree reports whether path is the tree or lives inside it, by PATH COMPONENT. A substring
// test says `cloud/control-plane-evil` is `cloud/control-plane`, and it is not.
func underTree(path, tree string) bool {
	rel, err := filepath.Rel(filepath.Clean(tree), path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

// commandHead returns the command's executable word — the first after any leading VAR=VALUE
// assignments — and false when the command is nothing but assignments.
func commandHead(cmd []shellWord) (shellWord, bool) {
	for _, w := range cmd {
		k, _, isAssign := strings.Cut(w.value, "=")
		if isAssign && k != "" && !strings.ContainsAny(k, "/.-") {
			continue
		}
		return w, true
	}
	return shellWord{}, false
}

// shellSyntaxPrefix are the KEYWORDS bash reads as grammar and which a real command may follow on
// the same line. They are not programs, and treating one as a command head asks the wrong question
// entirely: `if nice $GO test; then` has head `if` to a reader that stops at the first word, and
// the thing that matters is what comes after it.
var shellSyntaxPrefix = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "while": true, "until": true,
	"do": true, "!": true, "{": true, "time": true, "coproc": true,
}

// shellSyntaxWhole are the keywords after which nothing on the same command is a program to run:
// `for NAME in WORDS` and `case WORD in` govern DATA (whatever executes inside those words is a
// substitution, which every caller reads separately), and the closers stand alone.
var shellSyntaxWhole = map[string]bool{
	"for": true, "select": true, "case": true, "in": true,
	"fi": true, "done": true, "esac": true, "}": true, ";;": true,
}

// commandAfterSyntax strips the shell grammar leading a command and returns what is left to
// classify, or false when the whole thing is grammar. splitShell is a word reader, not a bash
// parser, so keywords arrive as ordinary words; deciding a head without stripping them makes a
// deny-closed head rule reject `fi` and accept whatever `if` was hiding.
func commandAfterSyntax(cmd []shellWord) ([]shellWord, bool) {
	for i, w := range cmd {
		k, _, isAssign := strings.Cut(w.value, "=")
		if isAssign && k != "" && !strings.ContainsAny(k, "/.-") {
			continue
		}
		if shellSyntaxWhole[w.value] || isFunctionDefinition(w.value) {
			return nil, false
		}
		if shellSyntaxPrefix[w.value] {
			continue
		}
		return cmd[i:], true
	}
	return nil, false
}

// isFunctionDefinition recognizes `name()` — the head of a definition, which DEFINES rather than
// runs. It is decided by SHAPE and not by a table on purpose: a table would need this repository's
// own function names in it, and a shared list of local identifiers is a list that goes stale the
// day somebody renames one. Whatever the function's body runs is read where the body is, because
// splitShell hands the `{ … }` out as ordinary commands.
func isFunctionDefinition(w string) bool {
	name, ok := strings.CutSuffix(w, "()")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// commandIs reports whether this command's executable is name, after leading assignments.
func commandIs(cmd []shellWord, name string) bool {
	for _, w := range cmd {
		k, _, isAssign := strings.Cut(w.value, "=")
		if isAssign && k != "" && !strings.ContainsAny(k, "/.-") {
			continue
		}
		return filepath.Base(w.value) == name
	}
	return false
}

// headIndex is where commandHead's word sits, so the argv after it can be read. len(cmd) when
// the command is nothing but assignments.
func headIndex(cmd []shellWord) int {
	for i, w := range cmd {
		k, _, isAssign := strings.Cut(w.value, "=")
		if isAssign && k != "" && !strings.ContainsAny(k, "/.-") {
			continue
		}
		return i
	}
	return len(cmd)
}

// ---------------------------------------------------------------------------
// WHERE A WORD POINTS. Identity by RESOLVED FILE, never by name.
// ---------------------------------------------------------------------------
//
// The fourteenth contrast measured the worst shape a guard can have: its own safeguard
// recognized BY SUBSTRING, failing OPEN. `bash "$WRAPPER_ROOT/scripts/with-pg-env.sh" go test …`
// left the expected basename visible after an unresolvable prefix was erased, so a COUNTERFEIT
// wrapper in another directory ran while this program exited 0 and printed that every go test
// went through the wrapper. `dir: relocated` did the same with no variable at all: the relative
// path resolved somewhere else and the substring did not care.
//
// So a word is the wrapper when the FILE IT NAMES is the canonical wrapper under the directory
// the recipe actually runs in — and a word this program cannot resolve is never the wrapper,
// because runtime expansion is by definition not resolvable here and "I could not look" must
// fall on the side that denies.
const wrapperRel = "scripts/with-pg-env.sh"

// resolveUnder turns a shell word into the path it names when the command runs in dir.
func resolveUnder(word, dir string) string {
	if filepath.IsAbs(word) {
		return filepath.Clean(word)
	}
	return filepath.Clean(filepath.Join(dir, word))
}

// sameFile reports whether two resolved paths name the same file. Cleaned-path equality decides
// it; os.SameFile is consulted as well so a symlinked checkout is not read as a counterfeit.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(fa, fb)
}

// effectiveDir is where a target's recipe runs: the Taskfile's own directory, or the `dir:` the
// target declares. The key is found through the table, like every other, and a `dir` this
// program cannot resolve — a non-string, or go-task templating — is UNVERIFIED rather than
// ignored, because ignoring it is what let a relocated recipe keep the wrapper's name.
//
// PER TARGET, NOT INHERITED DOWN A DELEGATION, and that is MEASURED rather than assumed
// (go-task 3.51.1, 2026-08-04): a `test:race-probe` with `dir: relocated` calling `- task: sub`
// ran sub's relative `scripts/with-pg-env.sh` from the TASKFILE'S directory, not from
// `relocated/`. Had it been the other way round, computing each target's directory from the
// Taskfile root would call a relocated callee's wrapper canonical — a false green — so the
// question was answered by running it.
func effectiveDir(taskMap map[string]any, taskRoot, target string) string {
	dir := taskRoot
	for key, h := range raceTargetKeys {
		if !h.dir {
			continue
		}
		raw, ok := taskMap[key]
		if !ok {
			continue
		}
		d, ok := raw.(string)
		if !ok {
			unverified(fmt.Sprintf("race target %s carries a `%s:` that is not a string, so where its recipe runs — and therefore which file %s names — is unknown", target, key, wrapperRel))
		}
		if strings.Contains(d, "{{") {
			unverified(fmt.Sprintf("race target %s carries a templated `%s: %s`; go-task decides it at run time, so where its recipe runs is not resolvable here", target, key, d))
		}
		dir = resolveUnder(d, taskRoot)
	}
	return dir
}

// recipeHelpers are the repository scripts a race recipe may run OUTSIDE the wrapper, each with
// the reason it cannot introduce an unwrapped `go test`.
//
// THIS IS THE SAME KIND OF OBJECT AS wrapperExempt: a reviewed CLAIM with somewhere it can go
// red, not an absence of a check. The alternative was tried on paper and rejected by reading the
// script involved: following arbitrary repository shell needs a table of every command a
// shell can hold — `for`, `compgen`, an array literal, a nested substitution — which is the
// completeness this program's header has refused to promise since round 3, three times over.
//
// The premise each entry states is checkable and IS checked: scripts/test-pg-test-env.sh reads
// every path this program emits under `-mode print-helpers` and fails if the file invokes
// `go test`. Nobody transcribes the list; the program prints it.
//
// The list is deliberately tiny. Anything else a recipe hands work to is UNVERIFIED, by name.
var recipeHelpers = map[string]string{
	"scripts/race-hot-tests.sh": "emits the `go test -run` regex on stdout for a wrapped command to use; it runs no test itself",
}

// helperPremiseChecked memoises the premise check so a helper named by several recipes is read
// once and reported once.
var helperPremiseChecked = map[string]bool{}

// helperKeepsItsPremise enforces what a recipeHelpers entry CLAIMS, and it is enforced by this
// program rather than by a grep somewhere else.
//
// IT WAS A GREP, AND THE GREP WAS WALKED AROUND ON THE DAY IT SHIPPED. The premise "this helper
// runs no go test" was checked by scripts/test-pg-test-env.sh searching the file for the words
// `go test`; a helper reaching the tests through a variable satisfies that search and runs them —
// measured 2026-08-04, checker exit 0. That is the NINTH contrast's finding regenerated by the
// FOURTEENTH contrast's fix, in the same file that carries the sentence it violates: a guard that
// reads a lexical shape can always be walked around by a semantic one.
//
// So the premise is now three things this program's own reader can decide, and the reader is the
// same one that judges recipes:
//
//   - no command in the helper RUNS `go test` (semantic: the executable, not two adjacent words);
//   - no command in it has a head this program cannot resolve — `$GO test` is exactly how the
//     tests hid from the grep, and an unresolvable head is unresolvable here too;
//   - no command in it hands work to a shell runner or to a `.sh`/`.bash` path — delegation is
//     the other way a helper reaches something this reader never saw.
//
// AND A FOURTH, WHICH IS THE DEFAULT AND WHICH THE OTHER THREE NEEDED (2026-08-05). Those three
// enumerate what is dangerous, and this comment used to concede the consequence as a limit: "a
// helper may run an ordinary program that itself starts a test binary. `sed` and `awk` can be
// made to; so can anything not on inertCommands." The sixteenth contrast turned that concession
// into a finding by executing it — a helper body of `GO=go` and `nice $GO test …`, checker exit 0,
// go-task exit 0, the helper's own `go` observer reporting `wrapper=absent`. A conceded limit that
// produces a false green is a defect, not a limit. So the head of every command has to be on a
// reviewed list (inertCommands or helperHeads) and anything else is UNVERIFIED.
//
// WHAT THAT COSTS, and it is paid deliberately: splitShell is a word reader, not a bash parser, so
// the grammar it cannot parse arrives as ordinary words. Two of its mis-parses were fixed rather
// than whitelisted — an array assignment and a command substitution are one word now, not a
// command boundary — and the keywords are stripped by commandAfterSyntax. What is left is a short
// list, which is the point: helperHeads has two entries and both name their own residual.
func (s recipeScan) helperKeepsItsPremise(name, where, body string) {
	if helperPremiseChecked[name] {
		return
	}
	helperPremiseChecked[name] = true
	path := filepath.Join(s.repoRoot, filepath.FromSlash(name))
	raw, err := os.ReadFile(path)
	if err != nil {
		unverified(fmt.Sprintf("target %s (reached from race target %s) runs the reviewed helper %s in %s, which this program cannot read (%v); its premise that it runs no go test is therefore unchecked", s.target, s.root, name, where, err))
	}
	s.helperShell(name, path, string(raw), 0)
}

func (s recipeScan) helperShell(name, path, body string, depth int) {
	if depth > scanDepth {
		unverified(fmt.Sprintf("reviewed helper %s nests shell more than %d levels deep; its premise cannot be checked that far down", name, scanDepth))
	}
	for _, sub := range commandSubstitutions(body) {
		s.helperShell(name, path, sub, depth+1)
	}
	for _, raw := range splitShell(body, nil) {
		if commandRunsGoTest(raw) {
			unverified(fmt.Sprintf("reviewed helper %s RUNS go test (%s), so the premise that lets a race recipe call it outside %s is false; remove the entry from recipeHelpers or stop the helper running tests", name, path, wrapperRel))
		}
		cmd, isCommand := commandAfterSyntax(raw)
		if !isCommand {
			continue
		}
		head, ok := commandHead(cmd)
		if !ok {
			continue
		}
		if !head.ok {
			unverified(fmt.Sprintf("reviewed helper %s runs a command whose executable this program cannot resolve statically (%s); a helper that reaches a program through a variable is exactly how a `go test` hides from a check of this premise", name, path))
		}
		base := filepath.Base(head.value)
		for _, w := range cmd {
			if strings.HasSuffix(w.value, ".sh") || strings.HasSuffix(w.value, ".bash") {
				unverified(fmt.Sprintf("reviewed helper %s hands work to %s (%s); its premise is about what IT runs, and this program has not read what that runs", name, w.value, path))
			}
		}
		if shellRunners[base] {
			unverified(fmt.Sprintf("reviewed helper %s runs %q (%s), which runs a shell this program has not read", name, base, path))
		}
		// AND THE DEFAULT IS DENY, which is the whole of the sixteenth contrast's second finding.
		// The four rules above enumerate what is DANGEROUS — a visible `go test`, an unresolvable
		// head, a shell runner, a `.sh` — and every one of them was walked past by
		// `GO=go` / `nice $GO test …`: `nice` is an ordinary head that resolves, and the word
		// carrying the program is neither the head nor a `.sh`. Checker exit 0, task exit 0, the
		// helper's own `go` observer reporting `wrapper=absent`. Enumerating the dangerous is a
		// blacklist wearing a whitelist's clothes; what a gate can enumerate is what is PERMITTED.
		if !inertCommands[base] && helperHeads[base] == "" {
			unverified(fmt.Sprintf("reviewed helper %s runs %q (%s), which is not on the reviewed list of heads that do not run a word of their own argv — whether it reaches a `go test` is UNKNOWN. Classify it in inertCommands or helperHeads (one line, with the reason) before trusting this premise", name, base, path))
		}
	}
}

// helperHeads extends inertCommands for the ONE question the reviewed-helper premise asks: does
// this command run a word of its own argv as a program. That is narrower than inertCommands' claim
// ("starts nothing at all"), so an entry belongs here rather than there — putting one in
// inertCommands would also widen what a race recipe may run unwrapped, which is a different
// permission nobody asked for.
//
// IT IS EMPTY, AND THAT IS THE POINT (2026-08-05). It briefly held `sed` and `compgen`, because
// scripts/race-hot-tests.sh used both; neither can honestly claim to run no word of its own argv —
// GNU sed's `e` command runs a shell command out of the sed SCRIPT, and `compgen -C` names a
// command bash runs — so each entry NAMED that residual and leaned on the helper being human
// reviewed. The seventeenth contrast went straight through the sed one: a helper body of
// `GO=go sed -n 'e $GO test … >&2' <<<'x'` reached a `go test` with the checker at exit 0. A
// residual a review has written down is still a residual, and a table entry that says "this is
// safe except when it is not" is the shape of claim this file exists to refuse.
//
// So the helper was changed instead of the table: it uses `grep`/`cut` where it used `sed`, and
// bash's own glob where it used `compgen`, with byte-identical output — see the note at the top of
// scripts/race-hot-tests.sh. The table stays as the extension point, and empty is the honest state
// of it. Anything added here has to run NO word of its own argv, unconditionally; if that needs an
// "except", the helper is what changes.
var helperHeads = map[string]string{}

// what a command actually hands work to.
const (
	execNone   = iota // nothing beyond the head itself
	execInline        // a shell body handed over, `bash -c "<shell>"`
	execWord          // a word naming the program that runs
	execOption        // an option this program does not model, which may eat the next word
)

// recipeExecutable answers "what does this command RUN". A shell runner (`bash`, `sh`, `env`)
// runs what it is given, so the answer is its `-c` body or its first non-flag, non-assignment
// argument — not the runner. Reading the runner as the program is how `bash scripts/whatever.sh`
// looked like a command this gate had understood.
// IT RETURNS WHERE, TOO. The argv walk used to find the executable's position by scanning for a
// word EQUAL to the one returned, which is not the same question: `env env go test` has two
// identical words, the scan found the first, the walk re-entered at the same place and SPUN
// FOREVER. A position is what the caller needs, so a position is what this returns.
func recipeExecutable(cmd []shellWord) (int, string, shellWord, int) {
	i := headIndex(cmd)
	if i >= len(cmd) {
		return execNone, "", shellWord{}, -1
	}
	head := cmd[i]
	if !shellRunners[filepath.Base(head.value)] {
		return execWord, "", head, i
	}
	for j := i + 1; j < len(cmd); j++ {
		a := cmd[j]
		if !a.ok {
			return execWord, "", a, j // unresolvable: the caller denies, by name
		}
		if strings.HasPrefix(a.value, "-") {
			// AN OPTION THAT TAKES A VALUE EATS THE NEXT WORD, and skipping options without
			// knowing which ones do was a false green measured on 2026-08-04:
			// `env -u scripts/with-pg-env.sh go test …` had the canonical wrapper CONSUMED as
			// the argument of `-u`, so the program read the wrapper as the executable and
			// waved through a `go test` that ran unwrapped. Modeling every option of every
			// runner is not something this program can promise, so it models exactly one —
			// `-c`, which every real recipe here uses — and refuses the rest BY NAME.
			if isDashC(a.value) && j+1 < len(cmd) {
				return execInline, cmd[j+1].value, cmd[j+1], j + 1
			}
			return execOption, a.value, a, j
		}
		if k, _, isAssign := strings.Cut(a.value, "="); isAssign && k != "" && !strings.ContainsAny(k, "/.-") {
			continue // `env FOO=1 prog …`
		}
		return execWord, "", a, j
	}
	return execNone, "", head, i
}

// isDashC recognizes the one option this program models: `-c`, and the clustered short forms a
// real recipe writes it in (`-lc`, `-euc`). `--command`-style long options are NOT accepted:
// they are a different grammar per runner, and guessing at one is what this rule exists to stop.
func isDashC(w string) bool {
	if len(w) < 2 || w[0] != '-' || w[1] == '-' {
		return false
	}
	seen := false
	for _, c := range w[1:] {
		if c == 'c' {
			seen = true
			continue
		}
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return seen
}

// ---------------------------------------------------------------------------
// THE LIST. Deny-closed over go-task's own grammar.
// ---------------------------------------------------------------------------
//
// Eleven rounds of contrast went the same way: this program enumerated the keys it knew how to
// READ, and each round found another key that executes — cmds, then deps, then status, then
// defer/for/vars/env. Enumerating what you can read is a blacklist wearing a whitelist's clothes:
// it passes every form nobody has written down yet, including the one the next round will write.
//
// So the default is inverted. A Taskfile is a YAML mapping with FINITE keys, which is what makes
// this layer a closed surface at all. Every key a race target carries is looked up here, and a
// key that is not in this table makes the answer UNVERIFIED — BY NAME, because a guard that says
// "I could not look" without saying at what teaches nobody anything.
//
// ADDING A KEY IS ONE LINE IN THIS TABLE AND NOTHING ELSE. That is the point: the moment a new
// key also needs a case in a switch somewhere, someone adds the case, forgets the table, and this
// breaks again by exactly the route it broke the first eleven times.
// localOptIn is the one variable that turns the wrapper's refusal into a pass.
const localOptIn = "OLIVARES_PG_LOCAL_DEFAULTS"

type keyHandling struct {
	shell   bool // its value carries shell this program must read
	targets bool // its value names other targets to walk
	dir     bool // it relocates where the recipe runs
}

// raceTargetKeys classifies every key a TARGET may carry.
var raceTargetKeys = map[string]keyHandling{
	"cmds":          {shell: true, targets: true},
	"deps":          {targets: true},
	"status":        {shell: true},
	"preconditions": {shell: true},
	"vars":          {shell: true}, // vars.<name>.sh runs a command
	"env":           {shell: true}, // env.<name>.sh runs a command
	"dir":           {dir: true},
	"desc":          {},
	"summary":       {},
	"aliases":       {},
	"silent":        {},
	"sources":       {},
	"generates":     {},
	"method":        {},
	"run":           {},
	"platforms":     {},
	"internal":      {},
	"interactive":   {},
	"prompt":        {},
	"label":         {},
	"requires":      {},
	"set":           {},
	"shopt":         {},
	"ignore_error":  {},
}

// recipeEntryKeys classifies every key ONE ENTRY of `cmds` may carry. Same rule, same reason:
// `defer` and `for` are entry keys and both execute.
var recipeEntryKeys = map[string]keyHandling{
	"cmd": {shell: true},
	// `defer` takes EITHER a shell string OR a call into another target — go-task accepts both,
	// and this table declared only the first. The fourteenth contrast wrote `- defer: {task: X}`
	// and the walk never reached X: the key was in the table, so the unknown-key gate stayed
	// quiet, and one of its two valid VALUE shapes was neither read as shell nor followed as a
	// delegation. A key classified for one of its shapes is a hole with a table entry on it.
	"defer":        {shell: true, targets: true},
	"task":         {targets: true},
	"for":          {},
	"vars":         {shell: true},
	"silent":       {},
	"ignore_error": {},
	"platforms":    {},
	"set":          {},
	"shopt":        {},
}

// refuseUnknownKeys is the deny-closed gate itself.
func refuseUnknownKeys(m map[string]any, table map[string]keyHandling, what, target string) {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if _, known := table[k]; !known {
			unverified(fmt.Sprintf("race target %s carries the %s key %q, which this program does not model — whether it executes a go test is UNKNOWN. Classify it in raceTargetKeys/recipeEntryKeys (one line) before trusting this gate", target, what, k))
		}
	}
}

// shellFrom extracts every shell string out of whatever shape a key's value takes: a list of
// strings, a list of entries carrying `cmd`/`defer`, or a mapping of vars/env entries with `sh`.
// One function, so a key marked shell:true in the table is READ without another case anywhere.
func shellFrom(v any) []string {
	var out []string
	switch t := v.(type) {
	case string:
		out = append(out, t)
	case []any:
		for _, e := range t {
			switch x := e.(type) {
			case string:
				out = append(out, x)
			case map[string]any:
				for _, k := range []string{"cmd", "defer", "sh"} {
					if sh, ok := x[k].(string); ok {
						out = append(out, sh)
					}
				}
				out = append(out, shellFrom(x["vars"])...)
			}
		}
	case map[string]any:
		for _, inner := range t {
			switch x := inner.(type) {
			case map[string]any:
				if sh, ok := x["sh"].(string); ok {
					out = append(out, sh)
				}
			}
		}
	}
	return out
}

// targetsFrom extracts every target NAME a key's value refers to.
//
// bareIsName comes from the table and not from a special case: a key that is BOTH shell and
// targets — `cmds` — holds shell in its bare strings and target names only in `- task:` entries,
// while a key that is targets alone — `deps` — holds names in its bare strings. Reading a cmds
// string as a target name is how the first table-driven draft asked the Taskfile for a target
// called `RUN_REGEX="$(bash …)" && …`.
// A MAPPING VALUE NAMES A TARGET TOO. `- task: X` is one spelling of a call; `- defer: {task: X}`
// is another, and go-task runs both. The entry-level walk is driven by recipeEntryKeys for the
// same reason the target-level one is driven by raceTargetKeys: a key classified `targets` is
// FOLLOWED without another case anywhere, whatever shape its value takes.
func targetsFrom(v any, bareIsName bool) []string {
	var out []string
	switch t := v.(type) {
	case string:
		if bareIsName {
			out = append(out, t)
		}
	case map[string]any:
		if n, ok := t["task"].(string); ok {
			out = append(out, n)
		}
	case []any:
		for _, e := range t {
			switch x := e.(type) {
			case string:
				if bareIsName {
					out = append(out, x)
				}
			case map[string]any:
				for k, h := range recipeEntryKeys {
					if h.targets {
						out = append(out, targetsFrom(x[k], !h.shell)...)
					}
				}
			}
		}
	}
	return out
}

// shellSurfaces returns every shell a TARGET executes — DERIVED FROM THE TABLE.
//
// The previous version listed the keys again, here, in its own code. That made the table a
// membership filter and nothing more: its shell/targets/dir flags were declared and never read,
// so "adding a key is one line" was FALSE — the line silenced the unknown-key gate and connected
// nothing. The twelfth contrast found exactly that. The flags drive the walk now, which is what
// makes the promise true.
func shellSurfaces(task map[string]any) []shellText {
	var out []shellText
	for key, h := range raceTargetKeys {
		if !h.shell {
			continue
		}
		for _, sh := range shellFrom(task[key]) {
			out = append(out, shellText{body: sh, where: key})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].where < out[j].where })
	return out
}

// checkEveryRaceTargetIsWrapped is the guarantee, and it is the only part of this program that
// can be complete.
//
// WHY IT MOVED HERE (2026-08-02, after the sixth contrast). The protected property is that no
// test skips in silence under a green job. This program used to try to establish it by reading
// the WORKFLOW: find every job that invokes a -race target, then check that job has a Postgres
// service. Three rules were written for that and three were broken — the token after `task` lost
// six spellings, any race-prefixed token in a task-invoking shell lost four more and invented a
// false positive, and a real shell word scanner lost the classes enumerated at raceTargetsIn:
// functions, aliases, arrays,
// command substitution, printf and eval, xargs, make and npm wrappers, a Task alias, an included
// Taskfile, a matrix through bash -c, a composite action nested two deep, a sourced script, an
// extensionless executable, a `command` prefix, and two attacks on the depth bound itself.
//
// That is not a list of bugs. Completeness over arbitrary shell is not a thing a program can
// promise, and each round spent promising it was a round in which the gate's header claimed more
// than the gate delivered — the exact defect class this repository treats as first order.
//
// So the guarantee moved to a surface that IS closed. A Taskfile is YAML: the set of targets
// whose name starts with `test:race` is enumerable exactly, with no shell in the way. If every
// one of them routes every `go test` through scripts/with-pg-env.sh, then any invocation of any
// of them — by any spelling, from any job, through any wrapper — enters that script.
//
// TWO QUALIFICATIONS, both of which an earlier draft of this paragraph left out and both of
// which the contrasts had to put back. FIRST, what the script does then depends on the REGIME:
// fail-closed (the default, and what CI runs) and PROMOTION refuse; LOCAL opt-in exits 0 and
// runs the child, deliberately, so a developer with no server can still work. SECOND, a race
// target's walk may end at a reviewed EXEMPTION rather than at a wrapper, so "every go test" is
// true of the targets this program reports as wrapped and not of every race target it inspected
// — which is why the success output names those two sets apart, and lists the members rather
// than counting them.
//
// THAT SENTENCE USED TO SAY "one target's walk ends at a reviewed exemption", and then said in
// its own next clause that no cardinal appeared in it. `one` is a cardinal: an ordinary Taskfile
// commit that exempts a second entry point makes the prose false while every check stays green,
// which is precisely the rule this file ratified on 2026-08-03 and then broke inside the comment
// stating it. The fourteenth contrast found it. The count is not written here at all now — the
// run prints the members, and nobody transcribes them.
//
// With both said: in the regimes CI uses, a misconfigured job fails LOUDLY instead of skipping
// silently. That is the property, and it is worth stating no wider than it holds.
//
// This is also not theory. On 2026-08-02 a local gate run died on exactly that guard, before
// compiling a single test, because its DSN pointed at a neighbour's port.
// exempted collects the exemptions honored during a run, so they are PRINTED rather than
// silently applied. An exemption nobody sees is indistinguishable from a gap.
var exempted []string

// verifiedTargets collects the race targets layer 1 actually checked, so the run EMITS them
// instead of a human writing the count into prose somewhere. The rule this obeys was ratified
// on 2026-08-03 after the same finding regenerated four times: if a number changes with every
// commit, a check emits it or it is not written. A target count is exactly that number — it
// moves the day somebody adds a leg — so this program says it. Whether some other file also
// writes it down is not something this comment can promise, and an earlier version of it did.
var verifiedTargets []string

// exemptedRoots are the race targets whose walk STOPPED at a reviewed exemption instead of
// reaching a wrapped `go test`. They are reported apart from the fully-inspected ones, because
// the eighth contrast caught the success line saying every target routed through the wrapper
// while one of them delegates to test:cloud, which by design does not. The count was derived and
// the sentence beside it was still stronger than the walk — which is this finding regenerating
// inside its own fix.
var exemptedRoots = map[string]bool{}

func checkEveryRaceTargetIsWrapped(tasks map[string]any, taskfilePath, repoRoot string) []string {
	var findings []string
	names := make([]string, 0, len(tasks))
	for name := range tasks {
		if strings.HasPrefix(name, racePrefix) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		// Not "nothing to check" — UNOBSERVABLE. A Taskfile with no race target at all means
		// this program is reading the wrong file, or the targets were renamed out from under
		// the one guarantee that rests on them.
		unverified(fmt.Sprintf("no `%s*` target exists in %s — wrong file, or the race targets were renamed", racePrefix, taskfilePath))
	}
	sort.Strings(names)
	verifiedTargets = names
	// go-task resolves a relative `dir:` and a relative command path against the directory of the
	// Taskfile it read, so that is the base every path in this walk is resolved from. Taking the
	// repository root instead would answer about the wrong tree for every fixture, which is most
	// of what the battery is.
	taskRoot := filepath.Dir(taskfilePath)
	for _, name := range names {
		findings = append(findings, wrappedUnder(tasks, name, name, taskfilePath, taskRoot, repoRoot, map[string]bool{})...)
	}
	return findings
}

// wrapperExempt are the entry points reviewed and excused from with-pg-env.sh, with the reason
// each one rests on. An exemption is not an absence of a check: it is a claim, and a claim needs
// somewhere it can go red.
//
//   - test:cloud runs a SEPARATE module (cloud/control-plane, GOWORK=off) that has its own
//     PostgreSQL and does not participate in the shared test harness. Note what that does and
//     does not say: the tree IS Postgres-backed — it imports jackc/pgx and has real store
//     packages — so a wide "no Postgres here" sweep would produce a false red. The claim is
//     narrower and checkable: the tree reads none of the variables with-pg-env.sh exports.
//     scripts/test-pg-test-env.sh verifies exactly that premise on every run, and a battery case
//     asserts this list and the battery's agree, so the two cannot drift apart.
//
// The list is deliberately tiny. A new unwrapped entry point is a finding until somebody adds it
// here WITH a reason, which is the review step the whole gate exists to force.
// Each entry names the TREE its premise is about, because that is what can be verified. The
// battery sweeps the tree; this map says which entry points that sweep is standing behind. A
// drift guard that only checked entry-point NAMES would miss the dangerous direction — the
// checker excusing an entry whose tree nobody sweeps.
type exemption struct {
	tree   string
	reason string
}

var wrapperExempt = map[string]exemption{
	"test:cloud":        {"cloud/control-plane", "separate module, own PostgreSQL, does not read the shared harness"},
	"test:cloud:norace": {"cloud/control-plane", "same tree and same premise as test:cloud, without -race"},
	"test:release":      {"core/license", "no Postgres-backed package is reachable"},
}

// wrappedUnder checks one target and everything it DELEGATES to. go-task lets a recipe be a call
// into another target (`- task: other`), and `test:race-hot` is exactly that: a composite over
// the sub-legs it delegates to. Stopping at a delegator would check the aggregator and miss every
// runs, which is the same one-link-short mistake the workflow scanner kept making — so the
// delegation is followed, to any target, not only to other race ones. A non-race target reached
// this way is reached BY a race target, and an unwrapped `go test` there is just as silent.
func wrappedUnder(tasks map[string]any, target, root, taskfilePath, taskRoot, repoRoot string, seen map[string]bool) []string {
	if seen[target] {
		return nil // a cycle is go-task's problem, not this gate's; do not spin on it
	}
	seen[target] = true

	taskMap, ok := tasks[target].(map[string]any)
	if !ok {
		unverified(fmt.Sprintf("target %q, reached from race target %s, is absent from %s or is not a mapping", target, root, taskfilePath))
	}
	refuseUnknownKeys(taskMap, raceTargetKeys, "target", target)
	// A Task target's OWN env selects the regime just as a workflow job's does, statically or
	// through `<NAME>.sh`. checkNoJobEnablesLocalRegime reads the workflow; this reads the
	// Taskfile, because the variable does not care which file set it.
	if env, ok := taskMap["env"].(map[string]any); ok {
		for name := range env {
			if strings.TrimSuffix(name, ".sh") == localOptIn {
				unverified(fmt.Sprintf("race target %s sets %s in its own Task env: in the LOCAL regime an absent server is exit 0 and its PostgreSQL tests skip in silence", target, localOptIn))
			}
		}
	}
	if raw, ok := taskMap["cmds"].([]any); ok {
		for _, c := range raw {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			refuseUnknownKeys(m, recipeEntryKeys, "cmds entry", target)
			// AND INSIDE A NESTED CALL. `- defer: {task: X}` puts a second entry mapping one
			// level down, and refusing unknown keys only at the top left that one unread —
			// which is where the fourteenth contrast hid a delegation. The same table judges
			// it, because it is the same kind of object.
			for k, h := range recipeEntryKeys {
				if !h.targets {
					continue
				}
				if inner, nested := m[k].(map[string]any); nested {
					refuseUnknownKeys(inner, recipeEntryKeys, "cmds entry `"+k+"`", target)
				}
			}
		}
	}

	if ex, ok := wrapperExempt[target]; ok {
		// THE EXEMPTION MUST STILL BE ABOUT THE TREE IT NAMES. The premise the battery sweeps is
		// "this TREE does not read the shared harness"; the thing excused is an ENTRY POINT. If
		// the recipe is remapped to run somewhere else, the sweep keeps proving something true
		// about a tree the target no longer touches, and the excuse quietly transfers. The eighth
		// contrast demonstrated it: repointing test:cloud's recipe at modules left the program at
		// exit 0, still printing "tree cloud/control-plane".
		//
		// So the declared tree has to appear in the recipe that is being excused. That is a
		// coarse link and it is stated as one — it does not prove the recipe RUNS there, only
		// that it still mentions it — but it turns a silent transfer into a finding.
		// The tree must be named IN THE ARGV of the command that runs the tests, or in a `cd` of
		// the same recipe, or in the target's own `dir:`.
		//
		// Two weaker versions were tried and the ninth contrast walked past both. "Mentioned
		// anywhere in the recipe" fell to `echo cloud/control-plane >/dev/null; cd modules &&
		// go test …`. "Mentioned in a command that also contains go test" fell to the SAME line,
		// because a shell string holds several commands and that one holds all three. Splitting
		// the shell is what separates the decoy from the claim: in the real recipe the tree is a
		// word of the command that invokes go test; in the decoy it is a word of `echo`, and the
		// `cd` names somewhere else.
		//
		// This is still a TEXTUAL link and not proof of where the process runs. The limit is real
		// and stays stated — but it now costs a lie in the very argv whose tree is in question,
		// instead of a comment somewhere in the file.
		mentions := false
		for _, t := range cmdTexts(taskMap) {
			for _, cmd := range splitShell(t, nil) {
				runsTests, isCd := commandRunsGoTest(cmd), commandIs(cmd, "cd")
				namesTree := false
				for _, w := range cmd {
					if strings.Contains(w.value, ex.tree) {
						namesTree = true
					}
				}
				if namesTree && (runsTests || isCd) {
					mentions = true
					break
				}
			}
			if mentions {
				break
			}
		}
		// AND `dir:` DECIDES WHEN IT IS PRESENT. It sets where the recipe runs, so a target that
		// relocates itself has moved away from the tree its excuse is about — no matter what its
		// command line still names. The eleventh contrast transferred an exemption with
		// `dir: cloud/control-plane/../../modules`, which contains the declared tree as a
		// SUBSTRING and resolves to another one; cleaning the path is the difference between what
		// a string looks like and where a process runs.
		// COMPONENT-AWARE, not substring: `cloud/control-plane-evil` contains `cloud/control-plane`
		// and is a different tree. The dir key is found through the table too.
		for key, h := range raceTargetKeys {
			if !h.dir {
				continue
			}
			if d, ok := taskMap[key].(string); ok {
				mentions = underTree(filepath.Clean(d), ex.tree)
			}
		}
		if !mentions {
			return []string{fmt.Sprintf("target %s is excused on the premise that tree %s does not read the shared harness, but its recipe no longer mentions %s — the exemption is about a tree this target does not touch", target, ex.tree, ex.tree)}
		}
		exempted = append(exempted, fmt.Sprintf("%s <- reached from %s: EXEMPT — tree %s, named by its own recipe (%s)", target, root, ex.tree, ex.reason))
		exemptedRoots[root] = true
		return nil
	}

	scan := recipeScan{target: target, root: root, taskRoot: taskRoot, taskfilePath: taskfilePath,
		repoRoot: repoRoot, dir: effectiveDir(taskMap, taskRoot, target)}
	var findings []string
	inspected := false
	var viaShell []string
	for _, t := range shellSurfaces(taskMap) {
		f, d, ran := scan.shell(t.body, t.where, 0)
		findings = append(findings, f...)
		viaShell = append(viaShell, d...)
		inspected = inspected || ran
	}
	for _, d := range append(delegates(taskMap), viaShell...) {
		inspected = true
		findings = append(findings, wrappedUnder(tasks, d, root, taskfilePath, taskRoot, repoRoot, seen)...)
	}
	if !inspected {
		// Neither a `go test` nor a delegation: there is nothing here this program can read,
		// and a race target it cannot read is not a race target it has cleared.
		unverified(fmt.Sprintf("target %s (reached from race target %s) runs no `go test` and delegates to nothing this program can read", target, root))
	}
	return findings
}

// recipeScan carries the identity of the target being walked and the directory its recipe runs
// in, so the
// walk below can name the place in every answer it gives.
type recipeScan struct {
	target, root, dir, taskRoot, taskfilePath, repoRoot string
}

// wrapperEnvPrefix is the namespace with-pg-env.sh decides. A word in the argv the wrapper execs
// that CLEARS or OVERRIDES one of these undoes the decision after it was made — see the
// under-the-wrapper walk below for why that is a finding and not a stated limit.
const wrapperEnvPrefix = "OLIVARES_"

// underWrapper reads what the wrapper is asked to exec.
//
// THE ARGUMENT THIS REPLACES WAS WRONG, and it was the load-bearing one: "entering the wrapper
// makes everything downstream safe, because with-pg-env.sh decides the Postgres posture and then
// execs its argv". It decides the posture and the argv can throw the decision away. Measured on
// 2026-08-04 — `scripts/with-pg-env.sh env -u <the three DSNs> go test …` produced checker exit 0
// while the tests ran with no posture at all, which is the silent skip this gate exists to
// prevent, reached THROUGH the wrapper rather than around it. This session had written that case
// down as a stated limit before the contrast ran; a stated limit is what you write when you
// cannot close something, not when you have not tried.
//
// So the argv chain is walked: runner by runner, refusing any option this program does not model
// (an option can consume words and can rewrite the environment), and descending into a `-c` body.
//
// AND IT NO LONGER STOPS AT THE FIRST NON-RUNNER. That is where this comment used to end — "from
// there the words are that program's own arguments, and what it does with its environment is not
// readable here. That last sentence is the limit, and it is the real one." It was not a limit; it
// was a fail-open, and the sixteenth contrast measured it with one ordinary executable placed in
// front of the `env -u` the row above had just closed. An unrecognized head does not end an argv:
// it runs the words after it. The walk now ends only where a REVIEWED entry says it does —
// argvTerminals for a program whose remaining argv is its own data, argvRunnerScripts for a
// repository script the walk continues through after reading it — and anywhere else the answer is
// UNVERIFIED, by name.
//
// THE LIMIT THAT REMAINS, stated as narrowly as it is true: a reviewed terminal is a reviewed
// CLAIM. `go` is on that list because the Go tool builds and runs a test binary rather than
// exec'ing an argv word; if that entry were wrong, or if a wrong entry were added, this walk would
// end early again — which is exactly what the battery measures in both directions.
func (s recipeScan) underWrapper(cmd []shellWord, where, body string, depth int) []string {
	canonical := filepath.Join(s.taskRoot, wrapperRel)
	idx := -1
	for i, w := range cmd {
		if w.ok && sameFile(resolveUnder(w.value, s.dir), canonical) {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(cmd) {
		return nil // the wrapper with nothing after it runs nothing
	}
	return s.argvChain(cmd[idx+1:], where, body, depth, nil)
}

// argvChain walks ONE command as the argv of something running under the wrapper's decision, and
// it is one function on purpose: the wrapper's own argv and a shell body the wrapper execs are the
// same question, and answering it in two places is how the two answers drift apart. They did — the
// argv walk was hardened on 2026-08-05 and the `-c` reader beside it was left approving the very
// input the walk had just started refusing.
func (s recipeScan) argvChain(rest []shellWord, where, body string, depth int, localFuncs map[string]bool) []string {
	var findings []string
	for {
		findings = append(findings, s.posturePreserved(rest, where, body)...)
		kind, inline, exe, at := recipeExecutable(rest)
		switch kind {
		case execOption:
			unverified(fmt.Sprintf("target %s (reached from race target %s) passes the option %q to a runner INSIDE %s's argv in %s; an option this program does not model can consume the next word and can rewrite the environment the wrapper just decided, so what reaches the tests is unknown: %s",
				s.target, s.root, inline, wrapperRel, where, body))
		case execInline:
			return append(findings, s.underWrapperShell(inline, where+" (via -c, inside the wrapper's argv)", depth+1)...)
		case execNone:
			return findings
		}
		if !exe.ok {
			unverified(fmt.Sprintf("target %s (reached from race target %s) hands %s's argv in %s to a program this program cannot name statically (%q), so whether the posture survives to the tests is unknown: %s",
				s.target, s.root, wrapperRel, where, exe.value, body))
		}
		base := filepath.Base(exe.value)
		switch {
		case shellRunners[base]:
			// Still a runner: the words after it are a command, not its data. Keep walking; the
			// posture is re-checked at the TOP of the next iteration, which is the only place it
			// is checked. Doing it here as well printed the same finding three times over
			// `wrapper env FOO=1 env OLIVARES_TEST_POSTGRES_DSN=broken go test …` — measured.
			// `at` is strictly greater than the head index whenever the head is a runner, so the
			// walk always advances and the `env env` spin cannot come back through this door.
			rest = rest[at:]
		case inertCommands[base] || argvTerminals[base] != "":
			return findings // reviewed as taking the rest of the argv as its OWN data
		case localFuncs[base]:
			// A FUNCTION DEFINED IN THIS VERY BODY is not a program nobody has read: its `{ … }`
			// is handed out by splitShell as ordinary commands and every one of them is walked
			// right here. Deciding it by shape rather than by a table matters — a table would
			// hold this repository's own identifiers and rot the day one is renamed.
			// RESIDUAL, named: a local function that execs its OWN argv (`f() { "$@"; }`) would
			// pass its caller's words on, and this arm stops at the call. What still covers that
			// is posturePreserved, which reads the words of every command in the chain and does
			// not care which verb is about to consume them.
			return findings
		default:
			if rel, err := filepath.Rel(s.taskRoot, resolveUnder(exe.value, s.dir)); err == nil {
				if name := filepath.ToSlash(rel); argvRunnerScripts[name] != "" {
					findings = append(findings, s.argvRunnerKeepsItsPremise(name, where, body, depth)...)
					rest = rest[at+1:] // it RUNS what follows it: keep walking that
					if len(rest) == 0 {
						return findings
					}
					continue
				}
			}
			// THE FAIL-OPEN THAT WAS HERE, and it is the third time this class has been paid for
			// in this file. The walk used to `return findings` at the first executable outside
			// shellRunners — "a real program: the rest are its own arguments". That sentence is
			// only true of a program REVIEWED to be one. The sixteenth contrast wrote
			// `… with-pg-env.sh env WRAPPER_ENTERED=inside nice env -u <the three DSNs> go test …`
			// — every word valid, go-task exit 0, the executed `go` reporting all three DSNs
			// absent — and this program exited 0 while printing that every go test was wrapped.
			// `nice` is the example, not the class: any ordinary program that execs its argv does
			// it. The list that made the old rule wrong enumerated what is DANGEROUS
			// (shellRunners); what a gate can enumerate is what is PERMITTED, so an unrecognized
			// head is UNVERIFIED here exactly as it already is in recipeScan.shell.
			unverified(fmt.Sprintf("target %s (reached from race target %s) hands %s's argv in %s to %q, which this program does not model — whether it runs the words after it (and so whether the posture the wrapper decided survives to the tests) is UNKNOWN. Classify it in inertCommands, argvTerminals or argvRunnerScripts (one line, with the reason) before trusting this gate: %s",
				s.target, s.root, wrapperRel, where, exe.value, body))
		}
	}
}

// argvTerminals are the programs a wrapper's argv may END at: each one is REVIEWED as taking the
// words that follow it as its own data rather than running one of them, so the posture the
// wrapper decided is still in force when it starts.
//
// It is a separate table from inertCommands because it answers a different question. inertCommands
// says "this starts nothing at all", which is why `echo go test …` is not an invocation. This says
// "whatever this starts, it does not start a WORD OF THIS ARGV" — true of the Go tool, which is
// the whole point of the argv, and false of `echo`'s opposite number, an ordinary program nobody
// has read.
var argvTerminals = map[string]string{
	"go": "the Go tool builds and runs a test binary from the packages named in its argv; it does not exec an argv word as a command",
	// `"$@"` is the reviewed argv-runner script HANDING BACK the argv this walk is already
	// following: scripts/go-work-each.sh ends at `( cd "${m}" && "$@" )`. It introduces no new
	// program — the outer walk continues past the script into those same words and classifies
	// whatever they name. Stopping the inner read here is not a gap; it is the one place where
	// the two halves of the walk meet.
	"$@": "the script's own argv, which the outer walk continues through after this script",
	"$*": "the script's own argv, joined; same passthrough as `$@`",
}

// argvRunnerScripts are the repository scripts a race recipe may place INSIDE the wrapper's argv,
// with the reason the walk continues THROUGH each one instead of stopping at it.
//
// A script here is not excused from anything: it is read. argvRunnerKeepsItsPremise checks the
// premise each entry states — that the script adds no clearing or overriding of the namespace the
// wrapper just decided — with the same reader that judges the argv itself, and the walk then
// carries on down the argv the script will run.
var argvRunnerScripts = map[string]string{
	"scripts/go-work-each.sh": "runs the argv it is given once per go.work module (`( cd \"${m}\" && \"$@\" )`) and touches no OLIVARES_* variable of its own",
}

// argvRunnerKeepsItsPremise enforces what an argvRunnerScripts entry claims, rather than trusting
// it. The premise is about the environment namespace the wrapper decided, so the check is the same
// posture reader used on the argv, run over the script's own body.
// The NAME is resolved against the Taskfile's directory (that is where the recipe's relative path
// points) but the FILE is read from the repository root — exactly as helperKeepsItsPremise does,
// and for the same reason: every fixture in scripts/test-pg-test-env.sh is a mutated Taskfile in a
// temporary directory whose reviewed scripts live back in the real tree.
func (s recipeScan) argvRunnerKeepsItsPremise(name, where, body string, depth int) []string {
	path := filepath.Join(s.repoRoot, filepath.FromSlash(name))
	raw, err := os.ReadFile(path)
	if err != nil {
		unverified(fmt.Sprintf("target %s (reached from race target %s) runs %s inside %s's argv in %s, which this program cannot read (%v); its premise that it leaves the decided posture alone is therefore unchecked", s.target, s.root, name, wrapperRel, where, err))
	}
	return s.underWrapperShell(string(raw), where+" (inside "+name+", which the wrapper's argv runs)", depth+1)
}

// underWrapperShell reads a shell body the wrapper execs — a `-c` argument of its argv. A `go test`
// here is WRAPPED and fine; what is not fine is this body undoing the decision before reaching it.
//
// IT WALKS EACH COMMAND, and it did not until 2026-08-05. It read only the HEAD of each command,
// through recipeExecutable, and acted on two of the four answers — so the fix that had just closed
// `wrapper env … <ordinary> env -u <DSN> go test` on the argv left the identical input passing one
// quote deeper: `wrapper bash -c '<ordinary> env -u OLIVARES_TEST_POSTGRES_DSN go test …'` returned
// exit 0. That is this session's own repair regenerating the defect it was repairing, one function
// along, which is why the walk is now ONE function (argvChain) called from both sides rather than
// two readers that agree until somebody hardens one of them.
func (s recipeScan) underWrapperShell(body, where string, depth int) []string {
	if depth > scanDepth {
		unverified(fmt.Sprintf("target %s (reached from race target %s) nests shell more than %d levels deep inside %s's argv in %s", s.target, s.root, scanDepth, wrapperRel, where))
	}
	var findings []string
	for _, sub := range commandSubstitutions(body) {
		findings = append(findings, s.underWrapperShell(sub, where+" (command substitution)", depth+1)...)
	}
	funcs := map[string]bool{}
	for _, raw := range splitShell(body, nil) {
		for _, w := range raw {
			if name, ok := strings.CutSuffix(w.value, "()"); ok && isFunctionDefinition(w.value) {
				funcs[name] = true
			}
		}
	}
	for _, raw := range splitShell(body, nil) {
		cmd, isCommand := commandAfterSyntax(raw)
		if !isCommand {
			continue // shell grammar: `fi`, `done`, a `for … in` word list
		}
		findings = append(findings, s.argvChain(cmd, where, body, depth, funcs)...)
	}
	return findings
}

// posturePreserved reports the words of a wrapped command that clear or override a variable the
// wrapper decided. `unset` and an assignment are the two spellings a static reader can see; an
// option that does it (`env -u`, `env -i`) is refused by the option rule instead, because
// modeling every runner's flags is not something this program promises.
func (s recipeScan) posturePreserved(cmd []shellWord, where, body string) []string {
	var out []string
	clearing := commandIs(cmd, "unset")
	headBase := ""
	if h, ok := commandHead(cmd); ok {
		headBase = filepath.Base(h.value)
	}
	// A CLEAR OR A BIND WHOSE TARGET THIS PROGRAM CANNOT NAME IS "I COULD NOT LOOK", and this file
	// has one answer for that. The loop below skips unresolvable words, which is right for an
	// ordinary command — a CI shell is full of variables that decide nothing here — and wrong for
	// the two verbs that act on the namespace by name. Measured 2026-08-05: three decided DSNs put
	// in an array and cleared by `for n in "${names[@]}"; do unset "$n"; done` returned checker 0
	// with the executed `go` reporting all three absent, from INSIDE the wrapper. Neither the verb
	// rule nor the naming rule saw it: the verb was visible and its object was not.
	if clearing || assignBuiltins[headBase] {
		for _, w := range cmd {
			if w.ok {
				continue
			}
			// THE NAME IS WHAT MATTERS, NOT THE VALUE. `local mod="${1#./}"` is unresolvable and
			// harmless: which variable it binds is written right there, and only what goes INTO it
			// is unknown. `unset "$n"` is the opposite — the verb is visible and its target is
			// not — and that is the one this denies.
			if k, _, isAssign := strings.Cut(w.value, "="); isAssign && k != "" {
				continue
			}
			unverified(fmt.Sprintf("target %s (reached from race target %s) has `%s` act on a NAME this program cannot resolve statically inside %s's argv in %s, so whether the posture the wrapper decided survives to the tests is unknown: %s",
				s.target, s.root, headBase, wrapperRel, where, body))
		}
	}
	for _, w := range cmd {
		if !w.ok {
			continue
		}
		if k, _, isAssign := strings.Cut(w.value, "="); isAssign && strings.HasPrefix(k, wrapperEnvPrefix) {
			out = append(out, fmt.Sprintf("target %s (reached from race target %s) sets %s inside %s's argv in %s, overriding the posture the wrapper had just decided: %s", s.target, s.root, k, wrapperRel, where, body))
			continue
		}
		if clearing && strings.HasPrefix(w.value, wrapperEnvPrefix) {
			out = append(out, fmt.Sprintf("target %s (reached from race target %s) unsets %s inside %s's argv in %s, so the tests run with no posture and skip in silence: %s", s.target, s.root, w.value, wrapperRel, where, body))
			continue
		}
		// AND THE RULE THAT DOES NOT DEPEND ON Recognizing THE VERB. The two above key on HOW the
		// namespace is disturbed — an assignment, an `unset` head — and every round of this review
		// has found another spelling: `env -u`, then the same behind an ordinary executable, then
		// the same inside a reviewed script. There are unboundedly many ways to say "remove this
		// variable" and they all have to NAME it, so this keys on the OBJECT instead: a bare word
		// naming something in the namespace the wrapper just decided, in a command that is not
		// known to take its words as data, is a finding. Nothing under the wrapper has a reason to
		// name a decided variable to a program that may act on it.
		//
		// It is deliberately BELT AND BRACES rather than a replacement: `env -i` names nothing at
		// all and is caught by the option rule, and an unrecognized head is caught by the walk.
		// This is the arm that still holds when a head is recognized and the verb is not.
		if !inertCommands[headBase] && strings.HasPrefix(w.value, wrapperEnvPrefix) && !strings.Contains(w.value, "=") {
			out = append(out, fmt.Sprintf("target %s (reached from race target %s) names %s to %q inside %s's argv in %s, which is not a command known to take its words as data — whatever it does with the variable, the posture the wrapper decided is no longer certain to reach the tests: %s", s.target, s.root, w.value, headBase, wrapperRel, where, body))
		}
	}
	return out
}

// shell classifies EVERY command of one shell body a race target executes, and that word —
// every — is the difference between this and the twelve versions before it.
//
// THE OLD RULE WAS: find the commands that visibly run `go test`, check those, IGNORE the rest.
// Deny-closed over keys, wide open over commands. The fourteenth contrast walked through the
// gap five times with valid, executed go-task input: a `defer:` calling another target, a shell
// whose executable is `task` reaching an `includes:` file, a helper script, and the same helper
// named from `vars.<name>.sh` and `env.<name>.sh`. Every one produced exit 0 and an unwrapped
// `go test` running. It is the same defect fixed one level up — the table classified the
// keys while the executed graph had shapes no key named — so it gets the same fix: the default
// is inverted here too.
//
// A command is now one of these and NOTHING else:
//
//   - it ENTERS THE CANONICAL WRAPPER. Then everything downstream of it is wrapped, whatever
//     that is: with-pg-env.sh decides the Postgres posture and `exec`s its argv, so a program
//     it starts cannot escape the decision. This is why the real recipes need no reading past
//     the wrapper, and it is the only sound reason not to read them.
//   - it visibly RUNS `go test` outside the wrapper. A finding — or UNVERIFIED when a word of
//     it is unresolvable, because then whether it entered the wrapper is not known, and saying
//     "it did not" would be a claim this program cannot make either.
//   - it is `task`, and DELEGATES to targets this walk follows.
//   - it `source`s a file, which can select the LOCAL regime under the wrapper's own feet.
//   - it is INERT: it takes its arguments as data and starts nothing.
//   - it is a reviewed entry of recipeHelpers.
//
// Anything else is UNVERIFIED, BY NAME. Not because it is suspicious — because a program that
// starts other programs is a program this gate has not read, and this layer is the one that
// claims completeness.
func (s recipeScan) shell(body, where string, depth int) ([]string, []string, bool) {
	if depth > scanDepth {
		unverified(fmt.Sprintf("target %s (reached from race target %s) nests shell more than %d levels deep in %s; what it runs down there is unread", s.target, s.root, scanDepth, where))
	}
	var findings, delegated []string
	sawGoTest := false
	collect := func(f, d []string, ran bool) {
		findings = append(findings, f...)
		delegated = append(delegated, d...)
		sawGoTest = sawGoTest || ran
	}
	// A command substitution executes before the command that holds it, and it is shell.
	for _, sub := range commandSubstitutions(body) {
		collect(s.shell(sub, where+" (command substitution)", depth+1))
	}
commands:
	for _, cmd := range splitShell(body, nil) {
		// LAYER 1 CLAIMS COMPLETENESS, so anything it cannot read is UNVERIFIED — never
		// skipped. A command whose executable this program cannot resolve statically may be
		// a `go test` wearing a variable: `T='go test'; cd modules && $T ./...` runs the
		// tests and leaves nothing for commandRunsGoTest to recognize.
		//
		// That mutant DID exit 1 before this check existed — but the finding came from the
		// second layer, the workflow net, whose text search still saw the literal `go test`
		// in the recipe. Layer 1 stayed silent. Reading a green out of that arrangement is
		// exactly the mistake this whole split was made to stop: the net is declared
		// incomplete, so it is not allowed to be what catches something for the guarantee.
		if head, ok := commandHead(cmd); ok && !head.ok {
			unverified(fmt.Sprintf("target %s (reached from race target %s) runs a command in %s whose executable cannot be resolved statically, so whether it runs go test is unknown: %s", s.target, s.root, where, body))
		}
		runsTests := commandRunsGoTest(cmd)
		sawGoTest = sawGoTest || runsTests

		kind, inline, exe, _ := recipeExecutable(cmd)
		if kind == execInline {
			if !exe.ok {
				unverified(fmt.Sprintf("target %s (reached from race target %s) hands a shell to `-c` in %s that this program cannot resolve statically, so what it runs is unknown: %s", s.target, s.root, where, body))
			}
			collect(s.shell(inline, where+" (via -c)", depth+1))
			continue
		}
		if kind == execOption {
			unverified(fmt.Sprintf("target %s (reached from race target %s) passes the option %q to %q in %s; this program models only `-c`, and an option it does not model may consume the very word that would name %s, so what actually runs is unknown: %s",
				s.target, s.root, inline, filepath.Base(cmd[headIndex(cmd)].value), where, wrapperRel, body))
		}
		if kind == execNone {
			continue // nothing but assignments; their substitutions were read above
		}
		if !exe.ok {
			unverified(fmt.Sprintf("target %s (reached from race target %s) hands work in %s to a program whose path this program cannot resolve statically (%q): whether that path is %s, and whether it runs a go test, are both unknown: %s", s.target, s.root, where, exe.value, wrapperRel, body))
		}

		// UNDER THE WRAPPER. with-pg-env.sh decides the Postgres posture and execs its argv, so
		// nothing it starts can escape that decision — which is why this arm needs to read no
		// further, and why identity here is by RESOLVED FILE and never by name.
		//
		// THE LIMIT OF THAT ARGUMENT, stated rather than implied away. What the wrapper cannot
		// defend against is its OWN argv undoing the decision after it: `env -u
		// OLIVARES_TEST_POSTGRES_DSN`, or an `unset` inside a `bash -c` it is handed. Neither
		// the wrapper nor this program stops that, and pretending otherwise would be a claim
		// wider than the mechanism — the defect class this file exists to avoid. It is a
		// different shape from every escape measured so far: those HID a test from the gate,
		// while this one would be a recipe deliberately clearing the posture it had just asked
		// for, in the argv, in the diff, with nothing else to gain. Recorded as a known limit;
		// closing it needs a rule about which variables an argv may touch, and this program has
		// no such rule today.
		if sameFile(resolveUnder(exe.value, s.dir), filepath.Join(s.taskRoot, wrapperRel)) {
			findings = append(findings, s.underWrapper(cmd, where, body, depth)...)
			continue
		}

		if runsTests {
			for _, w := range cmd {
				if w.ok {
					continue
				}
				unverified(fmt.Sprintf("target %s (reached from race target %s) runs go test in %s with a word this program cannot resolve statically, so whether it entered %s is unknown — and reporting it unwrapped would be a claim this program cannot make either: %s", s.target, s.root, where, wrapperRel, body))
			}
			// SAY WHICH FILE, when the command names the wrapper's basename and means another
			// one. "runs go test WITHOUT with-pg-env.sh" over a line that spells out
			// `scripts/with-pg-env.sh` reads as a contradiction and teaches nobody where the
			// recipe actually points — which, under a `dir:`, is the whole finding.
			for _, w := range cmd {
				if filepath.Base(w.value) != filepath.Base(wrapperRel) {
					continue
				}
				findings = append(findings, fmt.Sprintf("target %s (reached from race target %s) runs go test in %s behind %q, which resolves to %s and NOT to the canonical %s: a file with the wrapper's name is not the wrapper: %s",
					s.target, s.root, where, w.value, resolveUnder(w.value, s.dir), filepath.Join(s.taskRoot, wrapperRel), body))
				continue commands
			}
			findings = append(findings, fmt.Sprintf("target %s (reached from race target %s) runs go test WITHOUT with-pg-env.sh in %s, so a job invoking it can skip its PostgreSQL tests in silence: %s", s.target, s.root, where, body))
			continue
		}

		base := filepath.Base(exe.value)
		if base == "task" {
			delegated = append(delegated, s.taskTargets(cmd, where, body)...)
			continue
		}
		if base == "source" || exe.value == "." {
			findings = append(findings, s.sourced(cmd, where, body)...)
			continue
		}
		if inertCommands[base] {
			continue
		}
		if rel, err := filepath.Rel(s.taskRoot, resolveUnder(exe.value, s.dir)); err == nil {
			if name := filepath.ToSlash(rel); recipeHelpers[name] != "" {
				s.helperKeepsItsPremise(name, where, body)
				continue
			}
		}
		unverified(fmt.Sprintf("target %s (reached from race target %s) runs %q in %s, which this program does not model — whether it starts a go test outside %s is UNKNOWN. Classify it in inertCommands or recipeHelpers (one line, with the reason) before trusting this gate: %s", s.target, s.root, exe.value, where, wrapperRel, body))
	}
	return findings, delegated, sawGoTest
}

// taskTargets reads the targets a `task …` command in a recipe invokes. A recipe that shells out
// to go-task is delegating, and the fourteenth contrast used exactly that to reach a target held
// in a top-level `includes:` — a file checkWiring never decodes. Following the name is what turns
// that into the honest answer: the target is absent from the Taskfile being inspected, so the
// walk says UNVERIFIED instead of exiting 0 over a `go test` it never saw.
//
// Deny-closed on flags: go-task's flag grammar (which options take a value, which change the
// Taskfile being read) is not modeled here, and guessing at it is how the workflow scanner lost
// six spellings in round 1.
func (s recipeScan) taskTargets(cmd []shellWord, where, body string) []string {
	argv := cmd[headIndex(cmd)+1:]
	if len(argv) == 0 {
		unverified(fmt.Sprintf("target %s (reached from race target %s) invokes `task` with no target in %s; go-task then runs its default target, which this program has not been pointed at: %s", s.target, s.root, where, body))
	}
	var out []string
	for _, a := range argv {
		if !a.ok {
			unverified(fmt.Sprintf("target %s (reached from race target %s) invokes `task` in %s with an argument this program cannot resolve statically; whether it names a target that runs go test is unknown: %s", s.target, s.root, where, body))
		}
		if strings.HasPrefix(a.value, "-") {
			unverified(fmt.Sprintf("target %s (reached from race target %s) invokes `task` in %s with the flag %q; this program does not model go-task's flag grammar, so which target runs is unknown: %s", s.target, s.root, where, a.value, body))
		}
		out = append(out, a.value)
	}
	return out
}

// sourced reads a file a recipe pulls into its own shell. `source` sets variables with nothing in
// the diff that looks like an assignment, and one of those variables turns the wrapper's refusal
// into a pass — see checkNoJobEnablesLocalRegime, which the fourteenth contrast defeated from the
// workflow side with `set -a; source ./local.env`.
func (s recipeScan) sourced(cmd []shellWord, where, body string) []string {
	argv := cmd[headIndex(cmd)+1:]
	if len(argv) == 0 || !argv[0].ok {
		unverified(fmt.Sprintf("target %s (reached from race target %s) sources a file in %s that this program cannot name statically; whether it sets %s is unknown: %s", s.target, s.root, where, localOptIn, body))
	}
	path := resolveUnder(argv[0].value, s.dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		unverified(fmt.Sprintf("target %s (reached from race target %s) sources %s in %s, which this program cannot read (%v); whether it sets %s is unknown", s.target, s.root, path, where, err, localOptIn))
	}
	if shellSetsOptIn(string(raw)) {
		return []string{fmt.Sprintf("target %s (reached from race target %s) sources %s, which sets %s: in the LOCAL regime an absent server is exit 0 and its PostgreSQL tests skip in silence", s.target, s.root, path, localOptIn)}
	}
	return nil
}

// shellSetsOptIn reports whether a shell body assigns the LOCAL opt-in, through a leading
// assignment or an `export`/`declare`/`typeset` of it. One predicate, used by the workflow
// scanner, by the sourced-file reader on both sides, and by nothing that duplicates it.
func shellSetsOptIn(body string) bool {
	for _, cmd := range splitShell(body, nil) {
		builtin := false
		for _, w := range cmd {
			if assignBuiltins[w.value] {
				builtin = true
				continue
			}
			if w.value == "--" || (strings.HasPrefix(w.value, "-") && len(w.value) <= 2) {
				continue
			}
			k, _, isAssign := strings.Cut(w.value, "=")
			if isAssign && k == localOptIn {
				return true
			}
			if !isAssign && !builtin {
				break // an ordinary command: assignments only LEAD it
			}
		}
	}
	return false
}

// assignBuiltins introduce assignments ANYWHERE in their argv, not only at the head of a command.
// `readonly` was missing and it is not exotic: `readonly OLIVARES_PG_LOCAL_DEFAULTS=1` in a sourced
// file sets the variable, `set -a` exports it, and this predicate walked straight past it —
// measured on 2026-08-04, checker exit 0 with the LOCAL regime selected. The list is a table for
// the same reason every other list here is one.
var assignBuiltins = map[string]bool{
	"export": true, "declare": true, "typeset": true, "readonly": true, "local": true,
}

// delegates returns every target a recipe hands work to BY NAME: the `- task: other` entries of
// `cmds`, and the `deps:` list, which go-task runs BEFORE the recipe. The other surfaces a target
// executes — `status:` and `preconditions:` — are shell, not target names, and shellSurfaces
// reads those. Between the two, what a target runs is: its recipe, its deps, its status and its
// preconditions.
//
// deps was missed by the first version and it is not exotic — test:release in this very Taskfile
// uses it. The ninth contrast hung an unwrapped `go test` off a `deps:` of a race target and the
// program exited 0, printing "every go test reached under …". A guarantee that inspects half of
// what a target runs is not a guarantee, and this is the load-bearing half of this program.
func delegates(task map[string]any) []string {
	var out []string
	for key, h := range raceTargetKeys {
		if !h.targets {
			continue
		}
		out = append(out, targetsFrom(task[key], !h.shell)...)
	}
	sort.Strings(out)
	return out
}

// checkNoJobEnablesLocalRegime keeps the premise the guarantee rests on from rotting.
//
// with-pg-env.sh only refuses to run tests without a Postgres posture in the fail-closed default
// and in PROMOTION. Under the LOCAL opt-in (OLIVARES_PG_LOCAL_DEFAULTS=1) an absent server is
// exit 0 and the child runs, so a real Postgres test reports SKIP and the suite reports PASS —
// which is precisely the silent skip this whole gate exists to prevent. That is not a defect in
// the wrapper: LOCAL exists so a developer without a server can still run the suite, and the
// pre-push hook is its one sanctioned caller.
//
// It IS a defect the moment CI enables it, so that is asserted rather than assumed.
//
// WHAT IT READS, exactly, because "at any level" was a claim this function did not honor. It
// reads the `env:` mappings at workflow, job and step level, AND the shell of every `run:` step
// for an inline assignment — `OLIVARES_PG_LOCAL_DEFAULTS=1 task …`, which the first version
// missed entirely: the eighth contrast mutated the canonical run to exactly that and the whole
// program still exited 0.
//
// AND THE FILES A STEP `source`s, which is where the fourteenth contrast defeated this function
// with valid, ordinary workflow YAML:
//
//	run: |
//	  set -a
//	  source ./local.env
//	  task test:race-hot:modules
//
// with `OLIVARES_PG_LOCAL_DEFAULTS=1` inside local.env. Nothing in that diff names the variable,
// the whole program exited 0, and the real wrapper then ran a test canary with NO PostgreSQL
// posture at all — the load-bearing premise green while the regime it asserts had been switched
// off. So a sourced file is READ, and one this program cannot read is UNVERIFIED: an unreadable
// file and a harmless one look identical from here, and only one of them is safe to report clean.
//
// The finding does not depend on `set -a`. A sourced assignment of this variable is reported
// whether or not the shell was told to export it: which of the two a run really does is a
// question about the whole step's shell, and the answer that costs a loud false red is the one
// this gate is allowed to get wrong.
//
// What it does NOT read: an assignment produced by something it cannot resolve statically —
// `export` inside a script this function does not follow, a value from a ${{ }} expression, a
// composite action's own steps. Those remain uncovered and are named here rather than implied
// away; the sentence "no job enables LOCAL" is only as strong as this list.
func checkNoJobEnablesLocalRegime(jobs map[string]any, wf map[string]any, workflowPath, repoRoot string) []string {
	const optIn = localOptIn
	var findings []string
	if _, ok := mapEnv(wf, nil)[optIn]; ok {
		findings = append(findings, fmt.Sprintf("%s sets %s at workflow level: every job would run the LOCAL regime, where an absent server is exit 0 and Postgres tests skip in silence", workflowPath, optIn))
	}
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		jobMap, ok := jobs[id].(map[string]any)
		if !ok {
			continue // discoverLegs is the one that judges job shapes
		}
		if _, ok := mapEnv(jobMap, nil)[optIn]; ok {
			findings = append(findings, fmt.Sprintf("job %s sets %s: in the LOCAL regime an absent server is exit 0 and its Postgres tests skip in silence", id, optIn))
		}
		steps, ok := jobMap["steps"].([]any)
		if !ok {
			continue
		}
		for _, st := range steps {
			m, ok := st.(map[string]any)
			if !ok {
				continue
			}
			if _, ok := mapEnv(m, nil)[optIn]; ok {
				findings = append(findings, fmt.Sprintf("a step of job %s sets %s in its env: in the LOCAL regime an absent server is exit 0 and its Postgres tests skip in silence", id, optIn))
			}
			// And in the shell, where a leading VAR=VALUE is the same switch with no `env:` to
			// read. splitShell already resolves the leading assignments of every command.
			run, ok := m["run"].(string)
			if !ok {
				continue
			}
			// `export NAME=value` is the same switch with a keyword in front, and the first
			// version of this check broke on the keyword: the ninth contrast wrote
			// `export OLIVARES_PG_LOCAL_DEFAULTS=1` on its own line and the program exited 0.
			// `export -- NAME=value` is valid too, and ended the previous loop before it reached
			// the assignment — measured by the tenth contrast at exit 0. Both live in
			// shellSetsOptIn now, so the sourced-file arm below cannot drift away from this one.
			if shellSetsOptIn(run) {
				findings = append(findings, fmt.Sprintf("a step of job %s sets %s in its shell: in the LOCAL regime an absent server is exit 0 and its Postgres tests skip in silence", id, optIn))
			}
			for _, cmd := range splitShell(run, nil) {
				head, ok := commandHead(cmd)
				if !ok || !head.ok || (filepath.Base(head.value) != "source" && head.value != ".") {
					continue
				}
				argv := cmd[headIndex(cmd)+1:]
				if len(argv) == 0 || !argv[0].ok {
					unverified(fmt.Sprintf("a step of job %s sources a file this program cannot name statically; whether it sets %s is unknown", id, optIn))
				}
				path := resolveUnder(argv[0].value, repoRoot)
				raw, err := os.ReadFile(path)
				if err != nil {
					unverified(fmt.Sprintf("a step of job %s sources %s, which this program cannot read (%v); whether it sets %s is unknown", id, path, err, optIn))
				}
				if shellSetsOptIn(string(raw)) {
					findings = append(findings, fmt.Sprintf("a step of job %s sources %s, which sets %s: in the LOCAL regime an absent server is exit 0 and its Postgres tests skip in silence", id, path, optIn))
				}
			}
		}
	}
	return findings
}

// repoRootFor resolves the directory that a `uses: ./...` path is relative to. An explicit
// -root wins; otherwise it is inferred from the workflow's canonical location,
// <root>/.github/workflows/<file>.yml. Fixtures that copy a workflow elsewhere must pass
// -root, and they get an UNVERIFIED naming the directory searched if they do not.
func repoRootFor(workflowPath, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(workflowPath)))
}

func checkWiring(workflowPath, taskfilePath, onlyJob, onlyTask, repoRoot string) {
	wf := loadMap(workflowPath)
	tf := loadMap(taskfilePath)
	jobs := section(wf, "jobs", workflowPath)
	tasks := section(tf, "tasks", taskfilePath)

	var findings []string

	// THE LOAD-BEARING CHECKS, and they run even when a single -job/-task pair was requested.
	// Everything below this line is the second layer.
	findings = append(findings, checkEveryRaceTargetIsWrapped(tasks, taskfilePath, repoRoot)...)
	findings = append(findings, checkNoJobEnablesLocalRegime(jobs, wf, workflowPath, repoRoot)...)

	var want []leg
	if onlyJob != "" {
		want = []leg{{job: onlyJob, task: onlyTask, what: onlyJob}}
	} else {
		want = discoverLegs(wf, jobs, workflowPath, repoRoot)
		if len(want) == 0 {
			// Not "no legs to check" — UNOBSERVABLE. A workflow whose -race steps this
			// program cannot see reports the absence of findings, which is the one answer
			// it must never give for the one input it exists to catch.
			unverified(fmt.Sprintf("no `%s*` task invocation found in any job of %s — wrong file, or the steps moved", racePrefix, workflowPath))
		}
		// The inventory is no longer the source of coverage, but it is still an assertion:
		// a leg that DISAPPEARS from the workflow is as interesting as one that appears
		// unregistered, and discovery alone cannot notice an absence.
		for _, known := range legs {
			present := false
			for _, d := range want {
				if d.job == known.job && d.task == known.task {
					present = true
					break
				}
			}
			if !present {
				findings = append(findings, fmt.Sprintf("inventoried leg %s <- %s is no longer invoked anywhere in %s", known.job, known.task, workflowPath))
			}
		}
	}

	for _, l := range want {
		jobRaw, ok := jobs[l.job]
		if !ok {
			unverified(fmt.Sprintf("job %q is absent from %s", l.job, workflowPath))
		}
		jobMap, ok := jobRaw.(map[string]any)
		if !ok {
			unverified(fmt.Sprintf("job %q in %s is not a mapping", l.job, workflowPath))
		}
		taskRaw, ok := tasks[l.task]
		if !ok {
			unverified(fmt.Sprintf("task %q is absent from %s", l.task, taskfilePath))
		}
		taskMap, ok := taskRaw.(map[string]any)
		if !ok {
			unverified(fmt.Sprintf("task %q in %s is not a mapping", l.task, taskfilePath))
		}

		svcs := pgServices(jobMap)
		if len(svcs) == 0 {
			findings = append(findings, fmt.Sprintf("job %s declares no `postgres` service, but it races %s", l.job, l.what))
		}
		for name, image := range svcs {
			if !strings.Contains(image, "@sha256:") {
				findings = append(findings, fmt.Sprintf("job %s service %s is not pinned by digest (%q)", l.job, name, image))
			}
		}

		var goTests []string
		for _, t := range cmdTexts(taskMap) {
			if strings.Contains(t, "go test") {
				goTests = append(goTests, t)
			}
		}
		if len(goTests) == 0 {
			// Not "wired" — UNOBSERVABLE. A recipe that no longer runs `go test` may be
			// perfectly correct, but this program can no longer verify anything about it
			// and must say so rather than report the absence of findings as a pass.
			unverified(fmt.Sprintf("task %s runs no `go test` command to inspect", l.task))
		}
		for _, t := range goTests {
			if !strings.Contains(t, "with-pg-env.sh") {
				findings = append(findings, fmt.Sprintf("task %s runs go test without with-pg-env.sh: %s", l.task, t))
			}
		}
	}

	if len(findings) > 0 {
		// ONE LINE PER DISTINCT FINDING. The argv walk re-checks the posture at every step of the
		// chain, so a clearing word near the end of an argv is seen once per step and was printed
		// three times for `wrapper env FOO=1 env OLIVARES_TEST_POSTGRES_DSN=broken go test …`. The
		// duplicates carry no information — same target, same root, same word — and a gate whose
		// output has to be READ is a gate whose output should not repeat itself. Two findings that
		// differ in any byte, including which race target they were reached from, stay apart.
		sort.Strings(findings)
		seen := ""
		for i, f := range findings {
			if i > 0 && f == seen {
				continue
			}
			seen = f
			fmt.Fprintf(os.Stderr, "        %s\n", f)
		}
		os.Exit(exitFinding)
	}
	for _, l := range want {
		fmt.Printf("        %s <- %s: postgres service + with-pg-env.sh\n", l.job, l.task)
	}
	{
		var wrapped, viaExemption []string
		for _, t := range verifiedTargets {
			if exemptedRoots[t] {
				viaExemption = append(viaExemption, t)
			} else {
				wrapped = append(wrapped, t)
			}
		}
		fmt.Printf("        layer 1: %d `%s*` target(s) in %s inspected\n",
			len(verifiedTargets), racePrefix, filepath.Base(taskfilePath))
		if len(wrapped) > 0 {
			fmt.Printf("        layer 1: every go test reached under %s goes through with-pg-env.sh\n",
				strings.Join(wrapped, ", "))
		}
		if len(viaExemption) > 0 {
			fmt.Printf("        layer 1: %s reached an entry point on the reviewed exemption list, so the walk stopped there — NOT a wrapper claim\n",
				strings.Join(viaExemption, ", "))
		}
	}
	for _, e := range exempted {
		fmt.Printf("        %s\n", e)
	}
	os.Exit(exitOK)
}

// checkImageAgreement is the drift guard for a duplication this repository chose on
// purpose: race-modules provisions Postgres IDENTICALLY to race-rest rather than minimally,
// so that a DSN exported in one -race leg is never absent in the other. Two legs pinned to
// different Postgres images would be a test that passes in one job and fails in the other
// for a reason nobody would think to look for.
func checkImageAgreement(workflowPath string) {
	wf := loadMap(workflowPath)
	jobs := section(wf, "jobs", workflowPath)

	where := map[string][]string{}
	for jobID, raw := range jobs {
		jobMap, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for name, image := range pgServices(jobMap) {
			where[image] = append(where[image], jobID+"."+name)
		}
	}
	if len(where) == 0 {
		unverified(fmt.Sprintf("no `postgres` service found anywhere in %s — wrong file, or the services were renamed", workflowPath))
	}
	if len(where) > 1 {
		images := make([]string, 0, len(where))
		for image := range where {
			images = append(images, image)
		}
		sort.Strings(images)
		fmt.Fprintf(os.Stderr, "        Postgres services in %s disagree on the image:\n", workflowPath)
		for _, image := range images {
			sites := where[image]
			sort.Strings(sites)
			fmt.Fprintf(os.Stderr, "          %s <- %s\n", image, strings.Join(sites, ", "))
		}
		os.Exit(exitFinding)
	}
	for image, sites := range where {
		fmt.Printf("        %d Postgres service(s) agree on %s\n", len(sites), image)
	}
	os.Exit(exitOK)
}
