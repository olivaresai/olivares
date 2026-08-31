// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func deliveryTestDigest(label string) []byte {
	sum := sha256.Sum256([]byte(label))
	return sum[:]
}

func validDeliveryDispatchParams() DeliveryDispatchParams {
	return DeliveryDispatchParams{
		TenantID:            "tenant-1",
		WorkspaceID:         "workspace-1",
		DeliveryID:          "delivery-1",
		MessageID:           "message-1",
		DispatchID:          "dispatch-1",
		AttemptID:           "attempt-1",
		EndpointID:          "endpoint-1",
		EndpointGeneration:  7,
		EndpointFingerprint: deliveryTestDigest("endpoint-fingerprint"),
		Provider:            "driver:test-provider",
		Transport:           "stdio",
		OperationID:         "dispatch-op-1",
		RequestDigest:       deliveryTestDigest("request-binding"),
		MessageKind:         DeliveryMessageKindDecisionRequest,
		Urgency:             DeliveryUrgencyCritical,
		WorkItemID:          "work-item-1",
		AckDueAt:            time.Date(2026, time.August, 16, 12, 30, 0, 0, time.FixedZone("test", 2*60*60)),
	}
}

func mustDeliveryDispatch(t *testing.T, p DeliveryDispatchParams) DeliveryDispatch {
	t.Helper()
	d, err := NewDeliveryDispatch(p)
	if err != nil {
		t.Fatalf("NewDeliveryDispatch: %v", err)
	}
	return d
}

func TestDeliveryClosedEnumVocabularies(t *testing.T) {
	for _, kind := range []DeliveryMessageKind{
		DeliveryMessageKindNotice,
		DeliveryMessageKindAnnouncement,
		DeliveryMessageKindRequest,
		DeliveryMessageKindDecisionRequest,
		DeliveryMessageKindHandoffOffer,
		DeliveryMessageKindSystem,
	} {
		if !kind.Valid() {
			t.Errorf("message kind %q is not valid", kind)
		}
	}
	if (DeliveryMessageKind("")).Valid() || DeliveryMessageKind("other").Valid() {
		t.Fatal("empty or unknown message kind must be invalid")
	}

	for _, urgency := range []DeliveryUrgency{
		DeliveryUrgencyNormal,
		DeliveryUrgencyHigh,
		DeliveryUrgencyCritical,
	} {
		if !urgency.Valid() {
			t.Errorf("urgency %q is not valid", urgency)
		}
	}
	if (DeliveryUrgency("")).Valid() || DeliveryUrgency("other").Valid() {
		t.Fatal("empty or unknown urgency must be invalid")
	}

	capabilities := []struct {
		value DeliveryCapability
		name  string
	}{
		{DeliveryCapabilityWake, "wake"},
		{DeliveryCapabilityReconcile, "reconcile"},
		{DeliveryCapabilityIdempotency, "idempotency"},
		{DeliveryCapabilityActiveTurn, "active_turn"},
	}
	for _, capability := range capabilities {
		if !capability.value.Valid() || capability.value.String() != capability.name {
			t.Errorf("capability %d = %q, valid=%v", capability.value, capability.value, capability.value.Valid())
		}
	}
	if (DeliveryCapability(0)).Valid() || DeliveryCapability(3).Valid() || DeliveryCapability(16).Valid() {
		t.Fatal("zero, compound, and unknown capability values must be invalid")
	}

	boundaries := []struct {
		value DeliveryTransmitBoundary
		name  string
	}{
		{DeliveryBoundaryUnknown, "unknown"},
		{DeliveryBoundaryCrossed, "crossed"},
		{DeliveryBoundaryNotCrossed, "not_crossed"},
	}
	for _, boundary := range boundaries {
		if !boundary.value.Valid() || boundary.value.String() != boundary.name {
			t.Errorf("boundary %d = %q, valid=%v", boundary.value, boundary.value, boundary.value.Valid())
		}
	}
	if DeliveryTransmitBoundary(3).Valid() || DeliveryTransmitBoundary(3).String() != "invalid" {
		t.Fatal("unknown boundary must be invalid")
	}

	attempts := []struct {
		value DeliveryAttemptOutcome
		name  string
	}{
		{DeliveryAttemptIndeterminate, "indeterminate"},
		{DeliveryAttemptAccepted, "accepted"},
		{DeliveryAttemptRefusedBeforeBoundary, "refused_before_boundary"},
	}
	for _, attempt := range attempts {
		if !attempt.value.Valid() || attempt.value.String() != attempt.name {
			t.Errorf("attempt outcome %d = %q, valid=%v", attempt.value, attempt.value, attempt.value.Valid())
		}
	}
	if DeliveryAttemptOutcome(3).Valid() || DeliveryAttemptOutcome(3).String() != "invalid" {
		t.Fatal("unknown attempt outcome must be invalid")
	}

	reconciliations := []struct {
		value DeliveryReconciliationOutcome
		name  string
	}{
		{DeliveryReconciliationIndeterminate, "indeterminate"},
		{DeliveryReconciliationAccepted, "accepted"},
		{DeliveryReconciliationNotAccepted, "not_accepted"},
	}
	for _, reconciliation := range reconciliations {
		if !reconciliation.value.Valid() || reconciliation.value.String() != reconciliation.name {
			t.Errorf("reconciliation outcome %d = %q, valid=%v", reconciliation.value, reconciliation.value, reconciliation.value.Valid())
		}
	}
	if DeliveryReconciliationOutcome(3).Valid() || DeliveryReconciliationOutcome(3).String() != "invalid" {
		t.Fatal("unknown reconciliation outcome must be invalid")
	}
}

