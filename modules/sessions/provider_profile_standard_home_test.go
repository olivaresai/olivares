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

// A profile that asks for the tool's saved login and names no home runs on the
// tenant's own login of the tool, in the home the product made for it
// (<tool-logins>/<tenant>/<driver>/.claude, where the console's sign-in runs the
// tool's login), and with a HOME the product creates for the profile. FH 036: the
// configuration home used to be the ENGINE USER's own ~/.claude, which handed a
// vendor login nobody gave the product to every own-login session. Neither home is
// ever the engine user's.
func TestProviderProfile_StandardAccountHome(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, st := openProfileModule(t, profileBackends(t)[0], nil)
	tenant := ensureTenant(t, st, "standard-home")

	// No root for profile homes on this node: refused, not the engine user's home.
	if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "claude", AuthSource: AuthSourceAccountHome}); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("no profile homes root = %v, want 422", err)
	}
	root := t.TempDir()
	m.UseProfileHomesRoot(root)
	// No root for the tools' own logins: refused too; there is no fallback to ~/.claude.
	if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "claude", AuthSource: AuthSourceAccountHome}); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("no tool-logins root = %v, want 422", err)
	}

	logins := t.TempDir()
	m.UseToolLoginsRoot(logins)
	p, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "claude", AuthSource: AuthSourceAccountHome})
	if err != nil {
		t.Fatalf("create with no homes: %v", err)
	}
	want, _ := filepath.EvalSymlinks(logins)
	want = filepath.Join(want, tenant.String(), "claude", ".claude")
	if p.ConfigHome != want {
		t.Fatalf("config home = %q, want the tenant's own login %q (never %q)", p.ConfigHome, want, filepath.Join(home, ".claude"))
	}
	if want := filepath.Join(root, p.Ref); p.UserHome != want || p.UserHome == home {
		t.Fatalf("HOME = %q, want the profile's own %q (never %q)", p.UserHome, want, home)
	}
	for _, dir := range []string{p.ConfigHome, p.UserHome} {
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s = %v, %v; want created 0700", dir, fi, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("the engine user's ~/.claude was touched: %v", err)
	}

	// Naming no home without asking for the saved login is still refused, and so
	// is a driver whose standard home this engine does not know.
	if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "codex"}); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("no homes, no auth source = %v, want 400", err)
	}
	if _, err := m.CreateProfile(ctx, tenant, CreateProfileInput{Driver: "someagent", AuthSource: AuthSourceAccountHome}); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("unknown driver = %v, want 400", err)
	}
}

// A key-backed profile (managed injection) that names no home gets homes the
// product makes, both inside the profile's own directory: it never shares the
// tool's own login folder with the operator's subscription sign-in.
func TestProviderProfile_ManagedHomesAreTheProfilesOwn(t *testing.T) {
	m := &Module{Dependencies: &Dependencies{}}
	if _, _, err := m.managedProfileHomes("claude", "ppf_x"); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("no profile homes root = %v, want 422", err)
	}
	root := t.TempDir()
	m.UseProfileHomesRoot(root)
	config, home, err := m.managedProfileHomes("codex", "ppf_x")
	if err != nil {
		t.Fatal(err)
	}
	if home != filepath.Join(root, "ppf_x") || config != filepath.Join(root, "ppf_x", ".codex") {
		t.Fatalf("homes = %q, %q", config, home)
	}
	if fi, err := os.Stat(config); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("config home = %v, %v; want created 0700", fi, err)
	}
	if _, _, err := m.managedProfileHomes("someagent", "ppf_y"); statusOf(err) != http.StatusBadRequest {
		t.Fatalf("unknown driver = %v, want 400", err)
	}
}
