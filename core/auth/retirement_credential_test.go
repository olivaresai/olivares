// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// corruptCredentialRepo injects an unreadable stored value at the repository
// seam. The normal writer rejects this value; a retirement read must also refuse
// it if an older or out-of-band writer left it behind.
type corruptCredentialRepo struct{ store.GenericRepo }

func (r corruptCredentialRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	rows, page, err := r.GenericRepo.List(ctx, q)
	if err == nil && len(rows) > 0 {
		rows[0]["credential"] = "not-a-canonical-credential"
	}
	return rows, page, err
}

func TestRowsNamingAccountReadsEveryBareCredential(t *testing.T) {
	ctx := context.Background()
	const kind model.Kind = "credentialfx.permit"
	decl := model.Ref(model.EncodeCredentialID, model.ClassAuthority)
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, func(reg store.ExtensionRegistry) error {
		return reg.Register(model.EntityDescriptor{Kind: kind, Table: "credentialfx_permit", Fields: []model.FieldSpec{
			{Name: "credential", Kind: model.KindUUID, Principal: decl},
		}})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	tenant := provisionTenant(t, st, "credential-retirement")
	otherTenant := provisionTenant(t, st, "credential-other")
	user, session, token, historical, unrelated := model.NewID(), model.NewID(), model.NewID(), model.NewID(), model.NewID()
	req := auth.RetirementRequest{Tenant: tenant, User: user, Credentials: []model.ID{session, token, historical}}
	var want []string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		// More than one real repository page, with each requested credential
		// separated by unrelated rows. A reader limited to one page loses a match.
		for i := 0; i < 503; i++ {
			credential := unrelated
			switch i {
			case 0:
				credential = session
			case 251:
				credential = token
			case 502:
				credential = historical
			case 1:
				credential = user // same bytes as req.User, but in the credential namespace
			}
			// Explicit ordered row ids keep the page boundary independent of
			// wall-clock time and UUID generation order.
			id := model.ID(fmt.Sprintf("0192f126-d074-7396-8f58-%012x", i+1))
			_, err := repo.CreateWithID(ctx, id, model.Record{"credential": credential.String()})
			if err != nil {
				return err
			}
			if i == 0 || i == 251 || i == 502 {
				want = append(want, id.String())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(ctx, otherTenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{"credential": session.String()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{"credential": "not-a-canonical-credential"})
		return err
	}); err == nil {
		t.Error("the normal writer accepted a malformed counted credential")
	}
	slices.Sort(want)
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, unknown, err := auth.RowsNamingAccount(ctx, repo, decl, "credential", req)
		if err != nil {
			return err
		}
		var got []string
		for _, row := range rows {
			got = append(got, row.String(model.ColID))
		}
		slices.Sort(got)
		if unknown || !slices.Equal(got, want) {
			t.Errorf("account credential rows = %v, unknown=%v; want %v", got, unknown, want)
		}
		req.Credentials = nil
		if rows, unknown, err := auth.RowsNamingAccount(ctx, repo, decl, "credential", req); err != nil || unknown || len(rows) != 0 {
			t.Errorf("account id was used as a credential: rows=%d unknown=%v err=%v", len(rows), unknown, err)
		}
		if rows, _, err := auth.RowsNamingAccount(ctx, corruptCredentialRepo{repo}, decl, "credential", req); err == nil || len(rows) != 0 {
			t.Errorf("malformed stored credential became a clean absence: rows=%d err=%v", len(rows), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
