// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"errors"
	"strings"
)

// ReviewableShellCommand refuses a masked command when masking can hide shell
// operations. The result is display-only: never execute the redacted string.
// A credential-shaped value can contain executable shell syntax. Never ask a
// person to approve a command whose operators or expansions were hidden by the
// redactor. Refusing preserves both the credential and the review boundary.
// Only leading secret assignments or curl credential/header values and URL
// userinfo may be masked. Unknown operand contexts refuse before human review.
func ReviewableShellCommand(command, redacted string) (string, error) {
	if command != redacted {
		display := shellReviewNamedMarkers(redacted)
		programs, known := shellReviewPrograms(command)
		shown, shownKnown := shellReviewPrograms(display)
		if strings.ContainsAny(command, "\\") || strings.Contains(command, "((") || !known || !shownKnown || programs != shown || shellReviewSyntax(command, false) != shellReviewSyntax(display, true) {
			return "", errors.New("command cannot be reviewed because redaction would hide shell syntax")
		}
		if !shellReviewCredentialPositions(command, display) {
			return "", errors.New("command not reviewable: masking is outside a credential data value")
		}
	}
	return redacted, nil
}

// Include program names, not just punctuation: a token-shaped executable must
// not disappear into a credential placeholder. Splitting conservatively can
// refuse quoted literals; it cannot grant a masked operation. Commands that
// interpret their arguments as another command require an unmasked review.
func shellReviewPrograms(command string) (string, bool) {
	var programs strings.Builder
	for _, part := range shellReviewSegments(command) {
		tokens, ambiguous := tokenizeBashCommand(part)
		if ambiguous {
			return "", false
		}
		for _, token := range tokens {
			arg := stripBashEnclosingQuotes(token.raw)
			if !bashEnvAssignment(token.raw) && bashTokenUsesPartialQuotes(token.raw) {
				return "", false
			}
			if strings.HasPrefix(arg, "--eval") || strings.HasPrefix(arg, "--command") ||
				strings.HasPrefix(arg, "-c") || strings.HasPrefix(arg, "-e") || strings.HasPrefix(arg, "-E") || strings.HasPrefix(arg, "-r") {
				// Unknown interpreter names must not bypass the guard through a
				// familiar code flag. Refuse conservatively without option parsing.
				return "", false
			}
		}
		for _, token := range tokens {
			word := stripBashEnclosingQuotes(token.raw)
			if bashEnvAssignment(token.raw) {
				continue
			}
			if bashTokenUsesPartialQuotes(token.raw) || strings.ContainsAny(word, "$`*?[]{}\\") {
				return "", false
			}
			if shellReviewControlWord(token.raw) {
				continue
			}
			switch word {
			case "eval", "exec", "command", "builtin", "env", "source", ".", "sh", "bash", "time", "coproc", "function",
				"trap", "alias", "bind", "enable", "fc", "history", "mapfile", "readarray",
				"complete", "compgen", "jobs", "hash", "let", "declare", "typeset", "local":
				return "", false
			}
			if strings.HasSuffix(word, "/sh") || strings.HasSuffix(word, "/bash") {
				return "", false
			}
			program := strings.ToLower(word[strings.LastIndexByte(word, '/')+1:])
			program = strings.TrimSuffix(program, ".exe")
			if strings.HasPrefix(program, "python") {
				return "", false
			}
			switch program {
			case "node", "nodejs", "deno", "bun", "perl", "ruby", "php", "lua", "luajit",
				"osascript", "pwsh", "powershell", "awk", "gawk", "mawk", "sed", "jq":
				return "", false
			}
			programs.WriteString(token.raw)
			programs.WriteByte(0)
			break
		}
	}
	return programs.String(), true
}

func shellReviewSyntax(command string, masked bool) string {
	var syntax strings.Builder
	for i := 0; i < len(command); i++ {
		// Generated placeholders are display text, not shell glob syntax. The
		// original is never stripped: an agent cannot supply its own exemption.
		if masked && strings.HasPrefix(command[i:], "[REDACTED") {
			end := strings.IndexByte(command[i:], ']')
			if end >= 0 && (command[i+len("[REDACTED")] == ']' || command[i+len("[REDACTED")] == ':') {
				i += end
				continue
			}
		}
		if strings.ContainsRune("$`\\\"'(){}[]<>|&;*?~#\n\r", rune(command[i])) {
			syntax.WriteByte(command[i])
		}
	}
	return syntax.String()
}

type bashToken struct{ raw string }