func TestDeliveryDispatchValidation(t *testing.T) {
	valid := validDeliveryDispatchParams()
	d := mustDeliveryDispatch(t, valid)
	if err := d.Validate(); err != nil {
		t.Fatalf("valid dispatch: %v", err)
	}
	if got, want := d.AckDueAt(), valid.AckDueAt.UTC(); got != want {
		t.Fatalf("AckDueAt = %v (%v), want canonical %v (%v)", got, got.Location(), want, want.Location())
	}

	cases := []struct {
		name   string
		mutate func(*DeliveryDispatchParams)
	}{
		{"missing tenant", func(p *DeliveryDispatchParams) { p.TenantID = "" }},
		{"missing workspace", func(p *DeliveryDispatchParams) { p.WorkspaceID = "" }},
		{"missing delivery", func(p *DeliveryDispatchParams) { p.DeliveryID = "" }},
		{"missing message", func(p *DeliveryDispatchParams) { p.MessageID = "" }},
		{"missing dispatch", func(p *DeliveryDispatchParams) { p.DispatchID = "" }},
		{"missing attempt", func(p *DeliveryDispatchParams) { p.AttemptID = "" }},
		{"missing endpoint", func(p *DeliveryDispatchParams) { p.EndpointID = "" }},
		{"whitespace identifier", func(p *DeliveryDispatchParams) { p.AttemptID = " attempt-1" }},
		{"embedded identifier whitespace", func(p *DeliveryDispatchParams) { p.MessageID = "message 1" }},
		{"control in identifier", func(p *DeliveryDispatchParams) { p.MessageID = "message\n1" }},
		{"invalid utf8 identifier", func(p *DeliveryDispatchParams) { p.MessageID = string([]byte{'m', 0xff}) }},
		{"zero endpoint generation", func(p *DeliveryDispatchParams) { p.EndpointGeneration = 0 }},
		{"missing endpoint fingerprint", func(p *DeliveryDispatchParams) { p.EndpointFingerprint = nil }},
		{"short endpoint fingerprint", func(p *DeliveryDispatchParams) { p.EndpointFingerprint = make([]byte, DeliveryDigestSize-1) }},
		{"zero endpoint fingerprint", func(p *DeliveryDispatchParams) { p.EndpointFingerprint = make([]byte, DeliveryDigestSize) }},
		{"uppercase provider token", func(p *DeliveryDispatchParams) { p.Provider = "Driver:test" }},
		{"provider whitespace", func(p *DeliveryDispatchParams) { p.Provider = "driver:two words" }},
		{"empty transport", func(p *DeliveryDispatchParams) { p.Transport = "" }},
		{"unknown transport character", func(p *DeliveryDispatchParams) { p.Transport = "stdio@local" }},
		{"missing operation", func(p *DeliveryDispatchParams) { p.OperationID = "" }},
		{"missing request digest", func(p *DeliveryDispatchParams) { p.RequestDigest = nil }},
		{"long request digest", func(p *DeliveryDispatchParams) { p.RequestDigest = make([]byte, DeliveryDigestSize+1) }},
		{"zero request digest", func(p *DeliveryDispatchParams) { p.RequestDigest = make([]byte, DeliveryDigestSize) }},
		{"unknown message kind", func(p *DeliveryDispatchParams) { p.MessageKind = "free-form" }},
		{"unknown urgency", func(p *DeliveryDispatchParams) { p.Urgency = "urgent" }},
		{"blank optional work item", func(p *DeliveryDispatchParams) { p.WorkItemID = " " }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			p.EndpointFingerprint = append([]byte(nil), valid.EndpointFingerprint...)
			p.RequestDigest = append([]byte(nil), valid.RequestDigest...)
			tc.mutate(&p)
			if _, err := NewDeliveryDispatch(p); !errors.Is(err, ErrInvalidDeliveryDispatch) {
				t.Fatalf("error = %v, want ErrInvalidDeliveryDispatch", err)
			}
		})
	}

	withoutOptional := valid
	withoutOptional.WorkItemID = ""
	withoutOptional.AckDueAt = time.Time{}
	if _, err := NewDeliveryDispatch(withoutOptional); err != nil {
		t.Fatalf("optional fields omitted: %v", err)
	}
}

