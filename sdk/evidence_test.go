// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"encoding/json"
	"testing"
)

func binding() EvidenceBinding {
	return EvidenceBinding{OperationID: "op-1", EffectDigest: "eff-abc"}
}

// The whole contract's safety rests on the zero value being a REFUSAL: a receipt that a
// PEP never populated (or that decoded from an empty body) must never authorize an effect.
func TestEvidenceReceiptZeroValueRefuses(t *testing.T) {
	var r EvidenceReceipt
	if r.AnchoredFor(binding()) {
		t.Fatal("zero-value receipt must not anchor any effect")
	}
	if !r.MustRefuse(binding()) {
		t.Fatal("zero-value receipt must refuse")
	}
	if r.FailureClass(binding()) != FailureEvidenceFault {
		t.Fatalf("zero-value receipt FailureClass = %q, want %q", r.FailureClass(binding()), FailureEvidenceFault)
	}
	// A receipt decoded from an empty/omitted body also refuses.
	if err := json.Unmarshal([]byte(`{}`), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !r.MustRefuse(binding()) {
		t.Fatal("receipt decoded from empty body must refuse")
	}
}

func TestEvidenceBindingValid(t *testing.T) {
	cases := []struct {
		name string
		b    EvidenceBinding
		want bool
	}{
		{"both present", EvidenceBinding{"op-1", "eff-abc"}, true},
		{"empty op", EvidenceBinding{"", "eff-abc"}, false},
		{"empty digest", EvidenceBinding{"op-1", ""}, false},
		{"whitespace op", EvidenceBinding{"  ", "eff-abc"}, false},
		{"whitespace digest", EvidenceBinding{"op-1", "\t"}, false},
		{"zero", EvidenceBinding{}, false},
	}
	for _, c := range cases {
		if got := c.b.Valid(); got != c.want {
			t.Errorf("%s: Valid()=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestAnchoredForRequiresExactBinding is the confused-deputy defense: a receipt anchored
// for ONE effect must never authorize a DIFFERENT effect, even with a valid ledger ref.
func TestAnchoredForRequiresExactBinding(t *testing.T) {
	anchored := EvidenceReceipt{OperationID: "op-1", EffectDigest: "eff-abc", EvidenceRef: "seq-42"}
	if !anchored.AnchoredFor(binding()) {
		t.Fatal("a receipt matching {op,digest} with a ref and no fault must anchor")
	}
	// Wrong operation, same digest → refuse (replay onto another logical effect).
	if anchored.AnchoredFor(EvidenceBinding{OperationID: "op-2", EffectDigest: "eff-abc"}) {
		t.Fatal("receipt must refuse a binding with a different OperationID")
	}
	// Same operation, different effect digest → refuse (rebind: same handle, new effect).
	if anchored.AnchoredFor(EvidenceBinding{OperationID: "op-1", EffectDigest: "eff-XYZ"}) {
		t.Fatal("receipt must refuse a binding with a different EffectDigest")
	}
	// Even a fully-matching receipt cannot authorize an INVALID (incomplete) binding.
	if anchored.AnchoredFor(EvidenceBinding{OperationID: "op-1"}) {
		t.Fatal("receipt must refuse an invalid binding")
	}
}

func TestAnchoredForRejectsMissingRefOrFault(t *testing.T) {
	// A matching binding but no ledger ref → not anchored.
	noRef := EvidenceReceipt{OperationID: "op-1", EffectDigest: "eff-abc"}
	if noRef.AnchoredFor(binding()) {
		t.Fatal("receipt without an EvidenceRef must not anchor")
	}
	// A ref present but a fault also set → not anchored (fault wins).
	faulted := EvidenceReceipt{OperationID: "op-1", EffectDigest: "eff-abc", EvidenceRef: "seq-42", Fault: EvidenceFaultSpoolDegraded}
	if faulted.AnchoredFor(binding()) {
		t.Fatal("receipt carrying a fault must not anchor even with a ref")
	}
}

func TestFailureClassMapsEveryFaultToEvidenceFault(t *testing.T) {
	for _, f := range []EvidenceFault{
		EvidenceFaultLedgerUnwired, EvidenceFaultLedgerUnavailable, EvidenceFaultSpoolFull,
		EvidenceFaultSpoolDegraded, EvidenceFaultTenantUnresolved, EvidenceFaultWriteError,
	} {
		r := EvidenceReceipt{OperationID: "op-1", EffectDigest: "eff-abc", Fault: f}
		if got := r.FailureClass(binding()); got != FailureEvidenceFault {
			t.Errorf("fault %q → FailureClass %q, want %q", f, got, FailureEvidenceFault)
		}
		if !r.MustRefuse(binding()) {
			t.Errorf("fault %q must refuse", f)
		}
	}
	anchored := EvidenceReceipt{OperationID: "op-1", EffectDigest: "eff-abc", EvidenceRef: "seq-42"}
	if got := anchored.FailureClass(binding()); got != FailureNone {
		t.Fatalf("anchored receipt FailureClass = %q, want %q", got, FailureNone)
	}
}

func TestClassifyAnchor(t *testing.T) {
	b := binding()
	cases := []struct {
		name         string
		ref          string
		dropped      bool
		txFault      EvidenceFault
		wantFault    EvidenceFault
		wantRef      string
		wantAnchored bool
	}{
		{"clean anchor", "seq-42", false, EvidenceFaultNone, EvidenceFaultNone, "seq-42", true},
		{"transaction fault wins over everything", "seq-42", true, EvidenceFaultSpoolFull, EvidenceFaultSpoolFull, "", false},
		{"ledger unavailable", "", false, EvidenceFaultLedgerUnavailable, EvidenceFaultLedgerUnavailable, "", false},
		{"degrade drop", "", true, EvidenceFaultNone, EvidenceFaultSpoolDegraded, "", false},
		{"drop with a ref is contradictory", "seq-42", true, EvidenceFaultNone, EvidenceFaultWriteError, "", false},
		{"committed but no ref", "", false, EvidenceFaultNone, EvidenceFaultWriteError, "", false},
	}
	for _, c := range cases {
		r := ClassifyAnchor(b, c.ref, c.dropped, c.txFault)
		if r.OperationID != b.OperationID || r.EffectDigest != b.EffectDigest {
			t.Errorf("%s: receipt lost its binding: %+v", c.name, r)
		}
		if r.Fault != c.wantFault {
			t.Errorf("%s: Fault=%q, want %q", c.name, r.Fault, c.wantFault)
		}
		if r.EvidenceRef != c.wantRef {
			t.Errorf("%s: EvidenceRef=%q, want %q", c.name, r.EvidenceRef, c.wantRef)
		}
		if got := r.AnchoredFor(b); got != c.wantAnchored {
			t.Errorf("%s: AnchoredFor=%v, want %v", c.name, got, c.wantAnchored)
		}
	}
}

// TestClassifyAnchorInvalidBindingRefuses proves a committed transaction with an
// incomplete binding never yields an anchored receipt (a caller-side contract violation).
func TestClassifyAnchorInvalidBindingRefuses(t *testing.T) {
	incomplete := EvidenceBinding{OperationID: "op-1"} // no EffectDigest
	r := ClassifyAnchor(incomplete, "seq-42", false, EvidenceFaultNone)
	if r.Fault != EvidenceFaultWriteError {
		t.Fatalf("invalid binding with a ref → Fault %q, want %q", r.Fault, EvidenceFaultWriteError)
	}
	if r.AnchoredFor(incomplete) {
		t.Fatal("an invalid binding must never anchor")
	}
}

// TestClassifyAnchorRoundTrip proves a classified receipt survives JSON (the receipt may
// cross a process boundary to a remote PDP) and still refuses/anchors identically.
func TestClassifyAnchorRoundTrip(t *testing.T) {
	b := binding()
	anchored := ClassifyAnchor(b, "seq-42", false, EvidenceFaultNone)
	raw, err := json.Marshal(anchored)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back EvidenceReceipt
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !back.AnchoredFor(b) {
		t.Fatalf("anchored receipt did not survive round-trip: %s", raw)
	}
	// The degrade receipt marshals its fault and still refuses after a round-trip.
	degraded := ClassifyAnchor(b, "", true, EvidenceFaultNone)
	raw, _ = json.Marshal(degraded)
	back = EvidenceReceipt{}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal degraded: %v", err)
	}
	if back.Fault != EvidenceFaultSpoolDegraded || !back.MustRefuse(b) {
		t.Fatalf("degraded receipt round-trip = %s, must refuse", raw)
	}
}
