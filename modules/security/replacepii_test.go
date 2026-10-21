// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"reflect"
	"testing"
)

// fakeAWSKey is a credential shape, split so secret scanners do not flag the fixture.
const fakeAWSKey = "AKIA" + "ABCDEFGHIJKLMNOP"

const replacePIIText = "mail jane@example.com, card 4111 1111 1111 1111 now, iban ES9121000418450200051332, key " + fakeAWSKey

func TestReplacePIIHandsEveryValueToTheCaller(t *testing.T) {
	type hit struct{ rule, value string }
	var got []hit
	out := ReplacePII(replacePIIText, func(rule, value string) string {
		got = append(got, hit{rule, value})
		return "<" + rule + ">"
	})

	want := []hit{{"email", "jane@example.com"}, {"iban", "ES9121000418450200051332"}, {"credit-card", "4111 1111 1111 1111"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %q, want %q", got, want)
	}
	// The space after the card is the document's, and the credential is not PII.
	if wantOut := "mail <email>, card <credit-card> now, iban <iban>, key " + fakeAWSKey; out != wantOut {
		t.Fatalf("out = %q, want %q", out, wantOut)
	}
}

func TestPIIRulesNameEveryClassReplacePIIHands(t *testing.T) {
	want := []string{"email", "iban", "ipv4", "mac-address", "crypto-wallet", "us-ssn", "es-nif", "es-nie", "credit-card"}
	if got := PIIRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PIIRules() = %q, want %q", got, want)
	}
}

// RedactSensitive's PII step is ReplacePII with a marker: the same text and the
// same counts as before the step was shared.
func TestRedactSensitiveKeepsItsPIIOutput(t *testing.T) {
	out, hits := RedactSensitive(replacePIIText)

	if want := "mail [REDACTED:email], card [REDACTED:credit-card] now, iban [REDACTED:iban], key [REDACTED:aws-access-key]"; out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	counts := map[string]int{}
	for _, h := range hits {
		counts[h.Rule] = h.Count
	}
	if want := map[string]int{"email": 1, "iban": 1, "credit-card": 1, "aws-access-key": 1}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts = %v, want %v", counts, want)
	}
}
