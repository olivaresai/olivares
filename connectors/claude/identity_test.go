// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestIdentityEdges(t *testing.T) {
	ev := claudeEvent{
		name: evtToolResult, sessionID: "s", orgID: "org-1",
		accountID: "user_01ABC", agentName: "reviewer", at: testTime,
	}
	edges := identityEdges(ev.identity(), ev.at)
	if len(edges) != 3 {
		t.Fatalf("want 3 identity edges, got %d", len(edges))
	}
	byKind := map[string]model.EdgeObservation{}
	for _, e := range edges {
		byKind[e.ResourceKind] = e
		if e.OriginKind != originSession || e.OriginRef != "s" {
			t.Errorf("origin = %s/%s", e.OriginKind, e.OriginRef)
		}
		if e.Mode != model.ModeUnknown || e.Source != model.SignalOTEL || e.Confidence != model.ConfidenceAttributed {
			t.Errorf("provenance = %s/%s/%s", e.Mode, e.Source, e.Confidence)
		}
	}
	if byKind[resIdentityOrg].ResourceRef != "org-1" {
		t.Errorf("org edge ref = %q", byKind[resIdentityOrg].ResourceRef)
	}
	if byKind[resIdentityAccount].ResourceRef != "user_01ABC" {
		t.Errorf("account edge ref = %q", byKind[resIdentityAccount].ResourceRef)
	}
	if byKind[resIdentityAgent].ResourceRef != "reviewer" {
		t.Errorf("agent edge ref = %q", byKind[resIdentityAgent].ResourceRef)
	}
}

func TestIdentityEdgesPartial(t *testing.T) {
	// Only org present → one edge; no session → none.
	if got := identityEdges(claudeIdentity{sessionID: "s", orgID: "org-1"}, testTime); len(got) != 1 {
		t.Errorf("partial identity: want 1 edge, got %d", len(got))
	}
	if got := identityEdges(claudeIdentity{orgID: "org-1"}, testTime); got != nil {
		t.Errorf("identity without a session must produce no edges, got %d", len(got))
	}
	if got := identityEdges(claudeIdentity{sessionID: "s"}, testTime); got != nil {
		t.Errorf("session with no identity attributes must produce no edges, got %d", len(got))
	}
}

func TestIdentitySeenFirstOncePerSession(t *testing.T) {
	s := newIdentitySeen()
	if !s.first("a") {
		t.Error("first sight of session a must be true")
	}
	if s.first("a") {
		t.Error("second sight of session a must be false")
	}
	if !s.first("b") {
		t.Error("first sight of session b must be true")
	}
	if s.first("") {
		t.Error("empty session is never first")
	}
}