func TestDeliveryDispatchDefensiveCopies(t *testing.T) {
	p := validDeliveryDispatchParams()
	wantEndpoint := append([]byte(nil), p.EndpointFingerprint...)
	wantRequest := append([]byte(nil), p.RequestDigest...)
	d := mustDeliveryDispatch(t, p)

	p.EndpointFingerprint[0] ^= 0xff
	p.RequestDigest[0] ^= 0xff
	if !reflect.DeepEqual(d.EndpointFingerprint(), wantEndpoint) {
		t.Fatal("constructor retained caller-owned endpoint fingerprint")
	}
	if !reflect.DeepEqual(d.RequestDigest(), wantRequest) {
		t.Fatal("constructor retained caller-owned request digest")
	}

	endpointCopy := d.EndpointFingerprint()
	requestCopy := d.RequestDigest()
	endpointCopy[0] ^= 0xff
	requestCopy[0] ^= 0xff
	if !reflect.DeepEqual(d.EndpointFingerprint(), wantEndpoint) {
		t.Fatal("EndpointFingerprint returned aliasable storage")
	}
	if !reflect.DeepEqual(d.RequestDigest(), wantRequest) {
		t.Fatal("RequestDigest returned aliasable storage")
	}

	snapshot := d.Params()
	snapshot.EndpointFingerprint[0] ^= 0xff
	snapshot.RequestDigest[0] ^= 0xff
	if !reflect.DeepEqual(d.EndpointFingerprint(), wantEndpoint) || !reflect.DeepEqual(d.RequestDigest(), wantRequest) {
		t.Fatal("Params returned aliasable digest storage")
	}

	clone := d.Clone()
	clone.endpointFingerprint[0] ^= 0xff
	clone.requestDigest[0] ^= 0xff
	if !reflect.DeepEqual(d.EndpointFingerprint(), wantEndpoint) || !reflect.DeepEqual(d.RequestDigest(), wantRequest) {
		t.Fatal("Clone retained aliases to the original")
	}
}

func TestDeliveryDispatchSurfaceIsPayloadFree(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf(DeliveryDispatchParams{}),
		reflect.TypeOf(DeliveryDispatch{}),
		reflect.TypeOf(DeliveryEndpointParams{}),
		reflect.TypeOf(DeliveryEndpointIdentity{}),
		reflect.TypeOf(DeliveryAttemptResult{}),
		reflect.TypeOf(DeliveryReconciliation{}),
		reflect.TypeOf(DeliveryReconciliationResult{}),
	} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, forbidden := range []string{
				"payload", "plaintext", "protected", "secret", "credential",
				"bearer", "token", "subject", "body", "content", "sender", "url",
			} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s.%s exposes forbidden %q material", typ.Name(), typ.Field(i).Name, forbidden)
				}
			}
		}
	}
}

