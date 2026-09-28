// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/compliance"
)

// Cmd-adapter contracts: the account eraser's refusal of a superadmin, the
// tenant's own leg for every other account (its membership and tenant tokens
// removed, the global record kept for the deployment's erasure ceremony), the
// "user:<id>" alias resolution, and the provider adapter's honest outcome folding.

func erasureTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := coreengine.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(context.Background())
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

func seedUser(t *testing.T, st store.Store, email string, super bool, tenants ...model.TenantID) model.User {
	t.Helper()
	var user model.User
	if err := st.AuthMutate(context.Background(), func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(context.Background(), model.User{
			Email: email, DisplayName: "Maria", Status: model.StatusActive,
			PasswordHash: "argon2id$fake", IsSuperadmin: super, ExternalID: "scim-1",
		})
		if err != nil {
			return err
		}
		for _, tn := range tenants {
			if _, err := as.Memberships().Create(context.Background(), model.Membership{
				UserID: user.ID, TargetTenantID: tn, Role: "editor",
			}); err != nil {
				return err
			}
		}
		if _, err := as.Sessions().Create(context.Background(), model.AuthSession{
			UserID: user.ID, Selector: "sel-" + user.ID.String(), SecretHash: []byte{1},
			ExpiresAt: model.NewTimestamp(model.SystemClock{}.Now().Time()), CreatedIP: "203.0.113.7",
		}); err != nil {
			return err
		}
		_, err = as.Tokens().Create(context.Background(), model.APIToken{
			Name: "maria's laptop", UserID: user.ID, Selector: "tok-" + user.ID.String(),
			SecretHash: []byte{2}, BoundTenantID: tenants[0], Role: "editor",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return user
}

func mkTenant(t *testing.T, st store.Store, slug string) model.TenantID {
	t.Helper()
	var tid model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		if err != nil {
			return err
		}
		tid = org.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return tid
}

func TestAccountEraserAnonymizesAndRefuses(t *testing.T) {
	st := erasureTestStore(t)
	tenant := mkTenant(t, st, "acme")
	other := mkTenant(t, st, "globex")
	a := &accountEraserAdapter{log: slog.Default()}
	a.use(st, auth.NewAuthenticator(st, nil))
	ctx := context.Background()

	// 1) A single-tenant user the tenant created (custody evidence), matched by
	// EMAIL: the tenant's own leg runs and the global record is kept. No tenant
	// erasure anonymizes an account until the deployment proves nothing outside
	// the tenant relies on it.
	user := seedUser(t, st, "maria@example.com", false, tenant)
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, user.ID)
		if err != nil {
			return err
		}
		u.CredentialCustody, u.CustodyTenantID = model.CustodyTenant, tenant
		_, err = as.Users().Update(ctx, u)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	out, err := a.EraseAccount(ctx, tenant, []string{"maria@example.com"}, "user:dpo", "user")
	if err != nil {
		t.Fatal(err)
	}
	if !out.Attempted || out.Erased != 0 || !strings.Contains(out.Detail, "deployment's erasure ceremony") {
		t.Fatalf("outcome = %+v, want attempted, nothing anonymized, the global record left to the deployment", out)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, user.ID)
		if err != nil {
			return err
		}
		if u.Email != "maria@example.com" || u.DisplayName != "Maria" || u.PasswordHash == "" ||
			u.ExternalID != "scim-1" || u.Status != model.StatusActive {
			t.Fatalf("the tenant's erasure changed the global account: %+v", u)
		}
		ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{eqFilter("user_id", user.ID.String())}})
		if err != nil {
			return err
		}
		if len(ms) != 0 {
			t.Fatalf("the tenant's membership survives its erasure: %d", len(ms))
		}
		ts, _, err := as.Tokens().List(ctx, model.Query{Filters: []model.Filter{eqFilter("user_id", user.ID.String())}})
		if err != nil {
			return err
		}
		for _, tok := range ts {
			if tok.BoundTenantID == tenant && !tok.Revoked {
				t.Fatalf("a token bound to the tenant survives its erasure: %+v", tok)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 2) "user:<id>" alias form resolves to the SAME account; the repeated leg
	// writes nothing new and says the same.
	out, err = a.EraseAccount(ctx, tenant, []string{"user:" + user.ID.String()}, "user:dpo", "user")
	if err != nil {
		t.Fatal(err)
	}
	if out.Erased != 0 || !strings.Contains(out.Detail, "deployment's erasure ceremony") {
		t.Fatalf("alias resolution outcome = %+v", out)
	}

	// 3) A superadmin is REFUSED.
	seedUser(t, st, "root@example.com", true, tenant)
	out, err = a.EraseAccount(ctx, tenant, []string{"root@example.com"}, "user:dpo", "user")
	if err != nil {
		t.Fatal(err)
	}
	if out.Erased != 0 || !strings.Contains(out.Detail, "superadmin") {
		t.Fatalf("superadmin outcome = %+v", out)
	}

	// 4) A principal with memberships in ANOTHER tenant keeps them and its global
	// record; only this tenant's membership goes.
	shared := seedUser(t, st, "shared@example.com", false, tenant, other)
	out, err = a.EraseAccount(ctx, tenant, []string{"shared@example.com"}, "user:dpo", "user")
	if err != nil {
		t.Fatal(err)
	}
	if out.Erased != 0 || !strings.Contains(out.Detail, "deployment's erasure ceremony") {
		t.Fatalf("multi-tenant outcome = %+v", out)
	}
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{eqFilter("user_id", shared.ID.String())}})
		if err != nil {
			return err
		}
		if len(ms) != 1 || ms[0].TargetTenantID != other {
			t.Fatalf("the shared account's memberships after the tenant's erasure = %+v, want only the other tenant's", ms)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 5) No match is an honest, non-fabricated outcome.
	out, err = a.EraseAccount(ctx, tenant, []string{"nobody@example.com"}, "user:dpo", "user")
	if err != nil {
		t.Fatal(err)
	}
	if out.Erased != 0 || !strings.Contains(out.Detail, "no matching engine account") {
		t.Fatalf("no-match outcome = %+v", out)
	}
}

func TestProviderEraserCountFoldsVerdictsHonestly(t *testing.T) {
	p := &providerEraserAdapter{log: slog.Default()}
	var out compliance.ProviderEraseOutcome
	p.count(nil, &out)
	p.count(&claudecompliance.EraseDenyError{Reason: "pending", Status: claudecompliance.ErasePending}, &out)
	p.count(&claudecompliance.EraseDenyError{Reason: "expired", Status: claudecompliance.EraseExpired}, &out)
	p.count(&claudecompliance.EraseDenyError{Reason: "rejected", Status: claudecompliance.EraseRejected}, &out)
	p.count(&claudecompliance.EraseDenyError{Reason: "allowlist"}, &out) // Status "" = hard deny
	p.count(errors.New("HTTP 500"), &out)
	if out.Erased != 1 || out.Pending != 2 || out.Failed != 3 {
		t.Fatalf("folded outcome = %+v (want erased 1, pending 2 — incl. expired —, failed 3)", out)
	}
}
