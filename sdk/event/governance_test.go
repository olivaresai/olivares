// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package event_test

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/event"
)

func TestApprovalRequestedRoundTrip(t *testing.T) {
	when := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	a := event.ApprovalRequest{
		ApprovalID: "ap-1", Action: "nhi.rotate", SubjectKind: "managed_agent",
		RiskTier: "critical", RequiredApprovals: 2, PolicyRef: "pol-7",
		ExpiresAt: when.Add(time.Hour),
	}
	e := event.ApprovalRequested("tenant-1", "module:governance", when, a)
	if e.Type != event.TypeApprovalRequested {
		t.Errorf("type = %q, want approval.requested", e.Type)
	}
	if e.Tenant != "tenant-1" || e.Source != "module:governance" || !e.Time.Equal(when) {
		t.Errorf("envelope not stamped: %+v", e)
	}
	got, ok := event.ApprovalRequestOf(e)
	if !ok || got != a {
		t.Fatalf("ApprovalRequestOf failed: ok=%v got=%+v", ok, got)
	}
	// Pointer payload on a directly built event is accepted too.
	direct := event.Event{Type: event.TypeApprovalRequested, Payload: &a}
	if g, ok := event.ApprovalRequestOf(direct); !ok || g != a {
		t.Errorf("ApprovalRequestOf must accept a pointer payload: ok=%v got=%+v", ok, g)
	}
}

func TestApprovalResolvedRoundTrip(t *testing.T) {
	when := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	a := event.ApprovalResolution{
		ApprovalID: "ap-1", Action: "nhi.rotate", SubjectKind: "managed_agent",
		RiskTier: "critical", Outcome: "approved", RequiredApprovals: 2,
		ApproveCount: 2, RejectCount: 0, PolicyRef: "pol-7", DecidedAt: when,
	}
	e := event.ApprovalResolved("tenant-1", "module:governance", when, a)
	if e.Type != event.TypeApprovalResolved {
		t.Errorf("type = %q, want approval.resolved", e.Type)
	}
	if e.Tenant != "tenant-1" || e.Source != "module:governance" || !e.Time.Equal(when) {
		t.Errorf("envelope not stamped: %+v", e)
	}
	got, ok := event.ApprovalResolutionOf(e)
	if !ok || got != a {
		t.Fatalf("ApprovalResolutionOf failed: ok=%v got=%+v", ok, got)
	}
	direct := event.Event{Type: event.TypeApprovalResolved, Payload: &a}
	if g, ok := event.ApprovalResolutionOf(direct); !ok || g != a {
		t.Errorf("ApprovalResolutionOf must accept a pointer payload: ok=%v got=%+v", ok, g)
	}
}

func TestPolicyChangedRoundTrip(t *testing.T) {
	when := time.Date(2026, 6, 11, 9, 30, 0, 0, time.UTC)
	p := event.PolicyChange{PolicyID: "pol-1", Kind: "abac", Op: event.PolicyOpUpdated, Enabled: true}
	e := event.PolicyChanged("tenant-1", "module:governance", when, p)
	if e.Type != event.TypePolicyChanged {
		t.Errorf("type = %q, want policy.changed", e.Type)
	}
	got, ok := event.PolicyChangeOf(e)
	if !ok || got != p {
		t.Fatalf("PolicyChangeOf failed: ok=%v got=%+v", ok, got)
	}
	direct := event.Event{Type: event.TypePolicyChanged, Payload: &p}
	if g, ok := event.PolicyChangeOf(direct); !ok || g != p {
		t.Errorf("PolicyChangeOf must accept a pointer payload: ok=%v got=%+v", ok, g)
	}
}

// The governance accessors reject wrong types and mismatched payloads rather
// than returning a zero value with ok=true (the EdgeOf contract).
func TestGovernanceAccessorsRejectMismatch(t *testing.T) {
	ap := event.ApprovalRequested("t", "s", time.Time{}, event.ApprovalRequest{ApprovalID: "x"})
	if _, ok := event.PolicyChangeOf(ap); ok {
		t.Error("PolicyChangeOf on an approval.requested event should be false")
	}
	if _, ok := event.ApprovalRequestOf(event.Event{Type: event.TypeApprovalRequested, Payload: event.PolicyChange{}}); ok {
		t.Error("ApprovalRequestOf must reject a mismatched payload")
	}
	if _, ok := event.ApprovalResolutionOf(event.Event{Type: event.TypeApprovalResolved, Payload: event.ApprovalRequest{}}); ok {
		t.Error("ApprovalResolutionOf must reject a mismatched payload")
	}
	if _, ok := event.ApprovalResolutionOf(event.Event{Type: event.TypeApprovalRequested, Payload: event.ApprovalResolution{}}); ok {
		t.Error("ApprovalResolutionOf on an approval.requested event should be false")
	}
	if _, ok := event.PolicyChangeOf(event.Event{Type: event.TypePolicyChanged, Payload: event.ApprovalRequest{}}); ok {
		t.Error("PolicyChangeOf must reject a mismatched payload")
	}
}
