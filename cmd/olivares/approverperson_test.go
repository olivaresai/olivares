// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// approverperson_test.go pins the ONE translation every downstream quorum
// depends on: approvalApproverEvidence turns immutable decision trail into the
// approver evidence five gates hand to eight counters (the inventory is in
// sessions-cuenta-credenciales.md §1).
//
// It counts PEOPLE. Actor() renders "user:<UserID>" for a session and "token:<CredID>"
// for a token, so ONE human holding both produces TWO actor strings; a translation that
// deduplicates actors reports that human as a two-person quorum. These tests feed the
// trail directly, so nothing here is protected by the far-away invariants that protect
// it in production (the unique index governance/schema.go:521-528 and the two handler
// guards approvals.go:467 / claudeagents.go:198). That is the point: they must hold with
// those invariants ABSENT.

// trailEntry is one decision as the governed API serves it (governance decisionDTO).
type trailEntry struct {
	Decision    string `json:"decision"`
	Decider     string `json:"decider"`
	DeciderUser string `json:"decider_user,omitempty"`
}

// bridgeServingTrail builds a bridge whose only answer is the given decision trail, so a
// test can state exactly what the engine recorded without going through the write path
// (which is precisely what the far invariants police).
func bridgeServingTrail(t *testing.T, entries ...trailEntry) (*approvalBridge, serviceCred) {
	t.Helper()
	tid := model.NewTenantID()
	b := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: tid.String(), Token: "svc-token"}},
	}, discardLog())
	if b == nil {
		t.Fatal("bridge should build")
	}
	b.useHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/decisions") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": entries})
	}))
	cred, ok := b.cred(tid)
	if !ok {
		t.Fatal("credential should resolve")
	}
	return b, cred
}

// TestApproverEvidenceCountsOneHumanBehindTwoCredentialsOnce is the REPRODUCTION: one
// human decides twice, once from a session and once from a token they hold. Two actor
// strings, ONE person. Anything that reports two approvers here hands a two-person
// quorum to an irreversible erasure on a single human's say-so.
func TestApproverEvidenceCountsOneAccountBehindTwoCredentialsOnce(t *testing.T) {
	b, cred := bridgeServingTrail(t,
		trailEntry{Decision: "approve", Decider: "user:alice", DeciderUser: "alice"},
		trailEntry{Decision: "approve", Decider: "token:cred-7", DeciderUser: "alice"},
	)
	ev := b.approvalApproverEvidence(context.Background(), cred, "appr-1")

	if got := len(ev.Persons); got != 1 {
		t.Fatalf("one human with two credentials is ONE approver, got %d: %v", got, ev.Persons)
	}
	if ev.Persons[0] != "alice" {
		t.Fatalf("the person is alice, got %q", ev.Persons[0])
	}
	// The credentials are still the audit provenance — both must survive, distinctly.
	if len(ev.Actors) != 2 {
		t.Fatalf("both credentials must remain as provenance, got %v", ev.Actors)
	}
	if ev.Unattributed != 0 {
		t.Fatalf("both decisions carry a person; unattributed = %d", ev.Unattributed)
	}
}

// TestApproverEvidenceCountsTwoDistinctHumansAsTwo is the other half: the control must
// still RELEASE for two genuinely distinct people. A fix that only ever denies is not a
// fix, and this is the test a "count nothing" mutant fails.
func TestApproverEvidenceCountsTwoDistinctAccountsAsTwo(t *testing.T) {
	b, cred := bridgeServingTrail(t,
		trailEntry{Decision: "approve", Decider: "user:alice", DeciderUser: "alice"},
		trailEntry{Decision: "approve", Decider: "user:bob", DeciderUser: "bob"},
	)
	ev := b.approvalApproverEvidence(context.Background(), cred, "appr-2")
	if got := len(ev.Persons); got != 2 {
		t.Fatalf("two distinct humans are two approvers, got %d: %v", got, ev.Persons)
	}
}

// TestApproverEvidenceRefusesADecisionWithNoPersonBehindIt: a decision row carrying no
// person cannot be counted as one of the two humans (core/auth/person.go Stable). No
// sanctioned writer can produce such a row today — both refuse a zero UserID — so this
// pins the deny-closed behavior for a row that only tampering or a future writer
// missing the guard could create. It must be REPORTED, not silently dropped: "I am one
// human short" and "there is a decision I cannot attribute to a human" are different
// facts and an operator must be able to tell them apart.
func TestApproverEvidenceRefusesADecisionWithNoAccountBehindIt(t *testing.T) {
	b, cred := bridgeServingTrail(t,
		trailEntry{Decision: "approve", Decider: "user:alice", DeciderUser: "alice"},
		trailEntry{Decision: "approve", Decider: "token:system-9"}, // no person behind it
	)
	ev := b.approvalApproverEvidence(context.Background(), cred, "appr-3")

	if got := len(ev.Persons); got != 1 {
		t.Fatalf("a personless credential is not a human; persons = %d %v", got, ev.Persons)
	}
	if ev.Unattributed != 1 {
		t.Fatalf("the personless decision must be REPORTED, unattributed = %d", ev.Unattributed)
	}
	for _, p := range ev.Persons {
		if p == "" || strings.HasPrefix(p, "token:") {
			t.Fatalf("an actor string leaked into the person list: %v", ev.Persons)
		}
	}
}

// TestApproverEvidenceIgnoresNonApprovals: only "approve" decisions are quorum evidence.
// A reject sitting in the trail must never be counted toward the humans who approved.
func TestApproverEvidenceIgnoresNonApprovals(t *testing.T) {
	b, cred := bridgeServingTrail(t,
		trailEntry{Decision: "approve", Decider: "user:alice", DeciderUser: "alice"},
		trailEntry{Decision: "reject", Decider: "user:bob", DeciderUser: "bob"},
	)
	ev := b.approvalApproverEvidence(context.Background(), cred, "appr-4")
	if len(ev.Persons) != 1 || ev.Persons[0] != "alice" {
		t.Fatalf("only approvers count, got %v", ev.Persons)
	}
	if ev.Unattributed != 0 {
		t.Fatalf("a reject is not an unattributed approval, got %d", ev.Unattributed)
	}
}

// TestApproverEvidenceDegradesToNothingOnAReadFailure keeps the deny-closed contract the
// callers rely on ("a read failure degrades to zero approvers"): no evidence is never a
// fabricated approver.
func TestApproverEvidenceDegradesToNothingOnAReadFailure(t *testing.T) {
	tid := model.NewTenantID()
	b := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: tid.String(), Token: "svc-token"}},
	}, discardLog())
	if b == nil {
		t.Fatal("bridge should build")
	}
	b.useHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	cred, _ := b.cred(tid)
	ev := b.approvalApproverEvidence(context.Background(), cred, "appr-5")
	if len(ev.Persons) != 0 || len(ev.Actors) != 0 || ev.Unattributed != 0 {
		t.Fatalf("an unreadable trail is no evidence at all, got %+v", ev)
	}
}
