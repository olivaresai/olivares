// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type beforeClaimWorkspaceRegistry struct{ store.ExtensionRegistry }

func (r beforeClaimWorkspaceRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == claimKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name != "workspace_id" {
				fields = append(fields, f)
			}
		}
		d.Fields, d.WorkspaceLineage = fields, model.WorkspaceLineageSpec{}
	}
	return r.ExtensionRegistry.Register(d)
}

func TestClaimWorkspaceHistoricalUpgradeHidesUnprovenLineage(t *testing.T) {
	for _, backend := range profileBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			ctx := t.Context()
			old := New()
			m, st := openProfileModule(t, backend, func(reg store.ExtensionRegistry) error { return old.RegisterSchema(beforeClaimWorkspaceRegistry{reg}) })
			tenant := ensureTenant(t, st, "claim-lineage-upgrade")
			var workspace, defaultWorkspace model.ID
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				def, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				defaultWorkspace = def.ID
				ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Claim project", Slug: "claim-project", Status: model.StatusActive})
				workspace = ws.ID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			sid, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claim-upgrade", ExternalID: "known", WorkspaceID: workspace})
			if err != nil {
				t.Fatal(err)
			}
			original := Lease{SID: sid, Holder: "engine:claim-upgrade", Fence: 1, ExpiresAt: baseTime.Add(time.Minute)}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(claimKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, model.Record{colClaimSID: sid, colHolder: original.Holder, colFence: original.Fence, colClaimState: claimActive, colLeaseExpires: model.NewTimestamp(original.ExpiresAt).String(), colClaimedAt: model.NewTimestamp(baseTime).String()})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			exhaustedSID, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claim-upgrade", ExternalID: "exhausted", WorkspaceID: workspace})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(claimKind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, model.Record{colClaimSID: exhaustedSID, colHolder: "engine:old-holder", colFence: int64(math.MaxInt64), colClaimState: claimActive, colLeaseExpires: model.NewTimestamp(baseTime.Add(-time.Minute)).String(), colClaimedAt: model.NewTimestamp(baseTime.Add(-2 * time.Minute)).String()})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			m, st = openProfileModule(t, backend, nil)
			defer st.Close()
			visible := func(ws model.ID, want int) {
				t.Helper()
				if err := st.View(ctx, tenant, func(sc store.Scope) error {
					confined, err := store.ConfineWorkspace(ctx, sc, ws)
					if err != nil {
						return err
					}
					repo, err := confined.Ext(claimKind)
					if err != nil {
						return err
					}
					rows, _, err := repo.List(ctx, model.Query{Limit: 10})
					if err == nil && len(rows) != want {
						t.Fatalf("confined claims = %d, want %d", len(rows), want)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			// Adding a nullable column cannot silently assign old claims to default.
			visible(defaultWorkspace, 0)
			visible(workspace, 0)
			if _, err := m.Claim(ctx, tenant, exhaustedSID, "engine:new-holder", time.Minute); !errors.Is(err, ErrFenceExhausted) {
				t.Fatalf("exhausted historical claim = %v", err)
			}
			// Refusal may retire the lapsed fence, but cannot backfill authority.
			visible(workspace, 0)
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(claimKind)
				if err != nil {
					return err
				}
				rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colClaimSID, exhaustedSID)}, Limit: 1})
				if err == nil && (len(rows) != 1 || !rows[0].IsNull("workspace_id") || rows[0].String(colClaimState) != claimExpired) {
					t.Fatal("refused claim persisted more than its expiry")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			renewed, err := m.Claim(ctx, tenant, sid, "engine:claim-upgrade", time.Minute)
			if err != nil || renewed.Fence != original.Fence {
				t.Fatalf("renew changed claim generation: %v", err)
			}
			visible(workspace, 1)
			visible(defaultWorkspace, 0)
			// A missing canonical identity proves no workspace, even at a privileged
			// engine claim boundary. It cannot become a default-workspace grant.
			orphan := "osn_" + model.NewID().String()
			if _, err := m.Claim(context.Background(), tenant, orphan, "engine:orphan", time.Minute); err != nil {
				t.Fatal(err)
			}
			visible(workspace, 1)
			visible(defaultWorkspace, 0)
		})
	}
}
