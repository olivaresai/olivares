// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// adoptUnnamed calls the boot-time adoption by name, so this package still
// compiles, and every other test still runs, in a tree without it.
func adoptUnnamed(t *testing.T, m *Module, tenant model.TenantID) int {
	t.Helper()
	method := reflect.ValueOf(m).MethodByName("AdoptUnnamedProfiles")
	if !method.IsValid() {
		t.Fatal("the sessions module has no AdoptUnnamedProfiles")
	}
	run, ok := method.Interface().(func(context.Context, model.TenantID) (int, error))
	if !ok {
		t.Fatalf("AdoptUnnamedProfiles is %s, want func(context.Context, model.TenantID) (int, error)", method.Type())
	}
	n, err := run(context.Background(), tenant)
	if err != nil {
		t.Fatalf("AdoptUnnamedProfiles: %v", err)
	}
	return n
}

// At boot every ACTIVE unnamed profile of a tool that supports accounts is named,
// oldest first, from the driver's stem: the first Claude profile becomes `claude`,
// the next `claude-b`. A profile that already has a name keeps it, a disabled one
// is left alone, nothing on disk is touched and the launch digest does not move.
// A second run changes nothing.
func TestProviderAccount_BootAdoptsLegacyProfilesOldestFirstAndIsIdempotent(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			tenant := ensureTenant(t, st, "legacy-"+be.name)
			a := newAcctAPI(m, st, tenant)

			claude := acctProfiles(t, m, tenant, "claude", 3) // created oldest first
			codex := acctProfiles(t, m, tenant, "codex", 1)[0]
			gemini := acctProfiles(t, m, tenant, "gemini-cli", 1)[0]
			if r := a.adopt(claude[1].Ref, "mine"); r.code != http.StatusOK {
				t.Fatal(r.raw)
			}
			disabled := ProfileDisabled
			if _, err := m.PatchProfile(context.Background(), tenant, claude[2].Ref, ProfilePatch{State: &disabled}); err != nil {
				t.Fatal(err)
			}
			digests := map[string]string{}
			rows := map[string]model.Record{}
			for _, p := range append(claude, codex, gemini) {
				rows[p.Ref] = acctRow(t, m, tenant, p.Ref)
				// Only an enabled Claude profile has a launch here to digest.
				if p.Driver == "claude" && p.Ref != claude[2].Ref {
					digests[p.Ref] = acctDigest(t, m, tenant, p.Ref)
				}
			}

			if n := adoptUnnamed(t, m, tenant); n != 3 {
				t.Fatalf("first run named %d profiles, want 3 (claude[0], codex, gemini-cli)", n)
			}
			want := map[string]string{claude[0].Ref: "claude", claude[1].Ref: "mine", claude[2].Ref: "", codex.Ref: "codex", gemini.Ref: "gemini"}
			for ref, name := range want {
				if got := acctRow(t, m, tenant, ref).String(colPPAccountName); got != name {
					t.Errorf("%s is named %q, want %q", ref, got, name)
				}
			}
			for _, p := range append(claude, codex, gemini) {
				before, after := rows[p.Ref], acctRow(t, m, tenant, p.Ref)
				for _, col := range []string{colPPConfigHome, colPPUserHome, colPPHomeSlot, colPPDriver, colPPEnvRef, colPPState, colPPAuthSource} {
					if before[col] != after[col] {
						t.Errorf("%s: %s moved: %v -> %v", p.Ref, col, before[col], after[col])
					}
				}
				if want, ok := digests[p.Ref]; ok && acctDigest(t, m, tenant, p.Ref) != want {
					t.Errorf("%s: the K4 launch digest moved", p.Ref)
				}
			}
			if after := acctRow(t, m, tenant, claude[0].Ref); after.String(colPPHomeMode) != AccountHomeAdopted || after.String(colPPIsolationLevel) != AccountIsolationShared {
				t.Errorf("legacy profile recorded home_mode=%q isolation=%q, want adopted and shared", after.String(colPPHomeMode), after.String(colPPIsolationLevel))
			}

			settled := map[string]model.Record{}
			for _, p := range append(claude, codex, gemini) {
				settled[p.Ref] = acctRow(t, m, tenant, p.Ref)
			}
			if n := adoptUnnamed(t, m, tenant); n != 0 {
				t.Fatalf("second run named %d profiles, want 0", n)
			}
			for ref, row := range settled {
				if !reflect.DeepEqual(row, acctRow(t, m, tenant, ref)) {
					t.Errorf("%s: the second run changed the row", ref)
				}
			}
			if got := len(acctAuditsOf(t, m, tenant, "sessions.provider_account.adopt")); got != 4 {
				t.Errorf("adopt events = %d, want 4 (the operator's one and three at boot)", got)
			}
		})
	}
}
