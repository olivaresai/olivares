// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"reflect"
	"strings"
	"testing"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// seededSensitivityText seeds one real shape per catalog rule family (each on its
// own line, separated by prose, so no two numeric seeds fall into one regex
// window). The email rule is seeded TWICE (two distinct addresses) to prove
// ClassifySensitivity counts occurrences rather than reporting presence.
const seededSensitivityText = `Contact alice at a@b.example and bob at c@d.example for access.
The applicant's SSN is 123-45-6789 on file.
Wire transfers go to IBAN ES9121000418450200051332 only.
Card on record reads 4111 1111 1111 1111 for billing.
The Spanish tax id is 12345678Z per the registry.
Origin host was 10.0.0.1 behind the proxy.
Interface hardware address 00:1A:2B:3C:4D:5E was seen.
Donations went to wallet 1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa last year.
A deploy credential AKIAIOSFODNN7EXAMPLE leaked in the log.
Session cookie carried eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2lnbmF0dXJl verbatim.
Provider keys sk-ant-abcdefghijklmnopqrstuv and sk-AAAAAAAAAAAAAAAAAAAAAAAA were pasted.
The config line read api_key=supersecretvalue at the end.`

// seededSensitivityLiterals are the raw seeded values above. No hit field may ever
// carry one of them (TestSensitivityHitsCarryNoValues, docs/SECURITY-HARDENING.md).
var seededSensitivityLiterals = []string{
	"a@b.example",
	"c@d.example",
	"123-45-6789",
	"ES9121000418450200051332",
	"4111 1111 1111 1111",
	"12345678Z",
	"10.0.0.1",
	"00:1A:2B:3C:4D:5E",
	"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
	"AKIAIOSFODNN7EXAMPLE",
	"eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2lnbmF0dXJl",
	"sk-ant-abcdefghijklmnopqrstuv",
	"sk-AAAAAAAAAAAAAAAAAAAAAAAA",
	"supersecretvalue",
}

// seededSensitivityWant is the EXACT expected output for the seeded text, already
// in the (class, rule) sort order ClassifySensitivity guarantees. Notable catalog
// behaviors it pins:
//   - email Count=2 (two distinct addresses; occurrences are counted, not presence);
//   - credit-card fires only because the seeded number is Luhn-valid;
//   - the SSN's 9 digits are NOT a credit-card candidate (ccCandidateRe needs 13–19);
//   - the sk-ant- key fires ONLY anthropic-key: the openai-key shape
//     (sk-[0-9A-Za-z]{20,}) cannot cross the second '-' in "sk-ant-", so it does not
//     double-count it — openai-key Count=1 comes from the plain sk- seed alone.
var seededSensitivityWant = []SensitivityHit{
	{Class: SensPIIContact, Rule: "email", Count: 2, Severity: sdkmodel.SeverityMedium},
	{Class: SensPIIFinancial, Rule: "credit-card", Count: 1, Severity: sdkmodel.SeverityHigh},
	{Class: SensPIIFinancial, Rule: "crypto-wallet", Count: 1, Severity: sdkmodel.SeverityMedium},
	{Class: SensPIIFinancial, Rule: "iban", Count: 1, Severity: sdkmodel.SeverityMedium},
	{Class: SensPIIGovernmentID, Rule: "es-nif", Count: 1, Severity: sdkmodel.SeverityMedium},
	{Class: SensPIIGovernmentID, Rule: "us-ssn", Count: 1, Severity: sdkmodel.SeverityMedium},
	{Class: SensPIINetwork, Rule: "ipv4", Count: 1, Severity: sdkmodel.SeverityLow},
	{Class: SensPIINetwork, Rule: "mac-address", Count: 1, Severity: sdkmodel.SeverityLow},
	{Class: SensSecretCredential, Rule: "anthropic-key", Count: 1, Severity: sdkmodel.SeverityHigh},
	{Class: SensSecretCredential, Rule: "aws-access-key", Count: 1, Severity: sdkmodel.SeverityHigh},
	{Class: SensSecretCredential, Rule: "jwt", Count: 1, Severity: sdkmodel.SeverityHigh},
	{Class: SensSecretCredential, Rule: "key-value-secret", Count: 1, Severity: sdkmodel.SeverityHigh},
	{Class: SensSecretCredential, Rule: "openai-key", Count: 1, Severity: sdkmodel.SeverityHigh},
}

// TestClassifySensitivityFindsSeededPII is the discovery contract: every
// seeded shape is found with the right (class, rule), the right occurrence Count
// and the catalog rule's Severity — and NOTHING else fires (no overlap rule
// double-counts a seed).
func TestClassifySensitivityFindsSeededPII(t *testing.T) {
	got := ClassifySensitivity(seededSensitivityText)

	type key struct{ class, rule string }
	byKey := make(map[key]SensitivityHit, len(got))
	for _, h := range got {
		if _, dup := byKey[key{h.Class, h.Rule}]; dup {
			t.Errorf("rule %s/%s appears twice in one result (a rule must yield one hit with a count)", h.Class, h.Rule)
		}
		byKey[key{h.Class, h.Rule}] = h
	}

	for _, want := range seededSensitivityWant {
		h, ok := byKey[key{want.Class, want.Rule}]
		if !ok {
			t.Errorf("seeded %s/%s not found", want.Class, want.Rule)
			continue
		}
		if h.Count != want.Count {
			t.Errorf("%s/%s Count = %d, want %d", want.Class, want.Rule, h.Count, want.Count)
		}
		if h.Severity != want.Severity {
			t.Errorf("%s/%s Severity = %q, want %q", want.Class, want.Rule, h.Severity, want.Severity)
		}
		delete(byKey, key{want.Class, want.Rule})
	}
	// Determinism includes the ABSENCE of overlap artifacts: no unexpected rule
	// fired on the seeded text (e.g. openai-key did not also claim the sk-ant- key,
	// the IBAN/SSN digits did not become a credit-card candidate).
	for k := range byKey {
		t.Errorf("unexpected hit %s/%s on the seeded text", k.class, k.rule)
	}
}

