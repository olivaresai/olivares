// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionPairingPendingRefIsNotAuthority(t *testing.T) {
	f := newPrincipalEvidenceFixture(t)
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		session, err := as.Sessions().Get(f.ctx, f.session.ID)
		if err != nil {
			return err
		}
		session.PairingSourceID = model.NewID()
		f.session, err = as.Sessions().Update(f.ctx, session)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ref := PrincipalRef{kind: KindUser, credentialID: f.session.ID, version: f.session.Version}
	if _, err := f.a.ResolvePrincipalScope(f.deadline(time.Minute), ref, f.tenant); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("pending scope proof=%v", err)
	}
	if err := f.a.credentialBindingAdmission(context.Background(), ref, f.tenant); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("pending credential binding=%v", err)
	}
	if _, err := f.a.BindQueuedCredential(f.ctx, QueuedCredential{ID: f.session.ID, UserID: f.user.ID, Kind: KindUser, Version: f.session.Version}); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("pending queue binding=%v", err)
	}
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		return verifyCredentialPin(f.ctx, as, credentialPin{kind: KindUser, id: f.session.ID, version: f.session.Version, user: f.user.ID})
	}); !errors.Is(err, ErrCredentialBindingInvalid) {
		t.Fatalf("pending credential pin=%v", err)
	}
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		_, _, _, err := f.a.revalidateSubject(f.ctx, as, model.DelegationHandle{SourceCredKind: "user", SourceCredID: f.session.ID, SubjectUserID: f.user.ID}, f.tenant)
		return err
	}); !errors.Is(err, ErrDelegationInvalid) {
		t.Fatalf("pending delegation source=%v", err)
	}

}
