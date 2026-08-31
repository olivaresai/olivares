// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestDelegationDescriptorsCreateOnFreshStore(t *testing.T) {
	st := openSQLiteTest(t, nil)
	ss := st.(*sqlStore)

	for table, descriptor := range map[string]model.EntityDescriptor{
		"pep_services":            pepServiceDescriptor,
		"pep_service_credentials": pepServiceCredentialDescriptor,
		"delegation_handles":      delegationHandleDescriptor,
		"pdp_decision_claims":     pdpDecisionClaimDescriptor,
	} {
		if got, want := tableColumns(t, ss.db, table), descriptor.AllColumns(); !equalStrings(got, want) {
			t.Errorf("%s columns = %v, want %v", table, got, want)
		}
	}

	for _, index := range []string{
		"pep_services_name_uniq",
		"pep_service_credentials_token_id_uniq",
		"delegation_handles_selector_uniq",
		"delegation_handles_expires_at_idx",
		"pdp_decision_claims_handle_jti_uniq",
		"pdp_decision_claims_service_nonce_uniq",
		"pdp_decision_claims_state_idx",
		"pdp_decision_claims_state_claimed_at_idx",
	} {
		if !indexExists(t, ss.db, index) {
			t.Errorf("fresh store is missing index %q", index)
		}
	}
}

