// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"testing"
)

// A generated account name starts from the driver's alias, not its key: Gemini
// CLI's key is gemini-cli, its accounts are gemini, gemini-b, gemini-c. The other
// drivers keep their keys as stems, on create and on adopt alike.
func TestProviderAccount_GeneratedNamesUseTheDriverAlias(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "alias-"+be.name)
			a := newAcctAPI(m, st, tenant)
			acctHomeRoot(t, m)

			var got []string
			for range 2 {
				r := a.create("gemini-cli", "")
				if r.code != http.StatusCreated {
					t.Fatalf("create gemini-cli = %d %s", r.code, r.raw)
				}
				got = append(got, r.body["name"].(string))
			}
			legacy := acctProfiles(t, m, tenant, "gemini-cli", 1)[0]
			r := a.adopt(legacy.Ref, "")
			if r.code != http.StatusOK {
				t.Fatalf("adopt gemini-cli = %d %s", r.code, r.raw)
			}
			got = append(got, r.body["name"].(string))
			for i, want := range []string{"gemini", "gemini-b", "gemini-c"} {
				if got[i] != want {
					t.Fatalf("gemini-cli generated names = %v, want gemini, gemini-b, gemini-c", got)
				}
			}

			if r := a.create("claude", ""); r.code != http.StatusCreated || r.body["name"] != "claude" {
				t.Fatalf("claude keeps its key as the stem: %d %s", r.code, r.raw)
			}
			if r := a.create("codex", ""); r.code != http.StatusCreated || r.body["name"] != "codex" {
				t.Fatalf("codex keeps its key as the stem: %d %s", r.code, r.raw)
			}
		})
	}
}
