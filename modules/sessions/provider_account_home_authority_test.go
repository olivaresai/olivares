// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// Keep both authorizer compositions explicit: flat RBAC permits an editor's
// route, so it is the actual confined store admission that must precede mkdir.
func newAccountHomeAuthorityFixture(t *testing.T, cfg store.Config, scoped bool) *streamConfinementFixture {
	t.Helper()
	ctx := context.Background()
	m, gov := New(), governance.New()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		if err := m.RegisterSchema(reg); err != nil {
			return err
		}
		return gov.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	m.UseExecutionEnvironmentRef(testEnvRef)
	gov.UseData(api.NewModuleData(st))
	stopModuleAtCleanup(t, m)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	setup := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := setup.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	authr := auth.NewAuthenticator(st, nil)
	authz := auth.NewAuthorizer(nil)
	if scoped {
		authz = auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants()))
	}
	srv, err := api.New(api.Options{Store: st, Authenticator: authr, Authorizer: authz, Signer: signer, SetupToken: setup, Version: "test", Modules: []api.Module{m, gov}})
	if err != nil {
		t.Fatal(err)
	}
	return &streamConfinementFixture{harness: &harness{t: t, m: m, srv: srv, st: st, setupTok: plaintext}, authr: authr, authz: authz}
}

func TestProviderAccount_CreateAdmissionPrecedesFilesystem(t *testing.T) {
	for _, be := range acctConfinedBackends(t) {
		for _, scoped := range []bool{false, true} {
			mode := "flat-rbac"
			if scoped {
				mode = "scoped-grants"
			}
			t.Run(be.name+"/"+mode, func(t *testing.T) {
				f := newAccountHomeAuthorityFixture(t, be.config(t), scoped)
				ctx := context.Background()
				admin := f.adminLogin()
				tenant := f.createOrg(admin, "home-authority")
				var workspace model.ID
				if err := f.st.Mutate(ctx, tenant, func(sc store.Scope) error {
					ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Limited", Slug: "limited", Status: model.StatusActive})
					workspace = ws.ID
					return err
				}); err != nil {
					t.Fatal(err)
				}
				confined := f.member(t, admin, tenant, "confined@home.test", workspace)
				// Assert the premise; a route-level refusal for an unrelated role is not a
				// control of the original post-filesystem confinement defect.
				principal, err := f.authr.Authenticate(ctx, confined)
				if err != nil {
					t.Fatal(err)
				}
				if ws, ok := principal.ConfinedWorkspaceIn(tenant); !ok || ws != workspace {
					t.Fatal("fixture is not confined")
				}
				root := filepath.Join(t.TempDir(), "accounts")
				f.m.UseAccountsRoot(root)
				request := map[string]any{"driver": "claude"}
				base := "/v1/m/sessions/provider-accounts"
				for _, existing := range []bool{false, true} {
					if existing {
						if err := os.Mkdir(root, 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.Chmod(root, 0755); err != nil {
							t.Fatal(err)
						}
					}
					denied := f.doJSON(http.MethodPost, base, confined, request, tenantHdr(tenant))
					if denied.code != 403 {
						t.Fatalf("confined create=%d %s", denied.code, denied.raw)
					}
					if !existing {
						if _, err := os.Lstat(root); !os.IsNotExist(err) {
							t.Fatalf("denied writer created root: %v", err)
						}
					} else {
						if mode := acctMode(t, root); mode != 0755 {
							t.Fatalf("denied writer changed root mode to %o", mode)
						}
						if entries := acctEntries(t, root); len(entries) != 0 {
							t.Fatalf("denied writer created %v", entries)
						}
					}
				}
				unauthorized := f.doJSON(http.MethodPost, base, "", request, tenantHdr(tenant))
				if unauthorized.code != 401 {
					t.Fatalf("unauthenticated create=%d", unauthorized.code)
				}
				if entries := acctEntries(t, root); len(entries) != 0 {
					t.Fatalf("unauthenticated writer changed root: %v", entries)
				}
				wide := f.member(t, admin, tenant, "wide@home.test", model.ID(""))
				widePrincipal, err := f.authr.Authenticate(ctx, wide)
				if err != nil {
					t.Fatal(err)
				}
				if _, limited := widePrincipal.ConfinedWorkspaceIn(tenant); limited || widePrincipal.Superadmin {
					t.Fatal("positive control must be a tenant-wide member")
				}
				positive := f.doJSON(http.MethodPost, base, wide, request, tenantHdr(tenant))
				if positive.code != 201 {
					t.Fatalf("tenant-authorized create=%d %s", positive.code, positive.raw)
				}
				key := positive.header.Get("Idempotency-Key")
				if key == "" {
					t.Fatal("create did not expose retry key")
				}
				replay := f.doJSON(http.MethodPost, base, confined, map[string]any{"driver": "claude", "idempotency_key": key}, tenantHdr(tenant))
				if replay.code != 403 {
					t.Fatalf("retry key bypassed confinement: %d %s", replay.code, replay.raw)
				}
			})
		}
	}
}