func TestDelegationAuthRepositoriesRoundTripCRUD(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	now := model.NewTimestamp(time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC))
	expires := model.NewTimestamp(now.Time().Add(5 * time.Minute))
	disabled := model.NewTimestamp(now.Time().Add(time.Minute))
	revoked := model.NewTimestamp(now.Time().Add(2 * time.Minute))

	var service model.PEPService
	var sourceUser, actAsUser model.User
	var sourceToken model.APIToken
	var credential model.PEPServiceCredential
	var handle model.DelegationHandle
	var claim model.PDPDecisionClaim
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		var err error
		sourceUser, err = a.Users().Create(ctx, model.User{
			Email: "delegation-roundtrip-source@example.test", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		actAsUser, err = a.Users().Create(ctx, model.User{
			Email: "delegation-roundtrip-act-as@example.test", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		sourceToken, err = a.Tokens().Create(ctx, model.APIToken{
			Name: "delegation-roundtrip-source", UserID: sourceUser.ID,
			Selector: "delegation-roundtrip-source", SecretHash: []byte("source-hash"),
		})
		if err != nil {
			return err
		}
		service, err = a.PEPServices().Create(ctx, model.PEPService{
			Name:              "litellm",
			PDPAudience:       "urn:olivares:pdp:inference",
			Capabilities:      map[string]bool{"buffer_request": true, "streaming": true},
			CapabilityVersion: 4,
		})
		if err != nil {
			return err
		}
		credential, err = a.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
			ServiceID: service.ID,
			TokenID:   sourceToken.ID,
		})
		if err != nil {
			return err
		}
		handle, err = a.DelegationHandles().Create(ctx, model.DelegationHandle{
			Selector:       "handle-selector",
			SecretHash:     []byte("secret-hash"),
			SourceCredKind: "token",
			SourceCredID:   sourceToken.ID,
			SubjectUserID:  sourceUser.ID,
			ActAsUserID:    actAsUser.ID,
			AgentRef:       "agent-ext-1",
			MintRole:       "editor",
			MintGroups:     []string{"engineering", "ml-platform"},
			PEPServiceID:   service.ID,
			Audience:       service.PDPAudience,
			Operations:     []string{"messages", "messages_batch"},
			BoundDigest:    "sha256:bound",
			ExpiresAt:      expires,
		})
		if err != nil {
			return err
		}
		// Claims are created only through the specialized ClaimDecision op (the
		// generic Create is not exposed on the read-only claim surface).
		claim, _, err = a.ClaimDecision(ctx, model.PDPDecisionClaim{
			HandleJTI:             handle.ID,
			PEPServiceID:          service.ID,
			NonceHash:             "nonce-hash",
			RequestFingerprint:    "request-fingerprint",
			RequestIssuedAt:       now,
			CapabilityVersion:     4,
			EffectiveCapabilities: map[string]bool{"buffer_request": true},
		}, nil)
		return err
	}); err != nil {
		t.Fatalf("create delegation records: %v", err)
	}

	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		var err error
		service, err = a.PEPServices().Get(ctx, service.ID)
		if err != nil {
			return err
		}
		if service.Name != "litellm" || !service.Capabilities["streaming"] {
			t.Fatalf("PEP service round-trip = %+v", service)
		}
		service.CapabilityVersion = 5
		service.DisabledAt = &disabled
		service, err = a.PEPServices().Update(ctx, service)
		if err != nil {
			return err
		}

		credential, err = a.PEPServiceCredentials().Get(ctx, credential.ID)
		if err != nil {
			return err
		}
		credential.DisabledAt = &disabled
		credential, err = a.PEPServiceCredentials().Update(ctx, credential)
		if err != nil {
			return err
		}

		handle, err = a.DelegationHandles().Get(ctx, handle.ID)
		if err != nil {
			return err
		}
		if len(handle.MintGroups) != 2 || len(handle.Operations) != 2 {
			t.Fatalf("delegation handle round-trip = %+v", handle)
		}
		// Handles are revoked only through the specialized RevokeDelegationHandle op
		// (the generic Update is not exposed on the narrow handle surface).
		if _, err := a.RevokeDelegationHandle(ctx, handle.ID, revoked); err != nil {
			return err
		}
		handle, err = a.DelegationHandles().Get(ctx, handle.ID)
		if err != nil {
			return err
		}

		claim, err = a.PDPDecisionClaims().Get(ctx, claim.ID)
		if err != nil {
			return err
		}
		if !claim.EffectiveCapabilities["buffer_request"] || claim.RequestIssuedAt.String() != now.String() {
			t.Fatalf("PDP decision claim round-trip = %+v", claim)
		}
		// Claims are mutated only through the specialized, pending-guarded
		// FinalizeDecisionClaim op (the generic Update is not exposed). The store
		// self-guards the verdict material, so the hash must match the bytes.
		verdictJSON := []byte(`{"decision":"allow"}`)
		if _, err := a.FinalizeDecisionClaim(ctx, claim.ID, claim.Version, verdictJSON, sha256HexBytes(verdictJSON), "policy-v1"); err != nil {
			return err
		}
		claim, err = a.PDPDecisionClaims().Get(ctx, claim.ID)
		return err
	}); err != nil {
		t.Fatalf("read/update delegation records: %v", err)
	}

	if service.CapabilityVersion != 5 || service.DisabledAt == nil {
		t.Fatalf("updated PEP service = %+v", service)
	}
	if credential.DisabledAt == nil || handle.RevokedAt == nil {
		t.Fatalf("updated credential/handle = credential=%+v handle=%+v", credential, handle)
	}
	if claim.State != "final" || claim.FinalizedAt == nil {
		t.Fatalf("updated claim = %+v", claim)
	}

	// A decision claim is a single-use record with no generic Delete: it is
	// retained (its unique handle_jti owns the single use). The other three
	// delegation entities support generic Delete.
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		for _, del := range []func() error{
			func() error { return a.DelegationHandles().Delete(ctx, handle.ID) },
			func() error { return a.PEPServiceCredentials().Delete(ctx, credential.ID) },
			func() error { return a.PEPServices().Delete(ctx, service.ID) },
		} {
			if err := del(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("delete delegation records: %v", err)
	}

	if err := st.AuthView(ctx, func(a store.AuthScope) error {
		for name, get := range map[string]func() error{
			"PEP service": func() error {
				_, err := a.PEPServices().Get(ctx, service.ID)
				return err
			},
			"PEP service credential": func() error {
				_, err := a.PEPServiceCredentials().Get(ctx, credential.ID)
				return err
			},
			"delegation handle": func() error {
				_, err := a.DelegationHandles().Get(ctx, handle.ID)
				return err
			},
		} {
			if err := get(); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s after delete err = %v, want ErrNotFound", name, err)
			}
		}
		// The retained claim is still readable.
		if _, err := a.PDPDecisionClaims().Get(ctx, claim.ID); err != nil {
			t.Errorf("retained decision claim Get err = %v, want nil", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify deletes: %v", err)
	}
}

func TestDelegationUniqueConstraints(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	expires := model.NewTimestamp(time.Now().Add(5 * time.Minute))

	var serviceA, serviceB model.PEPService
	var sourceUser model.User
	var sourceToken model.APIToken
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		var err error
		sourceUser, err = a.Users().Create(ctx, model.User{
			Email: "delegation-unique-source@example.test", Status: model.StatusActive,
		})
		if err != nil {
			return err
		}
		sourceToken, err = a.Tokens().Create(ctx, model.APIToken{
			Name: "delegation-unique-source", UserID: sourceUser.ID,
			Selector: "delegation-unique-source", SecretHash: []byte("source-hash"),
		})
		if err != nil {
			return err
		}
		serviceA, err = a.PEPServices().Create(ctx, model.PEPService{Name: "pep-a"})
		if err != nil {
			return err
		}
		serviceB, err = a.PEPServices().Create(ctx, model.PEPService{Name: "pep-b"})
		if err != nil {
			return err
		}
		if _, err = a.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
			ServiceID: serviceA.ID, TokenID: sourceToken.ID,
		}); err != nil {
			return err
		}
		candidate := testDelegationHandle("selector-a", serviceA.ID, expires)
		candidate.SourceCredID = sourceToken.ID
		candidate.SubjectUserID = sourceUser.ID
		_, err = a.DelegationHandles().Create(
			ctx, candidate,
		)
		return err
	}); err != nil {
		t.Fatalf("seed unique constraints: %v", err)
	}

	tests := []struct {
		name string
		run  func(store.AuthScope) error
	}{
		{
			name: "PEP service tenant and name",
			run: func(a store.AuthScope) error {
				_, err := a.PEPServices().Create(ctx, model.PEPService{Name: serviceA.Name})
				return err
			},
		},
		{
			name: "PEP service credential token",
			run: func(a store.AuthScope) error {
				_, err := a.PEPServiceCredentials().Create(ctx, model.PEPServiceCredential{
					ServiceID: serviceB.ID, TokenID: sourceToken.ID,
				})
				return err
			},
		},
		{
			name: "delegation handle selector",
			run: func(a store.AuthScope) error {
				candidate := testDelegationHandle("selector-a", serviceB.ID, expires)
				candidate.SourceCredID = sourceToken.ID
				candidate.SubjectUserID = sourceUser.ID
				_, err := a.DelegationHandles().Create(
					ctx, candidate,
				)
				return err
			},
		},
		// The pdp_decision_claims unique constraints (handle_jti and service+nonce)
		// are exercised through the specialized ClaimDecision op, which classifies a
		// collision as replay DATA (created=false) rather than a storage error — see
		// TestClaimDecisionCreatesPendingAndClassifiesConflictsAsData. The generic
		// Create is not exposed on the read-only claim surface.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := st.AuthMutate(ctx, tt.run)
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("duplicate err = %v, want ErrConflict", err)
			}
		})
	}
}

