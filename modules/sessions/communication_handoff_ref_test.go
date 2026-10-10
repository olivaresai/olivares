// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	handoffRefTestSHA1   = "0123456789abcdef0123456789abcdef01234567"
	handoffRefTestSHA256 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// TestHandoffContentWithoutBranchKeepsItsExactBytes is the compatibility proof of the
// optional branch and sha: a handoff that names neither is canonicalized to the bytes
// it had before the two fields existed, so every stored payload and digest still
// verifies, and the plain-JSON slot check still accepts those bytes.
func TestHandoffContentWithoutBranchKeepsItsExactBytes(t *testing.T) {
	t.Parallel()

	raw, err := CanonicalProtectedPayloadSlot(PayloadSlotHandoff,
		HandoffContent{Summary: "S", NextAction: "N", Risk: "R"})
	if err != nil {
		t.Fatalf("canonicalize an old-style handoff: %v", err)
	}
	const want = `{"next_action":"N","risk":"R","summary":"S"}`
	if string(raw) != want {
		t.Fatalf("old-style handoff bytes = %s, want %s", raw, want)
	}
	if err := validatePlainPayloadSlot(PayloadSlotHandoff, raw); err != nil {
		t.Fatalf("an old-style handoff stored before the fields existed no longer validates: %v", err)
	}
}

func TestHandoffContentCarriesBranchAndSHA(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]HandoffContent{
		"branch and sha": {Summary: "S", NextAction: "N", Branch: "olivares/3f9a1c20", SHA: handoffRefTestSHA1},
		"branch only":    {Summary: "S", NextAction: "N", Branch: "feature/handoff"},
		"sha only":       {Summary: "S", NextAction: "N", SHA: handoffRefTestSHA1},
		"sha-256 object": {Summary: "S", NextAction: "N", SHA: handoffRefTestSHA256},
	} {
		raw, err := CanonicalProtectedPayloadSlot(PayloadSlotHandoff, content)
		if err != nil {
			t.Fatalf("%s: canonicalize: %v", name, err)
		}
		if err := validatePlainPayloadSlot(PayloadSlotHandoff, raw); err != nil {
			t.Fatalf("%s: the canonical bytes do not validate: %v", name, err)
		}
		var back HandoffContent
		if err := json.Unmarshal(raw, &back); err != nil || !reflect.DeepEqual(back, content) {
			t.Fatalf("%s: round trip = %+v (%v), want %+v", name, back, err, content)
		}
		if content.Branch != "" && !strings.Contains(string(raw), `"branch":"`+content.Branch+`"`) ||
			content.SHA != "" && !strings.Contains(string(raw), `"sha":"`+content.SHA+`"`) {
			t.Fatalf("%s: the bytes lost a field: %s", name, raw)
		}
	}
}

func TestHandoffContentRefusesAMalformedBranchOrSHA(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]HandoffContent{
		"short sha":           {Summary: "S", NextAction: "N", SHA: handoffRefTestSHA1[:12]},
		"uppercase sha":       {Summary: "S", NextAction: "N", SHA: strings.ToUpper(handoffRefTestSHA1)},
		"non-hex sha":         {Summary: "S", NextAction: "N", SHA: strings.Repeat("g", 40)},
		"sha with a revision": {Summary: "S", NextAction: "N", SHA: "HEAD~1"},
		"sha of 41 digits":    {Summary: "S", NextAction: "N", SHA: handoffRefTestSHA1 + "0"},
		"branch with newline": {Summary: "S", NextAction: "N", Branch: "a\nb"},
		"branch with padding": {Summary: "S", NextAction: "N", Branch: " main"},
		"branch past a ref":   {Summary: "S", NextAction: "N", Branch: strings.Repeat("b", 513)},
		"option as branch":    {Summary: "S", NextAction: "N", Branch: "--upload-pack=x"},
		"branch with a tab":   {Summary: "S", NextAction: "N", Branch: "a\tb"},
		"branch with a bidi":  {Summary: "S", NextAction: "N", Branch: "main‮txt"},
		"branch with escape":  {Summary: "S", NextAction: "N", Branch: "a\x1b[31mb"},
	} {
		if _, err := CanonicalProtectedPayloadSlot(PayloadSlotHandoff, content); err == nil {
			t.Errorf("%s: accepted %+v", name, content)
		}
	}
}

// TestHandoffOfferedWithBranchAndSHAIsReadBack takes the real offer and the recipient's
// own read: the fields reach the recipient exactly, and a handoff that names neither
// shows neither key to a client.
func TestHandoffOfferedWithBranchAndSHAIsReadBack(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	// On every engine the run has: SQLite always, PostgreSQL when its test runtime is up.
	for _, engine := range vacantTransferEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			for name, content := range map[string]HandoffContent{
				"old style": {Summary: "Old style", NextAction: "Continue"},
				"with refs": {
					Summary: "Branch handoff", NextAction: "Review the branch",
					Branch: "olivares/3f9a1c20", SHA: handoffRefTestSHA1,
				},
			} {
				backend := engine.newBackend(t, "handoff-ref")
				fixture := newIncomingHandoffFixtureOn(t, backend)
				fixture.offer(t, content)
				read, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
					ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID)
				if err != nil {
					t.Fatalf("%s: recipient read: %v", name, err)
				}
				if !reflect.DeepEqual(read.Content, content) {
					t.Fatalf("%s: recipient content = %+v, want %+v", name, read.Content, content)
				}
				wire, err := json.Marshal(read.Content)
				if err != nil {
					t.Fatalf("%s: marshal: %v", name, err)
				}
				hasKeys := bytes.Contains(wire, []byte(`"branch"`)) || bytes.Contains(wire, []byte(`"sha"`))
				if hasKeys != (content.Branch != "" || content.SHA != "") {
					t.Fatalf("%s: wire content = %s", name, wire)
				}
			}
		})
	}
}

// newIncomingHandoffFixtureOn is newIncomingHandoffFixture on a chosen engine.
func newIncomingHandoffFixtureOn(t *testing.T, backend communicationSchemaBackend) incomingHandoffFixture {
	t.Helper()
	base := newHandoffServiceFixtureFor(t, handoffServiceFixtureSpec{backend: &backend})
	ring := newChannelCatalogNavigationKeyring(t, "k3handoffread")
	base.m.CursorKeyring = ring
	return incomingHandoffFixture{handoffServiceFixture: base, ring: ring}
}
