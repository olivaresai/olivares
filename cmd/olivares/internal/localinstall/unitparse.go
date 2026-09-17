// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"fmt"
	"regexp"
	"strings"
)

// UnitDataDir returns the data directory a rendered service definition
// executes THIS estate's engine with. Two things have to hold before a value
// counts, and the second is what makes it a witness rather than a mention:
//
//  1. it must sit in the directive of that format which actually starts the
//     service — systemd ExecStart= (line continuations joined, systemd quoting,
//     an empty assignment resets the list), OpenRC command_args= (the only
//     assignment openrc-run consumes; a command_args_base= counts only where a
//     command_args= assignment copies it in as the whole word
//     $command_args_base, which is how the packaged and signed-archive units
//     carry the serve line at top level and again in start_pre, and every
//     command_args= assignment must prove the same directory), or the first
//     ProgramArguments string of a launchd plist; and
//  2. that directive must run `program`, the executable this installation
//     recorded — the ExecStart= must be inside [Service] and start with it, the
//     OpenRC command_args= must sit beside a command= naming it, and the launchd
//     wrapper must be exactly it.
//
// Comments, Environment=, ExecStartPre=, sandbox directives, keys in another
// section and directives that run another program never count, whatever they
// mention. A definition that names no data directory, names several, uses the
// space-separated --data-dir form, or cannot be tokenised unambiguously is an
// error: an ambiguous witness never corroborates anything.
func UnitDataDir(init, body, program string) (string, error) {
	if program == "" {
		return "", fmt.Errorf("no expected program: a service definition can only witness the estate whose recorded executable it runs")
	}
	switch init {
	case "systemd":
		return systemdDataDir(body, program)
	case "openrc":
		return openrcDataDir(body, program)
	case "launchd":
		return launchdDataDir(body, program)
	}
	return "", fmt.Errorf("unsupported init adapter %q", init)
}

const dataDirFlag = "--data-dir="

func systemdDataDir(body, program string) (string, error) {
	var values []string
	section := ""
	outside, otherProgram := 0, ""
	for _, line := range unitLogicalLines(body) {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || trimmed[0] == '#' || trimmed[0] == ';' {
			continue
		}
		if trimmed[0] == '[' {
			// A section header is the whole line, [Name]; anything else is not a
			// header this parser understands, so what follows it counts as being
			// in no section at all rather than in the last one named.
			section = ""
			if name, rest, ok := strings.Cut(trimmed[1:], "]"); ok && strings.TrimSpace(rest) == "" {
				section = name
			}
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || strings.TrimSpace(key) != "ExecStart" {
			continue
		}
		if section != "Service" {
			// systemd only starts what [Service] declares. An ExecStart= anywhere
			// else is inert text, and it is named in the refusal because a unit
			// carrying one is a repair, not a witness.
			outside++
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			// systemd semantics: an empty assignment resets the command list.
			values = nil
			continue
		}
		args, err := splitSystemdCommandLine(value)
		if err != nil {
			return "", fmt.Errorf("ExecStart= cannot be parsed: %w", err)
		}
		if len(args) == 0 || args[0] != program {
			// Another executable's command line is not this estate's witness, and
			// neither is a systemd prefix (@ - : + !) on ours: the adapters never
			// render one, so an argv[0] that is not exactly the recorded binary is
			// reported instead of guessed at.
			if len(args) > 0 && otherProgram == "" {
				otherProgram = args[0]
			}
			continue
		}
		found, err := dataDirArguments(args)
		if err != nil {
			return "", fmt.Errorf("ExecStart=: %w", err)
		}
		values = append(values, found...)
	}
	dir, err := singleDataDir(values, "ExecStart=")
	if err != nil && len(values) == 0 {
		if otherProgram != "" {
			return "", fmt.Errorf("no ExecStart= in [Service] executes %s (it executes %q), so the unit does not witness this installation", program, otherProgram)
		}
		if outside > 0 {
			return "", fmt.Errorf("%w (%d ExecStart= assignment(s) sit outside [Service], where systemd never runs them)", err, outside)
		}
	}
	return dir, err
}

// unitLogicalLines joins backslash continuations the way systemd reads unit
// files: a line ending in a backslash continues on the next line, with the
// backslash replaced by a space.
func unitLogicalLines(body string) []string {
	var out []string
	var current strings.Builder
	continued := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		if continued {
			current.WriteString(" ")
			current.WriteString(strings.TrimLeft(line, " \t"))
		} else {
			current.WriteString(line)
		}
		if strings.HasSuffix(line, "\\") {
			s := current.String()
			current.Reset()
			current.WriteString(strings.TrimSuffix(s, "\\"))
			continued = true
			continue
		}
		out = append(out, current.String())
		current.Reset()
		continued = false
	}
	if continued {
		out = append(out, current.String())
	}
	return out
}

