// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Move the workspace's status after route admission, before the creation callback.
// The real transaction must refuse and roll back instead of trusting its earlier read.
type collectionWorkspaceMovesStore struct {
	store.Store
	workspace model.ID
	armed     atomic.Bool
}

func (s *collectionWorkspaceMovesStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		if s.armed.Swap(false) {
			ws, err := sc.Workspaces().Get(ctx, s.workspace)
			if err != nil {
				return err
			}
			ws.Status = model.StatusInactive
			if _, err = sc.Workspaces().Update(ctx, ws); err != nil {
				return err
			}
		}
		return fn(sc)
	})
}

func TestCollectionCreationRechecksWorkspaceInTransaction(t *testing.T) {
	for _, route := range []string{"agents", "agent-groups"} {
		t.Run(route, func(t *testing.T) {
			var wrapped *collectionWorkspaceMovesStore
			h := newHarnessOpts(t, func(o *api.Options) {
				wrapped = &collectionWorkspaceMovesStore{Store: o.Store}
				o.Store = wrapped
			})
			owner := h.adminLogin()
			tenant := h.createOrg(owner, "creation-race")
			h.elevate(owner)
			hdr := tenantHdr(tenant)
			ws := h.do("POST", "/v1/workspaces", owner, map[string]any{"name": "Department", "slug": "department"}, hdr)
			if ws.code != http.StatusCreated {
				t.Fatalf("workspace = %d %s", ws.code, ws.raw)
			}
			workspace := ws.body["id"].(string)
			user := h.do("POST", "/v1/users", owner, map[string]any{"email": "admin@race.example", "password": "memberpass1", "tenant": tenant.String(), "role": "admin", "workspace_id": workspace}, hdr)
			if user.code != http.StatusCreated {
				t.Fatalf("confined admin = %d %s", user.code, user.raw)
			}
			login := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "admin@race.example", "password": "memberpass1"}, nil)
			if login.code != http.StatusOK {
				t.Fatalf("login = %d %s", login.code, login.raw)
			}
			in := map[string]any{"name": "created", "workspace_id": workspace}
			if route == "agents" {
				in["kind"] = "claude-code"
			} else {
				in["slug"] = "created"
			}
			wrapped.workspace = model.ID(workspace)
			wrapped.armed.Store(true)
			r := h.do("POST", "/v1/"+route, login.body["token"].(string), in, hdr)
			if r.code != http.StatusForbidden {
				t.Fatalf("workspace changed after admission = %d %s, want 403", r.code, r.raw)
			}
			if wrapped.armed.Load() {
				t.Fatal("refused before reaching the creation transaction")
			}
			rows := h.do("GET", "/v1/"+route, owner, nil, hdr)
			if rows.code != http.StatusOK || len(rows.body["items"].([]any)) != 0 {
				t.Fatalf("refused create left rows: %d %s", rows.code, rows.raw)
			}
			read := h.do("GET", "/v1/workspaces/"+workspace, owner, nil, hdr)
			if read.code != http.StatusOK || read.body["status"] != "active" {
				t.Fatalf("transaction did not roll back: %d %s", read.code, read.raw)
			}
		})
	}
}
