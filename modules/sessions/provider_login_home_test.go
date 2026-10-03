// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderLoginHomeSharesSessionGuards(t *testing.T) {
	ctx := context.Background()
	m, st := openProfileModule(t, profileBackends(t)[0], nil)
	tenant := ensureTenant(t, st, "account-login")
	other := ensureTenant(t, st, "other-account-login")
	config, home, _, _ := twoHomes(t)
	p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: home, AuthSource: AuthSourceAccountHome})
	gotHome, gotConfig, err := m.ProviderLoginHome(ctx, tenant, "claude", p.Ref)
	if err != nil || gotHome != home || gotConfig != config {
		t.Fatalf("login home=%q config=%q err=%v", gotHome, gotConfig, err)
	}
	snap, _, err := m.resolveLaunchProfile(ctx, tenant, p.Ref)
	if err != nil || snap.UserHome != gotHome || snap.ConfigHome != gotConfig {
		t.Fatalf("login/session diverged: %+v %v", snap, err)
	}
	if _, _, err := m.ProviderLoginHome(ctx, other, "claude", p.Ref); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("foreign tenant: %v", err)
	}
	if _, _, err := m.ProviderLoginHome(ctx, tenant, "codex", p.Ref); err == nil {
		t.Fatal("another driver used this home")
	}
	disabled := ProfileDisabled
	if _, err := m.PatchProfile(ctx, tenant, p.Ref, ProfilePatch{State: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ProviderLoginHome(ctx, tenant, "claude", p.Ref); !errors.Is(err, ErrProfileDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	active := ProfileActive
	if _, err := m.PatchProfile(ctx, tenant, p.Ref, ProfilePatch{State: &active}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(config, config+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(home), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ProviderLoginHome(ctx, tenant, "claude", p.Ref); err == nil {
		t.Fatal("a replaced home was used")
	}
}

func TestProviderLoginHomeRefusesManagedCredentialsAndHostLogin(t *testing.T) {
	ctx := context.Background()
	m, st := openProfileModule(t, profileBackends(t)[0], nil)
	tenant := ensureTenant(t, st, "login-source")
	config, home, _, _ := twoHomes(t)
	p := mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: config, UserHome: home, AuthSource: AuthSourceManagedInjection})
	if _, _, err := m.ProviderLoginHome(ctx, tenant, "claude", p.Ref); err == nil {
		t.Fatal("native login overwrote a managed credential profile")
	}
	host := t.TempDir()
	t.Setenv("HOME", host)
	hostConfig := filepath.Join(host, ".claude")
	if err := os.Mkdir(hostConfig, 0700); err != nil {
		t.Fatal(err)
	}
	p = mustCreateProfile(t, m, tenant, CreateProfileInput{Driver: "claude", ConfigHome: hostConfig, UserHome: host, AuthSource: AuthSourceAccountHome})
	if _, _, err := m.ProviderLoginHome(ctx, tenant, "claude", p.Ref); err == nil {
		t.Fatal("engine user login was used")
	}
}