// TestClassifySensitivityLuhnRejectsInvalidCard pins the Luhn validation: a
// 16-digit run that fails the checksum is NOT a credit card (a random long id must
// never be labeled pii.financial).
func TestClassifySensitivityLuhnRejectsInvalidCard(t *testing.T) {
	// Same shape as the valid seed but the last digit breaks the checksum.
	got := ClassifySensitivity("Card on record reads 4111 1111 1111 1112 for billing.")
	for _, h := range got {
		if h.Rule == "credit-card" {
			t.Fatalf("credit-card fired on a Luhn-invalid number (Count=%d)", h.Count)
		}
	}
	if len(got) != 0 {
		t.Errorf("ClassifySensitivity returned %d hits for a text holding only a Luhn-invalid number, want 0: %+v", len(got), got)
	}
}

// TestClassifySensitivityDeterministic is the forensic contract: the classifier is
// pure — two runs over the same text are deeply equal — and the output is sorted
// by (class, rule) so the persisted label JSON is byte-stable across rescans.
func TestClassifySensitivityDeterministic(t *testing.T) {
	a := ClassifySensitivity(seededSensitivityText)
	b := ClassifySensitivity(seededSensitivityText)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two runs over the same text differ:\n  first  = %+v\n  second = %+v", a, b)
	}
	if len(a) == 0 {
		t.Fatal("seeded text yielded no hits (the determinism check would be vacuous)")
	}
	for i := 1; i < len(a); i++ {
		prev, cur := a[i-1], a[i]
		if prev.Class > cur.Class || (prev.Class == cur.Class && prev.Rule >= cur.Rule) {
			t.Errorf("output not strictly sorted by (class, rule): %s/%s before %s/%s",
				prev.Class, prev.Rule, cur.Class, cur.Rule)
		}
	}
}

// TestClassifySensitivityEmptyAndClean: an empty text yields nil, and a clean
// paragraph yields no hits — "scanned, clean" is a real, distinguishable outcome
// the knowledge plane records as an empty label, not an error.
func TestClassifySensitivityEmptyAndClean(t *testing.T) {
	if got := ClassifySensitivity(""); got != nil {
		t.Errorf("empty text: got %+v, want nil", got)
	}
	clean := "The quarterly governance review covers retrieval quality and chunking strategy. " +
		"Nothing personal appears in this paragraph and no credential material is quoted."
	if got := ClassifySensitivity(clean); len(got) != 0 {
		t.Errorf("clean paragraph: got %d hits, want 0: %+v", len(got), got)
	}
}

// TestSensitivityHitsCarryNoValues documents the minimal-data contract (docs/SECURITY-HARDENING.md
// §3): a hit is {class, rule, count, severity} and nothing else — no field ever
// carries a matched raw value. The reflection half is the compile-time-ish guard:
// adding any field to SensitivityHit (e.g. an excerpt) fails this test and forces
// a minimal-data review.
func TestSensitivityHitsCarryNoValues(t *testing.T) {
	typ := reflect.TypeOf(SensitivityHit{})
	wantFields := []string{"Class", "Rule", "Count", "Severity"}
	if typ.NumField() != len(wantFields) {
		t.Fatalf("SensitivityHit has %d fields, want exactly %v — a new field must be reviewed against the never-carry-a-value contract", typ.NumField(), wantFields)
	}
	for i, name := range wantFields {
		if typ.Field(i).Name != name {
			t.Fatalf("SensitivityHit field %d = %q, want %q", i, typ.Field(i).Name, name)
		}
	}

	hits := ClassifySensitivity(seededSensitivityText)
	if len(hits) == 0 {
		t.Fatal("seeded text yielded no hits (the no-values check would be vacuous)")
	}
	for _, h := range hits {
		for i, lit := range seededSensitivityLiterals {
			for _, field := range []string{h.Class, h.Rule, string(h.Severity)} {
				if strings.Contains(field, lit) {
					// The literal itself is deliberately withheld from the message.
					t.Errorf("hit %s/%s carries seeded literal #%d in a label field (value withheld)", h.Class, h.Rule, i)
				}
			}
		}
	}
}

// TestSensitivityCatalogVersionStable: the catalog version recorded on scan
// evidence is a non-empty, well-formed identifier (reproducibility — same content
// + same version = same hits).
func TestSensitivityCatalogVersionStable(t *testing.T) {
	if SensitivityCatalogVersion == "" {
		t.Fatal("SensitivityCatalogVersion is empty — scan evidence would be irreproducible")
	}
	if strings.TrimSpace(SensitivityCatalogVersion) != SensitivityCatalogVersion {
		t.Errorf("SensitivityCatalogVersion %q carries surrounding whitespace", SensitivityCatalogVersion)
	}
}