// splitSystemdCommandLine tokenises an ExecStart= value: whitespace separates
// words, double or single quotes protect whitespace within or around a word.
// Escape sequences are refused rather than guessed: the adapters never emit
// them and a hand-written one would make the witness ambiguous.
func splitSystemdCommandLine(value string) ([]string, error) {
	var args []string
	var word strings.Builder
	inWord := false
	var quote rune
	for _, r := range value {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' {
				return nil, fmt.Errorf("escape sequence inside quotes is not supported")
			} else {
				word.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			inWord = true
		case r == '\\':
			return nil, fmt.Errorf("escape sequence is not supported")
		case r == ' ' || r == '\t':
			if inWord {
				args = append(args, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unbalanced quote")
	}
	if inWord {
		args = append(args, word.String())
	}
	return args, nil
}

func dataDirArguments(args []string) ([]string, error) {
	var found []string
	for _, arg := range args {
		if arg == strings.TrimSuffix(dataDirFlag, "=") {
			return nil, fmt.Errorf("the space-separated --data-dir form is not accepted as a witness; use --data-dir=PATH")
		}
		if strings.HasPrefix(arg, dataDirFlag) {
			found = append(found, strings.TrimPrefix(arg, dataDirFlag))
		}
	}
	return found, nil
}

func singleDataDir(values []string, directive string) (string, error) {
	if len(values) == 0 {
		return "", fmt.Errorf("%s names no --data-dir", directive)
	}
	for _, v := range values[1:] {
		if v != values[0] {
			return "", fmt.Errorf("%s names several data directories (%q and %q); ambiguous", directive, values[0], v)
		}
	}
	if values[0] == "" {
		return "", fmt.Errorf("%s names an empty --data-dir", directive)
	}
	return values[0], nil
}

// openrcBaseReference reports whether word is the whole-word expansion by which
// the shipped OpenRC units copy command_args_base into command_args=, the only
// assignment openrc-run consumes: command_args="$command_args_base" at top
// level and again in start_pre. Nothing else makes command_args_base= a
// directive. A modified expansion such as ${command_args_base:-} is outside the
// template grammar and counts as operator runtime input like any other
// parameter, never as the copy.
func openrcBaseReference(word string) bool {
	return word == "$command_args_base" || word == "${command_args_base}"
}

func openrcDataDir(body, program string) (string, error) {
	var commands, baseValues []string
	// One entry per command_args= assignment, in file order: the data
	// directories that assignment proves through its own literal words and,
	// when it copies the base in, through what command_args_base= named by then.
	var assignments [][]string
	for _, raw := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		if value, ok := strings.CutPrefix(trimmed, "command="); ok {
			inner, _, err := unwrapShellString(value)
			if err != nil {
				return "", fmt.Errorf("command= cannot be parsed: %w", err)
			}
			commands = append(commands, inner)
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok || (key != "command_args" && key != "command_args_base") {
			continue
		}
		inner, literal, err := unwrapShellString(value)
		if err != nil {
			return "", fmt.Errorf("%s= cannot be parsed: %w", key, err)
		}
		var words []string
		copiesBase := false
		for _, word := range strings.Fields(inner) {
			if !strings.ContainsRune(word, '$') {
				words = append(words, word)
				continue
			}
			if literal {
				// The shell expands nothing inside single quotes: '$command_args_base'
				// is that text handed to the engine, never the copy the shipped units
				// make, and no template renders a literal dollar sign.
				return "", fmt.Errorf("%s= cannot be parsed: a dollar sign inside single quotes is literal text, not an expansion (%q)", key, word)
			}
			if !isShellParameter(word) {
				return "", fmt.Errorf("%s= cannot be parsed: embedded shell expansion in %q", key, word)
			}
			// A whole-word parameter expansion is either the copy of the base, the
			// shipped grammar, or operator runtime input such as
			// ${OLIVARES_EXTRA_ARGS:-} and $olivares_extra_args, never a literal
			// witness.
			if key == "command_args" && openrcBaseReference(word) {
				copiesBase = true
			}
		}
		found, err := dataDirArguments(words)
		if err != nil {
			return "", fmt.Errorf("%s=: %w", key, err)
		}
		if key == "command_args_base" {
			baseValues = append(baseValues, found...)
			continue
		}
		if copiesBase {
			found = append(found, baseValues...)
		}
		assignments = append(assignments, found)
	}
	// OpenRC runs whatever command_args= holds when start-stop-daemon is
	// invoked, and this parser does not interpret which assignment that is, so
	// every assignment has to prove the same directory. One that proves none
	// beside one that does — a copy of the base later reset, replaced by another
	// variable or by a literal without --data-dir — is a stale witness, and an
	// inconsistent unit corroborates nothing.
	var values []string
	provesNothing := 0
	for _, proved := range assignments {
		if len(proved) == 0 {
			provesNothing++
		}
		values = append(values, proved...)
	}
	if provesNothing > 0 && len(values) > 0 {
		return "", fmt.Errorf("command_args= names %q in one assignment and no --data-dir in another; ambiguous", values[0])
	}
	dir, err := singleDataDir(values, "command_args=")
	if err != nil {
		return "", err
	}
	// A command_args_base= that no assignment copies in is a mention, not a
	// directive OpenRC runs, so it never supplies the witness on its own: only
	// copied bases reached values above. One that names a DIFFERENT directory
	// still makes the unit inconsistent about this estate, which is ambiguity,
	// not silence.
	for _, b := range baseValues {
		if b != dir {
			return "", fmt.Errorf("command_args= names several data directories (%q and %q); ambiguous", dir, b)
		}
	}
	// command_args= is only this estate's witness when the command= beside it is
	// this estate's engine: OpenRC runs "$command $command_args".
	if len(commands) == 0 {
		return "", fmt.Errorf("command_args= names %q but no command= says which program receives it", dir)
	}
	for _, c := range commands[1:] {
		if c != commands[0] {
			return "", fmt.Errorf("command= is assigned several programs (%q and %q); ambiguous", commands[0], c)
		}
	}
	if commands[0] != program {
		return "", fmt.Errorf("command= runs %q, not %s, so the script does not witness this installation", commands[0], program)
	}
	return dir, nil
}

// unwrapShellString accepts one plainly quoted assignment value: "..." or '...'
// with no further quote inside, or a bare word without quotes or whitespace.
// literal reports single quotes, inside which the shell expands nothing.
func unwrapShellString(value string) (inner string, literal bool, err error) {
	if value == "" {
		return "", false, nil
	}
	switch value[0] {
	case '"', '\'':
		q := value[0]
		if len(value) < 2 || value[len(value)-1] != q || strings.ContainsRune(value[1:len(value)-1], rune(q)) {
			return "", false, fmt.Errorf("unbalanced or nested quotes")
		}
		inner = value[1 : len(value)-1]
		if strings.ContainsAny(inner, "\\`") {
			return "", false, fmt.Errorf("escape or command substitution inside the value is not supported")
		}
		return inner, q == '\'', nil
	}
	if strings.ContainsAny(value, " \t\"'\\$`") {
		return "", false, fmt.Errorf("unquoted value contains whitespace, quotes or expansion")
	}
	return value, false, nil
}

// isShellParameter reports whether word is one complete parameter expansion,
// ${NAME...} or $NAME, with no literal text attached.
func isShellParameter(word string) bool {
	if strings.HasPrefix(word, "${") {
		return strings.HasSuffix(word, "}") && strings.Count(word, "}") == 1
	}
	name := strings.TrimPrefix(word, "$")
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

var (
	xmlComment    = regexp.MustCompile(`(?s)<!--.*?-->`)
	programArgKey = "<key>ProgramArguments</key>"
	arrayOpen     = regexp.MustCompile(`(?s)<array\s*>`)
	stringElement = regexp.MustCompile(`(?s)<string\s*>(.*?)</string\s*>`)
)

func launchdDataDir(body, program string) (string, error) {
	body = xmlComment.ReplaceAllString(body, "")
	if n := strings.Count(body, programArgKey); n != 1 {
		return "", fmt.Errorf("plist must contain exactly one ProgramArguments key, found %d", n)
	}
	rest := body[strings.Index(body, programArgKey)+len(programArgKey):]
	open := arrayOpen.FindStringIndex(rest)
	if open == nil {
		return "", fmt.Errorf("ProgramArguments has no array")
	}
	rest = rest[open[1]:]
	end := strings.Index(rest, "</array>")
	if end < 0 {
		return "", fmt.Errorf("ProgramArguments array is not closed")
	}
	match := stringElement.FindStringSubmatch(rest[:end])
	if match == nil {
		return "", fmt.Errorf("ProgramArguments names no program")
	}
	first := strings.TrimSpace(match[1])
	if strings.ContainsAny(first, "&<>") {
		return "", fmt.Errorf("ProgramArguments program contains XML escapes; not accepted as a witness")
	}
	dir, ok := strings.CutSuffix(first, "/launchd-run.sh")
	if !ok || dir == "" {
		return "", fmt.Errorf("ProgramArguments program %q is not the Olivares launchd wrapper inside a data directory", first)
	}
	if first != program {
		return "", fmt.Errorf("ProgramArguments runs %q, not %s, so the plist does not witness this installation", first, program)
	}
	return dir, nil
}
