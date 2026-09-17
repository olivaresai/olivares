// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"reflect"
	"testing"
)

func TestReadSessionIdentityIsExactTenantScopedAndDoesNotTouchSQLite(t *testing.T) {
	m, st, tenant, _ := newSess(t)
	ctx := context.Background()
	sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "repeated-text", At: baseTime})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() []model.Record {
		var all []model.Record
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			for _, kind := range []model.Kind{identityKind, aliasKind} {
				repo, err := sc.Ext(kind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Limit: 100})
				if err != nil {
					return err
				}
				all = append(all, rows...)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return all
	}
	before := snapshot()
	for i := 0; i < 3; i++ {
		got, err := m.ReadSessionIdentity(ctx, tenant, sid)
		if err != nil || got.SID != sid || got.TenantID != tenant || got.Version < 1 {
			t.Fatalf("exact snapshot: %+v %v", got, err)
		}
	}
	for _, args := range []struct {
		tenant model.TenantID
		sid    string
	}{
		{"", sid}, {model.TenantID(model.NewID()), sid}, {tenant, "repeated-text"}, {tenant, "osn_" + model.NewID().String()},
	} {
		if _, err := m.ReadSessionIdentity(ctx, args.tenant, args.sid); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing/foreign identity: %v", err)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("identity reads changed persisted identities or aliases")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadSessionIdentity(ctx, tenant, sid); err == nil {
		t.Fatal("closed store appeared available")
	}
}
