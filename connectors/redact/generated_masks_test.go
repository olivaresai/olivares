// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import (
	"strings"
	"testing"
)

// Return offsets at replacement time, never by searching the masked display.
func generatedReviewMask(t *testing.T, command, literal, marker string) (string, []GeneratedMaskSpan) {
	t.Helper()
	at := strings.Index(command, literal)
	if at < 0 {
		t.Fatal("fixture has no literal")
	}
	masked := command[:at] + marker + command[at+len(literal):]
	return masked, []GeneratedMaskSpan{{Start: at, End: at + len(marker)}}
}

func TestCleanMaskedKeepsGeneratedCredentialMarkersReviewable(t *testing.T) {
	for _, command := range []string{
		"PASSWORD=literal_value printf visible",
		"curl --token=literal_value https://service.test",
		"curl --token literal_value https://service.test",
		"curl https://literal_value@service.test",
	} {
		t.Run(command, func(t *testing.T) {
			marker := "[secret env/literal]"
			masked, spans := generatedReviewMask(t, command, "literal_value", marker)
			cleaned, err := CleanMasked(masked, spans)
			if err != nil || cleaned != masked {
				t.Fatalf("generated marker changed: cleaned=%q err=%v", cleaned, err)
			}
			if shown, err := ReviewableShellCommand(command, cleaned); err != nil || shown != cleaned {
				t.Fatalf("proven literal is not reviewable: %v", err)
			}
		})
	}
}

func TestCleanMaskedScrubsOriginalMarkersAndOutsideText(t *testing.T) {
	original := "PASSWORD=[secret attacker] printf visible"
	got, err := CleanMasked(original, nil)
	if err != nil || got != Clean(original) || got == original {
		t.Fatal("original marker received an exemption")
	}
	masked, spans := generatedReviewMask(t, "PASSWORD=literal_value printf olvs_abcdefgh", "literal_value", "[secret env/literal]")
	got, err = CleanMasked(masked, spans)
	if err != nil || got != "PASSWORD=[secret env/literal] printf [REDACTED:olivares-token]" {
		t.Fatalf("outside pattern floor differs: %q %v", got, err)
	}
	// Original marker text beside a genuine generated mask remains unprotected.
	mixed, mixedSpans := generatedReviewMask(t, "PASSWORD=[secret attacker] TOKEN=literal_value printf visible", "literal_value", "[secret env/literal]")
	mixedClean, err := CleanMasked(mixed, mixedSpans)
	if err != nil || mixedClean != "PASSWORD=[REDACTED] attacker] TOKEN=[secret env/literal] printf visible" {
		t.Fatalf("original marker beside a generated span was exempted: %q %v", mixedClean, err)
	}
	// The caller must still prove the masked positions through the shell guard.
	if _, err := ReviewableShellCommand("PASSWORD=literal_value printf olvs_abcdefgh", got); err == nil {
		t.Fatal("masked ordinary operand became reviewable")
	}
}

func TestCleanMaskedPreservesMultipleOffsetsAndCrossingPatterns(t *testing.T) {
	masked := "PASSWORD=[secret env/one] OTHER_TOKEN=[secret env/two] curl https://user:prefix[secret env/three]@service.test"
	one := len("PASSWORD=")
	two := one + len("[secret env/one] OTHER_TOKEN=")
	three := two + len("[secret env/two] curl https://user:prefix")
	spans := []GeneratedMaskSpan{{Start: one, End: one + len("[secret env/one]")}, {Start: two, End: two + len("[secret env/two]")}, {Start: three, End: three + len("[secret env/three]")}}
	got, err := CleanMasked(masked, spans)
	want := "PASSWORD=[secret env/one] OTHER_TOKEN=[secret env/two] curl https://[REDACTED][secret env/three]@service.test"
	if err != nil || got != want {
		t.Fatalf("crossing pattern leaked data or damaged offsets: %q %v", got, err)
	}
	// Different passes change byte lengths before the generated span, including
	// ordinary UTF-8 text. The original provenance slice must not be mutated.
	masked, spans = generatedReviewMask(t, "π password=unprotected curl --token=literal_value olvs_abcdefgh", "literal_value", "[secret env/literal]")
	before := spans[0]
	got, err = CleanMasked(masked, spans)
	if err != nil || got != "π password=[REDACTED] curl --token=[secret env/literal] [REDACTED:olivares-token]" || spans[0] != before {
		t.Fatalf("offset provenance changed across pattern passes: %q %v", got, err)
	}
}

func TestCleanMaskedRefusesInvalidGeneratedSpans(t *testing.T) {
	text := "[secret env/one] [secret env/two]"
	for _, spans := range [][]GeneratedMaskSpan{
		{{Start: -1, End: 2}}, {{Start: 0, End: len(text) + 1}}, {{Start: 0, End: 0}}, {{Start: 4, End: 1}}, {{Start: 0, End: 2}},
		{{Start: 0, End: 16}, {Start: 0, End: 16}}, {{Start: 17, End: 33}, {Start: 0, End: 16}},
	} {
		if got, err := CleanMasked(text, spans); err == nil || got != "" {
			t.Fatalf("invalid provenance accepted: %+v", spans)
		}
	}
	for _, text := range []string{"ordinary", "[secret ]", "[secret env/a;touch]", "[REDACTED:]"} {
		if got, err := CleanMasked(text, []GeneratedMaskSpan{{Start: 0, End: len(text)}}); err == nil || got != "" {
			t.Fatalf("invalid marker accepted: %q", text)
		}
	}
}

func TestCleanMaskedScrubsCredentialSuffixAcrossGeneratedSpan(t *testing.T) {
	original := "AKIA12345678ZZZZABCD"
	masked, spans := generatedReviewMask(t, original, "ZZZZ", "[secret env/x]")
	cleaned, err := CleanMasked(masked, spans)
	want := "[REDACTED:aws-access-key][secret env/x][REDACTED:aws-access-key]"
	if err != nil || cleaned != want {
		t.Fatalf("bounded credential pattern exposed unprotected suffix: %q %v", cleaned, err)
	}
}