func TestClaimDecisionCreatesPendingAndClassifiesConflictsAsData(t *testing.T) {
	st := openSQLiteTest(t, nil)
	serviceA := model.NewID()
	serviceB := model.NewID()
	issued := model.NewTimestamp(time.Now())

	claim := model.PDPDecisionClaim{
		HandleJTI:          model.NewID(),
		PEPServiceID:       serviceA,
		NonceHash:          "nonce-hash-a",
		RequestFingerprint: "fingerprint-a",
		RequestIssuedAt:    issued,
		ClaimedAt:          issued,
	}
	first, created := claimDecision(t, st, claim)
	if !created {
		t.Fatal("first ClaimDecision created = false, want true")
	}
	if first.State != "pending" {
		t.Fatalf("first ClaimDecision state = %q, want pending", first.State)
	}

	duplicateHandle := claim
	duplicateHandle.PEPServiceID = serviceB
	duplicateHandle.NonceHash = "nonce-hash-b"
	duplicateHandle.RequestFingerprint = "fingerprint-b"
	got, created := claimDecision(t, st, duplicateHandle)
	if created || got.ID != first.ID {
		t.Fatalf("duplicate handle = (%s, %v), want existing %s, false", got.ID, created, first.ID)
	}

	nonceOwner := model.PDPDecisionClaim{
		HandleJTI:          model.NewID(),
		PEPServiceID:       serviceB,
		NonceHash:          "shared-nonce",
		RequestFingerprint: "fingerprint-c",
		RequestIssuedAt:    issued,
		ClaimedAt:          issued,
	}
	nonceFirst, created := claimDecision(t, st, nonceOwner)
	if !created {
		t.Fatal("first service/nonce ClaimDecision created = false, want true")
	}
	duplicateNonce := nonceOwner
	duplicateNonce.HandleJTI = model.NewID()
	duplicateNonce.RequestFingerprint = "fingerprint-d"
	got, created = claimDecision(t, st, duplicateNonce)
	if created || got.ID != nonceFirst.ID {
		t.Fatalf("duplicate service/nonce = (%s, %v), want existing %s, false",
			got.ID, created, nonceFirst.ID)
	}

	sameNonceOtherService := nonceOwner
	sameNonceOtherService.HandleJTI = model.NewID()
	sameNonceOtherService.PEPServiceID = serviceA
	got, created = claimDecision(t, st, sameNonceOtherService)
	if !created || got.ID == nonceFirst.ID {
		t.Fatalf("same nonce for another service = (%s, %v), want distinct row, true", got.ID, created)
	}
}

