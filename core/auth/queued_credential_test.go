// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package auth

import (
	"errors"
	"github.com/olivaresai/olivares/core/store"
	"testing"
)

func TestQueuedCredentialSurvivesStepUpAndRefusesRotation(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	principal, err := f.a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatal(err)
	}
	q, ok := QueuedCredentialFrom(principal)
	if !ok {
		t.Fatal("missing authenticated reference")
	}
	q, err = f.a.BindQueuedCredential(f.ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.ElevateSession(f.ctx, principal, "webauthn", AAL3); err != nil {
		t.Fatal(err)
	}
	resumed, err := f.a.RevalidateQueuedCredential(f.ctx, q)
	if err != nil {
		t.Fatalf("same stepped-up credential refused: %v", err)
	}
	current, err := f.a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatal(err)
	}
	gotRef, gotBound := resumed.Ref()
	wantRef, wantBound := current.Ref()
	if !gotBound || !wantBound || gotRef != wantRef || resumed.Actor() != current.Actor() {
		t.Fatal("durable continuation did not restore the same credential's current authenticated reference")
	}
	replacement, err := NewCredential(PrefixSession)
	if err != nil {
		t.Fatal(err)
	}
	err = f.raw.AuthMutate(f.ctx, func(sc store.AuthScope) error {
		row, err := sc.Sessions().Get(f.ctx, q.ID)
		if err != nil {
			return err
		}
		row.SecretHash = replacement.SecretHash
		_, err = sc.Sessions().Update(f.ctx, row)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.RevalidateQueuedCredential(f.ctx, q); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("rotated credential accepted: %v", err)
	}
}

func TestApprovalCapacityIncludesGroupAdministrator(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	f.addGroup(f.tenant, "administrators", RoleAdmin, true)
	count, err := f.a.ApprovalCapacity(f.ctx, f.tenant)
	if err != nil || count != 1 {
		t.Fatalf("effective administrator count=%d error=%v", count, err)
	}
}
