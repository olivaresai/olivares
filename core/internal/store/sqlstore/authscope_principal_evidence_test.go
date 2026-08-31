// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAuthPrincipalEvidenceScopeUsesSameTransactionAndRestoresSystem(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "auth-principal-evidence")

	var user model.User
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(ctx, model.User{
			Email: "principal-evidence@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed auth principal: %v", err)
	}

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		evidence, ok := as.(store.AuthPrincipalEvidenceScope)
		if !ok {
			t.Fatal("AuthScope lacks AuthPrincipalEvidenceScope")
		}
		concrete := as.(*authScope)
		if concrete.ts.hasTransactionNow {
			t.Fatal("transaction clock was marked observed before the capability call")
		}

		first, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil {
			return err
		}
		if first.Kind != model.DirectoryEpochKind || first.ID != model.ID(tenant) ||
			first.Version < 1 {
			t.Fatalf("directory epoch fact = %+v, want exact tenant generation", first)
		}

		dbNow, err := evidence.TransactionNow(ctx)
		if err != nil {
			return err
		}
		if dbNow.IsZero() || !concrete.ts.hasTransactionNow ||
			!concrete.ts.transactionNow.Time().Equal(dbNow.Time()) {
			t.Fatalf("transaction clock state = observed:%t stored:%s returned:%s",
				concrete.ts.hasTransactionNow, concrete.ts.transactionNow, dbNow)
		}

		second, err := evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil {
			return err
		}
		if second != first {
			t.Fatalf("same auth transaction returned epoch facts %+v then %+v", first, second)
		}
		if concrete.ts.tenant != model.SystemTenantID {
			t.Fatalf("auth scope tenant field = %s, want SYSTEM", concrete.ts.tenant)
		}
		directorySnapshotWantBoundTenant(
			t, ctx, concrete.ts, store.EngineSQLite, model.SystemTenantID,
		)
		got, err := as.Users().Get(ctx, user.ID)
		if err != nil {
			return err
		}
		if got.ID != user.ID || got.TenantID != model.SystemTenantID {
			t.Fatalf("SYSTEM user after epoch read = %+v, want %s", got, user.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("read auth principal evidence: %v", err)
	}
}

func TestAuthPrincipalEvidenceScopeObservesUncommittedEpochInAuthTransaction(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "auth-principal-evidence-uncommitted")

	var user model.User
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(ctx, model.User{
			Email: "principal-evidence-uncommitted@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed auth principal: %v", err)
	}

	var before store.AuthorizationFactRef
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		before, err = as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(ctx, tenant)
		return err
	}); err != nil {
		t.Fatalf("read epoch before auth mutation: %v", err)
	}

	var inside store.AuthorizationFactRef
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		evidence := as.(store.AuthPrincipalEvidenceScope)
		if _, err := evidence.TransactionNow(ctx); err != nil {
			return err
		}
		if _, err := as.Memberships().Create(ctx, model.Membership{
			UserID: user.ID, TargetTenantID: tenant, Role: "viewer",
		}); err != nil {
			return err
		}
		var err error
		inside, err = evidence.ReadDirectoryEpochFact(ctx, tenant)
		if err != nil {
			return err
		}
		if inside.Kind != before.Kind || inside.ID != before.ID ||
			inside.Version != before.Version+1 {
			return fmt.Errorf("epoch inside auth mutation = %+v, want %+v at version %d",
				inside, before, before.Version+1)
		}
		got, err := as.Users().Get(ctx, user.ID)
		if err != nil {
			return err
		}
		if got.ID != user.ID || got.TenantID != model.SystemTenantID {
			return fmt.Errorf("SYSTEM user after uncommitted epoch read = %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("observe same auth mutation transaction: %v", err)
	}

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		after, err := as.(store.AuthPrincipalEvidenceScope).ReadDirectoryEpochFact(ctx, tenant)
		if err != nil {
			return err
		}
		if after != inside {
			return fmt.Errorf("committed epoch = %+v, want observed in-transaction %+v", after, inside)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify committed auth principal epoch: %v", err)
	}
}

func TestAuthPrincipalEvidenceScopeRestoresSystemAfterEpochFailure(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "auth-principal-evidence-missing")

	var user model.User
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		user, err = as.Users().Create(ctx, model.User{
			Email: "principal-evidence-restore@example.test", Status: model.StatusActive,
		})
		return err
	}); err != nil {
		t.Fatalf("seed auth principal: %v", err)
	}
	directorySnapshotCorruptEpoch(t, ctx, st, tenant, "absent")

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		evidence := as.(store.AuthPrincipalEvidenceScope)
		if _, err := evidence.ReadDirectoryEpochFact(ctx, tenant); !errors.Is(
			err, store.ErrDirectoryUnavailable,
		) {
			t.Fatalf("absent directory epoch error = %v, want ErrDirectoryUnavailable", err)
		}
		directorySnapshotWantBoundTenant(
			t, ctx, as.(*authScope).ts, store.EngineSQLite, model.SystemTenantID,
		)
		got, err := as.Users().Get(ctx, user.ID)
		if err != nil {
			return err
		}
		if got.ID != user.ID || got.TenantID != model.SystemTenantID {
			t.Fatalf("SYSTEM user after failed epoch read = %+v, want %s", got, user.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify SYSTEM restoration after epoch failure: %v", err)
	}
}

func TestAuthPrincipalEvidenceScopeRejectsNonBusinessTenant(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)

	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		evidence := as.(store.AuthPrincipalEvidenceScope)
		for name, tenant := range map[string]model.TenantID{
			"zero":   "",
			"system": model.SystemTenantID,
			"not-v7": "11111111-1111-4111-8111-111111111111",
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := evidence.ReadDirectoryEpochFact(ctx, tenant); !errors.Is(
					err, store.ErrDirectoryUnavailable,
				) {
					t.Fatalf("tenant %q error = %v, want ErrDirectoryUnavailable", tenant, err)
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect invalid business tenants: %v", err)
	}
}
