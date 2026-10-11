//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// Reconstruct a current allowed credential, then observe an expired proof on the
// final transaction clock. No authority row changes can explain the refusal.
func TestWorkspaceSnapshotFinalAuthorityExpiry(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			eng, token, tenant := wireBoot(t, cfg, t.TempDir())
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "research"), 0o700); err != nil {
				t.Fatal(err)
			}
			code, workspace, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sessions/workspaces", token, tenant.String(), map[string]any{"name": "expiry-snapshot", "root_path": root, "allow_subpaths": []string{"research"}, "mount_mode": "ro"})
			if code != http.StatusCreated {
				t.Fatalf("registration = %d", code)
			}
			supplied, err := eng.authr.Authenticate(t.Context(), token)
			if err != nil {
				t.Fatal(err)
			}
			scope := sourcescope.New()
			scope.UseData(api.NewModuleData(eng.store))
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			checker := workspaceSnapshotAuthority{principals: eng.authr, authz: eng.authz, scopes: scope.Resolver(), data: wireFutureReadStore{Store: eng.store, future: time.Now().Add(time.Minute)}}
			var registration model.ID
			if err := eng.store.View(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sessions.workspace")
				if err != nil {
					return err
				}
				rows, _, err := repo.List(t.Context(), model.Query{Filters: []model.Filter{{Column: "workspace_ref", Op: model.OpEq, Value: workspace["workspace_ref"].(string)}}})
				if err != nil {
					return err
				}
				if len(rows) != 1 {
					t.Fatalf("fixture registrations = %d", len(rows))
				}
				registration = model.ID(rows[0].String(model.ColID))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			live := checker
			live.data = eng.store
			if stamp, err := live.Check(ctx, tenant, supplied, workspace["workspace_ref"].(string), registration); err != nil || stamp == "" {
				t.Fatalf("unexpired native authority did not establish a snapshot stamp: %v", err)
			}
			stamp, err := checker.Check(ctx, tenant, supplied, workspace["workspace_ref"].(string), registration)
			if err == nil || stamp != "" {
				t.Fatal("expired final native authority established a snapshot stamp")
			}
		})
	}
}

type wireFutureReadStore struct {
	store.Store
	future time.Time
}

func (s wireFutureReadStore) View(ctx context.Context, tenant model.TenantID, read func(store.Scope) error) error {
	return s.Store.View(ctx, tenant, func(sc store.Scope) error {
		return read(wireFutureReadScope{Scope: sc, AuthoritySnapshotBundleReader: sc.(store.AuthoritySnapshotBundleReader), future: s.future})
	})
}

type wireFutureReadScope struct {
	store.Scope
	store.AuthoritySnapshotBundleReader
	future time.Time
}

func (s wireFutureReadScope) TransactionNow(context.Context) (model.Timestamp, error) {
	return model.NewTimestamp(s.future), nil
}
