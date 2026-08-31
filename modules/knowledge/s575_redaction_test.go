// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"strings"
	"testing"
)

// B-02 — the write path must not persist the PII the product's own
// contract says never reaches the store.
//
// The built-in shapes redact ten credential forms and exactly one personal-data
// form (email). Everything else the security catalog recognizes — IBAN, Luhn-valid
// cards, US SSN, ES NIF/NIE, IPs, MACs, wallets — went through scrub() byte for
// byte and landed in the chunk text in the clear. The catalog then ran over the
// POST-scrub body and reported them, which is to say: it detected them precisely
// because they had survived.
//
// These tests pin both halves. The fallback keeps doing exactly what it always
// did (an unwired minimizer must never be WEAKER than the one it replaced), and a
// wired redactor removes the rest.

// fakeRedactor is a wired Redactor whose behavior the test controls, so these
// cases exercise the SEAM and not the security module's catalog (which has its
// own tests). It removes anything between angle brackets.
type fakeRedactor struct{ calls int }

func (f *fakeRedactor) Redact(text string) (string, []SensitivityHit) {
	f.calls++
	out := text
	n := 0
	for {
		i := strings.Index(out, "<")
		j := strings.Index(out, ">")
		if i < 0 || j < i {
			break
		}
		out = out[:i] + "[REDACTED:fake]" + out[j+1:]
		n++
	}
	if n == 0 {
		return out, nil
	}
	return out, []SensitivityHit{{Class: "pii.financial", Rule: "fake", Count: n, Severity: "medium"}}
}

func (f *fakeRedactor) Version() string { return "fake.v1" }

// The measured gap, stated as a test: these four shapes survive the BUILT-IN
// scrub untouched. This is not an aspiration — it is what the code did, and it is
// why the seam exists. If a future change makes the built-in shapes cover them,
// this test tells whoever did it that the fallback moved.
func TestBuiltinScrubLeavesNonEmailPIIIntact(t *testing.T) {
	cases := map[string]string{
		"iban":        "ES9121000418450200051332",
		"credit-card": "4539578763621486",
		"us-ssn":      "123-45-6789",
		"es-nif":      "12345678Z",
	}
	for name, value := range cases {
		body := "record " + value + " end"
		clean, n := scrub(body)
		if !strings.Contains(clean, value) {
			t.Errorf("%s: the built-in scrub now removes %q — the fallback's coverage changed; "+
				"update this test AND the claim in doc.go", name, value)
		}
		if n != 0 {
			t.Errorf("%s: built-in scrub reported %d redactions on a body with none of its shapes", name, n)
		}
	}
	// The half of the claim that DOES hold: credentials and email.
	for name, value := range map[string]string{
		"aws-access-key": "AKIAIOSFODNN7EXAMPLE",
		"email":          "ana@example.com",
	} {
		clean, n := scrub("record " + value + " end")
		if strings.Contains(clean, value) {
			t.Errorf("%s: the built-in scrub must still remove %q", name, value)
		}
		if n == 0 {
			t.Errorf("%s: a removal must be counted", name)
		}
	}
}

// With a redactor wired, the write path uses it — and reports what it removed.
func TestScrubWithUsesTheWiredRedactor(t *testing.T) {
	f := &fakeRedactor{}
	m := &Module{redactor: f}

	clean, n, removed := m.scrubWith("account <ES9121000418450200051332> closed")
	if f.calls != 1 {
		t.Fatalf("the wired redactor must be called exactly once, got %d", f.calls)
	}
	if strings.Contains(clean, "ES9121000418450200051332") {
		t.Errorf("the wired redactor's output still carries the value: %q", clean)
	}
	if n != 1 {
		t.Errorf("redaction count = %d, want 1", n)
	}
	if len(removed) != 1 || removed[0].Rule != "fake" {
		t.Errorf("what was removed must be reported, got %v", removed)
	}
}

// Unwired, the seam degrades to the built-in shapes — never to no redaction. A
// minimizer that fails open is worse than the one it replaced.
func TestScrubWithFallsBackToBuiltinShapes(t *testing.T) {
	m := &Module{}
	clean, n, removed := m.scrubWith("key AKIAIOSFODNN7EXAMPLE here")
	if strings.Contains(clean, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("the unwired fallback must still remove credentials")
	}
	if n != 1 {
		t.Errorf("fallback redaction count = %d, want 1", n)
	}
	if removed != nil {
		t.Errorf("the built-in fallback reports no per-rule detail, got %v", removed)
	}
}

// An empty body is not a redaction event.
func TestScrubWithEmptyText(t *testing.T) {
	m := &Module{redactor: &fakeRedactor{}}
	clean, n, removed := m.scrubWith("")
	if clean != "" || n != 0 || removed != nil {
		t.Errorf("empty text must pass through: %q %d %v", clean, n, removed)
	}
}