func tokenizeBashCommand(command string) ([]bashToken, bool) {
	var tokens []bashToken
	var b strings.Builder
	var quote byte
	ambiguous := false
	flush := func() {
		if b.Len() == 0 {
			return
		}
		tokens = append(tokens, bashToken{raw: b.String()})
		b.Reset()
	}

	for i := 0; i < len(command); i++ {
		c := command[i]
		switch quote {
		case 0:
			if bashSpace(c) {
				flush()
				continue
			}
			if c == '\\' {
				// Unquoted backslash escapes the next byte to a literal (bash: \x -> x).
				// Without this the extracted token keeps the backslash, so `cat
				// /app/config/.en\v` scanned as the literal ".en\v" silently bypasses an
				// operator deny rule for ".env". A trailing backslash is an incomplete
				// line continuation -> ambiguous (fail to ASK).
				if i+1 >= len(command) {
					ambiguous = true
					continue
				}
				i++
				b.WriteByte(command[i])
				continue
			}
			if bashOutsideAmbiguousAt(command, i) {
				ambiguous = true
			}
			if c == '\'' || c == '"' {
				quote = c
			}
			b.WriteByte(c)
		case '\'':
			b.WriteByte(c)
			if c == '\'' {
				quote = 0
			}
		case '"':
			if c == '\\' && i+1 < len(command) && bashDoubleQuoteEscape(command[i+1]) {
				// Inside double quotes bash only treats \ as an escape before $ ` " \
				// or newline; the escaped byte is literal. Consume both so the resolved
				// path matches what exec sees.
				i++
				b.WriteByte(command[i])
				continue
			}
			if bashDoubleQuoteAmbiguousAt(command, i) {
				ambiguous = true
			}
			b.WriteByte(c)
			if c == '"' {
				quote = 0
			}
		}
	}
	if quote != 0 {
		ambiguous = true
	}
	flush()
	return tokens, ambiguous
}

func bashOutsideAmbiguousAt(s string, i int) bool {
	c := s[i]
	switch c {
	case '|', ';', '`', '>', '<':
		return true
	case '&':
		return i+1 < len(s) && s[i+1] == '&'
	case '$':
		return bashDollarAmbiguousAt(s, i)
	default:
		return false
	}
}

// bashDoubleQuoteEscape reports whether a backslash inside double quotes escapes the
// given following byte (bash: only $ ` " \ and newline are escapable in double quotes).
func bashDoubleQuoteEscape(next byte) bool {
	switch next {
	case '$', '`', '"', '\\', '\n':
		return true
	default:
		return false
	}
}

func bashDoubleQuoteAmbiguousAt(s string, i int) bool {
	switch s[i] {
	case '`':
		return true
	case '$':
		return bashDollarAmbiguousAt(s, i)
	default:
		return false
	}
}

func bashDollarAmbiguousAt(s string, i int) bool {
	if i+1 >= len(s) {
		return false
	}
	next := s[i+1]
	return next == '(' || next == '{' || bashNameStart(next)
}

func bashEnvAssignment(token string) bool {
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	if !bashNameStart(token[0]) {
		return false
	}
	for i := 1; i < eq; i++ {
		if !bashNamePart(token[i]) {
			return false
		}
	}
	return true
}

func bashTokenUsesPartialQuotes(token string) bool {
	if !strings.ContainsAny(token, `'"`) {
		return false
	}
	if len(token) >= 2 {
		q := token[0]
		if (q == '\'' || q == '"') && token[len(token)-1] == q && !strings.ContainsRune(token[1:len(token)-1], rune(q)) {
			return false
		}
	}
	return true
}

func stripBashEnclosingQuotes(token string) string {
	if len(token) < 2 {
		return token
	}
	q := token[0]
	if (q == '\'' || q == '"') && token[len(token)-1] == q {
		return token[1 : len(token)-1]
	}
	return token
}

func bashSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func bashNameStart(c byte) bool {
	return c == '_' || ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z')
}

func bashNamePart(c byte) bool {
	return bashNameStart(c) || ('0' <= c && c <= '9')
}

// Prove WHERE the difference is, rather than guessing which programs may execute
// an operand. Original and displayed tokens must agree outside one credential
// DATA value. Syntax/opaque-context checks above still refuse hidden operations.
func shellReviewCredentialPositions(command, redacted string) bool {
	original, displayed := shellReviewSegments(command), shellReviewSegments(redacted)
	if len(original) != len(displayed) {
		return false
	}
	for n, part := range original {
		tokens, ambiguous := tokenizeBashCommand(part)
		shown, shownAmbiguous := tokenizeBashCommand(displayed[n])
		if ambiguous || shownAmbiguous || len(tokens) != len(shown) {
			return false
		}
		program := -1
		for i, token := range tokens {
			if bashEnvAssignment(token.raw) || shellReviewControlWord(token.raw) {
				continue
			}
			program = i
			break
		}
		positions := shellReviewCredentialValues(tokens, program)
		for i, token := range tokens {
			if token.raw == shown[i].raw {
				continue
			}
			lo, hi, ok := positions[i].lo, positions[i].hi, positions[i].known
			if !ok || len(shown[i].raw) < lo+len(token.raw)-hi || !strings.HasPrefix(shown[i].raw, token.raw[:lo]) || !strings.HasSuffix(shown[i].raw, token.raw[hi:]) {
				return false
			}
		}
	}
	return true
}

func shellReviewControlWord(word string) bool {
	switch word {
	case "!", "if", "then", "else", "elif", "while", "until", "do":
		return true
	}
	return false
}

type shellReviewValue struct {
	lo, hi int
	known  bool
}