func TestDeliveryEndpointCapabilitiesAreExplicitAndBound(t *testing.T) {
	d := mustDeliveryDispatch(t, validDeliveryDispatchParams())
	endpoint := d.EndpointIdentity()
	if err := endpoint.Validate(); err != nil {
		t.Fatalf("endpoint identity: %v", err)
	}

	for mask := 0; mask < 16; mask++ {
		caps := NewDeliveryCapabilities(mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0)
		if err := caps.Validate(); err != nil {
			t.Fatalf("capability mask %04b: %v", mask, err)
		}
		got := []bool{caps.Wake(), caps.Reconcile(), caps.Idempotency(), caps.ActiveTurn()}
		want := []bool{mask&1 != 0, mask&2 != 0, mask&4 != 0, mask&8 != 0}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("capability mask %04b = %v, want %v", mask, got, want)
		}
		witness, err := NewDeliveryCapabilityWitness(endpoint, caps)
		if err != nil {
			t.Fatalf("capability mask %04b witness: %v", mask, err)
		}
		if !witness.Matches(endpoint) {
			t.Fatalf("capability mask %04b lost endpoint binding", mask)
		}
	}

	// A provider key, even a familiar one, never manufactures Wake or any other bit.
	noCaps := NewDeliveryCapabilities(false, false, false, false)
	witness, err := NewDeliveryCapabilityWitness(endpoint, noCaps)
	if err != nil {
		t.Fatalf("zero-capability witness: %v", err)
	}
	if witness.Capabilities().Wake() || witness.Capabilities().Reconcile() ||
		witness.Capabilities().Idempotency() || witness.Capabilities().ActiveTurn() {
		t.Fatal("provider/transport identity inferred an undeclared capability")
	}

	changed := endpoint.Params()
	changed.EndpointGeneration++
	changedEndpoint, err := NewDeliveryEndpointIdentity(changed)
	if err != nil {
		t.Fatalf("changed endpoint: %v", err)
	}
	if witness.Matches(changedEndpoint) {
		t.Fatal("witness survived an endpoint-generation change")
	}
	changed = endpoint.Params()
	changed.EndpointFingerprint = deliveryTestDigest("different-fingerprint")
	changedEndpoint, err = NewDeliveryEndpointIdentity(changed)
	if err != nil {
		t.Fatalf("changed fingerprint endpoint: %v", err)
	}
	if witness.Matches(changedEndpoint) {
		t.Fatal("witness survived an endpoint-fingerprint change")
	}
	normalized := NormalizeDeliveryCapabilityWitness(witness, nil)
	if !normalized.Matches(endpoint) {
		t.Fatal("a valid capability witness did not survive normalization")
	}
	normalized.endpoint.endpointFingerprint[0] ^= 0xff
	if !witness.Matches(endpoint) {
		t.Fatal("normalized witness retained aliases to its input")
	}
	ignored := NormalizeDeliveryCapabilityWitness(witness, errors.New("untrusted provider detail"))
	if ignored.Validate() == nil || ignored.Matches(endpoint) {
		t.Fatal("a witness returned with an error must normalize to the invalid zero value")
	}
	if UsableDeliveryCapabilityWitness(endpoint, witness, errors.New("untrusted provider detail")) {
		t.Fatal("a driver error must invalidate a simultaneous capability witness")
	}
	if UsableDeliveryCapabilityWitness(changedEndpoint, witness, nil) {
		t.Fatal("a capability witness for another endpoint must not be usable")
	}
	if !UsableDeliveryCapabilityWitness(endpoint, witness, nil) {
		t.Fatal("an exact valid capability witness should be usable")
	}

	unknown := DeliveryCapabilities{mask: DeliveryCapability(1 << 7)}
	if !errors.Is(unknown.Validate(), ErrInvalidDeliveryCapabilityWitness) {
		t.Fatalf("unknown capability bit error = %v", unknown.Validate())
	}
	if unknown.Has(DeliveryCapabilityWake) {
		t.Fatal("an invalid capability set must expose no known capability")
	}
}

