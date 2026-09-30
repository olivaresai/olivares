// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The group-origin seam on Postgres : the reconciled column round-trips
// with the same NULL/"" semantics as SQLite, and the write boundary holds on
// the split-owner engine the deployment actually runs.
func openGroupOriginPostgres(t *testing.T) store.Store {
	t.Helper()
	if !pgtest.Available(t) {
		t.Skip("no Postgres configured")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := sqlstore.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, MaxConns: 8,
	}, nil)
	if err != nil {
		t.Fatalf("open split-owner postgres store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestGroupOriginPostgresColumnAndBoundary(t *testing.T) {
	ctx := context.Background()
	st := openGroupOriginPostgres(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "origin-pg")

	// A fresh store renders the column; a NULL origin reads as "" exactly as
	// on SQLite (today's rows are NULL by construction).
	g, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Eng", ExternalID: "grp-pg"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if g.Group.ProvisionedBy != "" {
		t.Fatalf("fresh row origin = %q, want NULL/\"\"", g.Group.ProvisionedBy)
	}
	// The operator may write it (today's behavior on the reconciled column).
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, g.Group.ID, auth.RoleViewer); err != nil {
		t.Fatalf("map role on NULL-origin group: %v", err)
	}

	// Claim it as the operator does and assert both boundaries on Postgres.
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		row, err := as.Groups().Get(ctx, g.Group.ID)
		if err != nil {
			return err
		}
		row.ProvisionedBy = "operator"
		_, err = as.Groups().Update(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("claim origin: %v", err)
	}
	if _, err := a.SCIMReplaceGroup(ctx, super, tenant, g.Group.ID, auth.SCIMGroupInput{
		DisplayName: "Renamed", ExternalID: "grp-pg",
	}, 0); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
		t.Fatalf("SCIM replace on operator group err = %v, want ErrGroupOriginReadOnly", err)
	}
	if err := a.SCIMDeleteGroup(ctx, super, tenant, g.Group.ID); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
		t.Fatalf("SCIM delete on operator group err = %v, want ErrGroupOriginReadOnly", err)
	}
	// The console still writes its own origin.
	if _, err := a.ConfigureGroupParent(ctx, super, tenant, g.Group.ID, model.ID("")); err != nil {
		t.Fatalf("clear parent on operator group: %v", err)
	}

	// A named provisioner's group is read-only to the console too.
	foreign, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{DisplayName: "Foreign", ExternalID: "grp-pg-f"})
	if err != nil {
		t.Fatalf("create foreign: %v", err)
	}
	if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
		row, err := as.Groups().Get(ctx, foreign.Group.ID)
		if err != nil {
			return err
		}
		row.ProvisionedBy = "ldap"
		_, err = as.Groups().Update(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("claim foreign origin: %v", err)
	}
	if _, err := a.ConfigureGroupRole(ctx, super, tenant, foreign.Group.ID, auth.RoleViewer); !errors.Is(err, auth.ErrGroupOriginReadOnly) {
		t.Fatalf("map role on named-origin group err = %v, want ErrGroupOriginReadOnly", err)
	}
}
