// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSetOrgSettingsRejectsStaleVersionOnPostgres(t *testing.T) {
	ctx := context.Background()
	st, _ := openAccessEvidencePG(t)
	tenant := provisionTenant(t, st, "settings-stale-version")
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		org.Settings = map[string]any{"governed_connections": []any{"department-db"}}
		_, err = sc.SetOrgSettings(ctx, org.Settings)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	var stale model.Org
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		stale, err = sc.Org(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stale.Settings["tool_version_rings"] = []any{"pin"}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		delete(org.Settings, "governed_connections")
		_, err = sc.SetOrgSettings(ctx, org.Settings)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.SetOrgSettings(ctx, stale.Settings, stale.Version)
		return err
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale settings writer = %v, want store.ErrConflict so the revoke is not reverted", err)
	}

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		if org.Settings == nil {
			org.Settings = map[string]any{}
		}
		org.Settings["tool_version_rings"] = []any{"pin"}
		_, err = sc.SetOrgSettings(ctx, org.Settings)
		return err
	}); err != nil {
		t.Fatalf("fresh settings retry: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		if _, restored := org.Settings["governed_connections"]; restored {
			t.Fatal("stale writer restored the revoked connection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
