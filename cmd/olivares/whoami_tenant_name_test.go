// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// whoami names each of the principal's own tenants (tenant_name beside tenant), so
// the console's organization switcher shows names on an install whose cross-tenant
// org listing (/v1/system/orgs) needs the admin pool it does not have. Real
// PostgreSQL with the application and owner roles only; one boot.
func TestWhoamiNamesThePrincipalsOwnTenants(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "postgres", DSN: pg.App, OwnerDSN: pg.Owner,
		Version: version, Logger: discardLogger(), ApplyModuleProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	h := eng.api.Handler()
	tok, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	const orgName = "Olivares Whoami Fixture"
	code, setup, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/setup", "", "", map[string]any{
		"token": tok, "email": "root@x.io", "password": "supersecret1", "organization": orgName,
	})
	if code != http.StatusCreated {
		t.Fatalf("setup = %d: %s", code, raw)
	}
	org, _ := setup["organization"].(map[string]any)
	tenant, _ := org["tenant_id"].(string)
	code, login, raw := doDemoViewJSON(t, h, http.MethodPost, "/v1/auth/login", "", "", map[string]any{"email": "root@x.io", "password": "supersecret1"})
	if code != http.StatusOK {
		t.Fatalf("login = %d: %s", code, raw)
	}
	admin, _ := login["token"].(string)
	code, who, raw := doDemoViewJSON(t, h, http.MethodGet, "/v1/auth/whoami", admin, "", nil)
	if code != http.StatusOK {
		t.Fatalf("whoami = %d: %s", code, raw)
	}
	grants, _ := who["grants"].([]any)
	for _, g := range grants {
		if grant, _ := g.(map[string]any); grant["tenant"] == tenant {
			if grant["tenant_name"] != orgName {
				t.Fatalf("whoami grant for %s: tenant_name = %v, want %q (%s)", tenant, grant["tenant_name"], orgName, raw)
			}
			return
		}
	}
	t.Fatalf("whoami has no grant for the setup tenant %s: %s", tenant, raw)
}