func TestClaimDecisionConcurrentSameKeysCreatesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	issued := model.NewTimestamp(time.Now())
	claim := model.PDPDecisionClaim{
		HandleJTI:          model.NewID(),
		PEPServiceID:       model.NewID(),
		NonceHash:          "concurrent-nonce",
		RequestFingerprint: "concurrent-fingerprint",
		RequestIssuedAt:    issued,
		ClaimedAt:          issued,
	}

	type result struct {
		stored  model.PDPDecisionClaim
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			var r result
			r.err = st.AuthMutate(ctx, func(a store.AuthScope) error {
				var err error
				r.stored, r.created, err = a.ClaimDecision(ctx, claim, nil)
				return err
			})
			results <- r
		}()
	}
	ready.Wait()
	close(start)

	first := <-results
	second := <-results
	for i, r := range []result{first, second} {
		if r.err != nil {
			t.Fatalf("concurrent result %d: %v", i, r.err)
		}
	}
	created := 0
	if first.created {
		created++
	}
	if second.created {
		created++
	}
	if created != 1 {
		t.Fatalf("created count = %d, want 1 (results: %+v, %+v)", created, first, second)
	}
	if first.stored.ID != second.stored.ID {
		t.Fatalf("concurrent rows differ: %s != %s", first.stored.ID, second.stored.ID)
	}
}

func TestAPITokenPurposeRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	var restrictedID, ordinaryID model.ID
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		restricted, err := a.Tokens().Create(ctx, model.APIToken{
			Name: "pdp", Selector: "purpose-pep", SecretHash: []byte("hash"), Purpose: "pep",
		})
		if err != nil {
			return err
		}
		ordinary, err := a.Tokens().Create(ctx, model.APIToken{
			Name: "ordinary", Selector: "purpose-empty", SecretHash: []byte("hash"),
		})
		restrictedID, ordinaryID = restricted.ID, ordinary.ID
		return err
	}); err != nil {
		t.Fatalf("create API tokens: %v", err)
	}

	if err := st.AuthView(ctx, func(a store.AuthScope) error {
		restricted, err := a.Tokens().Get(ctx, restrictedID)
		if err != nil {
			return err
		}
		if restricted.Purpose != "pep" {
			t.Errorf("restricted token purpose = %q, want pep", restricted.Purpose)
		}
		ordinary, err := a.Tokens().Get(ctx, ordinaryID)
		if err != nil {
			return err
		}
		if ordinary.Purpose != "" {
			t.Errorf("ordinary token purpose = %q, want empty", ordinary.Purpose)
		}
		return nil
	}); err != nil {
		t.Fatalf("read API tokens: %v", err)
	}
}

func TestAPITokenSessionBindingRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	wantSID := "osn_" + model.NewID().String()
	wantWorkspace := model.NewID()
	wantRun := model.NewID().String()
	const wantFence int64 = 42
	var id model.ID
	if err := st.AuthMutate(ctx, func(a store.AuthScope) error {
		created, err := a.Tokens().Create(ctx, model.APIToken{
			Name: "session-bound", Selector: "session-bound", SecretHash: []byte("hash"),
			SessionRef: wantSID, WorkspaceID: wantWorkspace,
			SessionRunRef: wantRun, SessionFence: wantFence,
		})
		id = created.ID
		return err
	}); err != nil {
		t.Fatalf("create session-bound API token: %v", err)
	}
	if err := st.AuthView(ctx, func(a store.AuthScope) error {
		got, err := a.Tokens().Get(ctx, id)
		if err == nil && (got.SessionRef != wantSID || got.WorkspaceID != wantWorkspace ||
			got.SessionRunRef != wantRun || got.SessionFence != wantFence) {
			t.Errorf("session binding = sid %q workspace %s run %q fence %d; want %q %s %q %d",
				got.SessionRef, got.WorkspaceID, got.SessionRunRef, got.SessionFence,
				wantSID, wantWorkspace, wantRun, wantFence)
		}
		return err
	}); err != nil {
		t.Fatalf("read session-bound API token: %v", err)
	}
}

func claimDecision(
	t *testing.T,
	st store.Store,
	claim model.PDPDecisionClaim,
) (model.PDPDecisionClaim, bool) {
	t.Helper()
	var stored model.PDPDecisionClaim
	var created bool
	if err := st.AuthMutate(context.Background(), func(a store.AuthScope) error {
		var err error
		stored, created, err = a.ClaimDecision(context.Background(), claim, nil)
		return err
	}); err != nil {
		t.Fatalf("ClaimDecision: %v", err)
	}
	return stored, created
}

func testDelegationHandle(
	selector string,
	serviceID model.ID,
	expires model.Timestamp,
) model.DelegationHandle {
	return model.DelegationHandle{
		Selector:       selector,
		SecretHash:     []byte("secret-hash"),
		SourceCredKind: "token",
		SourceCredID:   model.NewID(),
		SubjectUserID:  model.NewID(),
		MintRole:       "viewer",
		PEPServiceID:   serviceID,
		Audience:       "urn:olivares:pdp:inference",
		Operations:     []string{"messages"},
		ExpiresAt:      expires,
	}
}
