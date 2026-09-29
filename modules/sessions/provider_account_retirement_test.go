// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Exercise the module's real retirement reader, including its authority fence.
// A provider home belongs to the tenant/environment/profile, not to a human.
func TestProviderAccount_RetirementKeepsCustodyAndRefusesUnknownMetadata(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			m, st := openProfileModule(t, be, nil)
			defer st.Close()
			tenant := ensureTenant(t, st, "account-retirement")
			otherTenant := ensureTenant(t, st, "other-account-retirement")
			root := acctHomeRoot(t, m)
			api := newAcctAPI(m, st, tenant)
			created := api.call("POST", "/provider-accounts", map[string]any{"driver": "claude", "idempotency_key": "retirement-create"})
			if created.code != 201 {
				t.Fatalf("create = %d %s", created.code, created.raw)
			}
			ref := created.body["account_ref"].(string)
			home := acctCustodyDir(root, tenant, ref)
			marker := filepath.Join(home, accountCustodyMarker)
			markerBefore, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}

			req, foreign := accountRetirementRequest(t, st, tenant)
			// A foreign tenant's unknown owner cannot poison this tenant's pass.
			other := newAcctAPI(m, st, otherTenant).create("codex", "")
			if other.code != 201 {
				t.Fatalf("other create = %d %s", other.code, other.raw)
			}
			accountRetirementSet(t, st, otherTenant, providerProfileKind, "profile_ref", other.body["account_ref"].(string), "owner_ref", "unresolved-owner")

			cases := []struct{ name, owner, state, unknown string }{
				{name: "absent-owner", state: "complete"},
				{name: "reserved-unknown-owner", owner: "unresolved-owner", state: "complete", unknown: "sessions.provider_profile"},
				{name: "another-person-is-not-an-owner-contract", owner: foreign.String(), state: "complete", unknown: "sessions.provider_profile"},
				{name: "unknown-operation-state", state: "unrecognized", unknown: "sessions.provider_account_home_operation"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					var owner any
					if tc.owner != "" {
						owner = tc.owner
					}
					accountRetirementSet(t, st, tenant, providerProfileKind, "profile_ref", ref, "owner_ref", owner)
					accountRetirementSet(t, st, tenant, accountHomeOperationKind, "retry_key", "retirement-create", "state", tc.state)
					got, err := m.RetirementStep().RetireUser(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					if tc.unknown == "" {
						if !got.Clean() {
							t.Fatalf("valid tenant-wide custody is not human authority: %+v", got)
						}
					} else if got.Clean() || !slices.Contains(got.UnknownKinds, tc.unknown) {
						t.Fatalf("unknown metadata must invalidate the actual retirement reader: %+v", got)
					}
					if got.FactVersion == 0 {
						t.Fatal("retirement did not pin an authorization fact")
					}
					if read := api.call("GET", "/provider-accounts/"+ref, nil); read.code != 200 || read.body["account_ref"] != ref {
						t.Fatalf("retirement lost the provider account: %d %s", read.code, read.raw)
					}
					after, err := os.ReadFile(marker)
					if err != nil || string(after) != string(markerBefore) {
						t.Fatalf("retirement changed home custody: %v", err)
					}
					if _, err := os.Stat(filepath.Join(home, "config")); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(filepath.Join(home, "home")); err != nil {
						t.Fatal(err)
					}
				})
			}
			// Resolve only the unknown test metadata. The original operation still
			// replays to the same account, proving retirement did not delete its reservation.
			accountRetirementSet(t, st, tenant, providerProfileKind, "profile_ref", ref, "owner_ref", nil)
			accountRetirementSet(t, st, tenant, accountHomeOperationKind, "retry_key", "retirement-create", "state", "complete")
			replay := api.call("POST", "/provider-accounts", map[string]any{"driver": "claude", "idempotency_key": "retirement-create"})
			if replay.code != 201 || replay.body["account_ref"] != ref {
				t.Fatalf("reservation lost: %d %s", replay.code, replay.raw)
			}
			if got := len(acctAuditsOf(t, m, tenant, "sessions.provider_account.create")); got != 1 {
				t.Fatalf("create audits = %d, want one", got)
			}
		})
	}
}

func accountRetirementRequest(t *testing.T, st store.Store, tenant model.TenantID) (auth.RetirementRequest, model.ID) {
	t.Helper()
	ctx := context.Background()
	var users [2]model.ID
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		for i := range users {
			u, err := as.Users().Create(ctx, model.User{Email: fmt.Sprintf("retirement-%d-%s@accounts.test", i, model.NewID()), DisplayName: "Retirement subject", Status: model.StatusActive})
			if err != nil {
				return err
			}
			users[i] = u.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req := auth.RetirementRequest{Tenant: tenant, User: users[0], Generation: 1}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		reader, ok := as.(store.AuthUserAuthorityEvidenceScope)
		if !ok {
			return fmt.Errorf("store exposes no user authority evidence")
		}
		fact, err := reader.ReadUserAuthorityFact(ctx, req.User)
		req.UserAuthority = fact.Version
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return req, users[1]
}

// Deliberately seed unsupported stored metadata through the store seam; no public
// account request accepts owner_ref or the operation's internal state.
func accountRetirementSet(t *testing.T, st store.Store, tenant model.TenantID, kind model.Kind, key, value, column string, content any) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{{Column: key, Op: model.OpEq, Value: value}}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("expected one %s row, got %d", kind, len(rows))
		}
		rows[0][column] = content
		_, err = repo.Update(ctx, rows[0])
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
