// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/security"
)

// B-02 — the composition root's redactor adapter, tested where the two
// modules actually meet.
//
// The unit tests in modules/knowledge exercise the SEAM with a fake. This one
// exercises the WIRING with the real catalog, because the defect was never in
// either module on its own: the classifier knew about IBANs and cards, the write
// path did not, and nothing connected them. The product's contract says secrets
// and PII never reach the store or an embedder; for the PII half that was false.

// The four shapes measured to cross the built-in scrub byte for byte must not
// survive the wired redactor.
func TestWiredRedactorRemovesTheShapesTheBuiltinScrubMissed(t *testing.T) {
	r := securityRedactor{}
	cases := map[string]string{
		"iban":        "ES9121000418450200051332",
		"credit-card": "4539578763621486",
		"us-ssn":      "123-45-6789",
		"es-nif":      "12345678Z",
		"es-nie":      "X1234567L",
		"ipv4":        "192.168.13.37",
	}
	for name, value := range cases {
		clean, hits := r.Redact("payroll record " + value + " filed")
		if strings.Contains(clean, value) {
			t.Errorf("%s: %q survived the wired redactor: %q", name, value, clean)
		}
		if len(hits) == 0 {
			t.Errorf("%s: a removal must be reported", name)
		}
		for _, h := range hits {
			// A hit names the rule and the class; it must never carry the value.
			if strings.Contains(h.Rule, value) || strings.Contains(h.Class, value) {
				t.Errorf("%s: the reported hit leaks the matched value: %+v", name, h)
			}
		}
	}
}

// Credentials keep working: the half of the claim that always held must not
// regress while the other half is being fixed.
func TestWiredRedactorStillRemovesCredentials(t *testing.T) {
	r := securityRedactor{}
	for name, value := range map[string]string{
		"aws-access-key": "AKIAIOSFODNN7EXAMPLE",
		"email":          "ana@example.com",
	} {
		clean, hits := r.Redact("see " + value)
		if strings.Contains(clean, value) {
			t.Errorf("%s: %q survived: %q", name, value, clean)
		}
		if len(hits) == 0 {
			t.Errorf("%s: a removal must be reported", name)
		}
	}
	// key=value keeps its structure and loses its secret.
	clean, _ := r.Redact("api_key=supersecretvalue123")
	if strings.Contains(clean, "supersecretvalue123") {
		t.Errorf("the key=value secret survived: %q", clean)
	}
	if !strings.HasPrefix(clean, "api_key=") {
		t.Errorf("the key=value rule must keep the key: %q", clean)
	}
}

// Over-redaction is safe; eating the surrounding document is not. A long digit
// run that is NOT a card must be left exactly as it was.
func TestWiredRedactorLeavesNonCardDigitRunsAlone(t *testing.T) {
	in := "order 1234567890123456789 shipped"
	clean, hits := securityRedactor{}.Redact(in)
	if clean != in {
		t.Errorf("a non-Luhn digit run must pass through unchanged:\n got %q\nwant %q", clean, in)
	}
	if len(hits) != 0 {
		t.Errorf("no removal should be reported, got %v", hits)
	}
}

// The redaction and the classification must be the SAME catalog: a version skew
// between "what we detect" and "what we remove" is how the two drifted apart in
// the first place.
func TestRedactorAndClassifierShareOneCatalogVersion(t *testing.T) {
	redVer := securityRedactor{}.Version()
	classVer := securitySensitivityClassifier{}.Version()
	if redVer != security.SensitivityCatalogVersion {
		t.Errorf("redactor version %q != catalog %q", redVer, security.SensitivityCatalogVersion)
	}
	if classVer != redVer {
		t.Errorf("classifier %q and redactor %q must report the same catalog", classVer, redVer)
	}
}

// What the redactor removes, the classifier must no longer find. This is the
// property the whole unit is for: detection that survives its own minimization
// means the value is still there.
func TestWhatIsRedactedIsNoLongerClassifiable(t *testing.T) {
	body := "IBAN ES9121000418450200051332, card 4539578763621486, ssn 123-45-6789, mail ana@example.com"
	if before := security.ClassifySensitivity(body); len(before) == 0 {
		t.Fatal("fixture: the raw body must classify as sensitive")
	}
	clean, removed := securityRedactor{}.Redact(body)
	if len(removed) == 0 {
		t.Fatal("the redactor reported nothing removed")
	}
	for _, h := range security.ClassifySensitivity(clean) {
		t.Errorf("after redaction the content still classifies as %s/%s (%d): %q",
			h.Class, h.Rule, h.Count, clean)
	}
}

// The two defects an adversarial review of the FIRST version of this redactor
// found. Both are regressions this package had already paid for once — the
// excerpt scrubber carries the same ordering rule and the same fixed-point loop,
// the latter added after a fuzzer caught four digits of a card surviving — and
// the first version of RedactSensitive reintroduced both.
func TestRedactorSurvivesTheOrderingAndSinglePassTraps(t *testing.T) {
	// ORDERING: the key=value rule's value class stops at whitespace, so on a PEM
	// block it consumes only "-----BEGIN" and the block shape can then never
	// match. If the shapes do not run first, the KEY BODY survives.
	pem := "secret=-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ\n-----END PRIVATE KEY-----"
	clean, hits := securityRedactor{}.Redact(pem)
	for _, leak := range []string{"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ", "-----END PRIVATE KEY-----"} {
		if strings.Contains(clean, leak) {
			t.Errorf("the private key body survived redaction (%q remains): %q", leak, clean)
		}
	}
	if len(hits) == 0 {
		t.Error("a private key removal must be reported")
	}

	// SINGLE PASS: a rule's match can be blocked by a character another rule would
	// have removed. The witness is the fuzzer's, reproduced here.
	clean, _ = securityRedactor{}.Redact("Api_keY=4 111111111111111-0000")
	for _, d := range []string{"111111111111111", "0000"} {
		if strings.Contains(clean, d) {
			t.Errorf("a single pass left %q behind: %q", d, clean)
		}
	}
}

// A value a precise shape already removed must not be re-counted and re-labeled
// by the broad key=value rule: the reported hits are the governance signal, and a
// credential identified as an AWS key must not be reported as a generic one.
func TestRedactorDoesNotDoubleCountAnAlreadyRedactedValue(t *testing.T) {
	clean, hits := securityRedactor{}.Redact("api_key=AKIAIOSFODNN7EXAMPLE")
	if strings.Contains(clean, "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("the credential survived: %q", clean)
	}
	total := 0
	rules := map[string]int{}
	for _, h := range hits {
		total += h.Count
		rules[h.Rule] = h.Count
	}
	if total != 1 {
		t.Errorf("one secret must be reported once, got %d: %v", total, hits)
	}
	if rules["aws-access-key"] != 1 {
		t.Errorf("the secret must keep its precise rule label, got %v", hits)
	}
}