func TestDeliveryCapabilityWitnessDefensiveCopies(t *testing.T) {
	d := mustDeliveryDispatch(t, validDeliveryDispatchParams())
	endpoint := d.EndpointIdentity()
	witness, err := NewDeliveryCapabilityWitness(
		endpoint,
		NewDeliveryCapabilities(true, true, true, false),
	)
	if err != nil {
		t.Fatalf("NewDeliveryCapabilityWitness: %v", err)
	}
	want := endpoint.EndpointFingerprint()

	endpoint.endpointFingerprint[0] ^= 0xff
	if !reflect.DeepEqual(witness.Endpoint().EndpointFingerprint(), want) {
		t.Fatal("witness retained caller endpoint fingerprint")
	}
	returned := witness.Endpoint()
	returned.endpointFingerprint[0] ^= 0xff
	if !reflect.DeepEqual(witness.Endpoint().EndpointFingerprint(), want) {
		t.Fatal("witness Endpoint returned aliasable storage")
	}
	clone := witness.Clone()
	clone.endpoint.endpointFingerprint[0] ^= 0xff
	if !reflect.DeepEqual(witness.Endpoint().EndpointFingerprint(), want) {
		t.Fatal("witness Clone retained aliases")
	}
}

func TestDeliveryAttemptResultClosedCrossProduct(t *testing.T) {
	receipt := deliveryTestDigest("provider-receipt")
	outcomes := []DeliveryAttemptOutcome{
		DeliveryAttemptIndeterminate,
		DeliveryAttemptAccepted,
		DeliveryAttemptRefusedBeforeBoundary,
	}
	boundaries := []DeliveryTransmitBoundary{
		DeliveryBoundaryUnknown,
		DeliveryBoundaryCrossed,
		DeliveryBoundaryNotCrossed,
	}
	for _, outcome := range outcomes {
		for _, boundary := range boundaries {
			for _, withReceipt := range []bool{false, true} {
				var hash []byte
				if withReceipt {
					hash = receipt
				}
				_, err := NewDeliveryAttemptResult(outcome, boundary, hash)
				wantValid := (outcome == DeliveryAttemptIndeterminate && boundary == DeliveryBoundaryUnknown && !withReceipt) ||
					(outcome == DeliveryAttemptAccepted && boundary == DeliveryBoundaryCrossed && withReceipt) ||
					(outcome == DeliveryAttemptRefusedBeforeBoundary && boundary == DeliveryBoundaryNotCrossed && !withReceipt)
				if (err == nil) != wantValid {
					t.Errorf("outcome=%s boundary=%s receipt=%v: err=%v, valid=%v", outcome, boundary, withReceipt, err, wantValid)
				}
			}
		}
	}

	if err := (DeliveryAttemptResult{}).Validate(); err != nil {
		t.Fatalf("zero result must be safe indeterminate: %v", err)
	}
	if _, err := NewDeliveryAttemptResult(DeliveryAttemptOutcome(99), DeliveryBoundaryUnknown, nil); !errors.Is(err, ErrInvalidDeliveryAttemptResult) {
		t.Fatalf("unknown outcome error = %v", err)
	}
	if _, err := NewDeliveryAttemptResult(DeliveryAttemptIndeterminate, DeliveryTransmitBoundary(99), nil); !errors.Is(err, ErrInvalidDeliveryAttemptResult) {
		t.Fatalf("unknown boundary error = %v", err)
	}
	if _, err := NewDeliveryAttemptResult(DeliveryAttemptAccepted, DeliveryBoundaryCrossed, make([]byte, DeliveryDigestSize)); !errors.Is(err, ErrInvalidDeliveryAttemptResult) {
		t.Fatalf("zero receipt digest error = %v", err)
	}
}

