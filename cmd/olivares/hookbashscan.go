// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
)

const (
	hookBashPathReviewReason = "Bash command may touch a path-scoped resource; operator review required"
	hookBashPathDenyReason   = "blocked by path-scoped Bash policy"
)

type bashToken struct {
	raw string
}

func extractBashPaths(command string) (paths []string, ambiguous bool) {
	tokens, ambiguous := tokenizeBashCommand(strings.TrimSpace(command))
	if ambiguous {
		return nil, true
	}

	i := 0
	for i < len(tokens) && bashEnvAssignment(tokens[i].raw) {
		i++
	}
	if i < len(tokens) {
		i++ // program
	}

	for ; i < len(tokens); i++ {
		raw := tokens[i].raw
		if bashTokenUsesPartialQuotes(raw) {
			return nil, true
		}
		token := stripBashEnclosingQuotes(raw)
		if !bashTokenLooksPath(token) {
			continue
		}
		if strings.ContainsAny(token, "*?[]{}") {
			return nil, true
		}
		paths = append(paths, token)
	}
	return paths, false
}

func bashPathScan(pol hookPolicyDoc, in claude.HookDecisionInput, root string, base hookDisposition) hookDisposition {
	if in.ResourceKind != hookResourceKindShell || !hookPolicyPathScoped(pol) {
		return base
	}

	cmd, _ := in.RewriteBase()["command"].(string)
	paths, ambiguous := extractBashPaths(cmd)

	// guardHit is a Bash INVARIANT signal: the command could not be soundly resolved —
	// ambiguous tokenization, an unresolved `..` traversal, or a raw deny-pattern the
	// structured scan could not confirm. It forces an invariant ask (never shadowable in
	// observe), DOMINATES a rule-ask, and tightens a business base-deny to ClassInvariant —
	// so a path-scoped Bash rule can never be evaded through ambiguity under observe.
	// Computed for EVERY shell call, including one whose base already denies (the old
	// early-return on base==deny skipped this scan, which would let observe shadow a business
	// base-deny that hides a Bash ambiguity).
	// Structured scan. structuredDeny is a clean path-deny match (its FIRST reason wins, for
	// enforce parity); unknownDenyHit is a matched deny rule with an UNKNOWN decision string (a
	// config error); ruleAskHit is a clean path-ask; dotdotGuard is an unresolved `..` traversal.
	structuredDeny := false
	denyReason := ""
	unknownDenyHit := false
	ruleAskHit := false
	dotdotGuard := false

	for _, r := range pol.Rules {
		if !bashPathRuleApplies(r, in) {
			continue
		}
		recognized := normalizeDisposition(r.Decision) != ""
		dec := bashRuleDecision(r)
		for _, p := range paths {
			abs, ok := normalizePath(p, root)
			if !ok {
				if bashTokenHasDotDot(p) {
					dotdotGuard = true // an unresolved traversal is an invariant, not a business ask
				}
				continue
			}
			if !hookRulePathMatches(r, abs, root) {
				continue
			}
			switch dec {
			case claude.DecisionDeny:
				if !structuredDeny { // keep the FIRST matching deny rule's reason (enforce parity)
					structuredDeny = true
					denyReason = firstNonEmptyStr(r.Reason, hookBashPathDenyReason)
				}
				if !recognized {
					unknownDenyHit = true // a typo'd deny rule is a config error → deny-closed invariant
				}
			case claude.DecisionAsk:
				ruleAskHit = true
			}
		}
	}

	// The raw deny-pattern backstop is a FALLBACK for a deny the tokenizer could not resolve to
	// a matched path (an escaped/obfuscated path). It is meaningful ONLY when the structured scan
	// found NO deny — otherwise its literal is, by construction, already present in the command it
	// just matched, and would spuriously tag an authored (shadowable) deny as invariant. This
	// mirrors pre enforce, where the raw check was reachable only after the structured deny
	// short-circuit did NOT fire.
	rawGuard := !structuredDeny && bashRawDenyPatternHit(pol, in, cmd, root)

	// invariantGuard: the command could not be soundly resolved (ambiguous tokenization,
	// unresolved traversal, an evaded raw deny-pattern) OR a matched deny rule had an unknown
	// decision. Any of these forces ClassInvariant — never shadowable in observe — and DOMINATES
	// a business base-deny, a clean rule-deny and a rule-ask, so a path-scoped Bash rule can never
	// be evaded through ambiguity or a config error under observe.
	invariantGuard := ambiguous || dotdotGuard || rawGuard || unknownDenyHit

	// base already denies (evalHookPolicy/default): keep its decision+reason so ENFORCE is
	// byte-identical; TIGHTEN the class to invariant on ANY Bash invariant signal (incl. an
	// unknown-decision deny rule this scan matched — the escape the base-deny branch must close).
	if base.decision == claude.DecisionDeny {
		if invariantGuard {
			base.class = auth.ClassInvariant
		}
		return base
	}
	// A clean path-scoped deny rule matched (base was not a deny). A recognized deny is
	// ClassPolicy (shadowable); any invariant signal tightens it to ClassInvariant.
	if structuredDeny {
		cls := auth.ClassPolicy
		if invariantGuard {
			cls = auth.ClassInvariant
		}
		return hookDisposition{decision: claude.DecisionDeny, reason: denyReason, class: cls, source: shadowSourceBashPath}
	}
	// Ambiguity/guard (invariant) or a rule ask (policy) → ask; ambiguity dominates.
	if invariantGuard {
		return bashAskWithClass(base, auth.ClassInvariant)
	}
	if ruleAskHit {
		return bashAskWithClass(base, auth.ClassPolicy)
	}
	return base
}

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

