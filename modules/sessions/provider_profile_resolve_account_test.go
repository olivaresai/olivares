// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// A new session can be pointed at an account by its name (or its profile
// reference): the answer is that account's profile, never another one, and a
// request without `account` is the rule it always was.
func TestProviderProfile_ResolveByAccount(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "resolve-account-"+be.name)
			a := newAcctAPI(m, st, tenant)
			claude := acctProfiles(t, m, tenant, "claude", 2)
			codex := acctProfiles(t, m, tenant, "codex", 1)[0]
			for i, name := range []string{"claude", "claude-b"} {
				if r := a.adopt(claude[i].Ref, name); r.code != http.StatusOK {
					t.Fatal(r.raw)
				}
			}
			if r := a.adopt(codex.Ref, "codex"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			resolve := func(driver, account string) acctResp {
				return a.call(http.MethodPost, "/provider-profiles/resolve", map[string]any{"driver": driver, "account": account})
			}

			for _, c := range []struct{ account, want string }{{"claude-b", claude[1].Ref}, {"claude", claude[0].Ref}, {claude[1].Ref, claude[1].Ref}} {
				r := resolve("claude", c.account)
				profile, _ := r.body["profile"].(map[string]any)
				if r.code != http.StatusOK || profile["profile_ref"] != c.want || r.body["created"] != false || r.body["reason"] != "account" {
					t.Fatalf("resolve %q = %d %s, want %s", c.account, r.code, r.raw, c.want)
				}
			}
			r := resolve("claude", "claude-b")
			if profile, _ := r.body["profile"].(map[string]any); profile["account_name"] != "claude-b" {
				t.Fatalf("resolved profile omits its account name: %s", r.raw)
			}

			for _, c := range []struct {
				name, driver, account string
				want                  int
			}{
				{"unknown name", "claude", "nobody", http.StatusNotFound},
				{"another tool's account", "claude", "codex", http.StatusConflict},
				{"unknown reference", "claude", newProfileRef(), http.StatusNotFound},
				{"unknown driver", "nope", "claude", http.StatusBadRequest},
			} {
				if r := resolve(c.driver, c.account); r.code != c.want {
					t.Fatalf("%s = %d %s, want %d", c.name, r.code, r.raw, c.want)
				}
			}

			state := ProfileDisabled
			if _, err := m.PatchProfile(context.Background(), tenant, claude[1].Ref, ProfilePatch{State: &state}); err != nil {
				t.Fatal(err)
			}
			if r := resolve("claude", "claude-b"); r.code != http.StatusConflict {
				t.Fatalf("disabled account = %d %s, want 409", r.code, r.raw)
			}

			// Without `account` the old rule answers as before: this module has no
			// sign-in reader wired, so it refuses, and it does so the same way.
			if r := a.call(http.MethodPost, "/provider-profiles/resolve", map[string]any{"driver": "claude"}); r.code != http.StatusServiceUnavailable {
				t.Fatalf("resolve without account = %d %s, want the unchanged 503", r.code, r.raw)
			}
		})
	}
}

// The beta contract lists `account` on the resolve body.
func TestProviderProfileResolveOpenAPIListsAccount(t *testing.T) {
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	op := doc["paths"].(map[string]any)["/v1/m/sessions/provider-profiles/resolve"].(map[string]any)["post"].(map[string]any)
	schema := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	if properties["account"] == nil || properties["account"].(map[string]any)["type"] != "string" {
		t.Fatalf("resolve body omits account: %v", properties)
	}
	if req, _ := schema["required"].([]any); len(req) != 1 || req[0] != "driver" {
		t.Fatalf("resolve body must still require only driver: %v", schema["required"])
	}
}