func TestDeliveryAttemptResultDefensiveCopies(t *testing.T) {
	receipt := deliveryTestDigest("provider-receipt")
	want := append([]byte(nil), receipt...)
	result, err := NewDeliveryAttemptResult(DeliveryAttemptAccepted, DeliveryBoundaryCrossed, receipt)
	if err != nil {
		t.Fatalf("NewDeliveryAttemptResult: %v", err)
	}
	receipt[0] ^= 0xff
	if !reflect.DeepEqual(result.ProviderReceiptHash(), want) {
		t.Fatal("attempt result retained the caller receipt hash")
	}
	returned := result.ProviderReceiptHash()
	returned[0] ^= 0xff
	if !reflect.DeepEqual(result.ProviderReceiptHash(), want) {
		t.Fatal("ProviderReceiptHash returned aliasable storage")
	}
	clone := result.Clone()
	clone.providerReceiptHash[0] ^= 0xff
	if !reflect.DeepEqual(result.ProviderReceiptHash(), want) {
		t.Fatal("attempt result Clone retained aliases")
	}
	ignored := NormalizeDeliveryAttemptResult(result, errors.New("untrusted provider detail"))
	if ignored.Outcome() != DeliveryAttemptIndeterminate || ignored.Boundary() != DeliveryBoundaryUnknown || len(ignored.ProviderReceiptHash()) != 0 {
		t.Fatalf("result plus error normalized to %+v, want safe indeterminate", ignored)
	}
	invalid := result
	invalid.boundary = DeliveryBoundaryUnknown
	ignored = NormalizeDeliveryAttemptResult(invalid, nil)
	if ignored.Outcome() != DeliveryAttemptIndeterminate || ignored.Boundary() != DeliveryBoundaryUnknown {
		t.Fatalf("invalid result normalized to %+v, want safe indeterminate", ignored)
	}
}

func TestDeliveryReconciliationClosedCrossProduct(t *testing.T) {
	receipt := deliveryTestDigest("reconciled-provider-receipt")
	outcomes := []DeliveryReconciliationOutcome{
		DeliveryReconciliationIndeterminate,
		DeliveryReconciliationAccepted,
		DeliveryReconciliationNotAccepted,
	}
	for _, outcome := range outcomes {
		for _, withReceipt := range []bool{false, true} {
			for _, withEvidence := range []bool{false, true} {
				var hash []byte
				if withReceipt {
					hash = receipt
				}
				var evidence string
				if withEvidence {
					evidence = "provider-observation:42"
				}
				_, err := NewDeliveryReconciliationResult(outcome, hash, evidence)
				wantValid := (outcome == DeliveryReconciliationIndeterminate && !withReceipt && !withEvidence) ||
					(outcome == DeliveryReconciliationAccepted && withReceipt && withEvidence) ||
					(outcome == DeliveryReconciliationNotAccepted && !withReceipt && withEvidence)
				if (err == nil) != wantValid {
					t.Errorf("outcome=%s receipt=%v evidence=%v: err=%v, valid=%v", outcome, withReceipt, withEvidence, err, wantValid)
				}
			}
		}
	}

	if err := (DeliveryReconciliationResult{}).Validate(); err != nil {
		t.Fatalf("zero reconciliation result must be safe indeterminate: %v", err)
	}
	if _, err := NewDeliveryReconciliationResult(DeliveryReconciliationOutcome(99), nil, ""); !errors.Is(err, ErrInvalidDeliveryReconciliationResult) {
		t.Fatalf("unknown reconciliation outcome error = %v", err)
	}
	if _, err := NewDeliveryReconciliationResult(DeliveryReconciliationNotAccepted, nil, "remote evidence"); !errors.Is(err, ErrInvalidDeliveryReconciliationResult) {
		t.Fatalf("whitespace evidence error = %v", err)
	}
	if _, err := NewDeliveryReconciliationResult(DeliveryReconciliationNotAccepted, nil, strings.Repeat("x", maxDeliveryEvidenceRefBytes+1)); !errors.Is(err, ErrInvalidDeliveryReconciliationResult) {
		t.Fatalf("oversized evidence error = %v", err)
	}
	if _, err := NewDeliveryReconciliationResult(DeliveryReconciliationNotAccepted, nil, string([]byte{'e', 0xff})); !errors.Is(err, ErrInvalidDeliveryReconciliationResult) {
		t.Fatalf("invalid UTF-8 evidence error = %v", err)
	}
}