func bashTokenLooksPath(token string) bool {
	return strings.Contains(token, "/") || strings.HasPrefix(token, "~")
}

func bashTokenHasDotDot(p string) bool {
	for _, segment := range strings.Split(p, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func bashPathRuleApplies(r hookPolicyRule, in claude.HookDecisionInput) bool {
	if !hookRulePathScoped(r) {
		return false
	}
	if r.Event != "" {
		if r.Event != in.Event {
			return false
		}
	} else if !claude.HookEnforcementFor(in.Event).ClassicGate {
		return false
	}
	if !toolGlob(r.Tool, in.Tool) {
		return false
	}
	if r.ResourceKind != "" && r.ResourceKind != hookResourceKindShell {
		return false
	}
	return r.Mode == ""
}

func bashRuleDecision(r hookPolicyRule) string {
	dec := normalizeDisposition(r.Decision)
	if dec == "" {
		return claude.DecisionDeny
	}
	return dec
}

func bashRawDenyPatternHit(pol hookPolicyDoc, in claude.HookDecisionInput, command, root string) bool {
	if command == "" {
		return false
	}
	for _, r := range pol.Rules {
		if !bashPathRuleApplies(r, in) || bashRuleDecision(r) != claude.DecisionDeny {
			continue
		}
		if subtree := bashRawSubtreePattern(r.Subtree, root); subtree != "" && strings.Contains(command, subtree) {
			return true
		}
		for _, glob := range r.Paths {
			if stem := bashGlobLiteralStem(glob); stem != "" && strings.Contains(command, stem) {
				return true
			}
		}
	}
	return false
}

func bashRawSubtreePattern(subtree, root string) string {
	subtree = strings.TrimSpace(subtree)
	if subtree == "" {
		return ""
	}
	if abs, ok := normalizePath(subtree, root); ok {
		return abs
	}
	return subtree
}

func bashGlobLiteralStem(glob string) string {
	glob = strings.TrimSpace(glob)
	if glob == "" {
		return ""
	}
	if idx := strings.IndexAny(glob, "*?[]{}"); idx >= 0 {
		glob = glob[:idx]
	}
	return glob
}

// bashAskWithClass escalates to an ask carrying the given provenance class. When the base is
// ALREADY an ask it keeps the base reason (enforce parity) and stamps the class, PRESERVING the
// base's provenance source: the base producer already determined the ask verdict, so a redundant
// path-rule match must not relabel it bash_path (accurate promotion-report attribution). Only when
// the Bash scan INTRODUCES the ask (base was not an ask) is the source bash_path. An invariant class
// (Bash ambiguity) here overrides a base policy-ask, so ambiguity is never shadowable in observe.
func bashAskWithClass(base hookDisposition, cls auth.DecisionClass) hookDisposition {
	if base.decision == claude.DecisionAsk {
		base.class = cls
		if base.source == "" {
			base.source = shadowSourceBashPath // base carried no source ⇒ this Bash rule is the origin
		}
		return base
	}
	return hookDisposition{
		decision: claude.DecisionAsk,
		reason:   hookBashPathReviewReason,
		class:    cls,
		source:   shadowSourceBashPath,
	}
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
