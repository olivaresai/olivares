// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// These controls use the API and profile interfaces, including their refusals.
// Both database engines are required by the hosted qualification receipt.
func TestProviderAccount_RetryIdentity(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "retry")
			a := newAcctAPI(m, st, tenant)
			root := acctHomeRoot(t, m)
			body := map[string]any{"driver": "claude", "idempotency_key": "stable-create-1"}
			first := a.call("POST", "/provider-accounts", body)
			if first.code != 201 {
				t.Fatalf("first create = %d %s", first.code, first.raw)
			}
			again := a.call("POST", "/provider-accounts", body)
			if again.code != 201 || again.body["account_ref"] != first.body["account_ref"] {
				t.Fatalf("same intention created another account: first=%s replay=%s", first.raw, again.raw)
			}
			if got := len(acctEntries(t, filepath.Join(root, tenant.String(), testEnvRef))); got != 1 {
				t.Fatalf("retry has %d homes, want one", got)
			}
			body["driver"] = "codex"
			changed := a.call("POST", "/provider-accounts", body)
			if changed.code != http.StatusConflict {
				t.Fatalf("changed intention = %d, want 409", changed.code)
			}
			if got := len(a.list("").items()); got != 1 {
				t.Fatalf("accounts=%d, want one", got)
			}
		})
	}
}

func TestProviderAccount_ManagedNamespaceExcludesManualWriters(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "namespace")
			a := newAcctAPI(m, st, tenant)
			root := acctHomeRoot(t, m)
			r := a.create("claude", "")
			if r.code != 201 {
				t.Fatalf("create=%d %s", r.code, r.raw)
			}
			ref := r.body["account_ref"].(string)
			home := acctCustodyDir(root, tenant, ref)
			// A different driver bypasses the old home_slot index: custody is not per driver.
			_, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: "codex", ConfigHome: filepath.Join(home, "config"), UserHome: filepath.Join(home, "home")})
			if err == nil {
				t.Fatal("manual registration acquired a managed home through another driver")
			}
			outside := acctProfiles(t, m, tenant, "codex", 1)
			if got := a.call("POST", "/provider-accounts/"+outside[0].Ref+"/adopt", map[string]any{"name": "outside"}); got.code != 200 {
				t.Fatalf("outside home adoption=%d %s", got.code, got.raw)
			}
			if _, err := os.Stat(filepath.Join(home, "config")); err != nil {
				t.Fatalf("managed home changed after refusal: %v", err)
			}
		})
	}
}

func TestProviderAccount_StructuralSymlinkIsRefused(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "symlink")
			root := acctHomeRoot(t, m)
			if err := os.MkdirAll(filepath.Join(root, "other-tenant"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other-tenant", filepath.Join(root, tenant.String())); err != nil {
				t.Fatal(err)
			}
			r := newAcctAPI(m, st, tenant).create("claude", "")
			if r.code != 409 {
				t.Fatalf("internal symlink accepted: %d %s", r.code, r.raw)
			}
			if got := acctEntries(t, filepath.Join(root, "other-tenant")); len(got) != 0 {
				t.Fatalf("foreign namespace modified: %v", got)
			}
		})
	}
}