// Only a known non-delegating consumer can prove operand DATA. Its small exact
// grammar tracks consumed values, termination and uncertainty; an unfamiliar
// option never turns a following token into a credential value by coincidence.
func shellReviewCredentialValues(tokens []bashToken, program int) []shellReviewValue {
	positions := make([]shellReviewValue, len(tokens))
	if program < 0 {
		return positions
	}
	for i := 0; i < program; i++ {
		raw := tokens[i].raw
		if eq := strings.IndexByte(raw, '='); bashEnvAssignment(raw) && shellReviewSecretName(raw[:eq]) {
			positions[i] = shellReviewValue{eq + 1, len(raw), true}
		}
	}
	consumer := stripBashEnclosingQuotes(tokens[program].raw)
	consumer = consumer[strings.LastIndexByte(consumer, '/')+1:]
	if consumer != "curl" {
		return positions
	}
	operands := make([]shellReviewValue, len(tokens))
	for i := program + 1; i < len(tokens); i++ {
		raw := tokens[i].raw
		word := stripBashEnclosingQuotes(raw)
		offset := 0
		if raw != word {
			offset = 1
		}
		if eq := strings.IndexByte(word, '='); eq >= 0 && shellReviewCredentialOption(word[:eq]) {
			operands[i] = shellReviewValue{offset + eq + 1, offset + len(word), true}
			continue
		}
		if shellReviewCredentialOption(word) || word == "-H" || word == "--header" {
			header := word == "-H" || word == "--header"
			i++
			if i >= len(tokens) || strings.HasPrefix(stripBashEnclosingQuotes(tokens[i].raw), "-") {
				return positions
			}
			value := tokens[i].raw
			bare := stripBashEnclosingQuotes(value)
			at := 0
			if bare != value {
				at = 1
			}
			if header {
				operands[i] = shellReviewHeaderValue(bare, at)
			} else {
				operands[i] = shellReviewValue{at, at + len(bare), true}
			}
			continue
		}
		if strings.HasPrefix(word, "--header=") {
			operands[i] = shellReviewHeaderValue(word[len("--header="):], offset+len("--header="))
			continue
		}
		// Unknown options and `--` end the proof for ALL operands, even a prior
		// apparent credential flag. Leading secret assignments remain DATA.
		if strings.HasPrefix(word, "-") {
			return positions
		}
		if scheme := strings.Index(word, "://"); scheme > 0 && shellReviewURLScheme(word[:scheme]) {
			authority := word[scheme+3:]
			stop := strings.IndexAny(authority, "/?#")
			if stop < 0 {
				stop = len(authority)
			}
			if at := strings.LastIndexByte(authority[:stop], '@'); at > 0 && at+1 < stop {
				operands[i] = shellReviewValue{offset + scheme + 3, offset + scheme + 3 + at, true}
			}
		}
	}
	for i := program + 1; i < len(tokens); i++ {
		positions[i] = operands[i]
	}
	return positions
}

func shellReviewHeaderValue(word string, offset int) shellReviewValue {
	if colon := strings.IndexByte(word, ':'); colon >= 0 && shellReviewCredentialHeader(strings.TrimSpace(word[:colon])) {
		return shellReviewValue{offset + colon + 1, offset + len(word), true}
	}
	return shellReviewValue{}
}

func shellReviewSegments(command string) []string {
	return strings.FieldsFunc(command, func(c rune) bool { return strings.ContainsRune("|&;(){}\n\r", c) })
}

// Named per-launch markers are generated by the session redactor. Normalize only
// the DISPLAY for lexical proof so their internal space is not a shell token.
// Original input is never normalized and therefore cannot grant itself an exemption.
func shellReviewNamedMarkers(display string) string {
	var out strings.Builder
	for i := 0; i < len(display); i++ {
		if strings.HasPrefix(display[i:], "[secret ") {
			if end := strings.IndexByte(display[i:], ']'); end > len("[secret ") && shellReviewSecretMarkerName(display[i+len("[secret "):i+end]) {
				out.WriteString("[REDACTED]")
				i += end
				continue
			}
		}
		out.WriteByte(display[i])
	}
	return out.String()
}

func shellReviewSecretMarkerName(name string) bool {
	for _, c := range []byte(name) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '/' {
			continue
		}
		return false
	}
	return name != ""
}

func shellReviewSecretName(name string) bool {
	name = strings.ToUpper(name)
	for _, suffix := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "PASSWD", "PWD", "AUTH", "BEARER", "COOKIE"} {
		if name == suffix || strings.HasSuffix(name, "_"+suffix) {
			return true
		}
	}
	return false
}

func shellReviewCredentialOption(word string) bool {
	switch word {
	case "--password", "--token", "--secret", "--api-key", "--auth", "--bearer":
		return true
	}
	return false
}

func shellReviewCredentialHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "x-api-key", "cookie":
		return true
	}
	return false
}

func shellReviewURLScheme(scheme string) bool {
	for i, c := range []byte(scheme) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			continue
		}
		return false
	}
	return scheme != ""
}
