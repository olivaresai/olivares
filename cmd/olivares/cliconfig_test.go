// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIConfigPathHonoursOverrideAndXDG(t *testing.T) {
	override := filepath.Join(t.TempDir(), "client.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", override)
	got, err := cliConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != override {
		t.Fatalf("override path = %q, want %q", got, override)
	}

	t.Setenv("OLIVARES_CLI_CONFIG", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	got, err = cliConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "olivares", "config.yaml")
	if got != want {
		t.Fatalf("XDG path = %q, want %q", got, want)
	}
}

func TestCLIConfigWriteAndReadUseTightPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "olivares", "config.yaml")
	cfg := cliConfig{
		CurrentContext: "production",
		Contexts: []cliContext{{
			Name:      "production",
			Server:    "https://plane.example.test",
			Token:     "olvk_secret-value",
			Tenant:    "tenant-a",
			CACert:    "/etc/olivares/ca.pem",
			PinSHA256: []string{"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		}},
	}
	if err := writeCLIConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("config directory mode = %#o, want 0700", got)
	}

	loaded, err := readCLIConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CurrentContext != cfg.CurrentContext || len(loaded.Contexts) != 1 {
		t.Fatalf("round trip = %#v, want %#v", loaded, cfg)
	}
	if got := loaded.Contexts[0]; got.Token != cfg.Contexts[0].Token || got.CACert != cfg.Contexts[0].CACert || len(got.PinSHA256) != 1 {
		t.Fatalf("round-trip context = %#v, want %#v", got, cfg.Contexts[0])
	}
}

func TestCLIConfigRejectsPermissionsWiderThan0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("current-context: ''\ncontexts: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readCLIConfig(path)
	if err == nil {
		t.Fatal("read must deny a group/world-readable client config")
	}
	if got := err.Error(); !strings.Contains(got, "chmod 600") || !strings.Contains(got, path) {
		t.Fatalf("permission error = %q, want path and chmod 600 guidance", got)
	}
}

func TestResolveCLIConfigPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	if err := writeCLIConfig(path, cliConfig{
		CurrentContext: "ctx",
		Contexts: []cliContext{{
			Name:      "ctx",
			Server:    "https://context.example.test",
			Token:     "context-token",
			Tenant:    "context-tenant",
			CACert:    "context-ca.pem",
			PinSHA256: []string{"context-pin"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_SERVER_URL", "https://env.example.test")
	t.Setenv("OLIVARES_TOKEN", "env-token")
	t.Setenv("OLIVARES_TENANT", "env-tenant")

	resolved, err := resolveCLIConfig(cliResolutionOptions{
		Server:         "https://flag.example.test/",
		Token:          "flag-token",
		Tenant:         "flag-tenant",
		CACert:         "flag-ca.pem",
		PinSHA256:      []string{"flag-pin"},
		ServerExplicit: true,
		TokenExplicit:  true,
		TenantExplicit: true,
		CACertExplicit: true,
		PinsExplicit:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Server != "https://flag.example.test" || resolved.Token != "flag-token" || resolved.Tenant != "flag-tenant" {
		t.Fatalf("flag resolution = %#v", resolved)
	}
	if resolved.CACert != "flag-ca.pem" || len(resolved.PinSHA256) != 1 || resolved.PinSHA256[0] != "flag-pin" {
		t.Fatalf("flag TLS resolution = %#v", resolved)
	}
	if resolved.ContextName != "ctx" {
		t.Fatalf("active context = %q, want ctx", resolved.ContextName)
	}

	resolved, err = resolveCLIConfig(cliResolutionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Server != "https://env.example.test" || resolved.Token != "env-token" || resolved.Tenant != "env-tenant" {
		t.Fatalf("environment resolution = %#v", resolved)
	}
	if resolved.CACert != "context-ca.pem" || resolved.PinSHA256[0] != "context-pin" {
		t.Fatalf("context TLS fallback = %#v", resolved)
	}

	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	resolved, err = resolveCLIConfig(cliResolutionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Server != "https://context.example.test" || resolved.Token != "context-token" || resolved.Tenant != "context-tenant" {
		t.Fatalf("context resolution = %#v", resolved)
	}
}

// TestLoadCLIConfigDegradesWhenThereIsNowhereToLook pins the READ polarity of
// the split: a process with no $HOME and no $XDG_CONFIG_HOME — every container
// that does not invent one, and the CI runners that failed 54 tests this way —
// must still run commands that carry --server/--token on the command line.
// Having nowhere to look is the same answer as looking and finding nothing.
func TestLoadCLIConfigDegradesWhenThereIsNowhereToLook(t *testing.T) {
	t.Setenv("OLIVARES_CLI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	cfg, path, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("no config location must not be an error, got %v", err)
	}
	if path != "" {
		t.Fatalf("path must be empty so the write side can refuse, got %q", path)
	}
	if cfg.CurrentContext != "" || len(cfg.Contexts) != 0 {
		t.Fatalf("degraded config must be empty, got %+v", cfg)
	}
}

// TestWriteCLIConfigRefusesEmptyPathInsteadOfWritingIntoCwd pins the WRITE
// polarity. loadCLIConfig hands its path straight to writeCLIConfig in
// cmd_auth.go; measured with the guard removed, filepath.Dir("") is ".", so a
// temp file carrying the BEARER TOKEN is created in the operator's working
// directory before os.Rename to "" fails and the deferred cleanup removes it —
// leaving «rename ./.config-1450271771 : no such file or directory», which
// names neither cause nor remedy. The token does not survive; the point is
// that it was written somewhere nobody chose and the operator cannot tell why.
// The test asserts both halves: it errors NAMING THE CAUSE, and it leaves the
// working directory untouched.
func TestWriteCLIConfigRefusesEmptyPathInsteadOfWritingIntoCwd(t *testing.T) {
	cwd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	err = writeCLIConfig("", cliConfig{
		CurrentContext: "prod",
		Contexts:       []cliContext{{Name: "prod", Server: "https://example.invalid", Token: "s3cr3t"}},
	})
	if err == nil {
		t.Fatal("writing a config with no location must fail")
	}
	if !strings.Contains(err.Error(), "neither $XDG_CONFIG_HOME nor $HOME") {
		t.Fatalf("the error must name the cause, got %v", err)
	}

	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("nothing may be written into the working directory, found %q", e.Name())
	}
}