func TestDeliveryReconciliationDefensiveCopies(t *testing.T) {
	d := mustDeliveryDispatch(t, validDeliveryDispatchParams())
	reconciliation, err := NewDeliveryReconciliation(d)
	if err != nil {
		t.Fatalf("NewDeliveryReconciliation: %v", err)
	}
	wantEndpoint := d.EndpointFingerprint()
	wantRequest := d.RequestDigest()
	d.endpointFingerprint[0] ^= 0xff
	d.requestDigest[0] ^= 0xff
	if !reflect.DeepEqual(reconciliation.Dispatch().EndpointFingerprint(), wantEndpoint) ||
		!reflect.DeepEqual(reconciliation.Dispatch().RequestDigest(), wantRequest) {
		t.Fatal("reconciliation retained aliases to the dispatch")
	}
	returned := reconciliation.Dispatch()
	returned.endpointFingerprint[0] ^= 0xff
	returned.requestDigest[0] ^= 0xff
	if !reflect.DeepEqual(reconciliation.Dispatch().EndpointFingerprint(), wantEndpoint) ||
		!reflect.DeepEqual(reconciliation.Dispatch().RequestDigest(), wantRequest) {
		t.Fatal("reconciliation Dispatch returned aliases")
	}

	receipt := deliveryTestDigest("reconciled-provider-receipt")
	wantReceipt := append([]byte(nil), receipt...)
	result, err := NewDeliveryReconciliationResult(
		DeliveryReconciliationAccepted,
		receipt,
		"provider-observation:42",
	)
	if err != nil {
		t.Fatalf("NewDeliveryReconciliationResult: %v", err)
	}
	receipt[0] ^= 0xff
	if !reflect.DeepEqual(result.ProviderReceiptHash(), wantReceipt) {
		t.Fatal("reconciliation result retained caller receipt hash")
	}
	resultCopy := result.ProviderReceiptHash()
	resultCopy[0] ^= 0xff
	if !reflect.DeepEqual(result.ProviderReceiptHash(), wantReceipt) {
		t.Fatal("reconciliation ProviderReceiptHash returned aliases")
	}
	ignored := NormalizeDeliveryReconciliationResult(result, errors.New("untrusted provider detail"))
	if ignored.Outcome() != DeliveryReconciliationIndeterminate || len(ignored.ProviderReceiptHash()) != 0 || ignored.EvidenceRef() != "" {
		t.Fatalf("result plus error normalized to %+v, want safe indeterminate", ignored)
	}
	invalid := result
	invalid.evidenceRef = ""
	ignored = NormalizeDeliveryReconciliationResult(invalid, nil)
	if ignored.Outcome() != DeliveryReconciliationIndeterminate || ignored.EvidenceRef() != "" {
		t.Fatalf("invalid result normalized to %+v, want safe indeterminate", ignored)
	}
}

