// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"
	"testing"
)

var piiFuzzSeeds = []string{
	"ordinary plaintext with no sensitive value",
	"AWS credential AKIAIOSFODNN7EXAMPLE",
	"JWT eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signature1234",
	"contact alice@example.com for access",
	"Authorization: Bearer abcdefghijklmnop",
	"api_key=correct-horse-battery",
}

// FuzzScrubExcerpt verifies that every redact-intended raw match is removed and
// that scrubbing is idempotent.
func FuzzScrubExcerpt(f *testing.F) {
	for _, seed := range piiFuzzSeeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		scrubbed := scrubExcerpt(input)
		if twice := scrubExcerpt(scrubbed); twice != scrubbed {
			t.Fatalf("scrubExcerpt is not idempotent: once=%q twice=%q", scrubbed, twice)
		}

		assertKeyValueMatchesRemoved(t, input, scrubbed)
		for _, candidate := range secretShapes {
			if candidate.redact {
				assertRegexpMatchesRemoved(t, input, scrubbed, candidate.rule, candidate.re)
			}
		}
		for _, candidate := range piiShapes {
			if candidate.redact {
				assertRegexpMatchesRemoved(t, input, scrubbed, candidate.rule, candidate.re)
			}
		}
		for _, match := range ccCandidateRe.FindAllString(input, -1) {
			if luhnValid(match) && containsRegexpMatch(scrubbed, match, ccCandidateRe) {
				t.Fatalf("credit-card raw regex match survived scrubbing: match=%q output=%q", match, scrubbed)
			}
		}
	})
}

// FuzzPIIInspect is a cheap no-panic guard for the detector.
func FuzzPIIInspect(f *testing.F) {
	for _, seed := range piiFuzzSeeds {
		f.Add(seed)
	}

	detector := piiDetector{}
	f.Fuzz(func(t *testing.T, input string) {
		_ = detector.Inspect(GuardrailInput{Text: input})
	})
}

func assertKeyValueMatchesRemoved(t *testing.T, input, scrubbed string) {
	t.Helper()
	for _, match := range keyValueSecretRe.FindAllStringSubmatch(input, -1) {
		// The canonical replacement is already scrubbed, even though the key/value
		// regexp recognizes it again. It contains no raw value to leak.
		if match[2] == "[redacted]" {
			continue
		}
		if containsRegexpMatch(scrubbed, match[0], keyValueSecretRe) {
			t.Fatalf("key-value-secret raw regex match survived scrubbing: match=%q output=%q", match[0], scrubbed)
		}
	}
}

func assertRegexpMatchesRemoved(t *testing.T, input, scrubbed, rule string, re *regexp.Regexp) {
	t.Helper()
	for _, match := range re.FindAllString(input, -1) {
		if containsRegexpMatch(scrubbed, match, re) {
			t.Fatalf("%s raw regex match survived scrubbing: match=%q output=%q", rule, match, scrubbed)
		}
	}
}

func containsRegexpMatch(input, want string, re *regexp.Regexp) bool {
	for _, match := range re.FindAllString(input, -1) {
		if match == want {
			return true
		}
	}
	return false
}
