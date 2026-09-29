// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This extends the real store snapshot with the retirement record: a refused
// identity must not lift an exclusion even if membership creation later fails.
type scimCreateIdentityState struct {
	account     scimGuardState
	retirements []model.TenantExclusion
}

func scimCreateIdentitySnapshot(t *testing.T, f *scimGuardFixture, id model.ID) scimCreateIdentityState {
	t.Helper()
	s := scimCreateIdentityState{account: f.state(t, id)}
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		s.retirements = scimGuardRows(t, f.ctx, as.TenantExclusions(), id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func scimCreateIdentityDepart(t *testing.T, f *scimGuardFixture, a *auth.Authenticator, id model.ID, complete bool) {
	t.Helper()
	if err := a.SCIMDeprovisionUser(f.ctx, f.actor, f.tenant, id); err != nil {
		t.Fatal(err)
	}
	if !complete {
		return
	}
	// The fixture represents the retirement pump's completed module census. It
	// uses the real record and store, as the existing consent fixtures do.
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		rows := scimGuardRows(t, f.ctx, as.TenantExclusions(), id)
		if len(rows) != 1 || rows[0].RetirementState != model.RetirementRetiring {
			t.Fatal("offboard fixture did not produce one retiring record")
		}
		rec := rows[0]
		rec.RetirementState = model.RetirementRetired
		rec.NextAttemptAt = nil
		_, err := as.TenantExclusions().Update(f.ctx, rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSCIMProvisionUserExternalIdentity(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			a := auth.NewAuthenticator(f.raw, nil)
			for _, tc := range []struct {
				name, stored, incoming string
				departed, foreign      bool
				pending                bool
				holder, foreignDomain  bool
				wantErr                error
			}{
				{name: "member-different", stored: "idp-9", incoming: "idp-other", wantErr: store.ErrConflict},
				{name: "member-stored-absent", incoming: "idp-9", wantErr: store.ErrConflict},
				{name: "member-case-different", stored: "IdP-9", incoming: "idp-9", wantErr: store.ErrConflict},
				{name: "member-space-different", stored: "idp-9", incoming: " idp-9 ", wantErr: store.ErrConflict},
				{name: "member-space-explicit", incoming: " ", wantErr: store.ErrConflict},
				{name: "member-exact", stored: "IdP-9", incoming: "IdP-9"},
				{name: "member-optional", stored: "idp-9"},
				{name: "member-both-absent"},
				{name: "foreign-different", stored: "idp-9", incoming: "idp-other", foreign: true, wantErr: store.ErrConflict},
				{name: "foreign-exact", stored: "idp-9", incoming: "idp-9", foreign: true, wantErr: store.ErrConflict},
				{name: "foreign-optional", stored: "idp-9", foreign: true, wantErr: store.ErrConflict},
				{name: "departed-different", stored: "idp-9", incoming: "idp-other", departed: true, wantErr: store.ErrConflict},
				{name: "departed-stored-absent", incoming: "idp-9", departed: true, wantErr: store.ErrConflict},
				{name: "departed-exact", stored: "idp-9", incoming: "idp-9", departed: true},
				{name: "departed-optional", stored: "idp-9", departed: true},
				{name: "departed-pending", stored: "idp-9", incoming: "idp-9", departed: true, pending: true, wantErr: auth.ErrRetirementPending},
				{name: "holder-exact", stored: "idp-9", incoming: "idp-9", departed: true, holder: true, wantErr: store.ErrConflict},
				{name: "foreign-domain-exact", stored: "idp-9", incoming: "idp-9", foreignDomain: true, wantErr: auth.ErrForeignDomain},
			} {
				t.Run(tc.name, func(t *testing.T) {
					owning := f.tenant
					if tc.foreign {
						owning = f.other
					}
					u, created, err := a.SCIMProvisionUser(f.ctx, f.actor, owning, auth.SCIMUserInput{
						UserName: "user@" + tc.name + ".example.test", ExternalID: tc.stored, DisplayName: "Original", Active: true,
					})
					if err != nil || !created {
						t.Fatalf("seed = created %t, %v", created, err)
					}
					f.credentials(t, u.ID)
					mintUserCreds(t, f.raw, u.ID, owning)
					if tc.departed {
						scimCreateIdentityDepart(t, f, a, u.ID, !tc.pending)
					}
					if tc.holder {
						if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
							current, err := as.Users().Get(f.ctx, u.ID)
							if err != nil {
								return err
							}
							current.CredentialCustody = model.CustodyHolder
							current.CustodyTenantID = ""
							_, err = as.Users().Update(f.ctx, current)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					if tc.foreignDomain {
						domain := tc.name + ".example.test"
						config := seedConfig(t, f.raw, f.other, "identity", "https://idp.example.test", domain)
						if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
							_, err := as.FederationDomainClaims().Create(f.ctx, model.FederationDomainClaim{TargetTenantID: f.other, ConfigID: config, Domain: domain})
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					before := scimCreateIdentitySnapshot(t, f, u.ID)
					got, created, err := a.SCIMProvisionUser(f.ctx, f.actor, f.tenant, auth.SCIMUserInput{
						UserName: " " + strings.ToUpper(u.Email) + " ", ExternalID: tc.incoming,
						DisplayName: "Replacement", Active: false,
					})
					if !errors.Is(err, tc.wantErr) || created {
						t.Fatalf("create = created %t, %v; want false, %v", created, err, tc.wantErr)
					}
					after := scimCreateIdentitySnapshot(t, f, u.ID)
					if tc.wantErr != nil {
						if got != (model.User{}) {
							t.Error("refused create disclosed the existing account")
						}
						if !reflect.DeepEqual(before, after) {
							t.Error("refused create changed account, authority, credentials, membership, retirement or audit")
						}
						return
					}
					if got.ID != u.ID || got.ExternalID != tc.stored || got.DisplayName != "Original" || got.Status != model.StatusActive {
						t.Error("existing identity or directory attributes changed on create")
					}
					if !tc.departed {
						if !reflect.DeepEqual(before, after) {
							t.Error("idempotent member create wrote stored state")
						}
						return
					}
					if _, err := a.SCIMGetMember(f.ctx, f.tenant, u.ID); err != nil {
						t.Fatalf("legitimate custodian readmission = %v", err)
					}
					if len(after.retirements) != 1 || after.retirements[0].RetirementState != model.RetirementLifted || len(after.account.members) != 1 {
						t.Error("readmission did not lift its completed retirement and restore one membership")
					}
					if after.account.audit.Seq != before.account.audit.Seq+1 {
						t.Error("readmission did not append exactly one audit event")
					}
					if !reflect.DeepEqual(before.account.sessions, after.account.sessions) || !reflect.DeepEqual(before.account.tokens, after.account.tokens) || !reflect.DeepEqual(before.account.credentials, after.account.credentials) {
						t.Error("readmission changed existing credentials")
					}
				})
			}
		})
	}
}

func TestSCIMProvisionUserExternalIdentityRollback(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			f := newSCIMGuardFixture(t, engine)
			a := auth.NewAuthenticator(f.raw, nil)
			u, _, err := a.SCIMProvisionUser(f.ctx, f.actor, f.tenant, auth.SCIMUserInput{
				UserName: "rollback@example.test", ExternalID: "idp-9", Active: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			f.credentials(t, u.ID)
			scimCreateIdentityDepart(t, f, a, u.ID, true)
			before := scimCreateIdentitySnapshot(t, f, u.ID)
			for _, mode := range []string{"cancel-during-membership-read", "audit-append-failure"} {
				t.Run(mode, func(t *testing.T) {
					ctx, cancel := context.WithCancel(f.ctx)
					defer cancel()
					wrapped := &scimGuardStore{Store: f.raw}
					wantErr := errors.New("readmission audit unavailable")
					if mode == "cancel-during-membership-read" {
						wantErr = context.Canceled
						wrapped.onMemberRead = cancel
					} else {
						wrapped.auditErr = wantErr
					}
					_, created, err := auth.NewAuthenticator(wrapped, nil).SCIMProvisionUser(ctx, f.actor, f.tenant, auth.SCIMUserInput{
						UserName: u.Email, ExternalID: "idp-9", Active: true,
					})
					if !errors.Is(err, wantErr) || created {
						t.Fatalf("failed readmission = created %t, %v; want %v", created, err, wantErr)
					}
					if after := scimCreateIdentitySnapshot(t, f, u.ID); !reflect.DeepEqual(before, after) {
						t.Error("failed readmission committed account, membership, retirement, credential or audit changes")
					}
				})
			}
		})
	}
}
