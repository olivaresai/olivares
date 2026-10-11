// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

// The profile view carries the account's name, so a picker can show it; a
// profile nobody has named has no such field (omitempty), and a rename shows.
func TestProviderProfile_DTOCarriesTheAccountName(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "profile-name-"+be.name)
			a := newAcctAPI(m, st, tenant)
			profiles := acctProfiles(t, m, tenant, "claude", 2)
			ref, plain := profiles[0].Ref, profiles[1].Ref

			if r := a.call(http.MethodGet, "/provider-profiles/"+ref, nil); r.code != http.StatusOK || r.body["account_name"] != nil {
				t.Fatalf("unnamed profile = %d %s, want no account_name", r.code, r.raw)
			}
			if r := a.adopt(ref, "claude-b"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			if r := a.call(http.MethodGet, "/provider-profiles/"+ref, nil); r.body["account_name"] != "claude-b" {
				t.Fatalf("get after adopt = %s", r.raw)
			}
			if r := a.call(http.MethodPatch, "/provider-accounts/"+ref, map[string]any{"name": "work"}); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			list := a.call(http.MethodGet, "/provider-profiles", nil).items()
			names := map[string]any{}
			for _, item := range list {
				names[item["profile_ref"].(string)] = item["account_name"]
			}
			if names[ref] != "work" || names[plain] != nil {
				t.Fatalf("list names = %v, want %s=work and %s unnamed", names, ref, plain)
			}
		})
	}
}
