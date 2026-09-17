// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCommunicationSessionRecipientWitnessTracksExactClaim(t *testing.T) {
	t.Parallel()
	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	ctx := context.Background()
	sid, err := f.m.ResolveSession(ctx, f.tenant, SessionBinding{
		Provider: "claude", ExternalID: model.NewID().String(), At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	witness, err := f.m.CommunicationSessionRecipient(ctx, f.tenant, f.workspace, sid)
	if err != nil {
		t.Fatalf("unclaimed witness: %v", err)
	}
	if !witness.Found || !witness.WorkspaceEligible || witness.Active || witness.Fence != 0 || witness.SID != sid {
		t.Fatalf("unclaimed witness = %+v", witness)
	}
	lease, err := f.m.Claim(ctx, f.tenant, sid, "holder-a", 0)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	witness, err = f.m.CommunicationSessionRecipient(ctx, f.tenant, f.workspace, sid)
	if err != nil || !witness.Active || witness.Fence != lease.Fence || witness.ClaimExpiresAt.IsZero() {
		t.Fatalf("claimed witness = %+v err=%v", witness, err)
	}
	other, err := f.m.CommunicationSessionRecipient(ctx, f.tenant, model.NewID(), sid)
	if err != nil || !other.Found || other.WorkspaceEligible {
		t.Fatalf("other-workspace witness = %+v err=%v", other, err)
	}
	for _, sid := range []string{"osn_" + model.NewID().String(), "not-a-sid", ""} {
		absent, err := f.m.CommunicationSessionRecipient(ctx, f.tenant, f.workspace, sid)
		if err != nil || absent.Found || absent.Active {
			t.Fatalf("absent witness for %q = %+v err=%v", sid, absent, err)
		}
	}
	unwired := New()
	if _, err := unwired.CommunicationSessionRecipient(ctx, f.tenant, f.workspace, sid); err == nil {
		t.Fatal("unwired module answered instead of refusing")
	}

	// A launched session: the operated alias binds the sid to a run row, and the
	// witness reports that run and its agent attribution.
	runRef := model.NewID().String()
	if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{
			colRunRef: runRef, colTransport: string(TransportStreamJSON), colPermissionMode: "default",
			colIsolation: string(IsolationNative), colState: stateRunning, colLastEventSeq: int64(0),
			colRunAgentRef: "agent-x",
		})
		return err
	}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	launched, err := f.m.ResolveSession(ctx, f.tenant, SessionBinding{
		Provider: ProviderOperated, ExternalID: runRef, Origin: OriginOperated, WorkspaceID: f.workspace,
		At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("bind operated session: %v", err)
	}
	witness, err = f.m.CommunicationSessionRecipient(ctx, f.tenant, f.workspace, launched)
	if err != nil || !witness.Found || !witness.WorkspaceEligible || witness.Active ||
		witness.RunRef != runRef || witness.AgentRef != "agent-x" {
		t.Fatalf("launched witness = %+v err=%v", witness, err)
	}
}

func TestCommunicationCursorTokenKeyringPublicPort(t *testing.T) {
	t.Parallel()
	current := bytes.Repeat([]byte{0x11}, 32)
	previous := bytes.Repeat([]byte{0x22}, 40)
	keyring, err := NewCommunicationCursorTokenKeyring("k2", []CommunicationCursorTokenKey{
		{KID: "k2", Material: current}, {KID: "k1", Material: previous},
	})
	if err != nil {
		t.Fatalf("construct keyring: %v", err)
	}
	// The snapshot is immutable: erasing the caller's buffers changes nothing.
	for i := range current {
		current[i] = 0
	}
	if keyring.SigningKID() != "k2" || len(keyring.VerificationKIDs()) != 2 || keyring.VerificationKIDs()[0] != "k1" {
		t.Fatalf("keyring identifiers = %s %v", keyring.SigningKID(), keyring.VerificationKIDs())
	}
	if err := keyring.SelfTest(time.Now()); err != nil {
		t.Fatalf("self-test: %v", err)
	}
	for name, keys := range map[string]struct {
		signing string
		keys    []CommunicationCursorTokenKey
	}{
		"missing current": {"k9", []CommunicationCursorTokenKey{{KID: "k1", Material: previous}}},
		"duplicate kid":   {"k1", []CommunicationCursorTokenKey{{KID: "k1", Material: previous}, {KID: "k1", Material: previous}}},
		"short material":  {"k1", []CommunicationCursorTokenKey{{KID: "k1", Material: []byte("short")}}},
		"empty ring":      {"k1", nil},
	} {
		if _, err := NewCommunicationCursorTokenKeyring(keys.signing, keys.keys); err == nil {
			t.Errorf("%s: invalid keyring accepted", name)
		}
	}

	m := New()
	if m.CommunicationCursorTokenKeyringBound() || m.communicationCursorTokenKeyring() != nil {
		t.Fatal("fresh module reports a bound cursor keyring")
	}
	m.UseCommunicationCursorTokenKeyring(keyring)
	ready, err := m.CommunicationCursorTokenKeyringReady(context.Background())
	if !m.CommunicationCursorTokenKeyringBound() || m.communicationCursorTokenKeyring() == nil || !ready || err != nil {
		t.Fatal("binding a valid keyring did not make it available")
	}
	m.UseCommunicationCursorTokenKeyring(nil)
	if m.CommunicationCursorTokenKeyringBound() {
		t.Fatal("nil keyring stayed bound")
	}
}
