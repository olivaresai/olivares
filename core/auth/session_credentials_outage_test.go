// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// outageAuthStore fails the auth partition's reads while down is set, the way
// the first request after a pool reset does: with the store's own unavailable
// sentinel and the driver's cause. Everything else is the real store.
type outageAuthStore struct {
	store.Store
	down atomic.Bool
}

func (s *outageAuthStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.down.Load() {
		return fmt.Errorf("%w: %w", store.ErrStoreUnavailable, errors.New("conn closed"))
	}
	return s.Store.AuthView(ctx, fn)
}

// the regression: a store that could not be read says nothing about a session
// credential. The request that met the outage is told so (503 at the endpoints),
// and the same bearer works on the next request once the store answers again. A
// credential cached as revoked by the outage would turn the retryable 503 into
// a permanent 401 for the whole session.
func TestSessionCredentialSurvivesAStoreOutage(t *testing.T) {
	ctx := t.Context()
	flaky := &outageAuthStore{Store: testStore(t)}
	a := auth.NewAuthenticator(flaky, nil)
	admin := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, flaky, "session-outage")
	var workspace model.ID
	if err := flaky.View(ctx, tenant, func(sc store.Scope) error { ws, err := sc.DefaultWorkspace(ctx); workspace = ws.ID; return err }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateUser(ctx, admin, auth.NewUser{Email: "outage@example.invalid", Password: "launcher-password-1", Tenant: tenant, Role: auth.RoleEditor}); err != nil {
		t.Fatal(err)
	}
	login, _, err := a.Login(ctx, "outage@example.invalid", "launcher-password-1", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := a.Authenticate(ctx, login)
	if err != nil {
		t.Fatal(err)
	}
	service := auth.NewSessionCredentials(a, func(context.Context, auth.SessionScope) error { return nil })
	run := model.NewID().String()
	token, err := service.Mint(ctx, launcher, auth.SessionScope{
		TenantID: tenant, WorkspaceID: workspace, FolderRef: "folder", SessionRef: "session-" + run, RunRef: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, token); err != nil {
		t.Fatalf("before the outage: %v", err)
	}

	flaky.down.Store(true)
	_, err = service.Authenticate(ctx, token)
	if !errors.Is(err, store.ErrStoreUnavailable) {
		t.Fatalf("during the outage: err = %v, want store.ErrStoreUnavailable in its chain", err)
	}
	if errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("during the outage: err = %v, told the session its credential is invalid", err)
	}

	flaky.down.Store(false)
	if _, err := service.Authenticate(ctx, token); err != nil {
		t.Fatalf("after the store came back: err = %v, want the session credential to work again", err)
	}
}