func TestDeliveryIdempotencyIdentityBindsEveryRequestField(t *testing.T) {
	baseParams := validDeliveryDispatchParams()
	base := mustDeliveryDispatch(t, baseParams).IdempotencyIdentity()
	replay := mustDeliveryDispatch(t, baseParams).IdempotencyIdentity()
	if !base.Equal(replay) || !base.SameOperationKey(replay) || base.Conflicts(replay) {
		t.Fatal("identical dispatches must be exact idempotent replays")
	}

	mutations := []struct {
		name   string
		mutate func(*DeliveryDispatchParams)
	}{
		{"workspace", func(p *DeliveryDispatchParams) { p.WorkspaceID = "workspace-2" }},
		{"delivery", func(p *DeliveryDispatchParams) { p.DeliveryID = "delivery-2" }},
		{"message", func(p *DeliveryDispatchParams) { p.MessageID = "message-2" }},
		{"dispatch", func(p *DeliveryDispatchParams) { p.DispatchID = "dispatch-2" }},
		{"attempt", func(p *DeliveryDispatchParams) { p.AttemptID = "attempt-2" }},
		{"endpoint", func(p *DeliveryDispatchParams) { p.EndpointID = "endpoint-2" }},
		{"endpoint generation", func(p *DeliveryDispatchParams) { p.EndpointGeneration++ }},
		{"endpoint fingerprint", func(p *DeliveryDispatchParams) { p.EndpointFingerprint = deliveryTestDigest("other endpoint") }},
		{"provider", func(p *DeliveryDispatchParams) { p.Provider = "driver:other-provider" }},
		{"transport", func(p *DeliveryDispatchParams) { p.Transport = "https" }},
		{"request digest", func(p *DeliveryDispatchParams) { p.RequestDigest = deliveryTestDigest("other request") }},
		{"message kind", func(p *DeliveryDispatchParams) { p.MessageKind = DeliveryMessageKindRequest }},
		{"urgency", func(p *DeliveryDispatchParams) { p.Urgency = DeliveryUrgencyHigh }},
		{"work item", func(p *DeliveryDispatchParams) { p.WorkItemID = "work-item-2" }},
		{"ack due", func(p *DeliveryDispatchParams) { p.AckDueAt = p.AckDueAt.Add(time.Minute) }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			p := baseParams
			p.EndpointFingerprint = append([]byte(nil), baseParams.EndpointFingerprint...)
			p.RequestDigest = append([]byte(nil), baseParams.RequestDigest...)
			tc.mutate(&p)
			changed := mustDeliveryDispatch(t, p).IdempotencyIdentity()
			if base.Equal(changed) {
				t.Fatal("changed request retained the same complete identity")
			}
			if !base.SameOperationKey(changed) {
				t.Fatal("same tenant+OperationID should collide for rebind detection")
			}
			if !base.Conflicts(changed) {
				t.Fatal("same OperationID rebound to a changed request must conflict")
			}
		})
	}

	differentTenant := baseParams
	differentTenant.TenantID = "tenant-2"
	other := mustDeliveryDispatch(t, differentTenant).IdempotencyIdentity()
	if base.SameOperationKey(other) || base.Conflicts(other) {
		t.Fatal("OperationID is tenant-scoped; a different tenant is a distinct operation key")
	}
	differentOperation := baseParams
	differentOperation.OperationID = "dispatch-op-2"
	other = mustDeliveryDispatch(t, differentOperation).IdempotencyIdentity()
	if base.SameOperationKey(other) || base.Conflicts(other) {
		t.Fatal("different OperationID must be a distinct operation key")
	}
	if (DeliveryIdempotencyIdentity{}).Equal(base) || (DeliveryIdempotencyIdentity{}).SameOperationKey(base) {
		t.Fatal("an incomplete identity must never match a valid request")
	}
}

type deliveryNotifierContractFake struct{}

func (deliveryNotifierContractFake) Capabilities(_ context.Context, endpoint DeliveryEndpointIdentity) (DeliveryCapabilityWitness, error) {
	return NewDeliveryCapabilityWitness(endpoint, NewDeliveryCapabilities(true, true, true, false))
}

func (deliveryNotifierContractFake) Notify(_ context.Context, _ DeliveryDispatch) (DeliveryAttemptResult, error) {
	return NewDeliveryAttemptResult(
		DeliveryAttemptAccepted,
		DeliveryBoundaryCrossed,
		deliveryTestDigest("provider-receipt"),
	)
}

func (deliveryNotifierContractFake) Reconcile(_ context.Context, _ DeliveryReconciliation) (DeliveryReconciliationResult, error) {
	return NewDeliveryReconciliationResult(
		DeliveryReconciliationNotAccepted,
		nil,
		"provider-observation:not-accepted",
	)
}

var _ DeliveryNotifier = deliveryNotifierContractFake{}

func TestDeliveryNotifierContractRoundTrip(t *testing.T) {
	d := mustDeliveryDispatch(t, validDeliveryDispatchParams())
	notifier := deliveryNotifierContractFake{}
	witness, err := notifier.Capabilities(context.Background(), d.EndpointIdentity())
	if err != nil || !witness.Matches(d.EndpointIdentity()) || !witness.Capabilities().Wake() {
		t.Fatalf("capability witness = %+v, err=%v", witness, err)
	}
	attempt, err := notifier.Notify(context.Background(), d)
	if err != nil || attempt.Validate() != nil || attempt.Outcome() != DeliveryAttemptAccepted {
		t.Fatalf("attempt = %+v, err=%v", attempt, err)
	}
	reconciliation, err := NewDeliveryReconciliation(d)
	if err != nil {
		t.Fatalf("NewDeliveryReconciliation: %v", err)
	}
	observed, err := notifier.Reconcile(context.Background(), reconciliation)
	if err != nil || observed.Validate() != nil || observed.Outcome() != DeliveryReconciliationNotAccepted {
		t.Fatalf("reconciliation = %+v, err=%v", observed, err)
	}
}
