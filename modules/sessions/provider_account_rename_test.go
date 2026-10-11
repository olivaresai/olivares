// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"strings"
	"testing"
)

// A rename is an UPDATE of account_name and nothing else: the reference, the
// homes, the home slot and the K4 launch digest stay byte-identical, the old
// name is free again, and the act is one `renamed` audit event.
func TestProviderAccount_RenameChangesOnlyTheName(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "rename-"+be.name)
			a := newAcctAPI(m, st, tenant)
			ref := acctProfiles(t, m, tenant, "claude", 1)[0].Ref
			if r := a.adopt(ref, "claude-b"); r.code != http.StatusOK {
				t.Fatalf("adopt = %d %s", r.code, r.raw)
			}
			before, digest := acctRow(t, m, tenant, ref), acctDigest(t, m, tenant, ref)
			path := "/provider-accounts/" + ref

			r := a.call(http.MethodPatch, path, map[string]any{"name": "work"})
			if r.code != http.StatusOK || r.body["name"] != "work" || r.body["account_ref"] != ref {
				t.Fatalf("rename = %d %s", r.code, r.raw)
			}
			if strings.Contains(r.raw, before.String(colPPConfigHome)) {
				t.Fatal("rename response disclosed a home")
			}
			after := acctRow(t, m, tenant, ref)
			if after.String(colPPAccountName) != "work" {
				t.Fatalf("stored name = %q, want work", after.String(colPPAccountName))
			}
			for _, col := range []string{colPPConfigHome, colPPUserHome, colPPHomeSlot, colPPDriver, colPPEnvRef, colPPHomeMode, colPPHomeGeneration} {
				if before[col] != after[col] {
					t.Fatalf("rename moved %s: %v -> %v", col, before[col], after[col])
				}
			}
			if got := acctDigest(t, m, tenant, ref); got != digest {
				t.Fatal("rename moved the K4 launch digest")
			}
			events := acctAuditsOf(t, m, tenant, "sessions.provider_account.renamed")
			if len(events) != 1 || events[0].meta["name"] != "work" || events[0].meta["previous_name"] != "claude-b" {
				t.Fatalf("renamed audit = %+v", events)
			}

			// Repeating the current name is a no-op: no second event.
			if r := a.call(http.MethodPatch, path, map[string]any{"name": "work"}); r.code != http.StatusOK {
				t.Fatalf("repeat = %d %s", r.code, r.raw)
			}
			if got := len(acctAuditsOf(t, m, tenant, "sessions.provider_account.renamed")); got != 1 {
				t.Fatalf("renamed events after a repeat = %d, want 1", got)
			}

			// The old name is free again, for the next generated name too.
			other := acctProfiles(t, m, tenant, "claude", 1)[0].Ref
			if r := a.adopt(other, "claude-b"); r.code != http.StatusOK {
				t.Fatalf("adopt under the freed name = %d %s", r.code, r.raw)
			}
		})
	}
}

// A taken name is 409 and never replaced; a malformed name is 422; null and
// non-strings are 400; a name sent with a display label changes both at once.
func TestProviderAccount_RenameRefusals(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "rename-refuse-"+be.name)
			a := newAcctAPI(m, st, tenant)
			profiles := acctProfiles(t, m, tenant, "claude", 2)
			if r := a.adopt(profiles[0].Ref, "claude"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			if r := a.adopt(profiles[1].Ref, "claude-b"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			path := "/provider-accounts/" + profiles[1].Ref

			for _, c := range []struct {
				name string
				body map[string]any
				want int
			}{
				{"taken", map[string]any{"name": "claude"}, http.StatusConflict},
				{"shape upper", map[string]any{"name": "Work"}, http.StatusUnprocessableEntity},
				{"shape space", map[string]any{"name": " work"}, http.StatusUnprocessableEntity},
				{"empty", map[string]any{"name": ""}, http.StatusUnprocessableEntity},
				{"null", map[string]any{"name": nil}, http.StatusBadRequest},
				{"number", map[string]any{"name": 7}, http.StatusBadRequest},
			} {
				if r := a.call(http.MethodPatch, path, c.body); r.code != c.want {
					t.Fatalf("%s = %d %s, want %d", c.name, r.code, r.raw, c.want)
				}
			}
			if got := acctRow(t, m, tenant, profiles[1].Ref).String(colPPAccountName); got != "claude-b" {
				t.Fatalf("a refused rename changed the name to %q", got)
			}
			if r := a.call(http.MethodPatch, "/provider-accounts/ppf_doesnotexist0000", map[string]any{"name": "x"}); r.code != http.StatusNotFound {
				t.Fatalf("unknown ref = %d %s, want 404", r.code, r.raw)
			}

			r := a.call(http.MethodPatch, path, map[string]any{"name": "research", "display_name": "Research"})
			if r.code != http.StatusOK || r.body["name"] != "research" || r.body["display_name"] != "Research" {
				t.Fatalf("name and label together = %d %s", r.code, r.raw)
			}
		})
	}
}

// An account the engine built keeps a reservation of its name beside the profile
// (the home operation), and that reservation is read when a name is checked. A
// rename must move it too, or the old name stays taken for good and the new one
// is not held: renaming back, or giving the old name to another account, would
// answer 409 although nobody holds the name.
func TestProviderAccount_RenameMovesTheReservationOfAnEngineBuiltAccount(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "rename-reserved-"+be.name)
			a := newAcctAPI(m, st, tenant)
			acctHomeRoot(t, m)

			first := a.create("claude", "")
			if first.code != http.StatusCreated || first.body["name"] != "claude" {
				t.Fatalf("create = %d %s", first.code, first.raw)
			}
			ref := first.body["account_ref"].(string)
			path := "/provider-accounts/" + ref
			rename := func(name string) acctResp { return a.call(http.MethodPatch, path, map[string]any{"name": name}) }

			if r := rename("work"); r.code != http.StatusOK {
				t.Fatalf("rename to work = %d %s", r.code, r.raw)
			}
			// The old name is free: another engine-built account may take it.
			second := a.create("claude", "claude")
			if second.code != http.StatusCreated || second.body["name"] != "claude" {
				t.Fatalf("create under the freed name = %d %s", second.code, second.raw)
			}
			// The new name is held: a third account cannot take it.
			if r := a.create("claude", "work"); r.code != http.StatusConflict {
				t.Fatalf("create under the held name = %d %s, want 409", r.code, r.raw)
			}
			// Renaming away from a name and back works once nobody else holds it.
			other := second.body["account_ref"].(string)
			if r := a.call(http.MethodPatch, "/provider-accounts/"+other, map[string]any{"name": "other"}); r.code != http.StatusOK {
				t.Fatalf("rename second = %d %s", r.code, r.raw)
			}
			if r := rename("claude"); r.code != http.StatusOK || r.body["name"] != "claude" {
				t.Fatalf("rename back = %d %s", r.code, r.raw)
			}
			// Generated names agree with the stored ones: the next one is claude-b.
			if r := a.create("claude", ""); r.code != http.StatusCreated || r.body["name"] != "claude-b" {
				t.Fatalf("generated name = %d %s, want claude-b", r.code, r.raw)
			}
		})
	}
}
