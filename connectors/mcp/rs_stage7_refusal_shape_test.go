// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"
)

// P-2 and P-3 (hub review of this PR, 2026-08-01). Two defects with one root: the
// coreMRTRRefusal enum was consumed by ENUMERATION in one place and its per-class strings
// were written as `if duplicated { … } return <the other one>`, so a class the code does
// not model borrowed another's identity instead of being refused for what it is.
//
// WHAT WAS MEASURED BEFORE THE FIX, by mutation:
//
//	P-2  coreRefusal() returning mrtrReasonMalformedResult where it returned
//	     mrtrReasonUnsanctionedOrigin              -> ok 0.596s, nothing red
//	     collapsing reason() to always return mrtrReasonMalformedResult
//	                                               -> ok 0.593s, nothing red
//	     because `grep -rn mrtrReasonUnsanctionedOrigin --include=*_test.go connectors/`
//	     returned NOTHING: the constant was asserted neither by name nor by value.
//	P-3  governGenericMRTR switched on the value with two cases and no default, then fell
//	     through to the relay path — while elicitation and sampling asked refused().
//
// The table below is TOTAL over the enum rather than a list of the cases someone
// remembered, and it carries a row for a value the build does NOT map. That row is the
// point: coreMRTRRefusal is open by design and this stage widened it from two values to
// three, so the property that has to hold is not "these two are refused" but "everything
// that is not admitted is refused, and none of them borrows another's controlled code".
func TestCoreMRTRRefusalTotalShape(t *testing.T) {
	// An unmapped value, standing in for the fourth class somebody adds later. It is
	// deliberately NOT declared next to the real ones: a constant added to the enum
	// block would be a change to production code, and this row must hold for a value
	// that arrives without anyone touching this file.
	const future = coreMRTRDuplicated + 1

	for _, tc := range []struct {
		name    string
		ref     coreMRTRRefusal
		refused bool
		reason  string
		wire    string
	}{
		{"admitted", coreMRTRAdmitted, false, "", ""},
		{"unsanctioned", coreMRTRUnsanctioned, true, mrtrReasonUnsanctionedOrigin, mrtrUnsanctionedWireMessage},
		{"duplicated", coreMRTRDuplicated, true, mrtrReasonMalformedResult, mrtrAmbiguousDiscriminatorWireMessage},
		{"a class this build does not map", future, true, mrtrReasonUnmappedRefusal, mrtrUnmappedRefusalWireMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.refused(); got != tc.refused {
				t.Fatalf("refused() = %v, want %v", got, tc.refused)
			}
			if got := tc.ref.reason(); got != tc.reason {
				t.Errorf("reason() = %q, want %q", got, tc.reason)
			}
			if got := tc.ref.wire(); got != tc.wire {
				t.Errorf("wire() = %q, want %q", got, tc.wire)
			}
			if !tc.refused {
				return
			}
			// A refusal's audit sentence must carry ITS OWN controlled class. Asserting
			// only "non-empty" would survive the very collapse P-2 measured.
			sentence := tc.ref.genericAuditSentence()
			if !strings.Contains(sentence, tc.reason) {
				t.Errorf("genericAuditSentence() = %q, does not carry the reason class %q", sentence, tc.reason)
			}
		})
	}

	// M-1R2 is an invariant about DISTINCTNESS, and `strings.Contains(x, "")` is true for
	// every x — so an emptied constant would satisfy every Contains assertion in this
	// package while the two classes silently merged. Pin the values themselves.
	for name, v := range map[string]string{
		"mrtrReasonUnsanctionedOrigin":   mrtrReasonUnsanctionedOrigin,
		"mrtrReasonMalformedResult":      mrtrReasonMalformedResult,
		"mrtrReasonUnmappedRefusal":      mrtrReasonUnmappedRefusal,
		"mrtrUnsanctionedWireMessage":    mrtrUnsanctionedWireMessage,
		"mrtrAmbiguousDiscriminatorWire": mrtrAmbiguousDiscriminatorWireMessage,
		"mrtrUnmappedRefusalWireMessage": mrtrUnmappedRefusalWireMessage,
	} {
		if strings.TrimSpace(v) == "" {
			t.Errorf("%s is empty — every strings.Contains assertion about it is vacuously true", name)
		}
	}
	if mrtrReasonUnsanctionedOrigin == mrtrReasonMalformedResult {
		t.Error("M-1R2: the two refusals must never share a controlled reason class")
	}
	if mrtrUnsanctionedWireMessage == mrtrAmbiguousDiscriminatorWireMessage {
		t.Error("M-1R2: the two refusals must never share a client-facing message")
	}
	// The design's controlled enum owns the two real codes; the unmapped marker must not
	// impersonate one of them, which is the whole reason it exists.
	if mrtrReasonUnmappedRefusal == mrtrReasonUnsanctionedOrigin ||
		mrtrReasonUnmappedRefusal == mrtrReasonMalformedResult {
		t.Error("the unmapped marker must not borrow a controlled reason class")
	}

	// The wire of a duplicate may never affirm the literal the body does not carry
	// (M-1R2). Held here as well as at the HTTP level, so removing the handler test
	// cannot take the property with it.
	if strings.Contains(mrtrAmbiguousDiscriminatorWireMessage, "input_required") {
		t.Error("the ambiguous-discriminator wire claims a literal the body may not carry")
	}
}
