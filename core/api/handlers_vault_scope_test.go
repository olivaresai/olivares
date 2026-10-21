// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// Admitting every member to the tenant scope (so each keeps a vault) must give them
// nothing of the organization's own secrets: a viewer and an editor are refused every
// verb on mcp/ and env/ names, and what the admin stored is untouched afterwards.
func TestVaultMembersCannotTouchOrganizationSecrets(t *testing.T) {
	f := newVaultFixture(t)
	f.put(f.alice, "mcp/github", "org-mcp-secret-0451")
	f.put(f.alice, "env/GITHUB_TOKEN", "org-env-secret-0451")
	f.put(f.alice, f.item(f.aliceID, "bank"), vaultLogin)

	for role, email := range map[string]string{auth.RoleViewer: "viewer@vault.io", auth.RoleEditor: "editor@vault.io"} {
		token := f.h.mkMember(f.root, email, "fixturepass1", role, f.tenant)
		for _, name := range []string{"mcp/github", "env/GITHUB_TOKEN", "mcp/new"} {
			for verb, got := range map[string]resp{
				"put":    f.put(token, name, "member-value"),
				"delete": f.h.do("DELETE", vaultScopePath, token, map[string]any{"name": name}, tenantHdr(f.tenant)),
				"reveal": f.reveal(token, name),
				"code":   f.code(token, name),
			} {
				if got.code != http.StatusForbidden || strings.Contains(got.raw, "0451") {
					t.Fatalf("%s %s %s = %d %s, want 403 without a value", role, verb, name, got.code, got.raw)
				}
			}
		}
		if names := listed(t, f.list(token)); len(names) != 0 {
			t.Fatalf("%s lists %v, want only her own items", role, names)
		}
	}
	// Nothing the admin stored changed.
	adminView := listed(t, f.list(f.alice))
	if len(adminView) != 3 {
		t.Fatalf("admin list after the refusals = %v, want mcp/github, env/GITHUB_TOKEN and her item", adminView)
	}
}

// The tenant scope is for members of that tenant only, and the deployment-wide store stays
// the superadmin's.
func TestVaultScopeStaysClosedToOutsidersAndMembers(t *testing.T) {
	f := newVaultFixture(t)
	carol, carolID := f.member("carol@vault.io")
	other := f.h.createOrg(f.root, "outsider-co")

	// A viewer is not admitted to the global store, whatever she asks.
	for _, path := range []string{"/v1/console/secrets", "/v1/console/secrets/reveal", "/v1/console/secrets/totp", "/v1/console/secrets/generate"} {
		method := "POST"
		if path == "/v1/console/secrets" {
			method = "GET"
		}
		if got := f.h.do(method, path, carol, map[string]any{"name": f.item(carolID, "bank")}, nil); got.code != http.StatusForbidden {
			t.Fatalf("viewer %s %s without scope = %d %s, want 403", method, path, got.code, got.raw)
		}
	}
	// A member of one organization has no vault in another.
	for _, call := range []struct{ method, path string }{
		{"GET", vaultScopePath}, {"PUT", vaultScopePath}, {"POST", vaultRevealPath},
		{"POST", vaultTOTPPath}, {"POST", vaultGeneratePath},
	} {
		got := f.h.do(call.method, call.path, carol, map[string]any{"name": f.item(carolID, "bank"), "value": vaultValue}, tenantHdr(other))
		if got.code != http.StatusForbidden {
			t.Fatalf("%s %s through an organization she is not in = %d %s, want 403", call.method, call.path, got.code, got.raw)
		}
	}
}

// A name with whitespace at its edges is stored trimmed by the secret store; the owner check
// must not pass a name that would land somewhere it did not check.
func TestVaultNameEdgesCannotMakeAnOrphan(t *testing.T) {
	f := newVaultFixture(t)
	for _, name := range []string{f.item(f.aliceID, "bank") + " ", f.item(f.aliceID, " bank"), f.item(f.aliceID, "") + " "} {
		got := f.put(f.alice, name, vaultValue)
		if got.code != http.StatusForbidden && got.code != http.StatusBadRequest {
			t.Fatalf("put %q = %d %s, want a refusal", name, got.code, got.raw)
		}
	}
	if names := listed(t, f.list(f.alice)); len(names) != 0 {
		t.Fatalf("an edge-spaced name was stored: %v", names)
	}
}

// Replacing an item replaces its kind in the list too.
func TestVaultReplacingAnItemUpdatesItsKind(t *testing.T) {
	f := newVaultFixture(t)
	name := f.item(f.aliceID, "bank")
	f.put(f.alice, name, vaultLogin)
	if got := listed(t, f.list(f.alice))[name]; got != "login+totp" {
		t.Fatalf("hint after the login = %q", got)
	}
	f.put(f.alice, name, vaultValue)
	if got := listed(t, f.list(f.alice))[name]; got != "note" {
		t.Fatalf("hint after replacing it with a note = %q, want note", got)
	}
	// An organization secret keeps its fingerprint hint: twelve hex characters.
	f.put(f.alice, "mcp/github", "org-secret")
	if hint := listed(t, f.list(f.alice))["mcp/github"]; len(hint) != 12 || strings.Trim(hint, "0123456789abcdef") != "" {
		t.Fatalf("mcp/github hint = %q, want a 12-character fingerprint", hint)
	}
}
