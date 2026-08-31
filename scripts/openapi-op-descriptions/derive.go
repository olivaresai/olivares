// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Derivation and validation of a published operation description.
//
// deriveFromDoc turns a handler's Go doc comment into the sentence the contract
// publishes. It is deliberately a NARROW transform — drop the function name, keep
// whole sentences, capitalize — because the value of this source is that the prose was
// written by whoever wrote the handler. A doc comment it cannot turn into publishable
// prose is REJECTED BY NAME so a human writes one row of the catalog; it is never
// paraphrased, never truncated mid-sentence, and never padded out of the route's own
// facts (a description restating "<namespace> module route (requires <perm>)" would
// raise the coverage number and tell an integrator nothing, which is the failure this
// gate exists to end).
//
// validateDescription then holds BOTH sources — derived and hand-written — to the same
// rules, because both end up in the same two places: the published JSON and the JSDoc of
// web/src/lib/api/openapi.gen.ts, which openapi-typescript emits as `@description`.
//
// NOT, today, the client SDK docstrings, which this comment claimed until 2026-08-17:
// clients/generator/spec.go lists "description" among the path-item keys it SKIPS
// (spec.go:50) and reads only the summary and the x-stability fields, so no description
// reaches the Go/Python/TS/Java clients. The rules below stay as strict as if it did —
// the day the generator emits descriptions, the text is already publishable — but a
// claim about where a control bites has to be one the code supports.

package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// minDescription and minWords are the floor below which a description is not a
// description. They reject an empty gesture ("Lists them.", "Returns it.") and nothing
// more: a floor high enough to reject the author's own shortest true sentence
// ("Returns one route.") would not raise the quality of the contract, it would make a
// human pad 48 of them with words that carry no information. The anti-vacuity control
// that does the work is elsewhere — a beta description must be the handler's OWN prose
// (a generic filler cannot be written once and reused across 686 routes), and
// reflectorEcho refuses a description that restates the operation's summary.
const (
	minDescription = 16
	minWords       = 3
)

// maxDescription keeps one operation's prose from becoming the page. The longest
// derived sentence measured on 2026-08-16 was 465 characters.
const maxDescription = 700

// sentenceFloor is how much text deriveFromDoc accumulates before it stops taking
// sentences. One sentence is usually the whole answer ("Lists the discovered tools
// with their UNTRUSTED annotation hints, optionally filtered by ?server_id."), but a
// terse opener ("Returns one managed config.") needs its neighbour to be worth
// publishing.
const sentenceFloor = 60

// copulas are the words that, appearing where the verb belongs, mean the doc comment
// is a sentence ABOUT the function rather than a statement of what the operation does:
// "handleAdmitEntry is the POST /entries/{id}/admit dispatch" cannot have its subject
// removed and still read as prose. They go to the catalog.
var copulas = map[string]bool{
	"is": true, "was": true, "has": true, "does": true, "its": true,
	"this": true, "as": true, "us": true, "thus": true, "always": true,
	"plus": true, "less": true, "perhaps": true, "sometimes": true,
}

// verbLike matches the third-person singular verb a Go doc comment uses right after
// the function name ("returns", "lists", "re-applies", "upserts"). The house style
// puts a verb in capitals for emphasis ("handleReplay REPLAYS one event") and
// occasionally slashes three of them together, so case and "/" are allowed.
var verbLike = regexp.MustCompile(`^[A-Za-z][A-Za-z/-]*s$`)

// deriveFromDoc renders the handler's doc comment as the operation's published
// description, or explains why it cannot be published.
func deriveFromDoc(handlerName, doc string) (string, error) {
	if handlerName == "" {
		return "", fmt.Errorf("this gate could not resolve which function the route mounts")
	}
	text := strings.Join(strings.Fields(doc), " ")
	if text == "" {
		return "", fmt.Errorf("it has no doc comment")
	}
	rest, ok := strings.CutPrefix(text, handlerName+" ")
	if !ok {
		return "", fmt.Errorf("its doc comment does not open with %q, so the function name cannot be removed from the published sentence", handlerName)
	}
	first, _, _ := strings.Cut(rest, " ")
	if !verbLike.MatchString(first) || copulas[strings.ToLower(first)] {
		return "", fmt.Errorf("its doc comment reads %q after the function name, which is prose about the function rather than a statement of what the operation does", first)
	}

	out := takeSentences(rest, sentenceFloor)
	out = upperFirst(out)
	if !strings.HasSuffix(out, ".") {
		out += "."
	}
	if err := validateDescription(out); err != nil {
		return "", fmt.Errorf("the sentence it yields (%q) %v", clip(out), err)
	}
	return out, nil
}

// takeSentences returns whole sentences from s until at least floor characters have
// been taken. A sentence ends at ". " followed by a capital letter — the rule that
// leaves "e.g." and "?state=x." alone.
func takeSentences(s string, floor int) string {
	end := 0
	for {
		next := nextSentenceEnd(s, end)
		if next <= end {
			return strings.TrimSpace(s)
		}
		end = next
		if end >= floor {
			return strings.TrimSpace(s[:end])
		}
	}
}

// nextSentenceEnd returns the index just past the next sentence terminator at or after
// from, or 0 when there is none.
func nextSentenceEnd(s string, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		if i+1 == len(s) {
			return len(s)
		}
		if s[i+1] != ' ' {
			continue
		}
		rest := s[i+2:]
		if rest == "" {
			return i + 1
		}
		if r := []rune(rest)[0]; unicode.IsUpper(r) {
			return i + 1
		}
	}
	return 0
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func clip(s string) string {
	if len(s) <= 70 {
		return s
	}
	return s[:70] + "…"
}

