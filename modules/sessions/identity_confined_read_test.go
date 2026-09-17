// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// sessions.identity declares its core workspace lineage (NULL = tenant default)
// and is READ-ONLY under confinement. On every configured engine this proves that
// both identity producers keep writing through the unmarked engine path, that a
// confined View reads exactly its workspace's identities through
// ReadSessionIdentityInScope, and that a confined Mutate cannot write — including
// the cross-workspace rewrite that made the unrestricted declaration unsafe.
func TestIdentity_ConfinedReadOnlyLineage(t *testing.T) {
	t.Parallel()

	for _, be := range backends(t) {
		be := be
		t.Run(be.name, func(t *testing.T) {
			m, tenant, _ := be.open(t)
			ctx := context.Background()

			var defaultWS, alpha, bravo model.ID
			if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				def, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				defaultWS = def.ID
				a, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Alpha", Slug: "alpha", Status: model.StatusActive})
				if err != nil {
					return err
				}
				alpha = a.ID
				b, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Bravo", Slug: "bravo", Status: model.StatusActive})
				bravo = b.ID
				return err
			}); err != nil {
				t.Fatalf("[%s] workspaces: %v", be.name, err)
			}

			// Producer 1: mintIdentity through ResolveSession, unset and scoped. The
			// existing-alias touch and the declaration are raw Updates.
			sidDefault, err := m.ResolveSession(ctx, tenant, SessionBinding{Provider: "claude", ExternalID: "ro-default", At: baseTime})
			if err != nil {
				t.Fatalf("[%s] mint default identity: %v", be.name, err)
			}
			sidAlpha, err := m.ResolveSession(ctx, tenant, SessionBinding{
				Provider: "codex", ExternalID: "ro-alpha", At: baseTime, WorkspaceID: alpha,
			})
			if err != nil {
				t.Fatalf("[%s] mint alpha identity: %v", be.name, err)
			}
			again, err := m.ResolveSession(ctx, tenant, SessionBinding{
				Provider: "codex", ExternalID: "ro-alpha", At: baseTime.Add(time.Minute),
			})
			if err != nil || again != sidAlpha {
				t.Fatalf("[%s] resolve existing alias = %q, %v; want %q", be.name, again, err, sidAlpha)
			}
			if _, err := m.DeclareSession(ctx, tenant, SessionBinding{
				Provider: "codex", ExternalID: "ro-alpha", At: baseTime.Add(2 * time.Minute),
			}); err != nil {
				t.Fatalf("[%s] declare alpha identity: %v", be.name, err)
			}

			// Producer 2: the protocol synthetic identity, on the unconfined scope
			// its reservation transaction uses.
			sidBravo := newSID()
			if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
				now, err := transactionNow(ctx, sc)
				if err != nil {
					return err
				}
				return createProtocolSyntheticIdentity(ctx, sc, bravo, BindingProtocolA2A, sidBravo, model.NewID(), 1, now)
			}); err != nil {
				t.Fatalf("[%s] protocol synthetic identity: %v", be.name, err)
			}

			wantWorkspace := map[string]string{sidDefault: "", sidAlpha: alpha.String(), sidBravo: bravo.String()}
			rows := map[string]model.Record{}
			readRaw := func() map[string]model.Record {
				out := map[string]model.Record{}
				if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
					repo, err := sc.Ext(identityKind)
					if err != nil {
						return err
					}
					_, stamped := repo.(store.TransactionStampedGenericRepo)
					_, locker := repo.(store.RowLocker[model.Record])
					_, projector := repo.(store.DistinctProjector)
					if !stamped || !locker || !projector {
						t.Errorf("[%s] unconfined identity Ext lost a capability: stamped=%t locker=%t projector=%t",
							be.name, stamped, locker, projector)
					}
					for sid := range wantWorkspace {
						rec, ok, err := findIdentity(ctx, sc, sid)
						if err != nil || !ok {
							return fmt.Errorf("identity %s: found=%t err=%v", sid, ok, err)
						}
						out[sid] = rec
					}
					return nil
				}); err != nil {
					t.Fatalf("[%s] raw identity read: %v", be.name, err)
				}
				return out
			}
			rows = readRaw()
			for sid, want := range wantWorkspace {
				if got := rows[sid].String(colIDWorkspaceID); got != want {
					t.Fatalf("[%s] producer stored workspace %q for %s, want %q", be.name, got, sid, want)
				}
			}

			// Confined Views: each workspace reads its own identity and nothing else;
			// NULL lineage belongs to the default workspace only.
			own := map[model.ID]string{defaultWS: sidDefault, alpha: sidAlpha, bravo: sidBravo}
			for ws, ownSID := range own {
				if err := m.data.View(ctx, tenant, func(raw store.Scope) error {
					confined, err := store.ConfineWorkspace(ctx, raw, ws)
					if err != nil {
						return err
					}
					repo, err := confined.Ext(identityKind)
					if err != nil {
						return fmt.Errorf("confined identity Ext: %w", err)
					}
					for sid, row := range rows {
						snap, readErr := ReadSessionIdentityInScope(ctx, confined, sid)
						got, getErr := repo.Get(ctx, model.ID(row.String(model.ColID)))
						if sid == ownSID {
							if readErr != nil || snap.SID != sid || snap.WorkspaceID.String() != wantWorkspace[sid] ||
								snap.Version != row.Int(model.ColVersion) || snap.TenantID != tenant {
								t.Errorf("[%s] ws %s reading own %s = %+v, %v", be.name, ws, sid, snap, readErr)
							}
							if getErr != nil || got.String(colSID) != sid {
								t.Errorf("[%s] ws %s Get own %s = %v, %v", be.name, ws, sid, got, getErr)
							}
							continue
						}
						if !errors.Is(readErr, store.ErrNotFound) {
							t.Errorf("[%s] ws %s reading foreign %s error = %v, want ErrNotFound", be.name, ws, sid, readErr)
						}
						if !errors.Is(getErr, store.ErrNotFound) || got != nil {
							t.Errorf("[%s] ws %s Get foreign %s = %v, %v; want ErrNotFound", be.name, ws, sid, got, getErr)
						}
					}
					if _, err := confined.Ext(aliasKind); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
						t.Errorf("[%s] confined alias Ext error = %v, want ErrWorkspaceLineageRequired", be.name, err)
					}
					return nil
				}); err != nil {
					t.Fatalf("[%s] confined view %s: %v", be.name, ws, err)
				}
			}

			// A confined Mutate gets no writer: the cross-workspace rewrite, its own
			// row, fresh inserts, a delete and the synthetic producer all refuse.
			if err := m.data.Mutate(ctx, tenant, func(raw store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, raw, alpha)
				if err != nil {
					return err
				}
				repo, err := confined.Ext(identityKind)
				if err != nil {
					return err
				}
				_, stamped := repo.(store.TransactionStampedGenericRepo)
				_, locker := repo.(store.RowLocker[model.Record])
				projector, hasProjector := repo.(store.DistinctProjector)
				if stamped || locker || !hasProjector {
					t.Errorf("[%s] confined identity capabilities: stamped=%t locker=%t projector=%t; want projector only",
						be.name, stamped, locker, hasProjector)
				}
				forged := model.Record{}
				for k, v := range rows[sidBravo] {
					forged[k] = v
				}
				forged[colIDWorkspaceID] = alpha.String()
				ownRow := model.Record{}
				for k, v := range rows[sidAlpha] {
					ownRow[k] = v
				}
				at := model.NewTimestamp(baseTime).String()
				fresh := model.Record{
					colSID: newSID(), colOrigin: OriginObserved, colIDFirstSeen: at, colIDLastSeen: at,
					colIDWorkspaceID: alpha.String(),
				}
				refusals := map[string]error{}
				_, refusals["update forged cross-workspace"] = repo.Update(ctx, forged)
				_, refusals["update own"] = repo.Update(ctx, ownRow)
				_, refusals["create"] = repo.Create(ctx, fresh)
				_, refusals["create with id"] = repo.CreateWithID(ctx, model.NewID(), fresh)
				refusals["delete own"] = repo.Delete(ctx, model.ID(rows[sidAlpha].String(model.ColID)))
				now, err := transactionNow(ctx, raw)
				if err != nil {
					return err
				}
				refusals["synthetic producer through the confined scope"] = createProtocolSyntheticIdentity(
					ctx, confined, alpha, BindingProtocolMCP, newSID(), model.NewID(), 1, now)
				for name, err := range refusals {
					if !errors.Is(err, store.ErrWorkspaceConfinement) {
						t.Errorf("[%s] %s: error = %v, want ErrWorkspaceConfinement", be.name, name, err)
					}
				}
				page, err := projector.ProjectDistinct(ctx, store.DistinctProjection{
					Column:  colSID,
					Filters: []model.Filter{{Column: colIDWorkspaceID, Op: model.OpEq, Value: bravo.String()}},
					Limit:   10,
				})
				if err != nil {
					return fmt.Errorf("confined projection: %w", err)
				}
				if len(page.Values) != 1 || page.Values[0] != sidAlpha || page.HasMore {
					t.Errorf("[%s] confined projection = %+v, want only %s", be.name, page, sidAlpha)
				}
				return nil
			}); err != nil {
				t.Fatalf("[%s] confined mutate: %v", be.name, err)
			}

			after := readRaw()
			for sid, before := range rows {
				if after[sid].Int(model.ColVersion) != before.Int(model.ColVersion) ||
					after[sid].String(colIDWorkspaceID) != before.String(colIDWorkspaceID) {
					t.Fatalf("[%s] identity %s changed under refused writes: before %v after %v",
						be.name, sid, before, after[sid])
				}
			}
			if n := countRows(t, m, tenant, identityKind); n != 3 {
				t.Fatalf("[%s] identity rows = %d, want 3", be.name, n)
			}
		})
	}
}
