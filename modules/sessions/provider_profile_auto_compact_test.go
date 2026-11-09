// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
)

// A Claude profile can declare the token count at which its sessions compact
// (Claude Code's CLAUDE_CODE_AUTO_COMPACT_WINDOW, 100000 to 1000000). The value is
// stored on the profile, shown by its read, re-resolved with the session policy on
// every launch and withdrawn with 0. Anything outside the tool's own range is
// refused when it is declared, never stored and passed on.
func TestProfileAutoCompactWindow_DeclaredValueReachesTheLaunch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _ := registriesHarness(t)
	ref := ensureRuntimeTestProfileRef(t, m, tenant)

	window := 300000
	prof, err := m.PatchProfile(ctx, tenant, ref, ProfilePatch{AutoCompactWindow: &window})
	if err != nil {
		t.Fatalf("declare the window: %v", err)
	}
	if got := m.toProfileDTO(prof).AutoCompactWindow; got != window {
		t.Fatalf("profile read auto_compact_window = %d, want %d", got, window)
	}
	_, policy, err := m.resolveLaunchProfile(ctx, tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	p := CreateRunParams{Isolation: IsolationNative}
	if err := applySessionPolicy(&p, providerDriverClaude, policy); err != nil {
		t.Fatal(err)
	}
	if p.profileAutoCompactWindow != window {
		t.Fatalf("the launch carries window %d, want %d", p.profileAutoCompactWindow, window)
	}

	for _, bad := range []int{99999, 1000001, -1} {
		if _, err := m.PatchProfile(ctx, tenant, ref, ProfilePatch{AutoCompactWindow: &bad}); statusOf(err) != http.StatusBadRequest {
			t.Errorf("window %d: %v, want 400", bad, err)
		}
	}
	if got, err := m.GetProfile(ctx, tenant, ref); err != nil || got.AutoCompactWindow != window {
		t.Fatalf("a refused value changed the stored window: %d %v", got.AutoCompactWindow, err)
	}

	none := 0
	if prof, err = m.PatchProfile(ctx, tenant, ref, ProfilePatch{AutoCompactWindow: &none}); err != nil || prof.AutoCompactWindow != 0 {
		t.Fatalf("withdraw: %d %v", prof.AutoCompactWindow, err)
	}
}

// Only Claude Code has the variable: a declaration on another tool's profile would
// be stored, shown and never applied, so it is refused by name.
func TestProfileAutoCompactWindow_OnlyForClaude(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, _ := registriesHarness(t)
	m.UseExecutionEnvironmentRef(testEnvRef)
	codex := mustCreateProfile(t, m, tenant, CreateProfileInput{
		Driver: "codex", ConfigHome: t.TempDir(), UserHome: t.TempDir(), DisplayName: "codex",
	})
	window := 300000
	if _, err := m.PatchProfile(ctx, tenant, codex.Ref, ProfilePatch{AutoCompactWindow: &window}); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a Codex profile's window: %v, want 422", err)
	}
}