// forbidden are the substrings no published description may carry, each with the reason
// it is refused. The first group would CORRUPT the artifact the description is
// interpolated into: the `@description` JSDoc block of web/src/lib/api/openapi.gen.ts,
// where a stray */ closes the comment early. It is the same list
// clients/generator/spec.go validateCommentText applies to a summary, kept identical so
// the text is already publishable if that generator ever emits descriptions too. The
// second group is the canon's forbidden lexicon for public surfaces
// (CANON-OPERATIVO §371-372).
//
// The self-test walks THIS SLICE — it does not carry a hand-written case per entry —
// because measured 2026-08-17 a mutant that deleted five of the twelve entries survived
// the whole battery: the cases that existed covered the three somebody thought of.
var forbidden = []struct{ sub, why string }{
	{"*/", "would close a JSDoc block early in the generated clients"},
	{"`", "would close a TypeScript template literal in the generated clients"},
	{"${", "would open a template substitution in the generated clients"},
	{`\`, "would escape the next character in the generated clients"},
	{`"""`, "would close the Python docstring in the generated client"},
	{"impossible", "is a claim the canon forbids on a public surface"},
	{"infallible", "is a claim the canon forbids on a public surface"},
	{"tamper-proof", "is a claim the canon forbids on a public surface"},
	{"tamper proof", "is a claim the canon forbids on a public surface"},
	{"unhackable", "is a claim the canon forbids on a public surface"},
	{"bulletproof", "is a claim the canon forbids on a public surface"},
	{"100% secure", "is a claim the canon forbids on a public surface"},
}

// internalMarkers are the things a published contract must not carry because they mean
// nothing outside this repository — and one of them, the session id, is how an internal
// planning artifact ends up in a document integrators read.
//
// Each carries the FRAGMENT that must trip it. The self-test walks this list rather than
// a list of cases somebody remembered to write, so a marker added here with no probe is
// a marker nothing tests — and says so.
var internalMarkers = []struct {
	re    *regexp.Regexp
	why   string
	probe string
}{
	{regexp.MustCompile(`\bS[0-9]{2,4}\b`), "names an internal session id", "the S1234 shape"},
	{regexp.MustCompile(`[A-Za-z0-9_./-]+\.(go|ts|tsx|md|sh|sql|yml|yaml)\b`), "names a source file", "see demo.go for the shape"},
	{regexp.MustCompile(`\b(TODO|FIXME|XXX|HACK)\b`), "carries a work marker", "TODO: page this properly"},
}

// hedgeWords are the approximations scripts/check-public-counts.sh forbids everywhere
// else on a public surface: the counts this product states about itself are exact.
//
// "over" and "some" joined the list on 2026-08-17. They were in the sibling gate's
// pattern (scripts/check-public-counts.sh:565) and NOT here, so "over 20 shapes" was
// refused on every other public surface and publishable in an OpenAPI description —
// the same hedge, two verdicts. No description in the tree used either (measured over
// all 757), so this tightens without a waiver.
//
// The regex below is BUILT from this roster instead of being spelled out, for the reason
// the roster of internalMarkers carries a probe: measured 2026-08-17, a mutant that left
// only "more than" in the pattern — deleting the other six — survived the entire battery,
// because the one case that existed happened to use "more than 20".
var hedgeWords = []string{"more than", "over", "about", "around", "roughly", "approximately", "nearly", "some", "almost"}

// hedgedCount matches a hedge word standing in front of a number.
var hedgedCount = regexp.MustCompile(`(?i)\b(` + strings.Join(hedgeWords, "|") + `)\s+[0-9]`)

// reflectorEcho is the beta document's mechanical summary. A description that restates
// it is a field with no content, and 686 of them would be a coverage number that means
// nothing.
var reflectorEcho = regexp.MustCompile(`(?i)^[a-z-]+ module route\b`)

// validateDescription holds every published description — derived or hand-written — to
// the rules that keep it publishable, honest and non-vacuous.
func validateDescription(s string) error {
	switch {
	case s == "":
		return fmt.Errorf("is empty")
	case s != strings.TrimSpace(s):
		return fmt.Errorf("has leading or trailing whitespace")
	case strings.Contains(s, "  "):
		return fmt.Errorf("contains a double space")
	case len(s) < minDescription:
		return fmt.Errorf("is %d characters, below the %d-character floor a description has to clear to say anything", len(s), minDescription)
	case len(strings.Fields(s)) < minWords:
		return fmt.Errorf("is %d word(s), below the %d-word floor a description has to clear to say anything", len(strings.Fields(s)), minWords)
	case len(s) > maxDescription:
		return fmt.Errorf("is %d characters, above the %d-character ceiling", len(s), maxDescription)
	case !strings.HasSuffix(s, "."):
		return fmt.Errorf("does not end in a full stop")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("contains a control character")
		}
	}
	if r := []rune(s)[0]; !unicode.IsUpper(r) {
		return fmt.Errorf("does not start with a capital letter")
	}
	for _, f := range forbidden {
		if strings.Contains(strings.ToLower(s), f.sub) {
			return fmt.Errorf("contains %q, which %s", f.sub, f.why)
		}
	}
	for _, m := range internalMarkers {
		if hit := m.re.FindString(s); hit != "" {
			return fmt.Errorf("contains %q, which %s", hit, m.why)
		}
	}
	if hit := hedgedCount.FindString(s); hit != "" {
		return fmt.Errorf("hedges a count (%q); the counts a public surface states are exact", hit)
	}
	if reflectorEcho.MatchString(s) {
		return fmt.Errorf("restates the operation's summary instead of describing it")
	}
	return nil
}
