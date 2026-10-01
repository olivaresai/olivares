// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func successorCommunicationReadPrincipal(t *testing.T, f handoffRecognitionFixture) auth.PrincipalRef {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	witness, err := f.m.CommunicationSessionRecipient(ctx, f.tenant, f.workspace, f.sid)
	if err != nil || !witness.Active {
		t.Fatalf("read original session witness: %+v, %v", witness, err)
	}
	claim := handoffRecognitionClaim(t, f)
	if err := f.m.Release(ctx, f.tenant, f.sid, claim.String(colHolder), witness.Fence); err != nil {
		t.Fatal(err)
	}
	lease, err := f.m.Claim(ctx, f.tenant, f.sid, "resumed-reader", time.Hour)
	if err != nil || lease.Fence <= witness.Fence {
		t.Fatalf("acquire successor Claim: %+v, %v", lease, err)
	}
	issuer, err := auth.NewSystemOperator("test:resumed-reader", "issue successor session fixture credential")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := f.authr.IssueCommunicationSessionCredential(ctx, issuer, auth.CommunicationSessionCredentialSpec{
		Tenant: f.tenant, WorkspaceID: f.workspace, SessionRef: f.sid,
		RunRef: witness.RunRef, AgentRef: witness.AgentRef, ClaimFence: lease.Fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := f.authr.Authenticate(ctx, credential.Token)
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := principal.Ref()
	if !ok {
		t.Fatal("successor credential has no principal reference")
	}
	// Issuing the successor revokes its predecessor and advances authorization.
	// The fixture PDP must report that fresh epoch, as the real PDP does.
	if err := f.m.viewCommunication(ctx, f.scope, func(sc store.Scope) error {
		epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		fact, err := sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
		if err == nil {
			f.directory.epoch, f.grantClosure.epoch = epoch.Version, epoch.Version
			f.source.evidence.Facts = []store.AuthorizationFactRef{fact, {
				Kind: model.DirectoryEpochKind, ID: model.ID(f.tenant), Version: epoch.Version,
			}}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestSessionResumeReadsEarlierCarriers(t *testing.T) {
	f, _, _, _ := handoffRecognitionAccepted(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// The original generation can read its accepted handoff before retirement.
	if _, err := f.m.getIncomingHandoffByDeliveryWithAuthority(ctx, f.scope, f.targetRef, f.delivery.ID); err != nil {
		t.Fatal(err)
	}
	ref := successorCommunicationReadPrincipal(t, f)
	var opens int
	opener := func(ctx context.Context, sealer CommunicationContentSealer, plan ProtectedPayloadOpenPlan) (json.RawMessage, error) {
		opens++
		return OpenProtectedPayload(ctx, sealer, plan)
	}
	_, err := f.m.getIncomingHandoffByDeliveryWithAuthorityAndOpener(ctx, f.scope, ref, f.delivery.ID, opener)
	if err != nil || opens == 0 {
		t.Fatalf("successor handoff read = %v, opens=%d; want earlier content", err, opens)
	}
}

func TestSessionResumeReadWithdrawnDuringOpen(t *testing.T) {
	f, _, _, _ := handoffRecognitionAccepted(t)
	ref := successorCommunicationReadPrincipal(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var withdrawn bool
	got, err := f.m.getIncomingHandoffByDeliveryWithAuthorityAndOpener(ctx, f.scope, ref, f.delivery.ID,
		func(ctx context.Context, sealer CommunicationContentSealer, plan ProtectedPayloadOpenPlan) (json.RawMessage, error) {
			content, err := OpenProtectedPayload(ctx, sealer, plan)
			if !withdrawn {
				withdrawn = true
				claim := handoffRecognitionClaim(t, f)
				if releaseErr := f.m.Release(ctx, f.tenant, f.sid, claim.String(colHolder), claim.Int(colFence)); releaseErr != nil {
					t.Fatal(releaseErr)
				}
			}
			return content, err
		})
	if !withdrawn || err == nil || got.Content.Summary != "" {
		t.Fatalf("withdrawn successor published bytes: withdrawn=%t, %+v, %v", withdrawn, got, err)
	}
}

func TestSessionReadCorruptGenerationRemainsUnknown(t *testing.T) {
	f, _, _, _ := handoffRecognitionAccepted(t)
	ref := successorCommunicationReadPrincipal(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, column := range []string{colCommObservedClaimFence, colCommObservedSessionSID} {
		t.Run(column, func(t *testing.T) {
			attempt := &handoffRecognitionAttempt{list: func(kind model.Kind, _ int, rows []model.Record, page model.Page, err error) ([]model.Record, model.Page, error) {
				if kind == messageAudienceRecipientKind && len(rows) > 0 {
					rows[0] = maps.Clone(rows[0])
					if column == colCommObservedClaimFence {
						rows[0][column] = rows[0].Int(column) + 1
					} else {
						rows[0][column] = "osn_" + model.NewID().String()
					}
				}
				return rows, page, err
			}}
			var opens int
			_, err := f.m.getIncomingHandoffByDeliveryWithAuthorityAndOpener(context.WithValue(ctx, handoffRecognitionAttemptKey{}, attempt), f.scope, ref, f.delivery.ID,
				func(context.Context, CommunicationContentSealer, ProtectedPayloadOpenPlan) (json.RawMessage, error) {
					opens++
					return nil, errors.New("corrupt provenance must not reach payload open")
				})
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || opens != 0 {
				t.Fatalf("corrupt generation = %v, opens=%d; want UNKNOWN", err, opens)
			}
		})
	}
}
