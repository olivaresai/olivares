// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"reflect"
	"strings"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestCreateDemoOrgRefusesAnExistingOrganizationWithoutChangingData(t *testing.T) {
	ctx := t.Context()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var before []model.Org
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		if _, err := sys.CreateOrg(ctx, model.Org{Name: "Normal setup", Slug: "default", Status: model.StatusActive}); err != nil {
			return err
		}
		var err error
		before, err = sys.ListOrgs(ctx)
		return err
	}); err != nil {
		t.Fatalf("seed existing organization: %v", err)
	}

	tenant, err := createDemoOrg(ctx, st)
	if err == nil {
		t.Errorf("demo seeding created tenant %s beside an existing organization", tenant)
	} else if !strings.Contains(err.Error(), "fresh --data-dir") {
		t.Errorf("demo refusal does not name the remedy: %v", err)
	}
	if !tenant.IsZero() {
		t.Errorf("refused demo creation returned tenant %s", tenant)
	}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		after, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(after, before) {
			t.Errorf("refused demo creation changed organizations: before=%#v after=%#v", before, after)
		}
		return nil
	}); err != nil {
		t.Fatalf("read organization inventory: %v", err)
	}
}
